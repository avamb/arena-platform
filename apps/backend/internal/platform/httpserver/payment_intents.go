// payment_intents.go implements the payment intent state machine HTTP API (feature #137).
//
// A payment intent wraps a provider payment operation into a stateful object
// that tracks the full lifecycle including SCA/3DS challenges.
//
// State machine:
//
//	created → requires_action|processing
//	requires_action → processing|failed
//	processing → authorized|succeeded|failed|manual_review
//	authorized → succeeded|failed
//	manual_review → succeeded|failed
//	succeeded|failed → (terminal)
//
// Endpoints:
//
//	POST /v1/payment-intents            — create intent (payment_intent.create)
//	GET  /v1/payment-intents/{id}       — read intent   (payment_intent.read)
//	POST /v1/payment-intents/{id}/transition — advance state (payment_intent.update)
//	POST /v1/payment-intents/webhook    — provider webhook (no JWT auth)
//
// Webhook idempotency: the webhook endpoint records each (provider_payment_id,
// event_type) pair in payment_intent_events. Duplicate deliveries from the
// provider return 204 without reprocessing.
package httpserver

import (
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

// validPaymentIntentTransitions defines the valid state transitions for the
// payment intent state machine. Terminal states (succeeded, failed) map to
// empty sets — no further transitions are allowed.
var validPaymentIntentTransitions = map[string]map[string]bool{
	"created": {
		"requires_action": true,
		"processing":      true,
	},
	"requires_action": {
		"processing": true,
		"failed":     true,
	},
	"processing": {
		"authorized":    true,
		"succeeded":     true,
		"failed":        true,
		"manual_review": true,
	},
	"authorized": {
		"succeeded": true,
		"failed":    true,
	},
	"manual_review": {
		"succeeded": true,
		"failed":    true,
	},
	"succeeded": {},
	"failed":    {},
}

// isTerminalPaymentIntentState returns true for states that admit no further
// transitions (succeeded and failed).
func isTerminalPaymentIntentState(state string) bool {
	_, exists := validPaymentIntentTransitions[state]
	return exists && len(validPaymentIntentTransitions[state]) == 0
}

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// paymentIntentResponse is the JSON representation of a payment_intents row.
type paymentIntentResponse struct {
	ID                string  `json:"id"`
	CheckoutSessionID *string `json:"checkout_session_id"`
	OrgID             string  `json:"org_id"`
	Provider          string  `json:"provider"`
	ProviderPaymentID *string `json:"provider_payment_id"`
	Amount            int64   `json:"amount"`
	Currency          string  `json:"currency"`
	State             string  `json:"state"`
	ScaRedirectURL    *string `json:"sca_redirect_url"`
	ClientSecret      *string `json:"client_secret"`
	FailureCode       *string `json:"failure_code"`
	FailureMessage    *string `json:"failure_message"`
	AuthorizedAt      *string `json:"authorized_at"`
	SucceededAt       *string `json:"succeeded_at"`
	FailedAt          *string `json:"failed_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// paymentIntentFromRow converts a PaymentIntentRow to a paymentIntentResponse.
func paymentIntentFromRow(pi gen.PaymentIntentRow) paymentIntentResponse {
	resp := paymentIntentResponse{
		ID:                pi.ID.String(),
		OrgID:             pi.OrgID.String(),
		Provider:          pi.Provider,
		ProviderPaymentID: pi.ProviderPaymentID,
		Amount:            pi.Amount,
		Currency:          pi.Currency,
		State:             pi.State,
		ScaRedirectURL:    pi.ScaRedirectURL,
		ClientSecret:      pi.ClientSecret,
		FailureCode:       pi.FailureCode,
		FailureMessage:    pi.FailureMessage,
		CreatedAt:         pi.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         pi.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if pi.CheckoutSessionID != nil {
		s := pi.CheckoutSessionID.String()
		resp.CheckoutSessionID = &s
	}
	if pi.AuthorizedAt != nil {
		s := pi.AuthorizedAt.UTC().Format(time.RFC3339)
		resp.AuthorizedAt = &s
	}
	if pi.SucceededAt != nil {
		s := pi.SucceededAt.UTC().Format(time.RFC3339)
		resp.SucceededAt = &s
	}
	if pi.FailedAt != nil {
		s := pi.FailedAt.UTC().Format(time.RFC3339)
		resp.FailedAt = &s
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/payment-intents
// ─────────────────────────────────────────────────────────────────────────────

// createPaymentIntentRequest is the request body for POST /v1/payment-intents.
type createPaymentIntentRequest struct {
	CheckoutSessionID *string `json:"checkout_session_id"` // optional
	OrgID             string  `json:"org_id"`
	Provider          string  `json:"provider"`
	ProviderPaymentID *string `json:"provider_payment_id"` // optional; may be set later
	Amount            int64   `json:"amount"`
	Currency          string  `json:"currency"`
	// InitialState defaults to "created". Pass "requires_action" to create an
	// intent that immediately requires an SCA challenge (e.g. Stripe's 3DS).
	InitialState   string  `json:"initial_state"`
	ScaRedirectURL *string `json:"sca_redirect_url"` // optional; set for requires_action
	ClientSecret   *string `json:"client_secret"`    // optional; for SDK-based SCA
}

// handleCreatePaymentIntent serves POST /v1/payment-intents.
// Creates a new payment intent linked to an optional checkout session.
// Requires JWT + "payment_intent.create" permission.
func (s *Server) handleCreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	if s.paymentIntentQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.empty_body", "request body is required", r))
		return
	}

	var req createPaymentIntentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate required fields.
	orgID, err := uuid.Parse(req.OrgID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.invalid_org_id", "org_id must be a valid UUID", r,
			map[string]any{"field": "org_id"},
		))
		return
	}
	if req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.missing_provider", "provider is required", r,
			map[string]any{"field": "provider"},
		))
		return
	}
	if req.Amount < 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.invalid_amount", "amount must be a non-negative integer", r,
			map[string]any{"field": "amount"},
		))
		return
	}
	if req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.missing_currency", "currency is required", r,
			map[string]any{"field": "currency"},
		))
		return
	}

	// Validate initial state when provided.
	if req.InitialState != "" {
		if _, ok := validPaymentIntentTransitions[req.InitialState]; !ok {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"payment_intent.invalid_initial_state",
				"initial_state must be one of: created, requires_action, processing",
				r,
				map[string]any{"field": "initial_state"},
			))
			return
		}
		// Only non-terminal initial states are valid for creation.
		if isTerminalPaymentIntentState(req.InitialState) {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"payment_intent.invalid_initial_state",
				"cannot create a payment intent in a terminal state",
				r,
				map[string]any{"field": "initial_state"},
			))
			return
		}
	}

	// Validate optional checkout_session_id.
	var checkoutSessionID *uuid.UUID
	if req.CheckoutSessionID != nil {
		parsed, err := uuid.Parse(*req.CheckoutSessionID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"payment_intent.invalid_checkout_session_id",
				"checkout_session_id must be a valid UUID when provided", r,
				map[string]any{"field": "checkout_session_id"},
			))
			return
		}
		checkoutSessionID = &parsed
	}

	pi, err := s.paymentIntentQueries.InsertPaymentIntent(ctx,
		checkoutSessionID, orgID, req.Provider, req.ProviderPaymentID,
		req.Amount, req.Currency, req.InitialState,
		req.ScaRedirectURL, req.ClientSecret,
	)
	if err != nil {
		s.logger.Error("payment_intent: create failed",
			slog.String("org_id", orgID.String()),
			slog.String("provider", req.Provider),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_intent.create_failed", "failed to create payment intent", r,
		))
		return
	}

	s.logger.Info("payment_intent: created",
		slog.String("id", pi.ID.String()),
		slog.String("provider", pi.Provider),
		slog.String("state", pi.State),
		slog.Int64("amount", pi.Amount),
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"payment_intent": paymentIntentFromRow(pi),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/payment-intents/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetPaymentIntent serves GET /v1/payment-intents/{id}.
// Returns the current state of a payment intent.
// Requires JWT + "payment_intent.read" permission.
func (s *Server) handleGetPaymentIntent(w http.ResponseWriter, r *http.Request) {
	if s.paymentIntentQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_id", "payment intent id must be a valid UUID", r))
		return
	}

	pi, err := s.paymentIntentQueries.GetPaymentIntentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		s.logger.Error("payment_intent: get failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("payment_intent.get_failed", "failed to retrieve payment intent", r))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"payment_intent": paymentIntentFromRow(pi),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/payment-intents/{id}/transition
// ─────────────────────────────────────────────────────────────────────────────

// transitionPaymentIntentRequest is the request body for POST /v1/payment-intents/{id}/transition.
type transitionPaymentIntentRequest struct {
	// State is the target state (required).
	State string `json:"state"`
	// ScaRedirectURL is the 3DS redirect URL (set when transitioning to requires_action).
	ScaRedirectURL *string `json:"sca_redirect_url"`
	// ClientSecret is the provider's client secret (set when transitioning to requires_action).
	ClientSecret *string `json:"client_secret"`
	// FailureCode is a structured error code (set when transitioning to failed).
	FailureCode *string `json:"failure_code"`
	// FailureMessage is a human-readable error message (set when transitioning to failed).
	FailureMessage *string `json:"failure_message"`
	// ProviderPaymentID can be set on the first callback if not known at creation time.
	ProviderPaymentID *string `json:"provider_payment_id"`
}

// handleTransitionPaymentIntent serves POST /v1/payment-intents/{id}/transition.
// Validates the requested state transition against the state machine, then
// persists the new state.
// Returns 409 when the transition is not valid from the current state.
// Returns 409 when the intent is already in a terminal state.
// Requires JWT + "payment_intent.update" permission.
func (s *Server) handleTransitionPaymentIntent(w http.ResponseWriter, r *http.Request) {
	if s.paymentIntentQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_id", "payment intent id must be a valid UUID", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.empty_body", "request body is required", r))
		return
	}

	var req transitionPaymentIntentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_intent.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.State == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.missing_state", "state is required", r,
			map[string]any{"field": "state"},
		))
		return
	}

	// Fetch current state to validate the transition.
	current, err := s.paymentIntentQueries.GetPaymentIntentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		s.logger.Error("payment_intent: transition fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("payment_intent.fetch_failed", "failed to retrieve payment intent", r))
		return
	}

	// Guard: reject transitions from terminal states.
	if isTerminalPaymentIntentState(current.State) {
		writeJSON(w, http.StatusConflict, errorEnvelopeWithDetails(
			"payment_intent.terminal_state",
			"payment intent is in a terminal state and cannot be transitioned",
			r,
			map[string]any{
				"current_state":  current.State,
				"requested_state": req.State,
			},
		))
		return
	}

	// Guard: validate the transition.
	validTargets, ok := validPaymentIntentTransitions[current.State]
	if !ok || !validTargets[req.State] {
		writeJSON(w, http.StatusConflict, errorEnvelopeWithDetails(
			"payment_intent.invalid_transition",
			"requested state transition is not valid from the current state",
			r,
			map[string]any{
				"current_state":  current.State,
				"requested_state": req.State,
			},
		))
		return
	}

	// Validate SCA fields when transitioning to requires_action.
	if req.State == "requires_action" && req.ScaRedirectURL == nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_intent.missing_sca_redirect_url",
			"sca_redirect_url is required when transitioning to requires_action",
			r,
			map[string]any{"field": "sca_redirect_url"},
		))
		return
	}

	// Persist the transition.
	updated, err := s.paymentIntentQueries.UpdatePaymentIntentState(ctx,
		id, req.State,
		req.ScaRedirectURL, req.ClientSecret,
		req.FailureCode, req.FailureMessage,
		req.ProviderPaymentID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		s.logger.Error("payment_intent: transition failed",
			slog.String("id", id.String()),
			slog.String("target_state", req.State),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("payment_intent.transition_failed", "failed to transition payment intent", r))
		return
	}

	s.logger.Info("payment_intent: state transitioned",
		slog.String("id", id.String()),
		slog.String("from", current.State),
		slog.String("to", updated.State),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"payment_intent": paymentIntentFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/payment-intents/webhook
// ─────────────────────────────────────────────────────────────────────────────

// webhookPaymentIntentRequest is the normalized body for POST /v1/payment-intents/webhook.
// Real deployments should verify provider-specific HMAC/signature before parsing.
type webhookPaymentIntentRequest struct {
	// ProviderPaymentID identifies the payment intent at the provider side.
	ProviderPaymentID string `json:"provider_payment_id"`
	// EventType is the provider event type string
	// (e.g. "payment_intent.succeeded", "payment_intent.requires_action").
	EventType string `json:"event_type"`
	// TargetState is the desired new state to transition to.
	// The webhook handler maps EventType → TargetState automatically, but
	// callers may override by supplying this field directly (mock provider tests).
	TargetState string `json:"target_state"`
	// Optional supplemental fields forwarded to UpdatePaymentIntentState.
	ScaRedirectURL *string `json:"sca_redirect_url"`
	ClientSecret   *string `json:"client_secret"`
	FailureCode    *string `json:"failure_code"`
	FailureMessage *string `json:"failure_message"`
	// EventPayload is the raw provider webhook payload (stored for audit).
	EventPayload json.RawMessage `json:"event_payload"`
}

// webhookEventTypeToState maps normalized provider event types to payment intent states.
// This covers the common Stripe-compatible event type strings; real deployments
// should extend or override this map per-provider.
var webhookEventTypeToState = map[string]string{
	"payment_intent.requires_action":  "requires_action",
	"payment_intent.processing":       "processing",
	"payment_intent.amount_capturable": "authorized",
	"payment_intent.succeeded":        "succeeded",
	"payment_intent.payment_failed":   "failed",
	"payment_intent.manual_review":    "manual_review",
	// Shorthand aliases used by mock provider tests.
	"mock.requires_action": "requires_action",
	"mock.processing":      "processing",
	"mock.authorized":      "authorized",
	"mock.succeeded":       "succeeded",
	"mock.failed":          "failed",
	"mock.manual_review":   "manual_review",
}

// handlePaymentIntentWebhook serves POST /v1/payment-intents/webhook.
//
// This endpoint is intentionally unauthenticated — payment providers deliver
// webhooks from their own infrastructure and authenticate via HMAC signatures
// in production (Stripe-Signature header). For this foundation milestone the
// endpoint accepts a normalized JSON body without signature verification.
//
// Idempotency: each (provider_payment_id, event_type) is recorded in
// payment_intent_events with a UNIQUE constraint. Duplicate deliveries return
// 204 without reprocessing.
func (s *Server) handlePaymentIntentWebhook(w http.ResponseWriter, r *http.Request) {
	if s.paymentIntentQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("webhook.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("webhook.empty_body", "request body is required", r))
		return
	}

	var req webhookPaymentIntentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("webhook.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.ProviderPaymentID == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("webhook.missing_provider_payment_id", "provider_payment_id is required", r))
		return
	}
	if req.EventType == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("webhook.missing_event_type", "event_type is required", r))
		return
	}

	// Resolve target state.
	targetState := req.TargetState
	if targetState == "" {
		mapped, ok := webhookEventTypeToState[req.EventType]
		if !ok {
			// Unknown event type — acknowledge without processing (common for
			// provider events we don't handle, e.g. "payment_intent.created").
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

	// Look up the payment intent by provider ID.
	pi, err := s.paymentIntentQueries.GetPaymentIntentByProviderID(ctx, req.ProviderPaymentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("webhook.intent_not_found", "no payment intent found for provider_payment_id", r))
			return
		}
		s.logger.Error("webhook: intent lookup failed",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("webhook.lookup_failed", "failed to locate payment intent", r))
		return
	}

	// Idempotency check: record the event (ON CONFLICT DO NOTHING).
	// If pgx.ErrNoRows is returned, the event was already processed → 204.
	var eventPayload []byte
	if req.EventPayload != nil {
		eventPayload, _ = json.Marshal(req.EventPayload)
	}
	_, evtErr := s.paymentIntentQueries.InsertPaymentIntentEvent(ctx,
		pi.ID, req.ProviderPaymentID, req.EventType, eventPayload, &targetState,
	)
	if errors.Is(evtErr, pgx.ErrNoRows) {
		// Duplicate event delivery — already processed, return 204.
		s.logger.Info("webhook: duplicate event; skipping",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("event_type", req.EventType),
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if evtErr != nil {
		s.logger.Error("webhook: event record failed",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("event_type", req.EventType),
			slog.String("error", evtErr.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("webhook.event_record_failed", "failed to record webhook event", r))
		return
	}

	// Apply state transition if the target state is reachable from the current state.
	currentState := pi.State
	if isTerminalPaymentIntentState(currentState) {
		// Already terminal — acknowledge without transitioning.
		writeJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "payment intent is already in a terminal state",
		})
		return
	}

	validTargets := validPaymentIntentTransitions[currentState]
	if !validTargets[targetState] {
		// Transition not valid — acknowledge without transitioning (event recorded).
		writeJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "state transition not valid from current state",
		})
		return
	}

	updated, err := s.paymentIntentQueries.UpdatePaymentIntentState(ctx,
		pi.ID, targetState,
		req.ScaRedirectURL, req.ClientSecret,
		req.FailureCode, req.FailureMessage,
		nil, // provider_payment_id already set
	)
	if err != nil {
		s.logger.Error("webhook: state update failed",
			slog.String("id", pi.ID.String()),
			slog.String("target_state", targetState),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("webhook.state_update_failed", "failed to update payment intent state", r))
		return
	}

	s.logger.Info("webhook: state transitioned",
		slog.String("id", pi.ID.String()),
		slog.String("provider_payment_id", req.ProviderPaymentID),
		slog.String("event_type", req.EventType),
		slog.String("from", currentState),
		slog.String("to", updated.State),
	)

	// Issue tickets when payment reaches succeeded state and a checkout session is linked.
	// Idempotent: issueTicketsForCheckout returns existing tickets if already issued.
	if updated.State == "succeeded" && updated.CheckoutSessionID != nil &&
		s.ticketQueries != nil && s.checkoutQueries != nil && s.reservationQueries != nil {
		cs, csErr := s.checkoutQueries.GetCheckoutSessionByID(ctx, *updated.CheckoutSessionID)
		if csErr != nil {
			// Log but do not fail the webhook — payment state is already persisted.
			s.logger.Error("webhook: checkout lookup failed for ticket issuance",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", updated.CheckoutSessionID.String()),
				slog.String("error", csErr.Error()),
			)
		} else {
			tickets, ticketErr := s.issueTicketsForCheckout(ctx, cs)
			if ticketErr != nil {
				s.logger.Error("webhook: ticket issuance failed",
					slog.String("payment_intent_id", pi.ID.String()),
					slog.String("checkout_session_id", cs.ID.String()),
					slog.String("error", ticketErr.Error()),
				)
			} else {
				s.logger.Info("webhook: tickets issued on payment success",
					slog.String("payment_intent_id", pi.ID.String()),
					slog.String("checkout_session_id", cs.ID.String()),
					slog.Int("count", len(tickets)),
				)
				// Enqueue email delivery jobs (feature #141). Best-effort.
				s.enqueueDeliveryJobs(ctx, tickets)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"acknowledged":   true,
		"event_type":     req.EventType,
		"processed":      true,
		"payment_intent": paymentIntentFromRow(updated),
	})
}
