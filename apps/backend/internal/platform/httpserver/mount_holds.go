package httpserver

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// holdsIdempotencyTTL is how long a hold answer is replayed for one
// Idempotency-Key: the widget sends one key per click.
const holdsIdempotencyTTL = time.Hour

// mountHoldRoutes mounts the public seat hold at the click (spec
// 08_architecture/67, section 4). The routes are always mounted; with
// HOLD_ON_CLICK_ENABLED off every one answers 404 hold.disabled.
//
//	POST   /public/feeds/{feed_token}/holds
//	GET    /public/holds/{hold_token}
//	DELETE /public/holds/{hold_token}
//	DELETE /public/holds/{hold_token}/seats/{seat_key}
//	POST   /public/holds/{hold_token}/extend
func (s *Server) mountHoldRoutes(r chi.Router) {
	idem := s.holdsIdempotency()
	holds := func() *hfeed.HoldsHandler {
		cfg := hfeed.HoldSettings{Idempotency: idem}
		if s.cfg != nil {
			cfg.Enabled = s.cfg.HoldOnClickEnabled
			cfg.IPLimitEnabled = s.cfg.HoldIPLimitEnabled
			cfg.VisitorKey = s.cfg.HoldVisitorKey
		}
		return s.feedHandler().Holds(cfg)
	}
	r.Post("/public/feeds/{feed_token}/holds", func(w http.ResponseWriter, r *http.Request) {
		holds().HandleCreateHold(w, r)
	})
	r.Get("/public/holds/{hold_token}", func(w http.ResponseWriter, r *http.Request) {
		holds().HandleGetHold(w, r)
	})
	r.Delete("/public/holds/{hold_token}", func(w http.ResponseWriter, r *http.Request) {
		holds().HandleReleaseHold(w, r)
	})
	r.Delete("/public/holds/{hold_token}/seats/{seat_key}", func(w http.ResponseWriter, r *http.Request) {
		holds().HandleReleaseHoldSeat(w, r)
	})
	r.Post("/public/holds/{hold_token}/extend", func(w http.ResponseWriter, r *http.Request) {
		holds().HandleExtendHold(w, r)
	})
}

// holdsIdempotency is the Idempotency-Key middleware of POST .../holds, or
// nil without a store. Like checkout/start the key is optional, scoped per
// feed token, with no actor; only a 2xx answer is stored. It is built once
// per router, so its singleflight group runs two identical clicks in flight
// once.
func (s *Server) holdsIdempotency() func(http.Handler) http.Handler {
	if s.idem == nil {
		return nil
	}
	opts := idempotency.Options{
		Scope:        "POST holds",
		ScopeFunc:    hfeed.HoldsIdempotencyScope,
		TTL:          holdsIdempotencyTTL,
		Optional:     true,
		MaxBodyBytes: hfeed.HoldsMaxBody,
	}
	if s.typedMetrics != nil {
		metrics := s.typedMetrics
		opts.OnReplay = func() { metrics.IdempotencyReplaysTotal.Inc() }
	}
	return idempotency.Middleware(s.idem, opts)
}
