// debug_panic.go provides the GET /v1/debug/panic endpoint used to exercise
// the panic-recovery middleware in integration tests.
//
// This endpoint is only mounted when DEBUG_ROUTES_ENABLED=true (via
// Options.DebugRoutesEnabled). It must NEVER be enabled in production.
//
// The endpoint unconditionally calls panic("boom"), which the outermost
// panicRecoverer middleware in the adapter chain catches and converts into:
//
//   - HTTP 500 with the project-standard JSON error envelope.
//   - An slog ERROR log record including the panic message and stack trace.
//   - An increment of the arena_http_panics_total Prometheus counter.
//
// In development mode (APP_ENV=development) the stack trace is also echoed
// in the response body for developer convenience. In production / staging it
// is omitted.
package httpserver

import "net/http"

// handleDebugPanic intentionally panics to exercise the Recoverer middleware.
// It is registered only when DebugRoutesEnabled is true.
func (s *Server) handleDebugPanic(_ http.ResponseWriter, _ *http.Request) {
	panic("boom")
}
