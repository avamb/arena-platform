// cancel_tx.go carries the AB-49 cancellation TRANSACTION, extracted out of
// HandleCancelTicket by feature #509 (W1-B8, spec §7.13) so a second,
// non-HTTP caller — the Bil24-compatible gateway's REFUND_TICKET command —
// runs byte-for-byte the same inventory/gate/audit steps instead of a
// parallel re-implementation.
//
// Two surfaces live here:
//
//   - CancelTicketTx — the transaction itself. It returns a
//     *CancelTicketError carrying the HTTP status + error code each step
//     used to write directly, so HandleCancelTicket keeps its exact
//     per-step response mapping and no HTTP concern leaks into the core.
//   - RefundTicketForGateway — the gateway entry point: cancel with
//     refund_mode=manual under a `gateway:<fid>` audit actor, record the
//     refund_price, and project the consequence onto the ORDER aggregate
//     (orders.status → refunded / partially_refunded plus an
//     order_events.ticket_refunded row).
package htickets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// CancelTicketParams is the input of CancelTicketTx. ActorType/ActorID name
// the audit principal: ("user", <uuid>) for the operator endpoint and
// ("system", "") for the Bil24 gateway.
type CancelTicketParams struct {
	TicketID     uuid.UUID
	Reason       string
	RefundMode   string
	RefundAmount *int64
	ActorType    string
	// ActorID MUST be a UUID string or empty: audit_events.actor_id is a
	// nullable uuid column and the writer casts with NULLIF($3,'')::uuid, so
	// any other value aborts the whole cancel transaction with SQLSTATE 22P02.
	ActorID string
	// ActorLabel is a non-UUID principal name (the Bil24 gateway's
	// "gateway:<fid>", spec §7.13). It cannot live in actor_id, so it is
	// recorded as audit metadata "actor" instead. Empty for operator calls.
	ActorLabel string
}

// CancelTicketOutcome reports what the committed transaction changed.
type CancelTicketOutcome struct {
	Ticket           gen.TicketRow
	Release          TicketReleaseOutcome
	CapacityRestored bool
}

// CancelTicketError is a step failure of CancelTicketTx carrying the HTTP
// status and machine-readable code the operator endpoint answers with. Non-
// HTTP callers (the gateway) map Code onto their own protocol instead.
type CancelTicketError struct {
	// Status is the HTTP status the operator endpoint uses.
	Status int
	// Code is the machine-readable error code (ticket.not_active, …).
	Code string
	// Message is the human-readable message.
	Message string
	// Err is the underlying database error, when there was one.
	Err error
}

// Error implements the error interface.
func (e *CancelTicketError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the underlying database error to errors.Is/As.
func (e *CancelTicketError) Unwrap() error { return e.Err }

// CancelTicketTx runs the whole AB-49 cancellation transaction for one
// ticket: status transition, inventory release, ledger capacity restore,
// barcode/credential revocation and the audit row — then commits and fires
// the v1.ticket.cancelled outbox event.
//
// It performs NO money operation: the refund (automatic) or the outstanding
// obligation (manual) is the caller's business, strictly after this returns.
func (h *Handler) CancelTicketTx(ctx context.Context, p CancelTicketParams) (CancelTicketOutcome, *CancelTicketError) {
	var out CancelTicketOutcome
	if h.ticketQueries == nil || h.pool == nil {
		return out, &CancelTicketError{
			Status:  http.StatusServiceUnavailable,
			Code:    "dependency.database_unavailable",
			Message: "database is not available",
		}
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return out, &CancelTicketError{
			Status:  http.StatusServiceUnavailable,
			Code:    "dependency.database_unavailable",
			Message: "failed to begin transaction",
			Err:     err,
		}
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	txq := h.ticketQueries.WithTx(tx)

	cancelled, err := txq.CancelTicket(ctx, p.TicketID, p.Reason, p.RefundMode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost the race with a concurrent cancel — surface as conflict.
			return out, &CancelTicketError{
				Status:  http.StatusConflict,
				Code:    "ticket.not_active",
				Message: "ticket is no longer active",
				Err:     err,
			}
		}
		h.logger.Error("ticket.cancel: transition failed",
			slog.String("id", p.TicketID.String()), slog.String("error", err.Error()))
		return out, &CancelTicketError{
			Status:  http.StatusInternalServerError,
			Code:    "ticket.cancel_failed",
			Message: "failed to cancel ticket",
			Err:     err,
		}
	}

	release, err := ReleaseCancelledTicketInventoryTx(ctx, txq, cancelled)
	if err != nil {
		h.logger.Error("ticket.cancel: inventory release failed",
			slog.String("id", p.TicketID.String()),
			slog.String("session_id", cancelled.SessionID.String()),
			slog.String("error", err.Error()),
		)
		return out, &CancelTicketError{
			Status:  http.StatusInternalServerError,
			Code:    "ticket.release_failed",
			Message: "failed to release the ticket's inventory",
			Err:     err,
		}
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
				slog.String("id", p.TicketID.String()),
				slog.String("session_id", cancelled.SessionID.String()),
				slog.String("error", invErr.Error()),
			)
			return out, &CancelTicketError{
				Status:  http.StatusInternalServerError,
				Code:    "ticket.capacity_restore_failed",
				Message: "failed to restore inventory capacity",
				Err:     invErr,
			}
		}
		capacityRestored = true
	}

	// Revoke barcodes + credentials so the ticket stops admitting.
	RevokeTicketArtifactsTx(ctx, h.logger, h.barcodeQueries, h.credentialQueries, tx, []gen.TicketRow{cancelled})

	// Audit: who cancelled what, when, why, and the refund decision.
	if h.audit != nil {
		meta := map[string]any{
			"session_id":        cancelled.SessionID.String(),
			"reason":            p.Reason,
			"refund_mode":       p.RefundMode,
			"refund_amount":     p.RefundAmount,
			"seat_key":          cancelled.SeatKey,
			"seat_released":     release.SeatReleased,
			"ga_unit_released":  release.GAUnitReleased,
			"capacity_restored": capacityRestored,
		}
		if p.ActorLabel != "" {
			meta["actor"] = p.ActorLabel
		}
		if auditErr := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    p.ActorType,
			ActorID:      p.ActorID,
			Action:       "v1.ticket.cancel",
			ResourceType: "ticket",
			ResourceID:   cancelled.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			Metadata:     meta,
		}); auditErr != nil {
			h.logger.Error("ticket.cancel: audit write failed", slog.String("error", auditErr.Error()))
			return out, &CancelTicketError{
				Status:  http.StatusInternalServerError,
				Code:    "ticket.audit_failed",
				Message: "failed to write audit event",
				Err:     auditErr,
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return out, &CancelTicketError{
			Status:  http.StatusInternalServerError,
			Code:    "ticket.commit_failed",
			Message: "failed to commit cancellation",
			Err:     err,
		}
	}

	// The ticket no longer admits — notify the scanner pipeline (AB-50
	// consumes these outbox events to feed MACS).
	if h.publishTicketCancelledEvent != nil {
		h.publishTicketCancelledEvent(ctx, cancelled, p.Reason, p.RefundMode)
	}

	out.Ticket = cancelled
	out.Release = release
	out.CapacityRestored = capacityRestored
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// REFUND_TICKET gateway entry point (feature #509, W1-B8, spec §7.13)
// ─────────────────────────────────────────────────────────────────────────────

// GatewayRefundParams is the input of RefundTicketForGateway.
type GatewayRefundParams struct {
	// TicketID is the platform tickets.id to cancel + refund.
	TicketID uuid.UUID
	// OrgID is the org the CALLER is scoped to; it gates the orders write
	// so a gateway can never touch another tenant's aggregate. The ticket
	// itself has already been org-checked by the caller.
	OrgID uuid.UUID
	// Reason is the cancellation reason recorded on the ticket and in the
	// audit trail. Required — the gateway substitutes a default.
	Reason string
	// RefundPrice is the refunded amount in MINOR units, or nil when the
	// organizer has not decided the amount yet (spec §7.13: the ticket is
	// still cancelled, tickets.refund_price stays NULL).
	RefundPrice *int64
	// Actor is the audit/order-event principal, "gateway:<fid>".
	Actor string
}

// GatewayRefundResult reports the outcome of a gateway refund.
type GatewayRefundResult struct {
	// Ticket is the cancelled ticket row after the refund record landed.
	Ticket gen.TicketRow
	// RefundDate is the timestamp stamped on tickets.refund_date.
	RefundDate time.Time
	// OrderStatus is the status the owning order ended up in
	// ("refunded" | "partially_refunded"), or "" when the ticket's
	// checkout session has no orders row (pre-#488 legacy data).
	OrderStatus string
}

// RefundTicketForGateway cancels ONE ticket on behalf of the Bil24 gateway
// (spec §7.13): the AB-49 cancellation transaction with refund_mode=manual,
// then the refund record on the ticket, then the ORDER-aggregate projection.
//
// refund_mode=manual is deliberate and never negotiable here: the money is
// returned by the organizer in WooCommerce/the PSP dashboard, so the platform
// records an OUTSTANDING OBLIGATION (refund_date, optional refund_price) and
// performs no financial operation of its own.
//
// The order projection is best-effort: an orders-side failure must never undo
// a committed cancellation (the seat is already back on sale). Failures are
// logged and reported through an empty OrderStatus.
func (h *Handler) RefundTicketForGateway(ctx context.Context, p GatewayRefundParams) (GatewayRefundResult, error) {
	var res GatewayRefundResult

	outcome, cErr := h.CancelTicketTx(ctx, CancelTicketParams{
		TicketID:   p.TicketID,
		Reason:     p.Reason,
		RefundMode: RefundModeManual,
		ActorType:  "system",
		// actor_id is a uuid column; the gateway principal is a label.
		ActorLabel: p.Actor,
	})
	if cErr != nil {
		return res, cErr
	}
	cancelled := outcome.Ticket

	// Outstanding obligation: stamp the date the obligation was taken and,
	// when the caller supplied one, the amount owed.
	now := time.Now().UTC()
	res.RefundDate = now
	res.Ticket = cancelled
	if updated, recErr := h.ticketQueries.SetTicketRefundRecord(ctx, cancelled.ID, nil, &now, p.RefundPrice); recErr != nil {
		h.logger.Warn("bil24.refund_ticket: refund record failed — ticket stays cancelled",
			slog.String("ticket_id", cancelled.ID.String()),
			slog.String("error", recErr.Error()),
		)
	} else {
		res.Ticket = updated
		if updated.RefundDate != nil {
			res.RefundDate = updated.RefundDate.UTC()
		}
	}

	res.OrderStatus = h.projectRefundOntoOrder(ctx, res.Ticket, p, res.RefundDate)
	return res, nil
}

// projectRefundOntoOrder moves the owning order to refunded /
// partially_refunded and appends the order_events.ticket_refunded row
// (spec §7.13, order aggregate migration 0092). Returns the resulting order
// status, or "" when there is no orders row or the projection failed — both
// are non-fatal for an already-committed cancellation.
func (h *Handler) projectRefundOntoOrder(
	ctx context.Context,
	ticket gen.TicketRow,
	p GatewayRefundParams,
	refundDate time.Time,
) string {
	order, err := h.ticketQueries.GetOrderByCheckoutSession(ctx, ticket.CheckoutSessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("bil24.refund_ticket: order lookup failed",
				slog.String("ticket_id", ticket.ID.String()),
				slog.String("error", err.Error()),
			)
		}
		return ""
	}
	if order.OrgID != p.OrgID {
		// Defence in depth: the ticket was org-checked by the caller, so a
		// mismatch here means the order aggregate disagrees with the
		// sessions→events chain. Refuse to write across the tenant line.
		h.logger.Error("bil24.refund_ticket: order org mismatch — skipping order projection",
			slog.String("ticket_id", ticket.ID.String()),
			slog.String("order_org", order.OrgID.String()),
			slog.String("caller_org", p.OrgID.String()),
		)
		return ""
	}

	// Partial vs full: an order is fully refunded only once EVERY one of
	// its tickets is cancelled.
	status := "partially_refunded"
	siblings, sErr := h.ticketQueries.ListTicketsByCheckoutSession(ctx, ticket.CheckoutSessionID)
	if sErr != nil {
		h.logger.Warn("bil24.refund_ticket: sibling ticket scan failed — assuming partial refund",
			slog.String("ticket_id", ticket.ID.String()),
			slog.String("error", sErr.Error()),
		)
	} else {
		allCancelled := len(siblings) > 0
		for _, s := range siblings {
			if s.Status == "active" {
				allCancelled = false
				break
			}
		}
		if allCancelled {
			status = "refunded"
		}
	}

	if _, uErr := h.ticketQueries.UpdateOrderStatus(ctx, order.ID, order.OrgID, status, nil, nil); uErr != nil {
		h.logger.Warn("bil24.refund_ticket: order status update failed",
			slog.String("order_id", order.ID.String()),
			slog.String("status", status),
			slog.String("error", uErr.Error()),
		)
		return ""
	}

	payload := map[string]any{
		"ticket_id":        ticket.ID.String(),
		"system_ticket_id": ticket.SystemTicketID,
		"reason":           p.Reason,
		"refund_mode":      RefundModeManual,
		"refund_date":      refundDate.Format(time.RFC3339),
	}
	if p.RefundPrice != nil {
		payload["refund_price"] = *p.RefundPrice
	}
	raw, mErr := json.Marshal(payload)
	if mErr != nil {
		raw = nil
	}
	if _, eErr := h.ticketQueries.InsertOrderEvent(ctx, order.ID, "ticket_refunded", p.Actor, raw); eErr != nil {
		h.logger.Warn("bil24.refund_ticket: order event insert failed",
			slog.String("order_id", order.ID.String()),
			slog.String("error", eErr.Error()),
		)
	}
	return status
}
