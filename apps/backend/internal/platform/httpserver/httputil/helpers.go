// Package httputil provides shared HTTP helpers used by httpserver handlers.
// Extracting these functions here breaks the circular-import barrier that
// would otherwise prevent domain sub-packages (hauth, hcatalog, …) from
// calling WriteJSON or ErrorEnvelope without importing httpserver itself.
package httputil

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// WriteJSON serialises payload as JSON and writes it with the given HTTP
// status code. The Content-Type header is set to application/json.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// ErrorEnvelope builds the standard arena JSON error response body.
// request_id and trace_id are extracted from r's context when r is non-nil.
func ErrorEnvelope(code, message string, r *http.Request) map[string]any {
	requestID := ""
	traceID := ""
	if r != nil {
		requestID = logging.RequestID(r.Context())
		traceID = logging.TraceID(r.Context())
	}
	return map[string]any{
		"error": map[string]any{
			"code":       code,
			"message":    message,
			"request_id": requestID,
			"trace_id":   traceID,
		},
	}
}

// ErrorEnvelopeWithDetails is identical to ErrorEnvelope but additionally
// sets error.details to the provided map, making error context machine-readable.
func ErrorEnvelopeWithDetails(code, message string, r *http.Request, details map[string]any) map[string]any {
	env := ErrorEnvelope(code, message, r)
	if details != nil {
		env["error"].(map[string]any)["details"] = details
	}
	return env
}

// UUIDPathParam extracts the chi URL parameter named paramName and parses it
// as a UUID. On success it returns (id, true). On failure it writes a 400
// JSON error envelope and returns (uuid.UUID{}, false). Callers must return
// immediately when ok==false.
func UUIDPathParam(w http.ResponseWriter, r *http.Request, paramName string) (uuid.UUID, bool) {
	raw := chi.URLParam(r, paramName)
	id, err := uuid.Parse(raw)
	if err != nil {
		env := ErrorEnvelopeWithDetails(
			"http.invalid_path_param",
			"path parameter '"+paramName+"' must be a valid UUID, got: '"+raw+"'",
			r,
			map[string]any{"param": paramName},
		)
		WriteJSON(w, http.StatusBadRequest, env)
		return uuid.UUID{}, false
	}
	return id, true
}

// ClientIP extracts the real client IP from the request, preferring
// X-Forwarded-For (first hop) over RemoteAddr.
func ClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); ip != "" {
		if i := strings.IndexByte(ip, ','); i >= 0 {
			ip = ip[:i]
		}
		return strings.TrimSpace(ip)
	}
	return r.RemoteAddr
}

// ExtractClientIP returns a validated IP string from the request, checking
// X-Forwarded-For, X-Real-IP, and RemoteAddr in order. Returns "" when no
// valid IP is found so callers can store NULL in the DB rather than fail.
func ExtractClientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if idx := strings.Index(xff, ","); idx > 0 {
			xff = xff[:idx]
		}
		if ip := net.ParseIP(strings.TrimSpace(xff)); ip != nil {
			return ip.String()
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}
