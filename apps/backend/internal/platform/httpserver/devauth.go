// Package httpserver — devauth.go exposes the development-only JWT issuance
// endpoint that uses the github.com/golang-jwt/jwt/v5 library directly
// (rather than the manual HS256 implementation in StubProvider).
//
// Path: POST /v1/dev/auth/token
// Body: {"actor_id": "<uuid>", "org_id": "<uuid>", "roles": ["admin"],
//
//	"ttl_seconds": 3600}
//
// Returns: 200 {"token":"<jwt>","expires_at":"<rfc3339>","actor_id":"..."}
//
// # PLACEHOLDER
//
// This endpoint is part of the Auth Boundary PLACEHOLDER for the Backend
// Foundation Milestone. It is ONLY mounted when cfg.EnableStubAuth == true
// (i.e. ENABLE_DEV_AUTH=true in the environment). Production must run with
// ENABLE_DEV_AUTH=false — the route literally does not exist on the router
// in production deployments.
//
// The primary purpose of this endpoint is to bootstrap dev/test JWTs that
// the ValidateJWT middleware (in jwt_middleware.go) can later verify. Both
// share the same JWT_SIGNING_SECRET so tokens minted here are accepted
// transparently by the auth middleware.
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// devAuthTokenRequest is the POST body for /v1/dev/auth/token.
// All fields are optional — sane defaults are applied when missing.
type devAuthTokenRequest struct {
	// ActorID is the UUID to embed in the "sub" claim.
	// Defaults to "00000000-0000-0000-0000-000000000001" when empty.
	ActorID string `json:"actor_id"`

	// OrgID is an optional organisation UUID embedded as the "org_id" claim.
	// Omit to produce a personal / dev token with no org binding.
	OrgID string `json:"org_id,omitempty"`

	// Roles is the coarse-grained role set embedded in the "roles" claim.
	Roles []string `json:"roles,omitempty"`

	// TTLSeconds controls token lifetime. Defaults to 3600 (1 hour) when ≤ 0.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// devAuthTokenResponse is the JSON body returned on success.
type devAuthTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	ActorID   string `json:"actor_id"`
	Issuer    string `json:"issuer"`
	Audience  string `json:"audience"`
}

// handleDevAuthToken serves POST /v1/dev/auth/token.
// It mints an HS256 JWT via auth.IssueJWT (the jwt/v5-backed issuer).
func (s *Server) handleDevAuthToken(w http.ResponseWriter, r *http.Request) {
	// Guard: only callable when stub auth is enabled and the signing secret
	// is configured. Double-checked here even though the route is only mounted
	// when s.stub != nil && s.stub.Enabled().
	if s.stub == nil || !s.stub.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable,
			errorEnvelope("auth.disabled",
				"dev auth token mint is disabled (ENABLE_DEV_AUTH=false)", r))
		return
	}

	var req devAuthTokenRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorEnvelope("http.bad_request",
					"request body is not valid JSON: "+err.Error(), r))
			return
		}
	}

	// Apply defaults.
	if strings.TrimSpace(req.ActorID) == "" {
		req.ActorID = "00000000-0000-0000-0000-000000000001"
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}

	actorID, err := uuid.Parse(req.ActorID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest,
			errorEnvelope("http.bad_request",
				"actor_id must be a valid UUID: "+err.Error(), r))
		return
	}

	var orgIDPtr *uuid.UUID
	if strings.TrimSpace(req.OrgID) != "" {
		parsed, err := uuid.Parse(req.OrgID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorEnvelope("http.bad_request",
					"org_id must be a valid UUID: "+err.Error(), r))
			return
		}
		orgIDPtr = &parsed
	}

	// Mint the token using the jwt/v5-backed IssueJWT function.
	token, exp, err := auth.IssueJWT(
		s.cfg.JWTSecretStub,
		actorID,
		orgIDPtr,
		req.Roles,
		s.stub.Issuer(),
		s.stub.Audience(),
		ttl,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError,
			errorEnvelope("auth.token_mint_failed", err.Error(), r))
		return
	}

	writeJSON(w, http.StatusOK, devAuthTokenResponse{
		Token:     token,
		ExpiresAt: exp.UTC().Format(time.RFC3339),
		ActorID:   req.ActorID,
		Issuer:    s.stub.Issuer(),
		Audience:  s.stub.Audience(),
	})
}
