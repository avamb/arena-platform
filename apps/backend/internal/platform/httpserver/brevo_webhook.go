// brevo_webhook.go — POST /v1/webhooks/brevo/{token}: Brevo's transactional
// webhook, the delivery status of the buyer letters arena sends
// (internal/platform/emailstatus, runbook docs/ops/brevo_delivery_status_webhook_ru.md).
//
// Rules of the route:
//   - it is mounted only when BREVO_WEBHOOK_TOKEN is set — never open;
//   - the token in the path is compared in constant time (both sides hashed
//     first, so not even the length leaks); the router redacts it from logs
//     and traces (adapters/http.redactPath);
//   - the body is capped, one object or an array of at most
//     emailstatus.MaxBatch events;
//   - an event for a message arena did not send, and an event type arena
//     does not model, is acknowledged with 200: the Brevo account may be
//     shared with the WordPress sites, and Brevo disables an endpoint that
//     keeps failing;
//   - a database error answers 500 so Brevo delivers again (applying an
//     event twice changes nothing);
//   - metrics carry only bounded labels.
package httpserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

const (
	// brevoWebhookMaxBody caps one delivery: a full batch of
	// emailstatus.MaxBatch events is far below it.
	brevoWebhookMaxBody = 256 << 10
	// brevoWebhookIPRateLimit is requests per minute per client address.
	// Brevo sends from a handful of addresses, one request per event unless
	// batching is on; a busy sale day stays well below this.
	brevoWebhookIPRateLimit = 3000
)

// mountBrevoWebhookRoutes mounts the delivery-status webhook when a token is
// configured and the database is there to store what it reports.
func (s *Server) mountBrevoWebhookRoutes(r chi.Router) {
	if s.cfg == nil || s.cfg.BrevoWebhookToken == "" || s.pool == nil {
		return
	}
	if s.brevoWebhookRL == nil {
		s.brevoWebhookRL = newPublicFeedRateLimiter(0, 0, brevoWebhookIPRateLimit)
	}
	r.Post("/webhooks/brevo/{token}", s.handleBrevoWebhook)
}

func (s *Server) handleBrevoWebhook(w http.ResponseWriter, r *http.Request) {
	if ok, retry := s.brevoWebhookRL.checkIP(httputil.TrustedClientIP(r, 0)); !ok {
		s.brevoWebhookRejected("rate_limited")
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		httputil.WriteJSON(w, http.StatusTooManyRequests,
			httputil.ErrorEnvelope("webhook.rate_limited", "too many requests", r))
		return
	}
	got := sha256.Sum256([]byte(chi.URLParam(r, "token")))
	want := sha256.Sum256([]byte(s.cfg.BrevoWebhookToken))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		s.brevoWebhookRejected("bad_token")
		httputil.WriteJSON(w, http.StatusUnauthorized,
			httputil.ErrorEnvelope("webhook.unauthorized", "unknown webhook token", r))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, brevoWebhookMaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.brevoWebhookRejected("too_large")
			httputil.WriteJSON(w, http.StatusRequestEntityTooLarge,
				httputil.ErrorEnvelope("webhook.body_too_large", "request body is too large", r))
			return
		}
		s.brevoWebhookRejected("malformed")
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("webhook.invalid_body", "could not read the request body", r))
		return
	}
	events, err := emailstatus.ParseBrevo(body, time.Now())
	if err != nil {
		s.brevoWebhookRejected("malformed")
		httputil.WriteJSON(w, http.StatusBadRequest,
			httputil.ErrorEnvelope("webhook.invalid_body", "expected one Brevo event object or an array of them", r))
		return
	}

	applied := 0
	for _, ev := range events {
		outcome, err := emailstatus.Apply(r.Context(), s.pool, ev)
		if err != nil {
			s.brevoWebhookEvent(ev.Event, "error")
			s.logger.Error("brevo webhook: could not store an event; Brevo will deliver it again",
				slog.String("event", observability.EmailWebhookEventLabel(ev.Event)),
				slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError,
				httputil.ErrorEnvelope("webhook.internal", "could not store the event", r))
			return
		}
		s.brevoWebhookEvent(ev.Event, string(outcome))
		if outcome == emailstatus.OutcomeApplied || outcome == emailstatus.OutcomeDuplicate {
			applied++
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"acknowledged": true,
		"received":     len(events),
		"applied":      applied,
	})
}

func (s *Server) brevoWebhookRejected(reason string) {
	if s.typedMetrics != nil && s.typedMetrics.EmailWebhookRejectedTotal != nil {
		s.typedMetrics.EmailWebhookRejectedTotal.WithLabelValues(reason).Inc()
	}
}

func (s *Server) brevoWebhookEvent(event, outcome string) {
	if s.typedMetrics != nil && s.typedMetrics.EmailWebhookEventsTotal != nil {
		s.typedMetrics.EmailWebhookEventsTotal.WithLabelValues(
			observability.EmailWebhookEventLabel(event), outcome).Inc()
	}
}
