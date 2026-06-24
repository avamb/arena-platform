// auth_logout.go implements POST /v1/auth/logout (feature #118).
//
// Logout flow:
//  1. Require a valid JWT in the Authorization header (user must be authenticated).
//  2. Parse the request body for the refresh_token to revoke.
//  3. Fetch the refresh token row from the database; return 404 when not found.
//  4. Verify the token belongs to the authenticated actor (prevents cross-user
//     revocation via token-theft).
//  5. If the token is already revoked → return 204 (idempotent logout).
//  6. Revoke the token in the database (sets revoked_at = now()).
//  7. If a session store is wired, call RevokeSession to:
//       a. Write "arena:revoked:{token}" in Redis with TTL = remaining lifetime.
//       b. Remove the token from "arena:sessions:{userID}" sorted set.
//  8. Return 204 No Content.
//
// The endpoint is protected by auth.Middleware so unauthenticated requests
// receive a 401 before reaching the handler.
package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/jackc/pgx/v5"
)

// handleAuthLogout serves POST /v1/auth/logout.
// The endpoint is mounted only when the auth stub and pool are wired (see
// server.go mountV1Routes). The JWT middleware runs before this handler, so
// actor is always present when the handler fires.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- 1. Retrieve the authenticated actor ---
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		// Should never happen (middleware enforces auth), but guard defensively.
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthenticated", "authentication required", r))
		return
	}

	// --- 2. Parse request body ---
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.empty_body", "request body is required", r))
		return
	}

	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.refresh_token_required",
			"refresh_token is required",
			r,
			map[string]any{"field": "refresh_token"},
		))
		return
	}

	// --- 3. Require pool ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.logout: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 4. Look up the refresh token ---
	row, err := q.GetRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusNotFound, errorEnvelope("auth.refresh_token_not_found", "refresh token not found", r))
			return
		}
		logger.Error("auth.logout: get refresh token failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 5. Verify token ownership ---
	if row.UserID.String() != actor.ID {
		// The authenticated user is trying to revoke someone else's token.
		// Return 403 to indicate the operation is forbidden (not 404, which
		// could leak information about token existence).
		writeJSON(w, http.StatusForbidden, errorEnvelope("auth.logout_forbidden", "refresh token does not belong to the authenticated user", r))
		return
	}

	// --- 6. Idempotent: already revoked → 204 ---
	if row.RevokedAt != nil {
		// Token was already revoked. Ensure it is also in the Redis revocation
		// set (handles the case where logout was processed without Redis, and
		// Redis is now available).
		if s.sessionStore != nil {
			_ = s.sessionStore.RevokeSession(ctx, actor.ID, req.RefreshToken, row.ExpiresAt)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// --- 7. Revoke in database ---
	if err := q.RevokeRefreshToken(ctx, req.RefreshToken); err != nil {
		logger.Error("auth.logout: revoke refresh token failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.revoke_failed", "failed to revoke refresh token", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.logout: commit failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.transaction_failed", "failed to commit transaction", r))
		return
	}

	// --- 8. Update Redis revocation store ---
	if s.sessionStore != nil {
		if redisErr := s.sessionStore.RevokeSession(ctx, actor.ID, req.RefreshToken, row.ExpiresAt); redisErr != nil {
			// Redis failure is non-fatal: the DB revocation is already committed.
			// Log a warning — the Redis key will be populated on next refresh attempt
			// which falls through to the DB check on Redis miss.
			logger.Warn("auth.logout: redis revoke session failed (token still revoked in DB)",
				"error", redisErr,
				"user_id", actor.ID,
			)
		}
	}

	slog.Info("auth.logout: session revoked",
		"user_id", actor.ID,
		"token_prefix", req.RefreshToken[:min(len(req.RefreshToken), 8)],
	)

	// --- 9. Return 204 No Content ---
	w.WriteHeader(http.StatusNoContent)
}
