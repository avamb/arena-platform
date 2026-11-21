package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

// checkoutStartIdempotencyTTL is how long a checkout/start answer is
// replayed for one Idempotency-Key (spec 62 §5.4 п. 2.1).
const checkoutStartIdempotencyTTL = 24 * time.Hour

// checkoutStartIdempotency returns the Idempotency-Key middleware of
// POST /v1/public/feeds/{feed_token}/checkout/start, or nil without a store.
//
// The key is OPTIONAL there (a request without one runs as it always did),
// scoped per feed token, with no actor: the buyer is anonymous. The same key
// and body replay the stored 201 — the same checkout and payment link — even
// after the hold has been consumed; the same key with another body is 409.
// Only a 2xx answer is stored, so a failed attempt can be retried with the
// same key.
//
// Built once per process: the middleware's singleflight group is what makes
// two identical requests in flight run the checkout once.
func (s *Server) checkoutStartIdempotency() func(http.Handler) http.Handler {
	if s.idem == nil {
		return nil
	}
	s.checkoutStartIdemOnce.Do(func() {
		opts := idempotency.Options{
			Scope:        "POST checkout/start",
			ScopeFunc:    hfeed.CheckoutStartIdempotencyScope,
			TTL:          checkoutStartIdempotencyTTL,
			Optional:     true,
			MaxBodyBytes: hfeed.CheckoutStartMaxBody,
		}
		if s.typedMetrics != nil {
			metrics := s.typedMetrics
			opts.OnReplay = func() { metrics.IdempotencyReplaysTotal.Inc() }
		}
		s.checkoutStartIdem = idempotency.Middleware(s.idem, opts)
	})
	return s.checkoutStartIdem
}
