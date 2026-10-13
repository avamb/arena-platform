// brevo_webhook.go — Brevo's transactional webhook, the delivery status of
// the buyer letters arena sends (internal/platform/emailstatus, runbook
// docs/ops/brevo_delivery_status_webhook_ru.md). Two routes, one handler:
//
//   - POST /v1/webhooks/brevo with "Authorization: Bearer <token>": the
//     preferred one. Brevo sends the header when the webhook is created
//     through its API with an "auth" object; the token never appears in a
//     URL, so no proxy access log holds it;
//   - POST /v1/webhooks/brevo/{token}: for a webhook created in Brevo's web
//     form, which takes only a URL. The router redacts the segment from its
//     own logs and traces (adapters/http.redactPath); proxies in front
//     (Traefik, Cloudflare) may still log it.
//
// Rules of the routes:
//   - mounted only when BREVO_WEBHOOK_TOKEN is set — never open;
//   - BREVO_WEBHOOK_TOKEN_PREVIOUS is accepted too, so a token can be
//     rotated without refusing Brevo's deliveries in between;
//   - every token is compared in constant time (both sides hashed first, so
//     not even the length leaks);
//   - the body is capped by size only (1 MiB), one object or an array of
//     any number of events: a batch is never refused for its length;
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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

const (
	// brevoWebhookMaxBody caps one delivery. A Brevo event is a few hundred
	// bytes, so this holds a batch of a couple of thousand events.
	brevoWebhookMaxBody = 1 << 20
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
	r.Post("/webhooks/brevo", s.handleBrevoWebhook)
	r.Post("/webhooks/brevo/{token}", s.handleBrevoWebhook)
}

// brevoWebhookToken is the token a request presents: the path segment, or
// the bearer token of the Authorization header.
func brevoWebhookToken(r *http.Request) string {
	if t := chi.URLParam(r, "token"); t != "" {
		return t
	}
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// brevoTokenAccepted compares the presented token with the current and the
// previous configured token in constant time. An empty token never matches.
func (s *Server) brevoTokenAccepted(presented string) bool {
	if presented == "" {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	ok := 0
	for _, t := range []string{s.cfg.BrevoWebhookToken, s.cfg.BrevoWebhookTokenPrevious} {
		if t == "" {
			continue
		}
		want := sha256.Sum256([]byte(t))
		ok |= subtle.ConstantTimeCompare(got[:], want[:])
	}
	return ok == 1
}

func (s *Server) handleBrevoWebhook(w http.ResponseWriter, r *http.Request) {
	if ok, retry := s.brevoWebhookRL.checkIP(httputil.TrustedClientIP(r, 0)); !ok {
		s.brevoWebhookRejected("rate_limited")
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		httputil.WriteJSON(w, http.StatusTooManyRequests,
			httputil.ErrorEnvelope("webhook.rate_limited", "too many requests", r))
		return
	}
	if !s.brevoTokenAccepted(brevoWebhookToken(r)) {
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
