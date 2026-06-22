// devtoken.go exposes a development-only HTTP endpoint that mints a JWT
// from the stub IdentityProvider.
//
// Path: POST /v1/dev/token
// Body: {"actor_id": "<uuid>", "actor_type": "stub_user", "roles": ["admin"],
//        "ttl_seconds": 3600, "audience": "arena-api"}
// Returns: 200 {"token": "<jwt>", "expires_at": "<rfc3339>", ...}
//
// The route is ONLY mounted when ENABLE_DEV_AUTH=true (i.e. cfg.EnableStubAuth
// is true and a StubProvider was constructed). Production must run with
// ENABLE_DEV_AUTH=false (enforced by config.Validate), which means this
// handler simply does not exist on the router — there is no runtime check to
// bypass.
//
// This endpoint is intentionally outside the auth middleware: its purpose is
// to bootstrap the very token the auth middleware will then validate. Without
// it, the only way to get a dev JWT would be a CLI subcommand on the
// container, which is awkward for bash-driven integration tests like the
// query-tracer verification driven by feature #5.
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// devTokenRequest is the POST body. Defaults applied when fields are zero:
//
//   - actor_id   : "00000000-0000-0000-0000-000000000001"
//   - actor_type : "stub_user"
//   - roles      : []
//   - ttl_seconds: 3600
type devTokenRequest struct {
	ActorID    string   `json:"actor_id"`
	ActorType  string   `json:"actor_type"`
	Roles      []string `json:"roles"`
	TTLSeconds int      `json:"ttl_seconds"`
	Audience   string   `json:"audience"`
}

// devTokenResponse is the JSON body returned on success.
type devTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	ActorID   string `json:"actor_id"`
	ActorType string `json:"actor_type"`
	Issuer    string `json:"issuer"`
	Audience  string `json:"audience"`
}

// handleDevToken serves POST /v1/dev/token using the StubProvider.
func (s *Server) handleDevToken(w http.ResponseWriter, r *http.Request) {
	if s.stub == nil || !s.stub.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("auth_disabled",
			"dev token mint is disabled (ENABLE_DEV_AUTH=false)", r))
		return
	}

	var req devTokenRequest
	// Empty bodies are fine — defaults will fill in the gaps.
	if r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("invalid_json",
				"request body is not valid JSON: "+err.Error(), r))
			return
		}
	}

	if strings.TrimSpace(req.ActorID) == "" {
		req.ActorID = "00000000-0000-0000-0000-000000000001"
	}
	if strings.TrimSpace(req.ActorType) == "" {
		req.ActorType = string(auth.ActorTypeStubUser)
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second

	token, exp, err := s.stub.IssueToken(r.Context(), auth.IssueRequest{
		ActorID:   req.ActorID,
		ActorType: auth.ActorType(req.ActorType),
		Roles:     req.Roles,
		Audience:  req.Audience,
		TTL:       ttl,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("token_mint_failed", err.Error(), r))
		return
	}

	writeJSON(w, http.StatusOK, devTokenResponse{
		Token:     token,
		ExpiresAt: exp.UTC().Format(time.RFC3339),
		ActorID:   req.ActorID,
		ActorType: req.ActorType,
		Issuer:    s.stub.Issuer(),
		Audience:  s.stub.Audience(),
	})
}

// errorEnvelope returns the project-standard error body shape (see
// app_spec.txt §api_response_envelope) so every error served by the
// dev-token / echo handlers is consistently structured. The envelope
// carries request_id and trace_id resolved from the chi RequestID context
// and the slog ctx respectively — both are exposed as response headers
// (`X-Request-Id`, `X-Trace-Id`) so clients can correlate body, headers,
// and server-side logs without any custom plumbing.
func errorEnvelope(code, message string, r *http.Request) map[string]any {
	requestID := ""
	traceID := ""
	if r != nil {
		if id := chimw.GetReqID(r.Context()); id != "" {
			requestID = id
		} else {
			requestID = strings.TrimSpace(r.Header.Get("X-Request-Id"))
		}
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
