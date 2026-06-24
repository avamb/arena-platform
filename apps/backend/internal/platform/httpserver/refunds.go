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
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// isTerminalRefundState returns true for states that admit no further transitions.
func isTerminalRefundState(state string) bool {
	targets, exists := validRefundTransitions[state]
	return exists && len(targets) == 0
}

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

// handleCreateRefund serves POST /v1/refunds.
// Creates a new refund request in the 'requested' state.
// Requires JWT + "refund.create" permission.
func (s *Server) handleCreateRefund(w http.ResponseWriter, r *http.Request) {
	if s.refundQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.empty_body", "request body is required", r))
		return
	}

	var req createRefundRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate payment_intent_id.
	paymentIntentID, err := uuid.Parse(req.PaymentIntentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"refund.invalid_payment_intent_id", "payment_intent_id must be a valid UUID", r,
			map[string]any{"field": "payment_intent_id"},
		))
		return
	}

	// Validate amount.
	if req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"refund.invalid_amount", "amount must be a positive integer", r,
			map[string]any{"field": "amount"},
		))
		return
	}

	// Validate currency.
	if req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"refund.missing_currency", "currency is required", r,
			map[string]any{"field": "currency"},
		))
		return
	}

	// Look up the payment intent to get org_id.
	if s.paymentIntentQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "payment intent queries not available", r,
		))
		return
	}
	pi, err := s.paymentIntentQueries.GetPaymentIntentByID(ctx, paymentIntentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.payment_intent_not_found", "payment intent not found", r))
			return
		}
		s.logger.Error("refund: payment intent lookup failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.pi_lookup_failed", "failed to look up payment intent", r))
		return
	}

	refund, err := s.refundQueries.InsertRefund(ctx,
		paymentIntentID, pi.OrgID, req.Amount, req.Currency, req.Reason, req.RequestedBy,
	)
	if err != nil {
		s.logger.Error("refund: create failed",
			slog.String("payment_intent_id", paymentIntentID.String()),
			slog.Int64("amount", req.Amount),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"refund.create_failed", "failed to create refund", r,
		))
		return
	}

	s.logger.Info("refund: created",
		slog.String("id", refund.ID.String()),
		slog.String("payment_intent_id", paymentIntentID.String()),
		slog.Int64("amount", refund.Amount),
		slog.String("state", refund.State),
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"refund": refundFromRow(refund),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/refunds/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetRefund serves GET /v1/refunds/{id}.
// Returns the current state of a refund.
// Requires JWT + "refund.read" permission.
func (s *Server) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	if s.refundQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	refund, err := s.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		s.logger.Error("refund: get failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.get_failed", "failed to retrieve refund", r))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
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

// handleApproveRefund serves POST /v1/refunds/{id}/approve.
//
// Policy: if the refund is partial (amount < payment_intent.amount) AND the
// payment intent has a checkout_session_id AND there exist tickets that are not
// all 'active', the refund transitions directly to 'manual_review' for admin
// resolution. Otherwise it transitions to 'approved' then immediately to
// 'provider_pending' (simulating provider submission).
//
// Requires JWT + "refund.approve" permission.
func (s *Server) handleApproveRefund(w http.ResponseWriter, r *http.Request) {
	if s.refundQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	// Read (optional) body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) > 0 {
		var req approveRefundRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_json", "request body is not valid JSON", r))
			return
		}
	}

	// Fetch current refund.
	refund, err := s.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		s.logger.Error("refund: approve fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.fetch_failed", "failed to retrieve refund", r))
		return
	}

	// Guard: only 'requested' refunds can be approved.
	if refund.State != "requested" {
		writeJSON(w, http.StatusConflict, errorEnvelopeWithDetails(
			"refund.invalid_state",
			"only refunds in 'requested' state can be approved",
			r,
			map[string]any{"current_state": refund.State},
		))
		return
	}

	// Policy check: determine if manual review is required.
	// Condition: partial refund AND checkout session exists AND some tickets not 'active'.
	needsManualReview := s.refundNeedsManualReview(ctx, refund)

	if needsManualReview {
		// Transition directly to manual_review.
		updated, updateErr := s.refundQueries.UpdateRefundState(ctx, id, "manual_review", nil, nil)
		if updateErr != nil {
			if errors.Is(updateErr, pgx.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
				return
			}
			s.logger.Error("refund: approve→manual_review transition failed",
				slog.String("id", id.String()),
				slog.String("error", updateErr.Error()),
			)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.transition_failed", "failed to transition refund to manual_review", r))
			return
		}
		s.logger.Info("refund: approved → manual_review (partial refund with non-active tickets)",
			slog.String("id", id.String()),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"refund": refundFromRow(updated),
		})
		return
	}

	// Standard path: requested → approved → provider_pending.
	approved, approveErr := s.refundQueries.UpdateRefundState(ctx, id, "approved", nil, nil)
	if approveErr != nil {
		if errors.Is(approveErr, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		s.logger.Error("refund: approved transition failed",
			slog.String("id", id.String()),
			slog.String("error", approveErr.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.transition_failed", "failed to approve refund", r))
		return
	}

	// Immediately advance to provider_pending (simulating provider submission).
	updated, pendingErr := s.refundQueries.UpdateRefundState(ctx, approved.ID, "provider_pending", nil, nil)
	if pendingErr != nil {
		// Log but return the approved state — partial progress is still useful.
		s.logger.Error("refund: provider_pending transition failed",
			slog.String("id", id.String()),
			slog.String("error", pendingErr.Error()),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"refund": refundFromRow(approved),
		})
		return
	}

	s.logger.Info("refund: approved → provider_pending",
		slog.String("id", id.String()),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"refund": refundFromRow(updated),
	})
}

// refundNeedsManualReview checks whether an approval should route to manual_review.
// Returns true when the refund is partial AND the payment intent has a checkout
// session AND at least one ticket is not in 'active' status (e.g. scanned/used).
func (s *Server) refundNeedsManualReview(ctx context.Context, refund gen.RefundRow) bool {
	if s.paymentIntentQueries == nil {
		return false
	}
	pi, err := s.paymentIntentQueries.GetPaymentIntentByID(ctx, refund.PaymentIntentID)
	if err != nil {
		return false
	}
	isPartial := refund.Amount > 0 && refund.Amount < pi.Amount
	if !isPartial || pi.CheckoutSessionID == nil || s.ticketQueries == nil {
		return false
	}
	tickets, err := s.ticketQueries.ListTicketsByCheckoutSession(ctx, *pi.CheckoutSessionID)
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

// handleRejectRefund serves POST /v1/refunds/{id}/reject.
// Transitions the refund from 'requested' to the terminal 'rejected' state.
// Requires JWT + "refund.approve" permission.
func (s *Server) handleRejectRefund(w http.ResponseWriter, r *http.Request) {
	if s.refundQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund.invalid_id", "refund id must be a valid UUID", r))
		return
	}

	// Fetch current refund.
	refund, err := s.refundQueries.GetRefundByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		s.logger.Error("refund: reject fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.fetch_failed", "failed to retrieve refund", r))
		return
	}

	// Guard: only 'requested' refunds can be rejected.
	if refund.State != "requested" {
		writeJSON(w, http.StatusConflict, errorEnvelopeWithDetails(
			"refund.invalid_state",
			"only refunds in 'requested' state can be rejected",
			r,
			map[string]any{"current_state": refund.State},
		))
		return
	}

	updated, err := s.refundQueries.UpdateRefundState(ctx, id, "rejected", nil, nil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund.not_found", "refund not found", r))
			return
		}
		s.logger.Error("refund: reject transition failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund.transition_failed", "failed to reject refund", r))
		return
	}

	s.logger.Info("refund: rejected",
		slog.String("id", id.String()),
	)

	writeJSON(w, http.StatusOK, map[string]any{
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

// handleRefundWebhook serves POST /v1/refunds/webhook.
//
// This endpoint is intentionally unauthenticated — payment providers deliver
// webhooks from their own infrastructure. For this foundation milestone the
// endpoint accepts a normalized JSON body without signature verification.
//
// Idempotency: each (provider_refund_id, event_type) is recorded in
// refund_events with a UNIQUE constraint. Duplicate deliveries return 204.
//
// Ticket revocation: when a refund succeeds, all active tickets for the linked
// checkout session are cancelled via CancelTicketsByCheckoutSession.
func (s *Server) handleRefundWebhook(w http.ResponseWriter, r *http.Request) {
	if s.refundQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.empty_body", "request body is required", r))
		return
	}

	var req refundWebhookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.ProviderRefundID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.missing_provider_refund_id", "provider_refund_id is required", r))
		return
	}
	if req.EventType == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.missing_event_type", "event_type is required", r))
		return
	}
	if req.RefundID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.missing_refund_id", "refund_id is required", r))
		return
	}

	// Parse refund_id.
	refundID, err := uuid.Parse(req.RefundID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("refund_webhook.invalid_refund_id", "refund_id must be a valid UUID", r))
		return
	}

	// Resolve target state.
	targetState := req.TargetState
	if targetState == "" {
		mapped, ok := refundWebhookEventTypeToState[req.EventType]
		if !ok {
			// Unknown event type — acknowledge without processing.
			writeJSON(w, http.StatusOK, map[string]any{
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
	refund, err := s.refundQueries.GetRefundByID(ctx, refundID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("refund_webhook.refund_not_found", "no refund found for refund_id", r))
			return
		}
		s.logger.Error("refund_webhook: refund lookup failed",
			slog.String("refund_id", refundID.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund_webhook.lookup_failed", "failed to locate refund", r))
		return
	}

	// Idempotency check: record the event (ON CONFLICT DO NOTHING).
	// If pgx.ErrNoRows is returned, the event was already processed → 204.
	var eventPayload []byte
	if req.EventPayload != nil {
		eventPayload, _ = json.Marshal(req.EventPayload)
	}
	_, evtErr := s.refundQueries.InsertRefundEvent(ctx,
		refund.ID, req.ProviderRefundID, req.EventType, eventPayload, &targetState,
	)
	if errors.Is(evtErr, pgx.ErrNoRows) {
		// Duplicate event delivery — already processed.
		s.logger.Info("refund_webhook: duplicate event; skipping",
			slog.String("provider_refund_id", req.ProviderRefundID),
			slog.String("event_type", req.EventType),
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if evtErr != nil {
		s.logger.Error("refund_webhook: event record failed",
			slog.String("provider_refund_id", req.ProviderRefundID),
			slog.String("event_type", req.EventType),
			slog.String("error", evtErr.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund_webhook.event_record_failed", "failed to record webhook event", r))
		return
	}

	// Apply state transition if valid.
	currentState := refund.State
	if isTerminalRefundState(currentState) {
		writeJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "refund is already in a terminal state",
		})
		return
	}

	validTargets := validRefundTransitions[currentState]
	if !validTargets[targetState] {
		writeJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "state transition not valid from current state",
		})
		return
	}

	updated, updateErr := s.refundQueries.UpdateRefundState(ctx,
		refund.ID, targetState, &req.ProviderRefundID, req.FailureReason,
	)
	if updateErr != nil {
		s.logger.Error("refund_webhook: state update failed",
			slog.String("id", refund.ID.String()),
			slog.String("target_state", targetState),
			slog.String("error", updateErr.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("refund_webhook.state_update_failed", "failed to update refund state", r))
		return
	}

	s.logger.Info("refund_webhook: state transitioned",
		slog.String("id", refund.ID.String()),
		slog.String("provider_refund_id", req.ProviderRefundID),
		slog.String("event_type", req.EventType),
		slog.String("from", currentState),
		slog.String("to", updated.State),
	)

	// On succeeded: cancel active tickets for the linked checkout session.
	if updated.State == "succeeded" && s.paymentIntentQueries != nil {
		pi, piErr := s.paymentIntentQueries.GetPaymentIntentByID(ctx, updated.PaymentIntentID)
		if piErr != nil {
			s.logger.Error("refund_webhook: payment intent lookup failed for ticket cancellation",
				slog.String("refund_id", updated.ID.String()),
				slog.String("payment_intent_id", updated.PaymentIntentID.String()),
				slog.String("error", piErr.Error()),
			)
		} else if pi.CheckoutSessionID != nil {
			cancelled, cancelErr := s.refundQueries.CancelTicketsByCheckoutSession(ctx, *pi.CheckoutSessionID)
			if cancelErr != nil {
				s.logger.Error("refund_webhook: ticket cancellation failed",
					slog.String("refund_id", updated.ID.String()),
					slog.String("checkout_session_id", pi.CheckoutSessionID.String()),
					slog.String("error", cancelErr.Error()),
				)
			} else {
				s.logger.Info("refund_webhook: tickets cancelled on refund success",
					slog.String("refund_id", updated.ID.String()),
					slog.String("checkout_session_id", pi.CheckoutSessionID.String()),
					slog.Int64("cancelled_count", cancelled),
				)
				// Publish Bil24-compatible scanner refund events (feature #143).
				// Best-effort: errors are logged internally, not returned.
				s.publishTicketRefundedEvents(ctx,
					pi.CheckoutSessionID.String(),
					updated.ID.String(),
					updated.Currency,
					updated.Amount,
				)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"event_type":   req.EventType,
		"processed":    true,
		"refund":       refundFromRow(updated),
	})
}
