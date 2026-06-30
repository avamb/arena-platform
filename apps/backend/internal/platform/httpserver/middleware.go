// Package httpserver — chi middleware unique to arena_new.
//
// This file owns three small middlewares that the operational and /v1
// routes share:
//
//   - requestContext: copies chi's per-request RequestID into the slog
//     context (via logging.WithRequestID) so every downstream log line
//     attached to ctx automatically carries a "request_id" attribute.
//   - traceContext:   generates a UUIDv7-style trace identifier per request,
//     attaches it to ctx via logging.WithTraceID, surfaces it back to the
//     client through the X-Trace-Id response header, and emits an "http
//     request start" / "http request end" log pair. When the OpenTelemetry
//     SDK is wired in a later feature this middleware can be retired or
//     refactored to pull the trace_id from the active SpanContext.
//   - jsonBodyLimit:  enforces cfg.BodyLimitBytes for POST/PUT/PATCH so a
//     huge payload cannot exhaust process memory before chi/Timeout kicks in.
//
// The "auth" middleware is intentionally NOT here — it lives in the auth
// package alongside the StubProvider so the boundary stays cohesive.
package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// HeaderTraceID is the response header carrying the resolved trace identifier.
const HeaderTraceID = "X-Trace-Id"

// requestContext propagates chi's RequestID into the slog ctx so all log
// records tagged via logging.FromContext(ctx) automatically include a
// "request_id" attribute, AND mirrors that identifier into the
// `X-Request-Id` response header so clients can correlate failures with
// server-side traces without parsing the body.
//
// chi's own RequestID middleware only stores the identifier on context;
// surfacing it to the client is this middleware's responsibility.
//
//nolint:unused // retained as drop-in replacement when adapters/http router is bypassed
func requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := chimw.GetReqID(r.Context())
		if reqID == "" {
			reqID = newTraceID()
		}
		// Always set the response header — the contract is "every response
		// carries X-Request-Id", and feature #6 verifies it explicitly.
		w.Header().Set("X-Request-Id", reqID)
		ctx := logging.WithRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// traceContext attaches a trace identifier to ctx (via logging.WithTraceID),
// surfaces it through the X-Trace-Id response header, and logs the request
// start / end with elapsed_ms. The trace_id is a 32-hex-character random
// string until the OpenTelemetry SDK is wired and can supply real
// SpanContext trace ids over the same key.
//
//nolint:unused // retained as drop-in replacement when adapters/http router is bypassed
func traceContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get(HeaderTraceID)
		if traceID == "" {
			traceID = newTraceID()
		}
		w.Header().Set(HeaderTraceID, traceID)

		ctx := logging.WithTraceID(r.Context(), traceID)

		start := time.Now()
		logger := logging.FromContext(ctx)
		logger.Info("http request start",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_ip", clientIP(r),
		)

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(ctx))

		logger.Info("http request end",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes_out", ww.BytesWritten(),
			"elapsed_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
	})
}

// jsonBodyLimit wraps r.Body with http.MaxBytesReader so a single oversized
// payload cannot exhaust process memory. The limit is enforced for
// POST/PUT/PATCH; safe methods are passed through unchanged.
//
//nolint:unused // retained as drop-in replacement when adapters/http router is bypassed
func jsonBodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				if maxBytes > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// newTraceID produces a 32-hex-character identifier (128 bits). Cryptographic
// randomness is overkill for an observability identifier, but the W3C trace
// context spec mandates 16 random bytes — matching that shape now means a
// future OTEL wiring can drop in seamlessly.
//
//nolint:unused // helper for the unused-marked middlewares above
func newTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a timestamp-derived id; never panic on a hot path.
		t := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(t >> (i * 8)) //nolint:gosec // intentional low-byte truncation
		}
	}
	return hex.EncodeToString(b[:])
}

// clientIP delegates to httputil.ClientIP. Kept as an unexported alias so
// existing handler methods on *Server require no import changes.
func clientIP(r *http.Request) string {
	return httputil.ClientIP(r)
}
