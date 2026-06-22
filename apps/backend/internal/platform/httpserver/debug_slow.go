// debug_slow.go provides the GET /v1/debug/slow endpoint used to verify
// per-request timeout behaviour (feature #53). The handler sleeps for
// debugSlowDelay (default 35 s) before returning a 200 OK response. When the
// per-request context deadline fires first (e.g. because REQUEST_TIMEOUT_SECONDS
// defaults to 30 s), the handler detects ctx.Done() and writes:
//
//	HTTP 503  {"error":{"code":"http.request_timeout",...}}
//
// This endpoint is only mounted when DebugRoutesEnabled=true (via
// Options.DebugRoutesEnabled). It MUST NOT be enabled in production because it
// consumes a goroutine and connection for the full sleep duration when the timeout
// does NOT fire.
//
// Relationship to /v1/info-slow:
//
//   /v1/info-slow  — exists for graceful-shutdown tests; writes {"status":"cancelled"}
//                    when context is cancelled; always mounted.
//   /v1/debug/slow — exists for request-timeout tests; writes the standardised
//                    JSON error envelope with code='http.request_timeout'; only
//                    mounted when DebugRoutesEnabled=true.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// defaultDebugSlowDelay is the sleep duration used by GET /v1/debug/slow when
// Server.debugSlowDelay is zero. It is intentionally longer than the default
// REQUEST_TIMEOUT_SECONDS (30 s) so the default configuration always triggers
// the timeout path.
const defaultDebugSlowDelay = 35 * time.Second

// handleDebugSlow serves GET /v1/debug/slow.
//
// The handler sleeps for s.debugSlowDelay (default: 35 s) to simulate a
// long-running request.  Two completion paths:
//
//  1. The per-request context deadline fires first (timeout scenario). The
//     handler writes HTTP 503 with the project-standard JSON error envelope
//     and code='http.request_timeout'.
//
//  2. The sleep elapses without a context cancellation (non-timeout scenario).
//     This happens when REQUEST_TIMEOUT_SECONDS is set to a value longer than
//     the sleep duration (step 5 of feature #53). The handler writes HTTP 200.
func (s *Server) handleDebugSlow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	delay := s.debugSlowDelay
	if delay <= 0 {
		delay = defaultDebugSlowDelay
	}

	logger.Info("debug-slow: sleeping", "delay", delay.String())

	select {
	case <-time.After(delay):
		// Delay elapsed normally — the timeout did not fire.
		logger.Info("debug-slow: sleep complete, sending 200")
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"elapsed": delay.String(),
		})

	case <-ctx.Done():
		// Per-request context deadline exceeded (or client disconnected).
		// Return the project-standard JSON error envelope with the stable
		// machine-readable code 'http.request_timeout' so callers can
		// distinguish a server-side timeout from a dependency failure (503
		// with code='dependency.database_unavailable').
		logger.Warn("debug-slow: context cancelled before sleep completed",
			"reason", ctx.Err(),
			"delay", delay.String(),
		)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("http.request_timeout", "request timed out", r))
	}
}
