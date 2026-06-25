// slow.go implements GET /v1/info-slow — a synthetic endpoint used exclusively
// for graceful-shutdown integration testing (feature #26). The handler sleeps
// for slowDelay (default 5 s) to simulate a long-running request, allowing
// tests to verify that in-flight requests complete when SIGTERM is received.
//
// This endpoint intentionally has no authentication guard so it can be reached
// without credentials during shutdown tests.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const defaultSlowDelay = 5 * time.Second

// handleInfoSlow serves GET /v1/info-slow.
// It sleeps for s.slowDelay (default: 5 s) to simulate a long-running
// handler, then returns the same JSON envelope as /v1/info (minus the
// database fields) so callers can confirm the response was not truncated
// by a forced shutdown.
func (s *Server) handleInfoSlow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	delay := s.slowDelay
	if delay <= 0 {
		delay = defaultSlowDelay
	}

	logger.Info("info-slow: sleeping", "delay", delay.String())

	select {
	case <-time.After(delay):
		// Delay elapsed normally — send the response.
	case <-ctx.Done():
		// Request context was cancelled (client disconnected or server
		// shutdown reached the per-request deadline). Return 503 so
		// automated clients can distinguish a cancelled request from a
		// successful one.
		logger.Warn("info-slow: context cancelled before sleep completed", "reason", ctx.Err())
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "cancelled",
			"message": "request cancelled before completion",
		})
		return
	}

	logger.Info("info-slow: sleep complete, sending response")

	var dbVersion, dbNow string
	if s.pool != nil {
		var t time.Time
		err := s.pool.QueryRow(ctx,
			`SELECT current_setting('server_version') AS version, now() AS db_now`,
		).Scan(&dbVersion, &t)
		if err == nil {
			dbNow = t.UTC().Format(time.RFC3339Nano)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"app":         s.cfg.AppName,
		"version":     s.cfg.AppVersion,
		"server_time": time.Now().UTC().Format(time.RFC3339Nano),
		"db_version":  dbVersion,
		"db_now":      dbNow,
	})
}
