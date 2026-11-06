package httpserver

// site_forms.go wires the public forms of a static site, addressed by feed
// token (spec 62 section 10, П-17 and П-10): the organizer contact form and
// the subscription without a purchase. Both sit behind the Idempotency-Key
// middleware (optional, scoped per route and feed token, 24 h) and the scoped
// Turnstile check of package hsite.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// siteFormMaxBody caps what the idempotency middleware buffers.
const siteFormMaxBody = 32 << 10

// siteFormGuard builds the shared Turnstile guard. The widget action and the
// hosts are chosen per request by the handlers, so the verifier carries
// neither (the hosted page's own contact handler keeps its fixed pair).
func (s *Server) siteFormGuard() hsite.Guard {
	secret := ""
	production := false
	if s.cfg != nil {
		secret = s.cfg.ContactFormSecret()
		production = s.cfg.IsProduction()
	}
	return hsite.Guard{
		Verifier: &honboarding.TurnstileVerifier{Secret: secret, Required: production},
		Logger:   s.logger,
	}
}

func (s *Server) siteFormSalt() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.JWTSecretStub
}

func (s *Server) siteFormTrustedProxies() int {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.TrustedProxyCount
}

// siteFormIdempotency is the optional Idempotency-Key middleware of one site
// form route. A request without the header runs as usual; with it, the same
// key and body replay the first answer (a Turnstile token is single-use, so a
// retry must not be verified again) and another body is 409. Without a store
// it is a pass-through.
func (s *Server) siteFormIdempotency(route string) func(http.Handler) http.Handler {
	if s.idem == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	opts := idempotency.Options{
		Scope: "POST " + route,
		ScopeFunc: func(r *http.Request) string {
			return "POST " + route + ":" + chi.URLParam(r, "feed_token")
		},
		TTL:          24 * time.Hour,
		Optional:     true,
		MaxBodyBytes: siteFormMaxBody,
	}
	if s.typedMetrics != nil {
		metrics := s.typedMetrics
		opts.OnReplay = func() { metrics.IdempotencyReplaysTotal.Inc() }
	}
	return idempotency.Middleware(s.idem, opts)
}

func (s *Server) handleSiteContact(w http.ResponseWriter, r *http.Request) {
	(&hsite.ContactHandler{
		Pool: s.pgxPool, Guard: s.siteFormGuard(), IPSalt: s.siteFormSalt(),
		TrustedProxies: s.siteFormTrustedProxies(), Logger: s.logger,
	}).HandleSend(w, r)
}

func (s *Server) handleSiteSubscribe(w http.ResponseWriter, r *http.Request) {
	(&hsite.SubscribeHandler{
		Pool: s.pgxPool, Guard: s.siteFormGuard(),
		TrustedProxies: s.siteFormTrustedProxies(), Logger: s.logger,
	}).HandleSubscribe(w, r)
}

// myTicketsCaptcha is the Turnstile gate of the "my tickets" link request: the
// site forms' guard with action my_tickets, required only for a channel that
// lists site origins. Nil without a database (the route then answers 503 on
// its own).
func (s *Server) myTicketsCaptcha() hfeed.MyTicketsCaptcha {
	if s.pgxPool == nil {
		return nil
	}
	guard := s.siteFormGuard()
	pool := s.pgxPool
	lookup := func(ctx context.Context, feedToken string) (hsite.Channel, error) {
		return hsite.ChannelByFeedToken(ctx, pool, feedToken)
	}
	return func(ctx context.Context, feedToken, captchaToken, ip string) hsite.CaptchaOutcome {
		return guard.CaptchaForFeed(ctx, lookup, feedToken, captchaToken, ip, hsite.ActionMyTickets)
	}
}
