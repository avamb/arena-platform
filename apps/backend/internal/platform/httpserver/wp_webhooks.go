// Package httpserver — WordPress webhook subscriber management.
//
// Feature #156 — WordPress webhook receiver: subscriber registration.
//
// This file implements the platform-side endpoint that allows WordPress sites
// (or any other subscriber) to register themselves as webhook recipients.
// The registration response includes a generated signing secret that the WP
// plugin stores in arena_webhook_secret for HMAC-SHA256 signature verification.
//
// Routes (mounted in server.go):
//
//	POST   /v1/webhooks/subscribers          — register a new subscriber
//	GET    /v1/webhooks/subscribers          — list active subscribers
//	GET    /v1/webhooks/subscribers/{id}     — get a specific subscriber
//	DELETE /v1/webhooks/subscribers/{id}     — deactivate a subscriber
//
// All routes require JWT auth + webhook.subscriber.manage permission.
//
// Security notes:
//   - The signing_secret is returned exactly once (at registration). It is
//     stored in the platform DB for dispatcher use. Treat it as a credential.
//   - The secret is NOT returned in GET responses to avoid mass disclosure.
//   - Only the signing_secret is omitted in list/get responses; all other
//     fields are returned.
package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / response types
// ─────────────────────────────────────────────────────────────────────────────

// registerSubscriberRequest is the JSON body for POST /v1/webhooks/subscribers.
type registerSubscriberRequest struct {
	// SiteURL is the human-readable URL of the WordPress site being registered.
	// Example: "https://mywordpress.example.com"
	SiteURL string `json:"site_url"`

	// CallbackURL is the WP REST API webhook endpoint.
	// Example: "https://mywordpress.example.com/wp-json/arena-events/v1/webhook"
	CallbackURL string `json:"callback_url"`

	// EventTypes is the list of event_type values this subscriber wants to
	// receive. An empty or absent list means wildcard (all event types).
	// Supported values: "order_paid", "ticket_issued", "refund_succeeded".
	EventTypes []string `json:"event_types"`
}

// registerSubscriberResponse is returned by POST /v1/webhooks/subscribers.
//
// The signing_secret field is returned ONCE here and should be immediately
// stored by the caller (e.g. in the WP plugin's arena_webhook_secret option).
// It is not returned by any subsequent GET endpoint.
type registerSubscriberResponse struct {
	SubscriberID string   `json:"subscriber_id"`
	SiteURL      string   `json:"site_url"`
	CallbackURL  string   `json:"callback_url"`
	EventTypes   []string `json:"event_types"`
	Active       bool     `json:"active"`
	// SigningSecret is the HMAC-SHA256 signing key for this subscriber.
	// Copy this into the WordPress plugin's "Webhook Secret" settings field.
	// This is the ONLY time the secret is returned; it cannot be retrieved later.
	SigningSecret string `json:"signing_secret"`
}

// webhookSubscriberSummary is the safe (no-secret) representation returned by GET endpoints.
type webhookSubscriberSummary struct {
	SubscriberID string   `json:"subscriber_id"`
	SiteURL      string   `json:"site_url"`
	CallbackURL  string   `json:"callback_url"`
	EventTypes   []string `json:"event_types"`
	Active       bool     `json:"active"`
	CreatedAt    string   `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

// handleRegisterWebhookSubscriber handles POST /v1/webhooks/subscribers.
//
// Registers a new webhook subscriber, generates a random HMAC-SHA256 signing
// secret, and returns it in the response. The secret is stored in
// webhook_subscribers.signing_secret.
//
// Request body (JSON):
//
//	{
//	  "site_url":    "https://mywordpress.example.com",
//	  "callback_url": "https://mywordpress.example.com/wp-json/arena-events/v1/webhook",
//	  "event_types": ["order_paid", "ticket_issued", "refund_succeeded"]
//	}
//
// Responses:
//   - 201 Created:  registration successful; body includes signing_secret.
//   - 400:          missing callback_url or invalid JSON.
//   - 409:          callback_url already registered.
//   - 503:          queries or pool not available.
func (s *Server) handleRegisterWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	var req registerSubscriberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request", "invalid JSON body", r))
		return
	}

	if req.CallbackURL == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("validation_error",
			"callback_url is required", r))
		return
	}

	if req.SiteURL == "" {
		req.SiteURL = req.CallbackURL
	}

	if req.EventTypes == nil {
		req.EventTypes = []string{}
	}

	// Generate a cryptographically-random 32-byte hex secret.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		s.logger.Error("handleRegisterWebhookSubscriber: failed to generate secret",
			slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to generate signing secret", r))
		return
	}
	secret := hex.EncodeToString(secretBytes)

	row, err := s.webhookSubQueries.CreateWebhookSubscriber(
		r.Context(),
		req.SiteURL,
		req.CallbackURL,
		secret,
		req.EventTypes,
	)
	if err != nil {
		s.logger.Warn("handleRegisterWebhookSubscriber: db error",
			slog.String("error", err.Error()))
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, errorEnvelope("conflict",
				"callback_url already registered", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to register subscriber", r))
		return
	}

	s.logger.Info("webhook subscriber registered",
		slog.String("subscriber_id", row.ID.String()),
		slog.String("callback_url", row.CallbackURL),
		slog.String("site_url", row.SiteURL),
	)

	writeJSON(w, http.StatusCreated, registerSubscriberResponse{
		SubscriberID:  row.ID.String(),
		SiteURL:       row.SiteURL,
		CallbackURL:   row.CallbackURL,
		EventTypes:    row.EventTypes,
		Active:        row.Active,
		SigningSecret: secret,
	})
}

// handleListWebhookSubscribers handles GET /v1/webhooks/subscribers.
//
// Returns all active webhook subscribers. The signing_secret field is NEVER
// included in list responses.
func (s *Server) handleListWebhookSubscribers(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	rows, err := s.webhookSubQueries.ListActiveWebhookSubscribers(r.Context())
	if err != nil {
		s.logger.Warn("handleListWebhookSubscribers: db error", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to list subscribers", r))
		return
	}

	summaries := make([]webhookSubscriberSummary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, webhookSubscriberSummary{
			SubscriberID: row.ID.String(),
			SiteURL:      row.SiteURL,
			CallbackURL:  row.CallbackURL,
			EventTypes:   row.EventTypes,
			Active:       row.Active,
			CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"subscribers": summaries,
		"total":       len(summaries),
	})
}

// handleGetWebhookSubscriber handles GET /v1/webhooks/subscribers/{id}.
//
// Returns a single subscriber summary. The signing_secret is NOT included.
func (s *Server) handleGetWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request",
			"invalid subscriber id", r))
		return
	}

	row, err := s.webhookSubQueries.GetWebhookSubscriberByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("not_found",
				"subscriber not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to get subscriber", r))
		return
	}

	writeJSON(w, http.StatusOK, webhookSubscriberSummary{
		SubscriberID: row.ID.String(),
		SiteURL:      row.SiteURL,
		CallbackURL:  row.CallbackURL,
		EventTypes:   row.EventTypes,
		Active:       row.Active,
		CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// handleDeactivateWebhookSubscriber handles DELETE /v1/webhooks/subscribers/{id}.
//
// Soft-deletes the subscriber (sets active=false). The row is retained for audit purposes.
func (s *Server) handleDeactivateWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request",
			"invalid subscriber id", r))
		return
	}

	row, err := s.webhookSubQueries.DeactivateWebhookSubscriber(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("not_found",
				"subscriber not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to deactivate subscriber", r))
		return
	}

	s.logger.Info("webhook subscriber deactivated",
		slog.String("subscriber_id", row.ID.String()),
		slog.String("callback_url", row.CallbackURL),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"subscriber_id": row.ID.String(),
		"active":        row.Active,
		"message":       "subscriber deactivated",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/webhooks/subscribers/{id} (Feature #294 — S-3)
// ─────────────────────────────────────────────────────────────────────────────

// updateSubscriberRequest is the JSON body for PATCH /v1/webhooks/subscribers/{id}.
//
// Both fields are optional; a request that supplies neither is rejected
// with bad_request so callers cannot silently no-op. event_types
// REPLACES the entire filter — empty array means wildcard. active is
// applied via SetWebhookSubscriberActive so the same handler can both
// re-activate a soft-deleted subscriber and pause an active one.
//
// signing_secret rotation is intentionally NOT supported here: the
// secret is write-only (returned exactly once at registration), per
// the file-level package doc. To rotate, deactivate and re-register.
type updateSubscriberRequest struct {
	EventTypes *[]string `json:"event_types,omitempty"`
	Active     *bool     `json:"active,omitempty"`
}

// handleUpdateWebhookSubscriber handles PATCH /v1/webhooks/subscribers/{id}.
//
// Supports two distinct mutations, both optional:
//   - event_types (TEXT[]): replace the subscriber's event-type filter.
//     Pass [] to switch a typed subscriber back to wildcard. The
//     dispatcher reads this column on every fan-out cycle.
//   - active (boolean): pause or resume delivery. Re-activation is the
//     UI's reason to exist; soft-deletion goes through DELETE.
//
// Responses mirror handleGetWebhookSubscriber: the safe summary shape
// (no signing_secret) is returned on success. 400 / 404 / 500 / 503
// envelopes match the existing webhook subscriber handler conventions.
func (s *Server) handleUpdateWebhookSubscriber(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request",
			"invalid subscriber id", r))
		return
	}

	var req updateSubscriberRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request", "invalid JSON body", r))
		return
	}

	if req.EventTypes == nil && req.Active == nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("validation_error",
			"at least one of event_types or active must be provided", r))
		return
	}

	row, err := s.webhookSubQueries.GetWebhookSubscriberByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("not_found",
				"subscriber not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to load subscriber", r))
		return
	}

	if req.EventTypes != nil {
		next := *req.EventTypes
		if next == nil {
			next = []string{}
		}
		row, err = s.webhookSubQueries.UpdateWebhookSubscriberEventTypes(r.Context(), id, next)
		if err != nil {
			s.logger.Warn("handleUpdateWebhookSubscriber: update event_types failed",
				slog.String("subscriber_id", id.String()),
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
				"failed to update event_types", r))
			return
		}
	}

	if req.Active != nil {
		row, err = s.webhookSubQueries.SetWebhookSubscriberActive(r.Context(), id, *req.Active)
		if err != nil {
			s.logger.Warn("handleUpdateWebhookSubscriber: set active failed",
				slog.String("subscriber_id", id.String()),
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
				"failed to update active flag", r))
			return
		}
	}

	s.logger.Info("webhook subscriber updated",
		slog.String("subscriber_id", row.ID.String()),
		slog.Bool("active", row.Active),
		slog.Int("event_types_count", len(row.EventTypes)),
	)

	writeJSON(w, http.StatusOK, webhookSubscriberSummary{
		SubscriberID: row.ID.String(),
		SiteURL:      row.SiteURL,
		CallbackURL:  row.CallbackURL,
		EventTypes:   row.EventTypes,
		Active:       row.Active,
		CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/webhooks/subscribers/{id}/recent-deliveries (Feature #294 — S-3)
// ─────────────────────────────────────────────────────────────────────────────

// recentDeliveryAttempt mirrors the columns surfaced by the SuperAdmin
// webhooks UI delivery-attempts panel. The current outbox schema does
// not record per-subscriber delivery rows; the dispatcher logs each
// attempt but its persisted state is limited to outbox.attempts and
// outbox.dispatched_at on the source event row. The UI therefore lists
// the most recent outbox events that this subscriber's event_types
// filter would match, annotated with the dispatcher's attempt counter
// and dispatch timestamp.
type recentDeliveryAttempt struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	OccurredAt string `json:"occurred_at"`
	// DispatchedAt is omitted from the JSON envelope when the source
	// outbox row has not yet been dispatched. The platform's openapi
	// dialect avoids the OAS 3.1 `nullable` keyword (and the
	// `type: [string, "null"]` JSON Schema 2020-12 idiom that
	// oapi-codegen does not yet support) by using documented field
	// omission — see the package-level comment in openapi.yaml.
	DispatchedAt string `json:"dispatched_at,omitempty"`
	Attempts     int32  `json:"attempts"`
}

// recentDeliveriesResponse is the JSON body returned by
// GET /v1/webhooks/subscribers/{id}/recent-deliveries.
type recentDeliveriesResponse struct {
	SubscriberID string                  `json:"subscriber_id"`
	Wildcard     bool                    `json:"wildcard"`
	EventTypes   []string                `json:"event_types"`
	Attempts     []recentDeliveryAttempt `json:"attempts"`
	Total        int                     `json:"total"`
}

// recentDeliveriesLimit caps how many rows the panel ever shows; the
// dispatcher logs are paginated elsewhere. 50 mirrors the "recent" UX
// (a single screen of context) and matches existing similar admin
// panels in the codebase.
const recentDeliveriesLimit = 50

// handleListRecentWebhookDeliveries handles
// GET /v1/webhooks/subscribers/{id}/recent-deliveries.
//
// Returns the most recent outbox rows whose event_type would be sent
// to this subscriber (wildcard match when event_types is empty,
// otherwise filtered by the subscriber's typed list). The output is
// best-effort: per-subscriber delivery state is not persisted today
// so attempts/dispatched_at reflect the aggregate dispatcher state of
// the source event, not this subscriber specifically.
func (s *Server) handleListRecentWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if s.webhookSubQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("service_unavailable",
			"webhook subscriber service not available", r))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("bad_request",
			"invalid subscriber id", r))
		return
	}

	sub, err := s.webhookSubQueries.GetWebhookSubscriberByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("not_found",
				"subscriber not found", r))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to load subscriber", r))
		return
	}

	wildcard := len(sub.EventTypes) == 0

	// The outbox table is read directly here (raw SQL on the pool)
	// rather than via sqlc because no other code path needs this
	// admin-only listing and the query is naturally tied to the
	// subscriber filter shape.
	var (
		rows pgx.Rows
		qerr error
	)
	if wildcard {
		rows, qerr = s.pool.Query(r.Context(), `
			SELECT id, event_type, occurred_at, dispatched_at, attempts
			FROM   outbox
			ORDER  BY occurred_at DESC
			LIMIT  $1
		`, recentDeliveriesLimit)
	} else {
		rows, qerr = s.pool.Query(r.Context(), `
			SELECT id, event_type, occurred_at, dispatched_at, attempts
			FROM   outbox
			WHERE  event_type = ANY($1)
			ORDER  BY occurred_at DESC
			LIMIT  $2
		`, sub.EventTypes, recentDeliveriesLimit)
	}
	if qerr != nil {
		s.logger.Warn("handleListRecentWebhookDeliveries: query failed",
			slog.String("subscriber_id", id.String()),
			slog.String("error", qerr.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to list recent deliveries", r))
		return
	}
	defer rows.Close()

	attempts := make([]recentDeliveryAttempt, 0)
	for rows.Next() {
		var (
			eventID      uuid.UUID
			eventType    string
			occurredAt   time.Time
			dispatchedAt *time.Time
			cnt          int32
		)
		if err := rows.Scan(&eventID, &eventType, &occurredAt, &dispatchedAt, &cnt); err != nil {
			s.logger.Warn("handleListRecentWebhookDeliveries: scan failed",
				slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
				"failed to read delivery row", r))
			return
		}
		var dispatched string
		if dispatchedAt != nil {
			dispatched = dispatchedAt.UTC().Format(time.RFC3339)
		}
		attempts = append(attempts, recentDeliveryAttempt{
			EventID:      eventID.String(),
			EventType:    eventType,
			OccurredAt:   occurredAt.UTC().Format(time.RFC3339),
			DispatchedAt: dispatched,
			Attempts:     cnt,
		})
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("handleListRecentWebhookDeliveries: rows.Err",
			slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error",
			"failed to iterate delivery rows", r))
		return
	}

	writeJSON(w, http.StatusOK, recentDeliveriesResponse{
		SubscriberID: sub.ID.String(),
		Wildcard:     wildcard,
		EventTypes:   sub.EventTypes,
		Attempts:     attempts,
		Total:        len(attempts),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface guards
// ─────────────────────────────────────────────────────────────────────────────

// Verify that *gen.Queries implements the webhook subscriber methods used by
// this handler. These assertions prevent method-signature drift between the
// handler and the generated queries without requiring a live database.
var _ gen.Querier = (*gen.Queries)(nil)
