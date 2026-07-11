// public_funnel_events.go — POST /v1/public/feeds/{feed_token}/events
//
// Funnel telemetry sink for the embedded widget (feature #322 WID-0e).
//
// Design summary:
//   - Fire-and-forget: individual insert failures are logged but do NOT
//     fail the response.  The endpoint always returns 204 when the batch
//     is structurally valid.
//   - No PII stored — no email, name, phone, or IP.  The only linkage to
//     a checkout journey is the opaque checkout_token (random 256-bit hex).
//   - Heavily rate-limited: the endpoint is counted against the same per-token
//     and per-IP buckets as the browse endpoints so a misbehaving widget
//     cannot spike the audit table independently of the broader rate limit.
//   - Batch size is capped at maxFunnelBatchSize (50).
//   - Unknown event_type values within a batch are silently skipped —
//     forward-compatibility for future widget releases.
package hfeed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// validFunnelEventTypes is the set of permitted event_type values.
var validFunnelEventTypes = map[string]bool{
	"schema_viewed":   true,
	"seat_selected":   true,
	"cart_opened":     true,
	"payment_started": true,
	"recovered":       true,
}

// maxFunnelBatchSize is the maximum number of events accepted per request.
const maxFunnelBatchSize = 50

// WidgetFunnelEventInput is one event in the batch body.
type WidgetFunnelEventInput struct {
	// EventType is one of: schema_viewed, seat_selected, cart_opened,
	// payment_started, recovered.
	EventType string `json:"event_type"`

	// CheckoutToken is the opaque checkout token, present only when a checkout
	// journey has been started.  No PII — purely an opaque linkage key.
	CheckoutToken *string `json:"checkout_token,omitempty"`

	// SessionID is the UUID of the event session the user is browsing.
	SessionID *string `json:"session_id,omitempty"`

	// OccurredAt is an RFC 3339 timestamp of when the event happened on the
	// client.  Defaults to now() when absent or unparseable.
	OccurredAt *string `json:"occurred_at,omitempty"`
}

// PostWidgetFunnelEventsRequest is the JSON body for
// POST /v1/public/feeds/{feed_token}/events.
type PostWidgetFunnelEventsRequest struct {
	Events []WidgetFunnelEventInput `json:"events"`
}

// HandlePostFunnelEvents handles POST /v1/public/feeds/{feed_token}/events.
//
// Accepts a batch of widget funnel telemetry events.  Returns 204 on success.
// Responses:
//
//	204 — batch accepted (all valid events persisted; unknown types skipped)
//	400 — invalid JSON body, empty batch, or batch exceeds maxFunnelBatchSize
//	429 — rate limited (shared per-token + per-IP bucket with browse endpoints)
//	503 — database not available
func (h *Handler) HandlePostFunnelEvents(w http.ResponseWriter, r *http.Request) {
	if h.funnelQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	feedToken := chi.URLParam(r, "feed_token")
	clientIP := httputil.ExtractClientIP(r)

	// Rate limiting: counted against the same per-token + per-IP buckets as
	// the browse endpoints so the aggregate widget traffic is throttled.
	if !h.rl.CheckToken(feedToken) || !h.rl.CheckIP(clientIP) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"feed.rate_limited", "too many requests; please slow down", r,
		))
		return
	}

	// Decode the batch.
	var req PostWidgetFunnelEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"feed.invalid_body", "request body must be valid JSON", r,
		))
		return
	}

	if len(req.Events) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"feed.empty_batch", "events array must not be empty", r,
		))
		return
	}

	if len(req.Events) > maxFunnelBatchSize {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"feed.batch_too_large",
			fmt.Sprintf("events batch exceeds maximum of %d", maxFunnelBatchSize),
			r,
		))
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()

	for _, ev := range req.Events {
		// Skip unknown event types — forward-compatible with future widget versions.
		if !validFunnelEventTypes[ev.EventType] {
			continue
		}

		// Parse occurred_at if provided; fall back to server-side now().
		occurredAt := now
		if ev.OccurredAt != nil && *ev.OccurredAt != "" {
			if t, err := time.Parse(time.RFC3339, *ev.OccurredAt); err == nil {
				occurredAt = t.UTC()
			}
		}

		// Parse session_id UUID if provided and syntactically valid.
		var sessionID *uuid.UUID
		if ev.SessionID != nil && *ev.SessionID != "" {
			if id, err := uuid.Parse(*ev.SessionID); err == nil {
				sessionID = &id
			}
		}

		// Persist — fire-and-forget: log errors but do not abort the batch.
		if err := h.funnelQueries.InsertWidgetFunnelEvent(ctx,
			feedToken,
			ev.EventType,
			ev.CheckoutToken, // no PII; nil when no checkout in progress
			sessionID,
			occurredAt,
		); err != nil {
			h.logger.Warn("funnel_events: insert failed",
				slog.String("feed_token", feedToken),
				slog.String("event_type", ev.EventType),
				slog.String("error", err.Error()),
			)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
