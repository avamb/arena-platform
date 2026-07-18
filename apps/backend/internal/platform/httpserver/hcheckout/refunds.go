// refunds.go implements the refund state machine HTTP API (feature #138).
//
// A refund wraps a provider refund operation into a stateful object that
// tracks the full lifecycle from customer request to provider completion.
//
// State machine:
//
//	requested → approved → provider_pending → succeeded|failed|manual_review
//	requested → rejected (terminal)
//	manual_review → succeeded|failed (admin resolves)
//	succeeded|failed|rejected → (terminal)
//
// Endpoints:
//
//	POST /v1/refunds                  — create refund request (refund.create)
//	GET  /v1/refunds/{id}             — read refund state   (refund.read)
//	POST /v1/refunds/{id}/approve     — approve refund      (refund.approve)
//	POST /v1/refunds/{id}/reject      — reject refund       (refund.approve)
//	POST /v1/refunds/webhook          — provider webhook (no JWT auth)
//
// Webhook idempotency: the webhook endpoint records each (provider_refund_id,
// event_type) pair in refund_events. Duplicate deliveries from the provider
// return 204 without reprocessing.
//
// Ticket revocation: when a refund webhook reports 'succeeded', all active
// tickets for the linked checkout session are cancelled automatically via
// CancelTicketsByCheckoutSession.
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// State transition table
// ─────────────────────────────────────────────────────────────────────────────

// validRefundTransitions defines the valid state transitions for the refund
// state machine. Terminal states (succeeded, failed, rejected) map to empty
// sets — no further transitions are allowed.
var validRefundTransitions = map[string]map[string]bool{
	"requested": {
		"approved": true,
		"rejected": true,
	},
	"approved": {
		"provider_pending": true,
	},
	"provider_pending": {
		"succeeded":     true,
		"failed":        true,
		"manual_review": true,
	},
	"manual_review": {
		"succeeded": true,
		"failed":    true,
	},
	"rejected":  {},
	"succeeded": {},
	"failed":    {},
}

// ValidRefundTransitions is the exported form of validRefundTransitions, for use
// by the httpserver shim layer (openapi_refunds_274_test.go references
// validRefundTransitions from package httpserver via checkout_shims.go).
var ValidRefundTransitions = validRefundTransitions

// isTerminalRefundState returns true for states that admit no further transitions.
func isTerminalRefundState(state string) bool {
	targets, exists := validRefundTransitions[state]
	return exists && len(targets) == 0
}

// IsTerminalRefundState is the exported alias of isTerminalRefundState for use
// by the httpserver shim layer (refunds_138_test.go in package httpserver
// references isTerminalRefundState via checkout_shims.go).
func IsTerminalRefundState(state string) bool { return isTerminalRefundState(state) }

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// refundResponse is the JSON representation of a refunds row.
type refundResponse struct {
	ID               string  `json:"id"`
	PaymentIntentID  string  `json:"payment_intent_id"`
	OrgID            string  `json:"org_id"`
	Amount           int64   `json:"amount"`
	Currency         string  `json:"currency"`
	Reason           *string `json:"reason"`
	RequestedBy      *string `json:"requested_by"`
	State            string  `json:"state"`
	ProviderRefundID *string `json:"provider_refund_id"`
	FailureReason    *string `json:"failure_reason"`
	RequestedAt      string  `json:"requested_at"`
	ApprovedAt       *string `json:"approved_at"`
	SucceededAt      *string `json:"succeeded_at"`
	FailedAt         *string `json:"failed_at"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// refundFromRow converts a RefundRow to a refundResponse.
func refundFromRow(r gen.RefundRow) refundResponse {
	resp := refundResponse{
		ID:               r.ID.String(),
		PaymentIntentID:  r.PaymentIntentID.String(),
		OrgID:            r.OrgID.String(),
		Amount:           r.Amount,
		Currency:         r.Currency,
		Reason:           r.Reason,
		RequestedBy:      r.RequestedBy,
		State:            r.State,
		ProviderRefundID: r.ProviderRefundID,
		FailureReason:    r.FailureReason,
		RequestedAt:      r.RequestedAt.UTC().Format(time.RFC3339),
		CreatedAt:        r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.ApprovedAt != nil {
		s := r.ApprovedAt.UTC().Format(time.RFC3339)
		resp.ApprovedAt = &s
	}
	if r.SucceededAt != nil {
		s := r.SucceededAt.UTC().Format(time.RFC3339)
		resp.SucceededAt = &s
	}
	if r.FailedAt != nil {
		s := r.FailedAt.UTC().Format(time.RFC3339)
		resp.FailedAt = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/refunds
// ─────────────────────────────────────────────────────────────────────────────

// createRefundRequest is the request body for POST /v1/refunds.
type createRefundRequest struct {
	PaymentIntentID string  `json:"payment_intent_id"`
	Amount          int64   `json:"amount"`
	Currency        string  `json:"currency"`
	Reason          *string `json:"reason"`
	RequestedBy     *string `json:"requested_by"`
}

// HandleCreateRefund serves POST /v1/refunds.
//
// Creates a new refund request in the 'requested' state after validating:
//   - The payment intent exists and is in the 'succeeded' state.
//   - The refund currency matches the payment intent currency.
//   - The requested amount does not exceed the remaining refundable balance
//     (intent.amount − SUM of all non-failed refunds for that intent).
//
// All three checks are performed inside a single transaction that holds a
// FOR UPDATE lock on the payment_intents row. This serialises concurrent
// refund creation requests so that two simultaneous full-amount refund
// attempts cannot both pass the budget check (feature #361).
//
// Requires JWT + "refund.create" permission.
func (h *Handler) HandleCreateRefund(w http.ResponseWriter, r *http.Request) {
	if h.refundQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.empty_body", "request body is required", r))
		return
	}

	var req createRefundRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate payment_intent_id.
	paymentIntentID, err := uuid.Parse(req.PaymentIntentID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"refund.invalid_payment_intent_id", "payment_intent_id must be a valid UUID", r,
			map[string]any{"field": "payment_intent_id"},
		))
		return
	}

	// Validate amount.
	if req.Amount <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"refund.invalid_amount", "amount must be a positive integer", r,
			map[string]any{"field": "amount"},
		))
		return
	}

	// Validate currency.
	if req.Currency == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"refund.missing_currency", "currency is required", r,
			map[string]any{"field": "currency"},
		))
		return
	}

	if h.paymentIntentQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "payment intent queries not available", r,
		))
		return
	}

	// ── Transactional protection (feature #361) ───────────────────────────────
	// Begin a transaction and lock the payment intent row with FOR UPDATE.
	// This serialises concurrent refund creation so concurrent duplicate
	// full-amount requests cannot both pass the budget check.
	tx, txErr := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if txErr != nil {
		h.logger.Error("refund: begin tx failed", slog.String("error", txErr.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.tx_failed", "failed to begin transaction", r,
		))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txq := h.refundQueries.WithTx(tx)

	// Lock the payment intent row — blocks concurrent refund creation/approval.
	pi, piErr := txq.GetPaymentIntentByIDForUpdate(ctx, paymentIntentID)
	if piErr != nil {
		if errors.Is(piErr, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"refund.payment_intent_not_found", "payment intent not found", r,
			))
			return
		}
		h.logger.Error("refund: payment intent lookup failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.String("error", piErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.pi_lookup_failed", "failed to look up payment intent", r,
		))
		return
	}

	// Guard: the payment intent must be in the 'succeeded' state.
	// Refunding a non-succeeded intent (created, processing, failed, …) is
	// invalid because the charge was never fully captured.
	if pi.State != "succeeded" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.intent_not_succeeded",
			"refunds can only be created for succeeded payment intents",
			r,
			map[string]any{"intent_state": pi.State},
		))
		return
	}

	// Guard: the refund currency must match the payment intent currency.
	if pi.Currency != req.Currency {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.currency_mismatch",
			"refund currency must match payment intent currency",
			r,
			map[string]any{
				"intent_currency":    pi.Currency,
				"requested_currency": req.Currency,
			},
		))
		return
	}

	// Guard: the requested amount must not exceed the remaining refundable balance.
	// remaining = intent.amount − SUM(non-failed refunds for this intent)
	existingSum, sumErr := txq.SumNonFailedRefundsByIntent(ctx, paymentIntentID)
	if sumErr != nil {
		h.logger.Error("refund: sum of existing refunds failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.String("error", sumErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.sum_failed", "failed to compute refunded total", r,
		))
		return
	}
	remaining := pi.Amount - existingSum
	if req.Amount > remaining {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.amount_exceeds_refundable",
			"refund amount exceeds the remaining refundable balance",
			r,
			map[string]any{
				"requested":     req.Amount,
				"remaining":     remaining,
				"intent_amount": pi.Amount,
			},
		))
		return
	}

	// All validations passed — insert the refund within the same transaction.
	refund, err := txq.InsertRefund(ctx,
		paymentIntentID, pi.OrgID, req.Amount, req.Currency, req.Reason, req.RequestedBy,
	)
	if err != nil {
		h.logger.Error("refund: create failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.Int64("amount", req.Amount),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.create_failed", "failed to create refund", r,
		))
		return
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		h.logger.Error("refund: tx commit failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.String("error", commitErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.tx_commit_failed", "failed to commit refund transaction", r,
		))
		return
	}

	h.logger.Info("refund: created",
		slog.String("id", refund.ID.String()),
		slog.String("payment_intent_id", paymentIntentID.String()),
		slog.Int64("amount", refund.Amount),
		slog.String("state", refund.State),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"refund": refundFromRow(refund),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/refunds/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetRefund serves GET /v1/refunds/{id}.
// Returns the current state of a refund.
// Requires JWT + "refund.read" permission.
func (h *Handler) HandleGetRefund(w http.ResponseWriter, r *http.Request) {
	if h.refundQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	refund, err := h.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		h.logger.Error("refund: get failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.get_failed", "failed to retrieve refund", r))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"refund": refundFromRow(refund),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/refunds/{id}/approve
// ─────────────────────────────────────────────────────────────────────────────

// approveRefundRequest is the request body for POST /v1/refunds/{id}/approve.
type approveRefundRequest struct {
	Notes *string `json:"notes"` // optional approval notes
}

// HandleApproveRefund serves POST /v1/refunds/{id}/approve.
//
// Policy: if the refund is partial (amount < payment_intent.amount) AND the
// payment intent has a checkout_session_id AND there exist tickets that are not
// all 'active', the refund transitions directly to 'manual_review' for admin
// resolution. Otherwise it transitions to 'approved' then immediately to
// 'provider_pending' (simulating provider submission).
//
// Over-refund re-validation (feature #361): approval re-validates that:
//   - The payment intent is still in 'succeeded' state.
//   - The refund currency still matches.
//   - The total of all non-failed refunds (including this one) does not exceed
//     the payment intent amount.
//
// This check is performed inside a transaction with a FOR UPDATE lock on the
// payment_intents row to close the TOCTOU window where two concurrent approvals
// could both pass the budget check independently.
//
// Requires JWT + "refund.approve" permission.
func (h *Handler) HandleApproveRefund(w http.ResponseWriter, r *http.Request) {
	if h.refundQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	// Read (optional) body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) > 0 {
		var req approveRefundRequest
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	// ── Transactional protection (feature #361) ───────────────────────────────
	// Wrap the entire approve flow in a transaction that locks the payment
	// intent row. This prevents a race where two concurrent approvals of two
	// full-amount refunds against the same intent both succeed.
	tx, txErr := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if txErr != nil {
		h.logger.Error("refund: approve begin tx failed", slog.String("error", txErr.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.tx_failed", "failed to begin transaction", r,
		))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txq := h.refundQueries.WithTx(tx)

	// Fetch current refund inside the transaction for a consistent view.
	refund, err := txq.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		h.logger.Error("refund: approve fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.fetch_failed", "failed to retrieve refund", r))
		return
	}

	// Guard: only 'requested' refunds can be approved.
	if refund.State != "requested" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.invalid_state",
			"only refunds in 'requested' state can be approved",
			r,
			map[string]any{"current_state": refund.State},
		))
		return
	}

	// Lock the payment intent row to serialise concurrent approve operations.
	pi, piErr := txq.GetPaymentIntentByIDForUpdate(ctx, refund.PaymentIntentID)
	if piErr != nil {
		if errors.Is(piErr, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"refund.payment_intent_not_found", "payment intent not found", r,
			))
			return
		}
		h.logger.Error("refund: approve PI lookup failed",
			slog.String("refund_id", id.String()),
			slog.String("payment_intent_id", refund.PaymentIntentID.String()),
			slog.String("error", piErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.pi_lookup_failed", "failed to look up payment intent", r,
		))
		return
	}

	// Re-validate: intent must still be in 'succeeded' state.
	if pi.State != "succeeded" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.intent_not_succeeded",
			"cannot approve refund: payment intent is no longer in succeeded state",
			r,
			map[string]any{"intent_state": pi.State},
		))
		return
	}

	// Re-validate: currency must match (sanity guard against data corruption).
	if pi.Currency != refund.Currency {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.currency_mismatch",
			"refund currency does not match payment intent currency",
			r,
			map[string]any{
				"intent_currency": pi.Currency,
				"refund_currency": refund.Currency,
			},
		))
		return
	}

	// Re-validate: total non-failed refunds (including this 'requested' one)
	// must not exceed the payment intent amount. This closes the TOCTOU window
	// where two concurrent approvals could both pass independently.
	totalNonFailed, sumErr := txq.SumNonFailedRefundsByIntent(ctx, refund.PaymentIntentID)
	if sumErr != nil {
		h.logger.Error("refund: approve sum failed",
			slog.String("refund_id", id.String()),
			slog.String("error", sumErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.sum_failed", "failed to compute refunded total", r,
		))
		return
	}
	if totalNonFailed > pi.Amount {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.over_refund_detected",
			"approving this refund would exceed the payment intent amount",
			r,
			map[string]any{
				"total_non_failed": totalNonFailed,
				"intent_amount":    pi.Amount,
			},
		))
		return
	}

	// Policy check: determine if manual review is required.
	// Condition: partial refund AND checkout session exists AND some tickets not 'active'.
	needsManualReview := h.refundNeedsManualReviewWithPI(ctx, refund, pi)

	if needsManualReview {
		// Transition directly to manual_review within the same transaction.
		updated, updateErr := txq.UpdateRefundState(ctx, id, "manual_review", nil, nil)
		if updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
				return
			}
			h.logger.Error("refund: approve→manual_review transition failed",
				slog.String("id", id.String()),
				slog.String("error", updateErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"refund.transition_failed", "failed to transition refund to manual_review", r,
			))
			return
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			h.logger.Error("refund: approve→manual_review tx commit failed",
				slog.String("id", id.String()),
				slog.String("error", commitErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"refund.tx_commit_failed", "failed to commit refund transaction", r,
			))
			return
		}
		h.logger.Info("refund: approved → manual_review (partial refund with non-active tickets)",
			slog.String("id", id.String()),
		)
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"refund": refundFromRow(updated),
		})
		return
	}

	// Standard path: requested → approved → provider_pending (all inside the tx).
	approved, approveErr := txq.UpdateRefundState(ctx, id, "approved", nil, nil)
	if approveErr != nil {
		if errors.Is(approveErr, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		h.logger.Error("refund: approved transition failed",
			slog.String("id", id.String()),
			slog.String("error", approveErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.transition_failed", "failed to approve refund", r,
		))
		return
	}

	// Immediately advance to provider_pending (simulating provider submission).
	updated, pendingErr := txq.UpdateRefundState(ctx, approved.ID, "provider_pending", nil, nil)
	if pendingErr != nil {
		// Log but return the approved state — partial progress is still useful.
		h.logger.Error("refund: provider_pending transition failed",
			slog.String("id", id.String()),
			slog.String("error", pendingErr.Error()),
		)
		// Still commit the 'approved' transition.
		_ = tx.Commit(ctx) //nolint:errcheck
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"refund": refundFromRow(approved),
		})
		return
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		h.logger.Error("refund: approve tx commit failed",
			slog.String("id", id.String()),
			slog.String("error", commitErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"refund.tx_commit_failed", "failed to commit refund transaction", r,
		))
		return
	}

	h.logger.Info("refund: approved → provider_pending",
		slog.String("id", id.String()),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"refund": refundFromRow(updated),
	})
}

// refundNeedsManualReviewWithPI is the inner implementation of
// refundNeedsManualReview that accepts an already-fetched PaymentIntentRow.
// Called by the transactional HandleApproveRefund path (feature #361) where the
// PI row has already been locked with FOR UPDATE and passed in as pi.
func (h *Handler) refundNeedsManualReviewWithPI(ctx context.Context, refund gen.RefundRow, pi gen.PaymentIntentRow) bool {
	isPartial := refund.Amount > 0 && refund.Amount < pi.Amount
	if !isPartial || pi.CheckoutSessionID == nil || h.ticketQueries == nil {
		return false
	}
	tickets, err := h.ticketQueries.ListTicketsByCheckoutSession(ctx, *pi.CheckoutSessionID)
	if err != nil || len(tickets) == 0 {
		return false
	}
	for _, t := range tickets {
		if t.Status != "active" {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/refunds/{id}/reject
// ─────────────────────────────────────────────────────────────────────────────

// HandleRejectRefund serves POST /v1/refunds/{id}/reject.
// Transitions the refund from 'requested' to the terminal 'rejected' state.
// Requires JWT + "refund.approve" permission.
func (h *Handler) HandleRejectRefund(w http.ResponseWriter, r *http.Request) {
	if h.refundQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	// Fetch current refund.
	refund, err := h.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		h.logger.Error("refund: reject fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.fetch_failed", "failed to retrieve refund", r))
		return
	}

	// Guard: only 'requested' refunds can be rejected.
	if refund.State != "requested" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"refund.invalid_state",
			"only refunds in 'requested' state can be rejected",
			r,
			map[string]any{"current_state": refund.State},
		))
		return
	}

	updated, err := h.refundQueries.UpdateRefundState(ctx, id, "rejected", nil, nil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		h.logger.Error("refund: reject transition failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund.transition_failed", "failed to reject refund", r))
		return
	}

	h.logger.Info("refund: rejected",
		slog.String("id", id.String()),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"refund": refundFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/refunds/webhook
// ─────────────────────────────────────────────────────────────────────────────

// refundWebhookRequest is the normalized body for POST /v1/refunds/webhook.
type refundWebhookRequest struct {
	// ProviderRefundID identifies the refund at the provider side (e.g. Stripe's re_… string).
	ProviderRefundID string `json:"provider_refund_id"`
	// EventType is the provider event type string.
	EventType string `json:"event_type"`
	// TargetState is the desired new state to transition to. The webhook handler
	// maps EventType → TargetState automatically, but callers may override by
	// supplying this field directly (mock provider tests).
	TargetState string `json:"target_state"`
	// RefundID is the arena refund UUID — required for webhook lookup since we
	// need to map provider_refund_id back to a refund row.
	RefundID string `json:"refund_id"`
	// FailureReason is set when the refund fails.
	FailureReason *string `json:"failure_reason"`
	// EventPayload is the raw provider webhook payload (stored for audit).
	EventPayload json.RawMessage `json:"event_payload"`
}

// refundWebhookEventTypeToState maps normalized provider event types to refund states.
var refundWebhookEventTypeToState = map[string]string{
	"charge.refund.updated": "succeeded", // Stripe: refund succeeded
	"refund.succeeded":      "succeeded",
	"refund.failed":         "failed",
	"refund.manual_review":  "manual_review",
	// Test shorthands
	"mock.refund.succeeded":     "succeeded",
	"mock.refund.failed":        "failed",
	"mock.refund.manual_review": "manual_review",
}

// RefundWebhookEventTypeToState is the exported alias of refundWebhookEventTypeToState
// for use by the httpserver shim layer (refunds_138_test.go in package httpserver
// inspects this map via checkout_shims.go).
var RefundWebhookEventTypeToState = refundWebhookEventTypeToState

// HandleRefundWebhook serves POST /v1/refunds/webhook.
//
// This endpoint is intentionally unauthenticated via JWT — payment providers
// deliver webhooks from their own infrastructure. Authentication is performed
// via HMAC signature verification (Stripe-Signature or X-AllPay-Signature
// headers) before the body is parsed or any database state is mutated.
//
// Idempotency: each (provider_refund_id, event_type) is recorded in
// refund_events with a UNIQUE constraint. Duplicate deliveries return 204.
//
// Ticket revocation: when a refund succeeds, all active tickets for the linked
// checkout session are cancelled via CancelTicketsByCheckoutSession.
func (h *Handler) HandleRefundWebhook(w http.ResponseWriter, r *http.Request) {
	// Read body first so the HMAC can be verified over the raw bytes.
	// The body is intentionally read BEFORE the DB nil guard so that forged
	// requests are rejected with 401 without leaking service availability.
	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.empty_body", "request body is required", r))
		return
	}

	// Verify provider HMAC signature before processing the body. This prevents
	// forged refund events that could cancel legitimate tickets.
	// Returns nil in dev/mock mode (no secrets); production requires a secret.
	if sigErr := h.verifyWebhookSignature(r, body); sigErr != nil {
		if h.logger != nil {
			h.logger.Warn("refund_webhook: invalid signature; rejecting request",
				slog.String("error", sigErr.Error()),
				slog.String("remote_addr", r.RemoteAddr),
			)
		}
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"webhook.invalid_signature", "webhook signature verification failed", r,
		))
		return
	}

	if h.refundQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	var req refundWebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.ProviderRefundID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.missing_provider_refund_id", "provider_refund_id is required", r))
		return
	}
	if req.EventType == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.missing_event_type", "event_type is required", r))
		return
	}
	if req.RefundID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.missing_refund_id", "refund_id is required", r))
		return
	}

	// Parse refund_id.
	refundID, err := uuid.Parse(req.RefundID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("refund_webhook.invalid_refund_id", "refund_id must be a valid UUID", r))
		return
	}

	// Resolve target state.
	targetState := req.TargetState
	if targetState == "" {
		mapped, ok := refundWebhookEventTypeToState[req.EventType]
		if !ok {
			// Unknown event type — acknowledge without processing.
			httputil.WriteJSON(w, http.StatusOK, map[string]any{
				"acknowledged": true,
				"event_type":   req.EventType,
				"processed":    false,
				"reason":       "unknown event type; no state transition performed",
			})
			return
		}
		targetState = mapped
	}

	// Look up the refund by ID.
	refund, err := h.refundQueries.GetRefundByID(ctx, refundID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("refund_webhook.refund_not_found", "no refund found for refund_id", r))
			return
		}
		h.logger.Error("refund_webhook: refund lookup failed",
			slog.String("refund_id", refundID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund_webhook.lookup_failed", "failed to locate refund", r))
		return
	}

	// Idempotency check: record the event (ON CONFLICT DO NOTHING).
	// If pgx.ErrNoRows is returned, the event was already processed → 204.
	var eventPayload []byte
	if req.EventPayload != nil {
		eventPayload, _ = json.Marshal(req.EventPayload)
	}
	_, evtErr := h.refundQueries.InsertRefundEvent(ctx,
		refund.ID, req.ProviderRefundID, req.EventType, eventPayload, &targetState,
	)
	if errors.Is(evtErr, pgx.ErrNoRows) {
		// Duplicate event delivery — already processed.
		h.logger.Info("refund_webhook: duplicate event; skipping",
			slog.String("provider_refund_id", req.ProviderRefundID),
			slog.String("event_type", req.EventType),
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if evtErr != nil {
		h.logger.Error("refund_webhook: event record failed",
			slog.String("provider_refund_id", req.ProviderRefundID),
			slog.String("event_type", req.EventType),
			slog.String("error", evtErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund_webhook.event_record_failed", "failed to record webhook event", r))
		return
	}

	// Apply state transition if valid.
	currentState := refund.State
	if isTerminalRefundState(currentState) {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "refund is already in a terminal state",
		})
		return
	}

	validTargets := validRefundTransitions[currentState]
	if !validTargets[targetState] {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "state transition not valid from current state",
		})
		return
	}

	updated, updateErr := h.refundQueries.UpdateRefundState(ctx,
		refund.ID, targetState, &req.ProviderRefundID, req.FailureReason,
	)
	if updateErr != nil {
		h.logger.Error("refund_webhook: state update failed",
			slog.String("id", refund.ID.String()),
			slog.String("target_state", targetState),
			slog.String("error", updateErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("refund_webhook.state_update_failed", "failed to update refund state", r))
		return
	}

	h.logger.Info("refund_webhook: state transitioned",
		slog.String("id", refund.ID.String()),
		slog.String("provider_refund_id", req.ProviderRefundID),
		slog.String("event_type", req.EventType),
		slog.String("from", currentState),
		slog.String("to", updated.State),
	)

	// On succeeded: cancel active tickets for the linked checkout session.
	if updated.State == "succeeded" && h.paymentIntentQueries != nil {
		pi, piErr := h.paymentIntentQueries.GetPaymentIntentByID(ctx, updated.PaymentIntentID)
		if piErr != nil {
			h.logger.Error("refund_webhook: payment intent lookup failed for ticket cancellation",
				slog.String("refund_id", updated.ID.String()),
				slog.String("payment_intent_id", updated.PaymentIntentID.String()),
				slog.String("error", piErr.Error()),
			)
		} else if pi.CheckoutSessionID != nil {
			cancelled, cancelErr := h.refundQueries.CancelTicketsByCheckoutSession(ctx, *pi.CheckoutSessionID)
			if cancelErr != nil {
				h.logger.Error("refund_webhook: ticket cancellation failed",
					slog.String("refund_id", updated.ID.String()),
					slog.String("checkout_session_id", pi.CheckoutSessionID.String()),
					slog.String("error", cancelErr.Error()),
				)
			} else {
				h.logger.Info("refund_webhook: tickets cancelled on refund success",
					slog.String("refund_id", updated.ID.String()),
					slog.String("checkout_session_id", pi.CheckoutSessionID.String()),
					slog.Int64("cancelled_count", cancelled),
				)
				// Publish Bil24-compatible scanner refund events (feature #143).
				// Best-effort: errors are logged internally, not returned.
				if h.publishRefunded != nil {
					h.publishRefunded(ctx,
						pi.CheckoutSessionID.String(),
						updated.ID.String(),
						updated.Currency,
						updated.Amount,
					)
				}

				// Publish generic per-ticket v1.ticket.refunded events for the
				// webhook event catalog (feature S-1).  Best-effort: errors are
				// logged inside publishScannerEvent.  Listing tickets here is
				// fine because CancelTicketsByCheckoutSession has just updated
				// them to "cancelled"; the listed rows therefore reflect the
				// post-cancel state and carry the canonical ticket UUIDs that
				// scanner subscribers need to identify the revoked entries.
				if h.ticketQueries != nil && cancelled > 0 && h.publishRefundedV1 != nil {
					tickets, listErr := h.ticketQueries.ListTicketsByCheckoutSession(ctx, *pi.CheckoutSessionID)
					if listErr != nil {
						h.logger.Warn("refund_webhook: list tickets for v1.ticket.refunded events failed",
							slog.String("checkout_session_id", pi.CheckoutSessionID.String()),
							slog.String("error", listErr.Error()),
						)
					} else {
						ticketIDs := make([]string, 0, len(tickets))
						for _, t := range tickets {
							ticketIDs = append(ticketIDs, t.ID.String())
						}
						h.publishRefundedV1(ctx,
							ticketIDs,
							pi.CheckoutSessionID.String(),
							updated.ID.String(),
							updated.Currency,
							updated.Amount,
						)
					}
				}
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"event_type":   req.EventType,
		"processed":    true,
		"refund":       refundFromRow(updated),
	})
}
