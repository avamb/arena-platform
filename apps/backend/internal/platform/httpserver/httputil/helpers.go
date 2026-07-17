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
//
// Deprecated: ClientIP trusts the client-supplied X-Forwarded-For header
// unconditionally and is vulnerable to IP spoofing. Use TrustedClientIP
// with an explicit trustedProxies count derived from your deployment
// topology for any security-sensitive operation (e.g. rate limiting).
func ClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); ip != "" {
		if i := strings.IndexByte(ip, ','); i >= 0 {
			ip = ip[:i]
		}
		return strings.TrimSpace(ip)
	}
	return r.RemoteAddr
}

// TrustedClientIP derives the real client IP using a hop-count approach
// that is resistant to X-Forwarded-For spoofing.
//
// When trustedProxies == 0 (the recommended default for most deployments),
// the X-Forwarded-For header is completely ignored and net.SplitHostPort is
// applied to r.RemoteAddr. This is always safe because RemoteAddr is set by
// the Go net/http server from the TCP connection and cannot be forged by the
// client.
//
// When trustedProxies == N (N > 0), each trusted proxy is expected to append
// one entry to the end of the X-Forwarded-For list. The real client IP is
// therefore the entry at position len(xff)-N-1 (0-indexed from the left). If
// the XFF list contains fewer than N+1 entries the function falls back to
// RemoteAddr to avoid returning an attacker-controlled value.
//
// Example: behind one nginx reverse proxy set trustedProxies=1. The XFF value
// would be "<real-client>, <nginx-added>"; the function returns <real-client>.
func TrustedClientIP(r *http.Request, trustedProxies int) string {
	remoteAddr := func() string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}

	if trustedProxies <= 0 {
		// Default: never trust XFF; use the TCP peer address.
		return remoteAddr()
	}

	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff == "" {
		return remoteAddr()
	}

	// Split into individual entries and trim whitespace.
	parts := strings.Split(xff, ",")
	ips := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			ips = append(ips, p)
		}
	}

	// The real client is at len(ips)-trustedProxies-1. If there are not
	// enough entries, the header is suspicious — fall back to RemoteAddr.
	idx := len(ips) - trustedProxies - 1
	if idx < 0 {
		return remoteAddr()
	}

	if ip := net.ParseIP(ips[idx]); ip != nil {
		return ip.String()
	}

	// Unparseable IP in the expected slot — fall back to RemoteAddr.
	return remoteAddr()
}

// RequireAdminReason validates the X-Admin-Reason header used by superadmin and
// network-mutation endpoints. Returns the non-empty trimmed reason on success, or
// writes a 400 error envelope and returns ("", false) so callers can return
// immediately.
func RequireAdminReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	reason := strings.TrimSpace(r.Header.Get("X-Admin-Reason"))
	if reason == "" {
		WriteJSON(w, http.StatusBadRequest, ErrorEnvelope(
			"superadmin.missing_reason",
			"X-Admin-Reason header is required for superadmin operations", r,
		))
		return "", false
	}
	return reason, true
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
