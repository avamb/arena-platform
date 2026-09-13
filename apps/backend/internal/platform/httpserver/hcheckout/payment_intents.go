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
package hcheckout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/convertjob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/issuejob"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ordering"
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

// ValidPaymentIntentTransitions is the exported form of validPaymentIntentTransitions,
// for use by the httpserver shim layer (payment_intents_137_test.go and
// openapi_payment_intents_271_test.go reference validPaymentIntentTransitions from
// package httpserver via checkout_shims.go).
var ValidPaymentIntentTransitions = validPaymentIntentTransitions

// isTerminalPaymentIntentState returns true for states that admit no further
// transitions (succeeded and failed).
func isTerminalPaymentIntentState(state string) bool {
	_, exists := validPaymentIntentTransitions[state]
	return exists && len(validPaymentIntentTransitions[state]) == 0
}

// IsTerminalPaymentIntentState is the exported form of isTerminalPaymentIntentState,
// for use by the httpserver shim layer (payment_intents_137_test.go references
// isTerminalPaymentIntentState from package httpserver via checkout_shims.go).
func IsTerminalPaymentIntentState(state string) bool {
	return isTerminalPaymentIntentState(state)
}

// validWebhookTransitions is the transition table used ONLY by
// HandlePaymentIntentWebhook (the unauthenticated provider callback). It is
// strictly more permissive than validPaymentIntentTransitions for the
// non-terminal source states: a real provider (Stripe in particular)
// routinely delivers `succeeded` / `failed` / `authorized` directly from
// `created` or `requires_action` without an intermediate `processing` event
// ever reaching us. Before this table existed, such a webhook was answered
// 200 `processed:false` ("state transition not valid from current state")
// and the payment was silently dropped — the order stayed unpaid and no
// tickets were issued despite money having been captured.
//
// The authenticated POST /v1/payment-intents/{id}/transition endpoint keeps
// enforcing the strict validPaymentIntentTransitions chain unchanged — this
// table widens ONLY what the webhook will accept. Terminal states
// (succeeded, failed) still admit no further transitions from either table.
//
// Note on the audit trail: payment_intent_events records exactly one row per
// delivered event (keyed by (provider_payment_id, event_type)) carrying the
// event's own target_state; it is not a hop-by-hop state history table, so a
// webhook-driven created→succeeded jump does not need (and does not write) a
// synthetic intermediate `processing` row.
var validWebhookTransitions = map[string]map[string]bool{
	"created": {
		"requires_action": true,
		"processing":      true,
		"succeeded":       true,
		"authorized":      true,
		"failed":          true,
	},
	"requires_action": {
		"processing": true,
		"succeeded":  true,
		"authorized": true,
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

// ValidWebhookTransitions is the exported form of validWebhookTransitions,
// for use by the httpserver shim layer and tests.
var ValidWebhookTransitions = validWebhookTransitions

// validWebhookTransition reports whether target is reachable from current
// via a webhook-delivered event. Used ONLY by HandlePaymentIntentWebhook.
func validWebhookTransition(current, target string) bool {
	targets, ok := validWebhookTransitions[current]
	return ok && targets[target]
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

// HandleCreatePaymentIntent serves POST /v1/payment-intents.
// Creates a new payment intent linked to an optional checkout session.
// Requires JWT + "payment_intent.create" permission.
func (h *Handler) HandleCreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	if h.paymentIntentQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.empty_body", "request body is required", r))
		return
	}

	var req createPaymentIntentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Validate required fields.
	orgID, err := uuid.Parse(req.OrgID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.invalid_org_id", "org_id must be a valid UUID", r,
			map[string]any{"field": "org_id"},
		))
		return
	}
	if req.Provider == "" && req.CheckoutSessionID == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.missing_provider", "provider is required", r,
			map[string]any{"field": "provider"},
		))
		return
	}
	if req.Amount < 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.invalid_amount", "amount must be a non-negative integer", r,
			map[string]any{"field": "amount"},
		))
		return
	}
	if req.Currency == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.missing_currency", "currency is required", r,
			map[string]any{"field": "currency"},
		))
		return
	}

	// Validate initial state when provided.
	if req.InitialState != "" {
		if _, ok := validPaymentIntentTransitions[req.InitialState]; !ok {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"payment_intent.invalid_initial_state",
				"initial_state must be one of: created, requires_action, processing",
				r,
				map[string]any{"field": "initial_state"},
			))
			return
		}
		// Only non-terminal initial states are valid for creation.
		if isTerminalPaymentIntentState(req.InitialState) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"payment_intent.invalid_checkout_session_id",
				"checkout_session_id must be a valid UUID when provided", r,
				map[string]any{"field": "checkout_session_id"},
			))
			return
		}
		checkoutSessionID = &parsed
	}

	// AB-41: the client does not choose the provider. With a checkout
	// session the sales channel decides WHICH provider; a contradicting
	// client value is rejected. Either way the org must hold an active,
	// configured payment_provider_configs row for it — never a silent
	// fall-through to a default.
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if checkoutSessionID != nil && h.checkoutQueries != nil {
		cs, csErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, *checkoutSessionID)
		if csErr != nil {
			if errors.Is(csErr, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"payment_intent.checkout_not_found", "checkout session not found", r,
				))
				return
			}
			h.logger.Error("payment_intent: checkout lookup failed", slog.String("error", csErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"payment_intent.create_failed", "failed to load checkout session", r,
			))
			return
		}
		if cs.OrgID != orgID {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"payment_intent.checkout_org_mismatch", "checkout session belongs to another organization", r,
			))
			return
		}
		channelProvider, chErr := h.channelProviderForCheckout(ctx, cs)
		if chErr != nil {
			h.logger.Error("payment_intent: channel lookup failed", slog.String("error", chErr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"payment_intent.create_failed", "failed to resolve the sales channel provider", r,
			))
			return
		}
		if provider != "" && provider != strings.ToLower(channelProvider) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				ErrCodeProviderMismatch,
				"provider does not match the checkout session's sales channel", r,
				map[string]any{"requested": provider, "channel_provider": channelProvider},
			))
			return
		}
		provider = strings.ToLower(channelProvider)
	}
	if h.orgQueries != nil {
		if _, cfgErr := ResolveProviderConfig(ctx, h.orgQueries, orgID, provider); cfgErr != nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				cfgErr.Code, cfgErr.Message, r, cfgErr.Details,
			))
			return
		}
	}

	pi, err := h.paymentIntentQueries.InsertPaymentIntent(ctx,
		checkoutSessionID, orgID, provider, req.ProviderPaymentID,
		req.Amount, req.Currency, req.InitialState,
		req.ScaRedirectURL, req.ClientSecret,
	)
	if err != nil {
		h.logger.Error("payment_intent: create failed",
			slog.String("org_id", orgID.String()),
			slog.String("provider", req.Provider),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"payment_intent.create_failed", "failed to create payment intent", r,
		))
		return
	}

	h.logger.Info("payment_intent: created",
		slog.String("id", pi.ID.String()),
		slog.String("provider", pi.Provider),
		slog.String("state", pi.State),
		slog.Int64("amount", pi.Amount),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"payment_intent": paymentIntentFromRow(pi),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/payment-intents/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetPaymentIntent serves GET /v1/payment-intents/{id}.
// Returns the current state of a payment intent.
// Requires JWT + "payment_intent.read" permission.
func (h *Handler) HandleGetPaymentIntent(w http.ResponseWriter, r *http.Request) {
	if h.paymentIntentQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_id", "payment intent id must be a valid UUID", r))
		return
	}

	pi, err := h.paymentIntentQueries.GetPaymentIntentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		h.logger.Error("payment_intent: get failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("payment_intent.get_failed", "failed to retrieve payment intent", r))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
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

// HandleTransitionPaymentIntent serves POST /v1/payment-intents/{id}/transition.
// Validates the requested state transition against the state machine, then
// persists the new state.
// Returns 409 when the transition is not valid from the current state.
// Returns 409 when the intent is already in a terminal state.
// Requires JWT + "payment_intent.update" permission.
func (h *Handler) HandleTransitionPaymentIntent(w http.ResponseWriter, r *http.Request) {
	if h.paymentIntentQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_id", "payment intent id must be a valid UUID", r))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.empty_body", "request body is required", r))
		return
	}

	var req transitionPaymentIntentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("payment_intent.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.State == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.missing_state", "state is required", r,
			map[string]any{"field": "state"},
		))
		return
	}

	// Fetch current state to validate the transition.
	current, err := h.paymentIntentQueries.GetPaymentIntentByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		h.logger.Error("payment_intent: transition fetch failed",
			slog.String("id", id.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("payment_intent.fetch_failed", "failed to retrieve payment intent", r))
		return
	}

	// Guard: reject transitions from terminal states.
	if isTerminalPaymentIntentState(current.State) {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.terminal_state",
			"payment intent is in a terminal state and cannot be transitioned",
			r,
			map[string]any{
				"current_state":   current.State,
				"requested_state": req.State,
			},
		))
		return
	}

	// Guard: validate the transition.
	validTargets, ok := validPaymentIntentTransitions[current.State]
	if !ok || !validTargets[req.State] {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.invalid_transition",
			"requested state transition is not valid from the current state",
			r,
			map[string]any{
				"current_state":   current.State,
				"requested_state": req.State,
			},
		))
		return
	}

	// Validate SCA fields when transitioning to requires_action.
	if req.State == "requires_action" && req.ScaRedirectURL == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"payment_intent.missing_sca_redirect_url",
			"sca_redirect_url is required when transitioning to requires_action",
			r,
			map[string]any{"field": "sca_redirect_url"},
		))
		return
	}

	// Persist the transition.
	updated, err := h.paymentIntentQueries.UpdatePaymentIntentState(ctx,
		id, req.State,
		req.ScaRedirectURL, req.ClientSecret,
		req.FailureCode, req.FailureMessage,
		req.ProviderPaymentID,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("payment_intent.not_found", "payment intent not found", r))
			return
		}
		h.logger.Error("payment_intent: transition failed",
			slog.String("id", id.String()),
			slog.String("target_state", req.State),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("payment_intent.transition_failed", "failed to transition payment intent", r))
		return
	}

	h.logger.Info("payment_intent: state transitioned",
		slog.String("id", id.String()),
		slog.String("from", current.State),
		slog.String("to", updated.State),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
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

// stripeEventEnvelope is Stripe's standard webhook event envelope shape:
//
//	{"id":"evt_...","type":"payment_intent.succeeded",
//	 "data":{"object":{"id":"pi_...","status":"succeeded",
//	 "last_payment_error":{"code":"...","message":"..."}}}}
//
// This is what Stripe (and this repo's own internal/adapters/stripebilling/adapter.go,
// see HandleBillingWebhook) actually send — as opposed to the flat, normalised
// webhookPaymentIntentRequest shape used by the mock provider and AllPay.
// AllPay is left as-is: it POSTs the flat shape natively and has no envelope
// of its own to parse.
type stripeEventEnvelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID               string `json:"id"`
			Status           string `json:"status"`
			LastPaymentError *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"last_payment_error"`
		} `json:"object"`
	} `json:"data"`
}

// parseWebhookPaymentIntentRequest accepts BOTH bodies
// POST /v1/payment-intents/webhook supports:
//
//   - The flat, normalised shape (webhookPaymentIntentRequest) used by the
//     mock provider, AllPay, and every pre-existing test/integration fixture.
//   - A genuine Stripe event envelope (stripeEventEnvelope). Before this fix
//     a real Stripe payment_intent.succeeded webhook — which Stripe always
//     sends as an envelope, never as the flat shape — failed parsing with
//     webhook.missing_provider_payment_id because the decoder only ever
//     looked for a top-level provider_payment_id field.
//
// Detection: a top-level "type" string together with a top-level "data"
// object signals the envelope; the flat shape has neither key. When the
// envelope is detected, data.object.id maps to ProviderPaymentID, type maps
// to EventType, data.object.last_payment_error.code/message map to
// FailureCode/FailureMessage, and the raw body is kept as EventPayload for
// the audit trail either way.
func parseWebhookPaymentIntentRequest(body []byte) (webhookPaymentIntentRequest, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return webhookPaymentIntentRequest{}, err
	}
	_, hasType := probe["type"]
	_, hasData := probe["data"]
	if !hasType || !hasData {
		var req webhookPaymentIntentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return webhookPaymentIntentRequest{}, err
		}
		return req, nil
	}

	var env stripeEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return webhookPaymentIntentRequest{}, err
	}
	req := webhookPaymentIntentRequest{
		ProviderPaymentID: env.Data.Object.ID,
		EventType:         env.Type,
		EventPayload:      json.RawMessage(body),
	}
	if env.Data.Object.LastPaymentError != nil {
		if code := env.Data.Object.LastPaymentError.Code; code != "" {
			req.FailureCode = &code
		}
		if msg := env.Data.Object.LastPaymentError.Message; msg != "" {
			req.FailureMessage = &msg
		}
	}
	return req, nil
}

// webhookEventTypeToState maps normalized provider event types to payment intent states.
// This covers the common Stripe-compatible event type strings; real deployments
// should extend or override this map per-provider.
var webhookEventTypeToState = map[string]string{
	"payment_intent.requires_action":           "requires_action",
	"payment_intent.processing":                "processing",
	"payment_intent.amount_capturable":         "authorized",
	"payment_intent.amount_capturable_updated": "authorized", // real Stripe event type name
	"payment_intent.succeeded":                 "succeeded",
	"payment_intent.payment_failed":            "failed",
	"payment_intent.manual_review":             "manual_review",
	// Shorthand aliases used by mock provider tests.
	"mock.requires_action": "requires_action",
	"mock.processing":      "processing",
	"mock.authorized":      "authorized",
	"mock.succeeded":       "succeeded",
	"mock.failed":          "failed",
	"mock.manual_review":   "manual_review",
}

// WebhookEventTypeToState is the exported form of webhookEventTypeToState, for
// use by the httpserver shim layer (payment_intents_137_test.go references
// webhookEventTypeToState from package httpserver via checkout_shims.go).
var WebhookEventTypeToState = webhookEventTypeToState

// HandlePaymentIntentWebhook serves POST /v1/payment-intents/webhook.
//
// This endpoint is intentionally unauthenticated via JWT — payment providers
// deliver webhooks from their own infrastructure. Authentication is performed
// via HMAC signature verification (Stripe-Signature or X-AllPay-Signature
// headers) before the body is parsed or any database state is mutated.
//
// # Atomicity (feature #363, PR2-07)
//
// The idempotency event INSERT (InsertPaymentIntentEvent), the state transition
// UPDATE (UpdatePaymentIntentState), and the ticket-issuance job enqueue
// (INSERT worker_jobs "checkout.issue_tickets") are committed in a single
// PostgreSQL transaction. If the process crashes at any point before the
// commit, none of the three writes are visible. Provider redelivery finds no
// idempotency row and processes the event from scratch.
//
// Duplicate deliveries: if the event row is already committed (i.e. a prior
// invocation succeeded), InsertPaymentIntentEvent returns pgx.ErrNoRows
// (ON CONFLICT DO NOTHING). The handler rolls back the empty transaction and
// returns 204 without reprocessing.
func (h *Handler) HandlePaymentIntentWebhook(w http.ResponseWriter, r *http.Request) {
	// Read body first so the HMAC can be verified over the raw bytes.
	// The body is intentionally read BEFORE the DB nil guard so that forged
	// requests are rejected with 401 without leaking service availability.
	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("webhook.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("webhook.empty_body", "request body is required", r))
		return
	}

	// Verify provider HMAC signature before processing the body. This prevents
	// anyone who knows a provider_payment_id from forging payment.succeeded to
	// mint tickets or cancel them. Returns nil in dev/mock mode (no secrets
	// configured); production requires at least one secret (config validation).
	if sigErr := h.verifyWebhookSignature(r, body); sigErr != nil {
		if h.logger != nil {
			h.logger.Warn("webhook: invalid signature; rejecting request",
				slog.String("error", sigErr.Error()),
				slog.String("remote_addr", r.RemoteAddr),
			)
		}
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"webhook.invalid_signature", "webhook signature verification failed", r,
		))
		return
	}

	if h.paymentIntentQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	req, err := parseWebhookPaymentIntentRequest(body)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("webhook.invalid_json", "request body is not valid JSON", r))
		return
	}

	if req.ProviderPaymentID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("webhook.missing_provider_payment_id", "provider_payment_id is required", r))
		return
	}
	if req.EventType == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("webhook.missing_event_type", "event_type is required", r))
		return
	}

	// Resolve target state.
	targetState := req.TargetState
	if targetState == "" {
		mapped, ok := webhookEventTypeToState[req.EventType]
		if !ok {
			// Unknown event type — acknowledge without processing (common for
			// provider events we don't handle, e.g. "payment_intent.created").
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

	// Look up the payment intent by provider ID (outside the transaction so
	// we know the current state before acquiring a lock).
	pi, err := h.paymentIntentQueries.GetPaymentIntentByProviderID(ctx, req.ProviderPaymentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("webhook.intent_not_found", "no payment intent found for provider_payment_id", r))
			return
		}
		h.logger.Error("webhook: intent lookup failed",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.lookup_failed", "failed to locate payment intent", r))
		return
	}

	// Apply state transition if the target state is reachable from the current state.
	currentState := pi.State
	if isTerminalPaymentIntentState(currentState) {
		// Already terminal — acknowledge without transitioning.
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "payment intent is already in a terminal state",
		})
		return
	}

	if !validWebhookTransition(currentState, targetState) {
		// Transition not valid — acknowledge without transitioning.
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"acknowledged": true,
			"event_type":   req.EventType,
			"processed":    false,
			"reason":       "state transition not valid from current state",
		})
		return
	}

	// ── Atomic transaction (feature #363) ────────────────────────────────────
	// Three operations committed atomically:
	//   1. InsertPaymentIntentEvent — idempotency guard (ON CONFLICT DO NOTHING)
	//   2. UpdatePaymentIntentState — advance the state machine
	//   3. INSERT worker_jobs "checkout.issue_tickets" — durable ticket issuance
	//
	// A crash before the commit leaves no idempotency row; provider redelivery
	// processes the event from scratch (step 3 of feature #363 acceptance criteria).
	tx, txErr := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if txErr != nil {
		h.logger.Error("webhook: begin tx failed",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("error", txErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.tx_begin_failed", "failed to begin transaction", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txQ := gen.New(tx)

	// Step 1: Idempotency check within the transaction.
	// InsertPaymentIntentEvent uses ON CONFLICT (provider_payment_id, event_type)
	// DO NOTHING. If the event was already committed by a prior successful run,
	// pgx.ErrNoRows is returned and the caller must return 204.
	var eventPayload []byte
	if req.EventPayload != nil {
		eventPayload, _ = json.Marshal(req.EventPayload)
	}
	_, evtErr := txQ.InsertPaymentIntentEvent(ctx,
		pi.ID, req.ProviderPaymentID, req.EventType, eventPayload, &targetState,
	)
	if errors.Is(evtErr, pgx.ErrNoRows) {
		// Duplicate event delivery — the event was already processed in a prior
		// successful invocation. Roll back (no-op on empty tx) and return 204.
		h.logger.Info("webhook: duplicate event; skipping",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("event_type", req.EventType),
		)
		_ = tx.Rollback(ctx)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if evtErr != nil {
		h.logger.Error("webhook: event record failed",
			slog.String("provider_payment_id", req.ProviderPaymentID),
			slog.String("event_type", req.EventType),
			slog.String("error", evtErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.event_record_failed", "failed to record webhook event", r))
		return
	}

	// Step 2: Advance the state machine within the same transaction.
	updated, stateErr := txQ.UpdatePaymentIntentState(ctx,
		pi.ID, targetState,
		req.ScaRedirectURL, req.ClientSecret,
		req.FailureCode, req.FailureMessage,
		nil, // provider_payment_id already set
	)
	if stateErr != nil {
		h.logger.Error("webhook: state update failed",
			slog.String("id", pi.ID.String()),
			slog.String("target_state", targetState),
			slog.String("error", stateErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.state_update_failed", "failed to update payment intent state", r))
		return
	}

	// Step 2b (HIGH-severity fix, 2026-09-13): complete the linked checkout
	// session in the SAME transaction as the intent's succeeded transition.
	// Before this fix checkout_sessions.state never left 'pricing_confirmed'
	// on the widget/public purchase path — POST .../checkout/start → payment
	// → GET /v1/public/checkout/{token} answered "pending" forever even
	// though the order was marked paid and tickets were issued underneath
	// (proven live: a 251-purchase load test showed 251/251 orders paid with
	// tickets issued while checkout_sessions.state stayed pricing_confirmed).
	// See AGENTS.md.
	//
	// Idempotent: a replayed event for a session that is already 'completed'
	// is treated as success (checkoutCompleted=true) without re-running
	// CompleteCheckoutSession — everything gated on checkoutCompleted below
	// (ticket issuance, order mark-paid, reservation conversion) is itself
	// idempotent, so replaying them for an already-completed session is safe.
	//
	// A session that can no longer reach 'completed' for any OTHER reason
	// (its hold TTL'd out and the reservation/checkout expired before the
	// provider's webhook arrived, or it is already parked in manual_review)
	// is NOT silently force-completed — money has been captured but the hold
	// may be gone, so the session (and the order, when one exists) are
	// parked in 'manual_review' for an operator instead, mirroring hbil24's
	// payParkManualReview (PAY_ORDER, spec §7.9). Ticket issuance, the order
	// mark-paid step, and reservation conversion are all gated on
	// checkoutCompleted so a parked session never silently ships tickets for
	// seats that may no longer be held.
	checkoutCompleted := false
	if updated.State == "succeeded" && updated.CheckoutSessionID != nil {
		csID := *updated.CheckoutSessionID
		_, compErr := txQ.CompleteCheckoutSession(ctx, csID, pi.ID.String(), pi.Provider)
		switch {
		case compErr == nil:
			checkoutCompleted = true
		case errors.Is(compErr, pgx.ErrNoRows):
			cur, curErr := txQ.GetCheckoutSessionByID(ctx, csID)
			switch {
			case curErr != nil:
				h.logger.Error("webhook: checkout session reload failed after complete guard miss",
					slog.String("checkout_session_id", csID.String()),
					slog.String("error", curErr.Error()),
				)
			case cur.State == "completed":
				// Replayed event, or a completion that raced this one home
				// first — already done, nothing to redo.
				checkoutCompleted = true
			default:
				// Money captured but the session can no longer transition to
				// 'completed' normally (expired/abandoned/manual_review/
				// created/payment_started). Park it — and the order, when
				// one exists — for an operator instead of pretending success.
				// Best-effort writes inside the money transaction MUST sit behind a
				// SAVEPOINT (AGENTS.md): a failing statement would otherwise abort
				// the whole tx and the COMMIT would silently roll back the
				// succeeded payment intent.
				if spErr := h.parkCheckoutForManualReview(ctx, tx, csID); spErr != nil {
					h.logger.Error("webhook: park checkout for manual_review failed; payment state kept",
						slog.String("checkout_session_id", csID.String()),
						slog.String("error", spErr.Error()),
					)
				}
				h.logger.Warn("webhook: payment succeeded but checkout session could not be completed; parked for manual review",
					slog.String("payment_intent_id", pi.ID.String()),
					slog.String("checkout_session_id", csID.String()),
					slog.String("session_state_before", cur.State),
				)
			}
		default:
			// The statement failed and aborted the transaction: nothing after
			// it can commit. Answer 500 so the provider redelivers the event.
			h.logger.Error("webhook: complete checkout session failed",
				slog.String("checkout_session_id", csID.String()),
				slog.String("error", compErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.checkout_completion_failed", "failed to complete checkout session", r))
			return
		}
	}

	// Step 3: Enqueue the ticket-issuance durable job in the same transaction
	// (feature #363). The worker picks up checkout.issue_tickets and calls
	// IssueTicketsForCheckout (idempotent, feature #366). Delivery jobs are
	// enqueued inside IssueTicketsForCheckout — no separate call needed
	// (feature #367, removes duplicate enqueueDelivery). Gated on
	// checkoutCompleted (Step 2b) so a manual_review-parked session never
	// gets tickets issued for seats that may no longer be held.
	if checkoutCompleted {
		jobPayload, _ := json.Marshal(issuejob.Payload{
			CheckoutSessionID: updated.CheckoutSessionID.String(),
		})
		const insertWorkerJobSQL = `
			INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, $2::jsonb, $3, 'pending', now())`
		if _, jobEnqErr := tx.Exec(ctx, insertWorkerJobSQL,
			issuejob.JobType, jobPayload, 5,
		); jobEnqErr != nil {
			h.logger.Error("webhook: enqueue checkout.issue_tickets job failed; rolling back",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", updated.CheckoutSessionID.String()),
				slog.String("error", jobEnqErr.Error()),
			)
			// Rolling back ensures the idempotency row is not committed.
			// The provider will retry and we will attempt again from scratch.
			_ = tx.Rollback(ctx)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"webhook.enqueue_failed", "failed to enqueue ticket issuance job", r,
			))
			return
		}
	}

	// Step 3b (W1-A6c, feature #488; spec §14.1): move the order aggregate to
	// 'paid' in the SAME transaction as the state change, so money state and
	// order state can never disagree.
	//
	// Two degradations are deliberate and non-fatal:
	//
	//   - pgx.ErrNoRows means the checkout session predates the order aggregate
	//     (confirmed before this feature landed). Those sessions still issue
	//     tickets; they simply have no order to mark.
	//   - ordering.MarkPaid on an already-paid order is a no-op by design, so a
	//     replayed webhook does not double-write the audit event.
	//
	// Anything else is logged and swallowed: refusing to issue tickets for a
	// payment the provider has already captured would be strictly worse than an
	// order row lagging behind, which the paid-order reconciliation can repair.
	// Gated on checkoutCompleted (Step 2b) for the same reason as Step 3.
	if checkoutCompleted {
		if ord, ordErr := txQ.GetOrderByCheckoutSession(ctx, *updated.CheckoutSessionID); ordErr != nil {
			if !errors.Is(ordErr, pgx.ErrNoRows) {
				h.logger.Warn("webhook: order lookup for mark-paid failed (non-fatal)",
					slog.String("payment_intent_id", pi.ID.String()),
					slog.String("checkout_session_id", updated.CheckoutSessionID.String()),
					slog.String("error", ordErr.Error()),
				)
			}
		} else if _, paidErr := ordering.MarkPaid(ctx, txQ, ordering.PaidInput{
			OrderID: ord.ID,
			OrgID:   ord.OrgID,
			Actor:   ordering.ActorSystem,
			Payload: map[string]any{
				"payment_intent_id":   pi.ID.String(),
				"provider_payment_id": req.ProviderPaymentID,
				"event_type":          req.EventType,
			},
		}); paidErr != nil {
			h.logger.Warn("webhook: order mark-paid failed (non-fatal)",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("order_id", ord.ID.String()),
				slog.String("error", paidErr.Error()),
			)
		}
	}

	// Step 4 (PR2-27): Enqueue the durable reservation-conversion job in the
	// same transaction. Even if the inline convertReservationTx call below
	// fails (e.g. transient network hiccup), the worker will retry until
	// the reservation reaches 'converted' state.
	// If the inline call succeeds, convertReservationInTx is idempotent and
	// the job runs as a no-op. Gated on checkoutCompleted (Step 2b) for the
	// same reason as Step 3.
	if checkoutCompleted && h.checkoutQueries != nil {
		cs, csErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, *updated.CheckoutSessionID)
		if csErr == nil {
			convJobPayload, _ := json.Marshal(convertjob.Payload{
				ReservationID: cs.ReservationID.String(),
			})
			const insertConvertJobSQL = `
				INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
				VALUES ($1, $2::jsonb, $3, 'pending', now())`
			if _, convJobErr := tx.Exec(ctx, insertConvertJobSQL,
				convertjob.JobType, convJobPayload, 5,
			); convJobErr != nil {
				h.logger.Warn("webhook: enqueue checkout.convert_reservation job failed (non-fatal; inline conversion still attempted)",
					slog.String("payment_intent_id", pi.ID.String()),
					slog.String("reservation_id", cs.ReservationID.String()),
					slog.String("error", convJobErr.Error()),
				)
				// Non-fatal: the inline convertReservationTx call below still runs.
				// Only emit a warning — do NOT rollback here because the issue_tickets
				// job is already queued and tickets must be issued.
			}
		} else {
			h.logger.Warn("webhook: checkout lookup for convert job enqueue failed (non-fatal)",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", updated.CheckoutSessionID.String()),
				slog.String("error", csErr.Error()),
			)
		}
	}

	// Commit: event row + state change + checkout completion + job enqueue
	// are now durable.
	if commitErr := tx.Commit(ctx); commitErr != nil {
		h.logger.Error("webhook: commit failed",
			slog.String("id", pi.ID.String()),
			slog.String("error", commitErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("webhook.commit_failed", "failed to commit webhook transaction", r))
		return
	}

	h.logger.Info("webhook: state transitioned",
		slog.String("id", pi.ID.String()),
		slog.String("provider_payment_id", req.ProviderPaymentID),
		slog.String("event_type", req.EventType),
		slog.String("from", currentState),
		slog.String("to", updated.State),
	)

	// Convert the reservation inline (non-fatal, has its own transaction).
	// convertReservationTx moves held seats → sold, capacity_held → capacity_sold,
	// and sets reservation.state = 'converted' so the TTL worker cannot release
	// the seats (feature #360). This is idempotent: already-converted reservations
	// are silently skipped. If this call fails, the checkout.issue_tickets worker
	// job will still issue tickets; operators can re-trigger conversion manually.
	// Gated on checkoutCompleted (Step 2b) for the same reason as Step 3.
	if checkoutCompleted && h.checkoutQueries != nil {
		cs, csErr := h.checkoutQueries.GetCheckoutSessionByID(ctx, *updated.CheckoutSessionID)
		if csErr != nil {
			h.logger.Error("webhook: checkout lookup failed after payment succeeded (convert skipped)",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", updated.CheckoutSessionID.String()),
				slog.String("error", csErr.Error()),
			)
		} else if convErr := h.convertReservationTx(ctx, cs.ReservationID); convErr != nil {
			h.logger.Error("webhook: convert reservation failed after payment succeeded (non-fatal)",
				slog.String("payment_intent_id", pi.ID.String()),
				slog.String("checkout_session_id", cs.ID.String()),
				slog.String("reservation_id", cs.ReservationID.String()),
				slog.String("error", convErr.Error()),
			)
		}
	}
	// NOTE: Ticket issuance is now handled exclusively by the checkout.issue_tickets
	// worker job enqueued atomically above (feature #363). The inline h.issueTickets
	// call was removed from this webhook handler to prevent a partial failure from
	// poisoning the idempotency table. IssueTicketsForCheckout is idempotent
	// (feature #366) — if the job runs multiple times, only the first issues new
	// tickets; subsequent runs detect the complete set and return them unchanged.
	// Delivery jobs are enqueued inside IssueTicketsForCheckout (feature #367).

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged":       true,
		"event_type":         req.EventType,
		"processed":          true,
		"payment_intent":     paymentIntentFromRow(updated),
		"checkout_completed": checkoutCompleted,
	})
}

// parkCheckoutForManualReview moves a checkout session (and its order, when
// one exists) to manual_review inside a SAVEPOINT of the webhook transaction,
// so a failure here rolls back only these writes and never the payment
// intent's succeeded state.
func (h *Handler) parkCheckoutForManualReview(ctx context.Context, tx pgx.Tx, csID uuid.UUID) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin savepoint: %w", err)
	}
	defer func() { _ = sp.Rollback(ctx) }()
	q := gen.New(sp)

	if _, err := q.MarkCheckoutSessionManualReview(ctx, csID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("mark checkout session manual_review: %w", err)
	}
	ord, err := q.GetOrderByCheckoutSession(ctx, csID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return fmt.Errorf("order lookup: %w", err)
	default:
		if _, err := q.UpdateOrderStatus(ctx, ord.ID, ord.OrgID, "manual_review", nil, nil); err != nil {
			return fmt.Errorf("mark order manual_review: %w", err)
		}
	}
	return sp.Commit(ctx)
}
