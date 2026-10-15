package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmacsscan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// macsScanBatchesPerMinute is the batch budget of one MACS subscriber
// (in the memory of one arena-api process, like the other webhook limits):
// 120 batches of up to 500 scans is far above what any door produces.
const macsScanBatchesPerMinute = 120

// serverMACSScanRL is shared across requests; tests may swap it.
var serverMACSScanRL = ratelimit.New(ratelimit.Config{MaxAttempts: macsScanBatchesPerMinute, Window: time.Minute})

// handleMACSScanEvents serves POST /v1/macs/scan-events (task M1). The
// handler self-gates with 503 when the server has no database, so the route
// is always mounted and visible to the openapi-drift check.
func (s *Server) handleMACSScanEvents(w http.ResponseWriter, r *http.Request) {
	var (
		pool hmacsscan.TxStarter
		subs hmacsscan.SubscriberLookup
	)
	if s.pool != nil && s.pgxPool != nil {
		pool = s.pool
		subs = gen.New(s.pgxPool)
	}
	hmacsscan.New(pool, subs, s.outboxWriter, s.audit, serverMACSScanRL, s.logger).HandleScanEvents(w, r)
}
