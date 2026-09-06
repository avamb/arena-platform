// server_apikey_auth.go — organization API-key ("service") authentication for
// the v1 surface (spec §13.1, feature #513 / epic #466 W1-C1b).
//
// applyAuth previously wired auth.Middleware (JWT only). It now wires
// s.authMiddleware(), which inspects the bearer token: anything starting with
// the `ak_` wire prefix is authenticated against the api_keys table, anything
// else falls through to the unchanged JWT chain. The resulting principal is an
// auth.Actor{Type: service} whose Roles are empty and whose Permissions are
// exactly api_keys.scopes — permissions.DBChecker has a matching service
// branch, and the six org-membership guards accept it only for
// api_keys.org_id.
package httpserver

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// APIKeyRateLimit is the per-key request budget enforced on the v1 surface
// (spec §13.1: "rate limit … ключ = api_key.id, 600 req/min").
const APIKeyRateLimit = 600

// APIKeyRateWindow is the sliding window over which APIKeyRateLimit applies.
const APIKeyRateWindow = time.Minute

// newAPIKeyRateLimiter builds the limiter used by authenticateAPIKey. Kept as
// a constructor so wire.go and tests share one definition of the budget.
func newAPIKeyRateLimiter() ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{
		MaxAttempts: APIKeyRateLimit,
		Window:      APIKeyRateWindow,
	})
}

// serviceKeyFromHeader returns the raw API key when the request carries an
// `Authorization: Bearer ak_…` header. It performs no validation beyond the
// wire prefix — a JWT (or any other bearer value) yields ok=false so the
// caller can fall through to the JWT chain untouched.
func serviceKeyFromHeader(r *http.Request) (string, bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return "", false
	}
	tok := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(tok, apikeys.KeyWirePrefix) {
		return "", false
	}
	return tok, true
}

// authMiddleware returns the authentication middleware used by applyAuth. It
// dispatches on the bearer token shape: `ak_…` goes through the API-key path,
// everything else through the existing JWT middleware, which is constructed
// once here rather than per request.
func (s *Server) authMiddleware() func(http.Handler) http.Handler {
	jwt := auth.Middleware(s.authProvider(), auth.MiddlewareOptions{Logger: s.logger})
	return func(next http.Handler) http.Handler {
		jwtChain := jwt(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, isServiceKey := serviceKeyFromHeader(r)
			if !isServiceKey {
				jwtChain.ServeHTTP(w, r)
				return
			}
			actor, ok := s.authenticateAPIKey(w, r, raw)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
		})
	}
}

// authenticateAPIKey verifies raw against the api_keys table, enforces the
// per-key rate limit and refreshes last_used_at (throttled to once a minute by
// apikeys.TouchLastUsed). On failure it writes the response itself and returns
// ok=false.
//
// Every failure mode collapses into a single 401 auth.invalid_token so a
// caller cannot distinguish "unknown prefix" from "wrong secret" from
// "revoked" — the same information-leak reasoning auth.Middleware applies to
// forged JWT signatures. The precise reason is logged at WARN level.
func (s *Server) authenticateAPIKey(w http.ResponseWriter, r *http.Request, raw string) (auth.Actor, bool) {
	if s.apiKeyStore == nil {
		s.writeAPIKeyUnauthorized(w, r)
		return auth.Actor{}, false
	}

	now := time.Now()
	if s.clk != nil {
		now = s.clk.Now()
	}

	key, err := apikeys.Authenticate(r.Context(), s.apiKeyStore, raw, now)
	if err != nil {
		if isAPIKeyRejection(err) {
			s.logger.Warn("apikeys: authentication rejected", "reason", err.Error())
			s.writeAPIKeyUnauthorized(w, r)
			return auth.Actor{}, false
		}
		s.logger.Error("apikeys: authentication failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"auth.api_key_check_failed", "failed to verify the API key", r,
		))
		return auth.Actor{}, false
	}

	// Rate limit is keyed by api_key.id, so two keys of the same organization
	// get independent budgets and a noisy integration cannot starve the rest.
	if s.apiKeyRL != nil && !s.apiKeyRL.Allow(key.ID.String()) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"auth.rate_limited", "API key request rate limit exceeded; retry shortly", r,
		))
		return auth.Actor{}, false
	}

	if _, err := apikeys.TouchLastUsed(r.Context(), s.apiKeyStore, key, now); err != nil {
		// A failed last_used_at write is bookkeeping, not authorization:
		// log it and let the authenticated request proceed.
		s.logger.Warn("apikeys: last_used_at update failed", "error", err.Error())
	}

	return auth.Actor{
		ID:          key.ID.String(),
		Type:        auth.ActorTypeService,
		Permissions: key.Scopes,
		OrgID:       key.OrgID.String(),
		RawToken:    raw,
	}, true
}

// isAPIKeyRejection reports whether err is a caller-visible credential
// rejection (as opposed to an infrastructure failure such as a dropped
// connection), which must answer 401 rather than 500.
func isAPIKeyRejection(err error) bool {
	return errors.Is(err, apikeys.ErrMalformed) ||
		errors.Is(err, apikeys.ErrNotFound) ||
		errors.Is(err, apikeys.ErrSecretMismatch) ||
		errors.Is(err, apikeys.ErrRevoked) ||
		errors.Is(err, apikeys.ErrExpired)
}

// writeAPIKeyUnauthorized emits the uniform 401 envelope plus the RFC 7235
// challenge header, matching what auth.Middleware sends for a bad JWT.
func (s *Server) writeAPIKeyUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(auth.HeaderWWWAuthenticate, `Bearer realm="arena"`)
	httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
		"auth.invalid_token", "the presented API key is not valid", r,
	))
}
