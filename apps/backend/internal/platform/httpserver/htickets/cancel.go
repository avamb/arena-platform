// cancel.go implements the AB-49 operator ticket cancellation — the
// primitive that did not exist: the ONLY path that cancelled a ticket
// before this feature was the inbound payment-refund webhook, so money
// drove cancellation and no seat was ever returned to sale.
//
//	POST /v1/tickets/{id}/cancel
//
// # The governing rule (owner, 2026-08-01)
//
// Ticket cancellation is the primary operator action and the SOLE driver
// of inventory and gate state. By itself, immediately, and regardless of
// any money movement it: invalidates the ticket, returns its seat (or GA
// unit) to sale, restores capacity, revokes barcodes/credentials, and
// enqueues a scanner notification so the ticket stops admitting.
//
// The money refund is a separate, optional, possibly-later action:
//
//	none      — nothing owed (comp ticket, no-refund policy).
//	manual    — organizer refunds from the provider's own dashboard; the
//	            platform records an OUTSTANDING OBLIGATION and performs
//	            no financial operation. It must never read as done.
//	automatic — the operator confirms the amount (full or partial) and
//	            the platform creates a refunds row AFTER the cancellation
//	            transaction commits. A provider/refund failure leaves the
//	            ticket cancelled and the seat on sale — it surfaces as a
//	            retryable financial task, never as a rolled-back
//	            cancellation.
//
// # Seat state machine (owner-confirmed)
//
// sold -> available happens ONLY here (and in the other terminal ticket
// transitions: complimentary revocation, inbound refund webhook). There
// is no 'refunded' seat status — refund_date/refund_price live on the
// TICKET (the Bil24 export shape); the seat is resellable at once.
package htickets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// cancelBodyLimit caps the JSON payload for the cancel endpoint.
const cancelBodyLimit = 64 * 1024

// Refund modes an operator can choose at cancellation.
const (
	RefundModeNone      = "none"
	RefundModeManual    = "manual"
	RefundModeAutomatic = "automatic"
)

// cancelTicketRequest is the strict-decoded request body.
type cancelTicketRequest struct {
	// Reason is the operator-supplied cancellation reason. Required —
	// this is an audited, money-adjacent action.
	Reason string `json:"reason"`
	// RefundMode is the money decision: none | manual | automatic.
	RefundMode string `json:"refund_mode"`
	// RefundAmount is the confirmed amount in minor units for
	// refund_mode=automatic (full or partial, validated against the
	// payment intent's remaining refundable balance). Ignored otherwise.
	RefundAmount *int64 `json:"refund_amount"`
}

// cancelTicketResponse is the 200 envelope.
type cancelTicketResponse struct {
	Ticket           TicketResponse `json:"ticket"`
	SeatReleased     bool           `json:"seat_released"`
	GAUnitReleased   bool           `json:"ga_unit_released"`
	CapacityRestored bool           `json:"capacity_restored"`
	// Refund reports the financial consequence for refund_mode=automatic:
	// nil for none/manual. state mirrors the refunds row.
	Refund *cancelRefundSummary `json:"refund"`
	// RefundTaskFailed is true when refund_mode=automatic was requested
	// but the refunds row could not be created/advanced. The ticket IS
	// cancelled and the seat IS released — the money side must be
	// retried via POST /v1/refunds (retryable financial task).
	RefundTaskFailed bool `json:"refund_task_failed"`
}

// cancelRefundSummary is the trimmed refunds-row view embedded in the
// cancel response.
type cancelRefundSummary struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// HandleCancelTicket serves POST /v1/tickets/{id}/cancel.
//
// Auth: JWT + "ticket.cancel" permission (applyAuth at mount) plus org
// membership resolved from the ticket's session in the shim layer.
func (h *Handler) HandleCancelTicket(w http.ResponseWriter, r *http.Request) {
	if h.ticketQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"ticket.invalid_id", "ticket id must be a valid UUID", r,
		))
		return
	}

	req, ok := readCancelTicketRequest(w, r)
	if !ok {
		return
	}

	// Load the ticket for guards + inventory coordinates.
	ticket, err := h.ticketQueries.GetTicketByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"ticket.not_found", "ticket not found", r,
			))
			return
		}
		h.logger.Error("ticket.cancel: lookup failed", slog.String("id", id.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.lookup_failed", "failed to load ticket", r,
		))
		return
	}
	if ticket.Status != "active" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"ticket.not_active",
			"only active tickets can be cancelled",
			r,
			map[string]any{"current_status": ticket.Status},
		))
		return
	}

	// For refund_mode=automatic resolve the payment intent up front so a
	// misconfigured request fails BEFORE the cancellation commits. The
	// actual refund creation happens after commit — a provider problem
	// must never roll back the cancellation.
	var refundIntent *gen.PaymentIntentRow
	var refundAmount int64
	if req.RefundMode == RefundModeAutomatic {
		intent, amount, errCode, errMsg := h.resolveAutomaticRefundTarget(ctx, ticket, req.RefundAmount)
		if errCode != "" {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(errCode, errMsg, r))
			return
		}
		refundIntent = intent
		refundAmount = amount
	}

	// ── The cancellation transaction ─────────────────────────────────────
	// Everything inventory- and gate-related happens here, atomically,
	// and none of it is conditional on the money.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := h.ticketQueries.WithTx(tx)

	cancelled, err := txq.CancelTicket(ctx, id, req.Reason, req.RefundMode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost the race with a concurrent cancel — surface as conflict.
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"ticket.not_active", "ticket is no longer active", r,
			))
			return
		}
		h.logger.Error("ticket.cancel: transition failed", slog.String("id", id.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.cancel_failed", "failed to cancel ticket", r,
		))
		return
	}

	release, err := ReleaseCancelledTicketInventoryTx(ctx, txq, cancelled)
	if err != nil {
		h.logger.Error("ticket.cancel: inventory release failed",
			slog.String("id", id.String()),
			slog.String("session_id", cancelled.SessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.release_failed", "failed to release the ticket's inventory", r,
		))
		return
	}

	// Restore ledger capacity. Scope mirrors checkout conversion: a
	// released seat/GA-unit row means the reservation confirmed
	// session-level (nil tier) capacity; a row-less legacy/comp ticket
	// confirmed per-tier capacity (see checkout_convert.go).
	capacityRestored := false
	if h.inventoryQueries != nil {
		restoreTier := cancelled.TierID
		if release.RowReleased() {
			restoreTier = nil
		}
		if _, invErr := h.inventoryQueries.WithTx(tx).RestoreSoldCapacity(ctx, cancelled.SessionID, restoreTier, 1); invErr != nil {
			h.logger.Error("ticket.cancel: capacity restore failed",
				slog.String("id", id.String()),
				slog.String("session_id", cancelled.SessionID.String()),
				slog.String("error", invErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"ticket.capacity_restore_failed", "failed to restore inventory capacity", r,
			))
			return
		}
		capacityRestored = true
	}

	// Revoke barcodes + credentials so the ticket stops admitting.
	RevokeTicketArtifactsTx(ctx, h.logger, h.barcodeQueries, h.credentialQueries, tx, []gen.TicketRow{cancelled})

	// Audit: who cancelled what, when, why, and the refund decision.
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if auditErr := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.ticket.cancel",
			ResourceType: "ticket",
			ResourceID:   cancelled.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			Metadata: map[string]any{
				"session_id":        cancelled.SessionID.String(),
				"reason":            req.Reason,
				"refund_mode":       req.RefundMode,
				"refund_amount":     req.RefundAmount,
				"seat_key":          cancelled.SeatKey,
				"seat_released":     release.SeatReleased,
				"ga_unit_released":  release.GAUnitReleased,
				"capacity_restored": capacityRestored,
			},
		}); auditErr != nil {
			h.logger.Error("ticket.cancel: audit write failed", slog.String("error", auditErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"ticket.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"ticket.commit_failed", "failed to commit cancellation", r,
		))
		return
	}

	// The ticket no longer admits — notify the scanner pipeline (AB-50
	// consumes these outbox events to feed MACS).
	if h.publishTicketCancelledEvent != nil {
		h.publishTicketCancelledEvent(ctx, cancelled, req.Reason, req.RefundMode)
	}

	// ── The money — strictly after the inventory committed ──────────────
	resp := cancelTicketResponse{
		Ticket:           TicketFromRow(cancelled),
		SeatReleased:     release.SeatReleased,
		GAUnitReleased:   release.GAUnitReleased,
		CapacityRestored: capacityRestored,
	}
	switch req.RefundMode {
	case RefundModeAutomatic:
		summary, updatedTicket, refundErr := h.createAutomaticRefund(ctx, cancelled, refundIntent, refundAmount, req.Reason)
		if refundErr != nil {
			h.logger.Error("ticket.cancel: automatic refund task failed — ticket stays cancelled, retry the refund",
				slog.String("ticket_id", cancelled.ID.String()),
				slog.String("error", refundErr.Error()),
			)
			resp.RefundTaskFailed = true
		} else {
			resp.Refund = summary
			resp.Ticket = TicketFromRow(updatedTicket)
		}
	case RefundModeManual:
		// Outstanding obligation: record the date the obligation was
		// taken. refund_price stays NULL — the platform does not know
		// what the organizer will return outside it.
		now := time.Now().UTC()
		if updated, recErr := h.ticketQueries.SetTicketRefundRecord(ctx, cancelled.ID, nil, &now, nil); recErr != nil {
			h.logger.Warn("ticket.cancel: manual refund record failed", slog.String("error", recErr.Error()))
		} else {
			resp.Ticket = TicketFromRow(updated)
		}
	}

	h.logger.Info("ticket.cancelled",
		slog.String("ticket_id", cancelled.ID.String()),
		slog.String("session_id", cancelled.SessionID.String()),
		slog.String("refund_mode", req.RefundMode),
		slog.Bool("seat_released", release.SeatReleased),
		slog.Bool("ga_unit_released", release.GAUnitReleased),
		slog.String("event", "v1.ticket.cancel"),
	)

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// readCancelTicketRequest slurps and strictly validates the body.
func readCancelTicketRequest(w http.ResponseWriter, r *http.Request) (cancelTicketRequest, bool) {
	var req cancelTicketRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, cancelBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"ticket.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return req, false
	}
	if req.Reason == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"ticket.reason_required", "reason is required", r,
			map[string]any{"field": "reason"},
		))
		return req, false
	}
	switch req.RefundMode {
	case RefundModeNone, RefundModeManual, RefundModeAutomatic:
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"ticket.invalid_refund_mode",
			"refund_mode must be one of: none, manual, automatic",
			r,
			map[string]any{"field": "refund_mode", "value": req.RefundMode},
		))
		return req, false
	}
	if req.RefundMode == RefundModeAutomatic {
		if req.RefundAmount == nil || *req.RefundAmount <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"ticket.invalid_refund_amount",
				"refund_amount (minor units, > 0) is required for refund_mode=automatic",
				r,
				map[string]any{"field": "refund_amount"},
			))
			return req, false
		}
	}
	return req, true
}

// resolveAutomaticRefundTarget locates the succeeded payment intent of
// the ticket's checkout session and validates the confirmed amount
// against the remaining refundable balance. Returns a non-empty errCode
// on failure (mapped to 422 by the caller).
func (h *Handler) resolveAutomaticRefundTarget(
	ctx context.Context,
	ticket gen.TicketRow,
	amount *int64,
) (*gen.PaymentIntentRow, int64, string, string) {
	if h.reservationQueries == nil {
		return nil, 0, "ticket.refund_unavailable", "payment queries are not available"
	}
	intents, err := h.reservationQueries.ListPaymentIntentsByCheckout(ctx, ticket.CheckoutSessionID)
	if err != nil {
		return nil, 0, "ticket.refund_unavailable", "failed to look up payment intents for the order"
	}
	var succeeded *gen.PaymentIntentRow
	for i := range intents {
		if intents[i].State == "succeeded" {
			succeeded = &intents[i]
			break
		}
	}
	if succeeded == nil {
		return nil, 0, "ticket.no_succeeded_payment",
			"the order has no succeeded payment intent — use refund_mode=none or manual"
	}
	refunded, err := h.reservationQueries.SumNonFailedRefundsByIntent(ctx, succeeded.ID)
	if err != nil {
		return nil, 0, "ticket.refund_unavailable", "failed to compute the refunded total"
	}
	remaining := succeeded.Amount - refunded
	if *amount > remaining {
		return nil, 0, "ticket.refund_amount_exceeds_refundable",
			"refund_amount exceeds the remaining refundable balance of the order's payment"
	}
	return succeeded, *amount, "", ""
}

// createAutomaticRefund creates the refunds row for an automatic-mode
// cancellation and records the linkage on the ticket. Runs strictly
// AFTER the cancellation transaction committed; any failure here is a
// retryable financial task, never a rollback trigger.
func (h *Handler) createAutomaticRefund(
	ctx context.Context,
	ticket gen.TicketRow,
	intent *gen.PaymentIntentRow,
	amount int64,
	reason string,
) (*cancelRefundSummary, gen.TicketRow, error) {
	if intent == nil || h.reservationQueries == nil {
		return nil, ticket, errors.New("no payment intent resolved for automatic refund")
	}
	requestedBy := "ticket.cancel:" + ticket.ID.String()
	refund, err := h.reservationQueries.InsertRefund(ctx,
		intent.ID, intent.OrgID, amount, intent.Currency, &reason, &requestedBy,
	)
	if err != nil {
		return nil, ticket, err
	}
	now := time.Now().UTC()
	updated, recErr := h.ticketQueries.SetTicketRefundRecord(ctx, ticket.ID, &refund.ID, &now, &amount)
	if recErr != nil {
		// The refunds row exists; the ticket linkage is best-effort.
		h.logger.Warn("ticket.cancel: refund record on ticket failed",
			slog.String("ticket_id", ticket.ID.String()),
			slog.String("refund_id", refund.ID.String()),
			slog.String("error", recErr.Error()),
		)
		updated = ticket
	}
	return &cancelRefundSummary{
		ID:       refund.ID.String(),
		State:    refund.State,
		Amount:   refund.Amount,
		Currency: refund.Currency,
	}, updated, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared inventory-release core (admin cancel, inbound refund webhook,
// complimentary revocation)
// ─────────────────────────────────────────────────────────────────────────────

// TicketReleaseOutcome reports what a ReleaseCancelledTicketInventoryTx
// call actually freed.
type TicketReleaseOutcome struct {
	SeatReleased   bool
	GAUnitReleased bool
}

// RowReleased is true when a session_seats row (seat or GA unit) moved
// back to available — the signal that the reservation confirmed
// session-level capacity and the ledger restore must use a nil tier.
func (o TicketReleaseOutcome) RowReleased() bool {
	return o.SeatReleased || o.GAUnitReleased
}

// ReleaseCancelledTicketInventoryTx returns a cancelled ticket's place
// to sale inside the caller's transaction:
//
//   - assigned seat (ticket.SeatKey != nil): conditional sold->available
//     via ReleaseSoldSessionSeat, guarded against other ACTIVE tickets on
//     the same seat;
//   - GA ticket: exactly one sold GA unit of the ticket's reservation +
//     tier via ReleaseSoldGAUnitForReservation; zero rows means a legacy
//     pre-AB-51 reservation (ledger-only restore) — not an error;
//   - bumps sessions.seat_status_version first so widget/feed caches
//     invalidate (skipped when the ticket has neither seat nor
//     reservation rows to release).
//
// The caller remains responsible for RestoreSoldCapacity — the ledger
// scope depends on RowReleased() (see checkout_convert.go's confirm
// mirror rule).
func ReleaseCancelledTicketInventoryTx(
	ctx context.Context,
	txq *gen.Queries,
	ticket gen.TicketRow,
) (TicketReleaseOutcome, error) {
	var out TicketReleaseOutcome

	if ticket.SeatKey != nil && *ticket.SeatKey != "" {
		version, err := txq.IncrementSessionSeatStatusVersion(ctx, ticket.SessionID)
		if err != nil {
			return out, err
		}
		if _, err := txq.ReleaseSoldSessionSeat(ctx, ticket.SessionID, *ticket.SeatKey, version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Either the seat is not 'sold' or another ACTIVE ticket
				// still references it — both are inconsistencies the
				// caller must not paper over.
				return out, errors.New("seat " + *ticket.SeatKey + " is not releasable (not sold, or an active ticket still references it)")
			}
			return out, err
		}
		out.SeatReleased = true
		return out, nil
	}

	// GA ticket: resolve the reservation via the checkout session, then
	// free one sold unit of the ticket's tier.
	cs, err := txq.GetCheckoutSessionByID(ctx, ticket.CheckoutSessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil // orphaned checkout — nothing to release
		}
		return out, err
	}
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, ticket.SessionID)
	if err != nil {
		return out, err
	}
	if _, err := txq.ReleaseSoldGAUnitForReservation(ctx, ticket.SessionID, cs.ReservationID, ticket.TierID, version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Legacy pre-AB-51 reservation without unit rows: the ledger
			// restore (caller) is the whole release.
			return out, nil
		}
		return out, err
	}
	out.GAUnitReleased = true
	return out, nil
}

// RevokeTicketArtifactsTx revokes every barcode and the qr/pdf
// credentials of the given tickets inside the caller's transaction.
// Best-effort per artifact (a missing credential is normal); errors are
// logged, never returned — a stray barcode row must not block a
// cancellation, and the scanner pipeline is additionally told via the
// cancellation event.
func RevokeTicketArtifactsTx(
	ctx context.Context,
	logger *slog.Logger,
	barcodeQ, credentialQ *gen.Queries,
	tx pgx.Tx,
	tickets []gen.TicketRow,
) {
	if barcodeQ != nil {
		q := barcodeQ.WithTx(tx)
		for _, t := range tickets {
			barcodes, err := q.ListBarcodesByTicketID(ctx, t.ID)
			if err != nil {
				logger.Warn("ticket.cancel: list barcodes failed",
					slog.String("ticket_id", t.ID.String()), slog.String("error", err.Error()))
				continue
			}
			for _, b := range barcodes {
				if _, err := q.RevokeBarcode(ctx, b.ID); err != nil {
					logger.Warn("ticket.cancel: revoke barcode failed",
						slog.String("barcode_id", b.ID.String()), slog.String("error", err.Error()))
				}
			}
		}
	}
	if credentialQ != nil {
		q := credentialQ.WithTx(tx)
		for _, t := range tickets {
			for _, credType := range []string{"qr", "pdf"} {
				if _, err := q.RevokeCredential(ctx, t.ID, credType); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					logger.Warn("ticket.cancel: revoke credential failed",
						slog.String("ticket_id", t.ID.String()),
						slog.String("type", credType),
						slog.String("error", err.Error()))
				}
			}
		}
	}
}
