// login.go implements POST /v1/auth/login and POST /v1/auth/refresh
// (feature #115, hardened in feature #359 / PR2-03).
//
// Login flow:
//  1. Parse and validate the request body (email, password).
//  2. Check the per-IP+email rate limit (sliding window, 5 attempts / 15 min).
//  3. Look up the user by normalised email.
//  4. Verify the bcrypt password hash.
//  5. Issue an HS256 JWT access token (15-minute TTL) via auth.IssueJWT.
//  6. Generate a 64-char hex refresh token; store SHA-256(token) in DB (30-day TTL).
//     The raw token is returned to the client and tracked in Redis by raw value.
//  7. If a session store is wired (feature #118): evict excess sessions and
//     track the new session in Redis.
//  8. Return 200 with {access_token, refresh_token, token_type, expires_at}.
//
// Refresh flow (PR2-03 hardened):
//  1. Parse the refresh token from the request body.
//  2. Hash the token: tokenHash = SHA-256(rawToken).
//  3. Open a read-write DB transaction (rotation requires writes).
//  4. Fetch the token row by tokenHash; return 401 when not found.
//  5. If revoked_at IS NOT NULL → SESSION COMPROMISE: revoke all user sessions,
//     clear Redis session set, return 401 "auth.session_compromised".
//  6. Verify the token has not expired → 401.
//  7. Fetch the owning user to populate the new JWT claims.
//  8. Issue a new JWT access token (15-minute TTL).
//  9. Generate a new refresh token; revoke old token in DB, insert new hash.
//
// 10. Commit transaction.
// 11. Update Redis: revoke old raw token, track new raw token.
// 12. Return 200 with {access_token, refresh_token, token_type, expires_at}.
//
// Both endpoints are intentionally PUBLIC — no Authorization header required.
package hauth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 30 * 24 * time.Hour
)

// loginRateLimiterKey builds the per-key string used by the sliding-window
// rate limiter. The IP component is derived via httputil.TrustedClientIP so
// that a client cannot bypass the lockout by rotating the X-Forwarded-For
// header — when trustedProxyCount is 0 (the default) XFF is ignored entirely
// and the TCP RemoteAddr is used instead.
func (h *Handler) loginRateLimiterKey(r *http.Request, email string) string {
	return httputil.TrustedClientIP(r, h.trustedProxyCount) + ":" + email
}

// LoginRateLimiterKey is the exported form of loginRateLimiterKey, for use by
// the httpserver shim layer (auth_login_test.go calls loginRateLimiterKey directly
// from package httpserver via the shim forwarder in auth_shims.go).
func (h *Handler) LoginRateLimiterKey(r *http.Request, email string) string {
	return h.loginRateLimiterKey(r, email)
}

// Login serves POST /v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.empty_body", "request body is required", r))
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}

	email, err := users.NormalizeEmail(req.Email)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.email_required", "email is required", r,
			map[string]any{"field": "email"},
		))
		return
	}

	rlKey := h.loginRateLimiterKey(r, email)
	if !h.rateLimiter.Allow(rlKey) {
		logger.Warn("auth.login: rate limit exceeded", "email_prefix", email[:min(len(email), 5)])
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
			"auth.rate_limited", "too many login attempts; please wait before trying again", r,
			map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
		))
		return
	}

	if strings.TrimSpace(req.Password) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_required", "password is required", r,
			map[string]any{"field": "password"},
		))
		return
	}

	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	if strings.TrimSpace(h.jwtSecret) == "" {
		logger.Error("auth.login: JWT_SIGNING_SECRET is not configured")
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("auth.not_configured", "authentication is not configured", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.login: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	userRow, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if err == pgx.ErrNoRows {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
				"auth.invalid_credentials", "email or password is incorrect", r,
			))
			return
		}
		logger.Error("auth.login: get user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	if err := users.CheckPassword(userRow.PasswordHash, req.Password); err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.invalid_credentials", "email or password is incorrect", r,
		))
		return
	}

	h.rateLimiter.Reset(rlKey) // clear on successful login

	actorID := userRow.ID
	accessToken, exp, err := auth.IssueJWT(
		h.jwtSecret,
		actorID,
		nil,
		nil,
		h.jwtIssuer,
		h.jwtAudience,
		accessTokenTTL,
	)
	if err != nil {
		logger.Error("auth.login: issue JWT failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_mint_failed", "failed to issue access token", r))
		return
	}

	refreshToken, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.login: generate refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_generation_failed", "failed to generate refresh token", r))
		return
	}

	// PR2-03: store SHA-256(rawToken) in DB; return raw token to client only.
	refreshTokenHash := users.TokenHash(refreshToken)
	refreshExp := time.Now().UTC().Add(refreshTokenTTL)
	if err := q.InsertRefreshToken(ctx, refreshTokenHash, actorID, refreshExp); err != nil {
		logger.Error("auth.login: insert refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_insert_failed", "failed to save refresh token", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.login: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.transaction_failed", "failed to commit transaction", r))
		return
	}

	// Feature #118: session tracking + concurrent-session enforcement (post-commit, best-effort).
	if h.sessionStore != nil {
		userIDStr := actorID.String()
		now := time.Now().UTC()

		if h.maxConcurrentSessions > 0 {
			tokensToEvict, evictErr := h.sessionStore.PruneAndEvict(ctx, userIDStr, h.maxConcurrentSessions-1, now)
			if evictErr != nil {
				logger.Warn("auth.login: session prune failed (continuing)", "error", evictErr)
			} else if len(tokensToEvict) > 0 {
				evictTx, txErr := h.db.BeginTx(ctx, pgx.TxOptions{})
				if txErr != nil {
					logger.Warn("auth.login: cannot begin evict tx", "error", txErr)
				} else {
					eq := gen.New(evictTx)
					for _, tok := range tokensToEvict {
						if rErr := eq.RevokeRefreshToken(ctx, tok); rErr != nil {
							logger.Warn("auth.login: DB revoke evicted session failed",
								"error", rErr,
								"token_prefix", tok[:min(len(tok), 8)],
							)
						}
						_ = h.sessionStore.RevokeSession(ctx, userIDStr, tok, now.Add(refreshTokenTTL))
					}
					if cErr := evictTx.Commit(ctx); cErr != nil {
						logger.Warn("auth.login: commit evict tx failed", "error", cErr)
						_ = evictTx.Rollback(ctx)
					}
				}
			}
		}

		if trackErr := h.sessionStore.TrackSession(ctx, userIDStr, refreshToken, refreshExp); trackErr != nil {
			logger.Warn("auth.login: session track failed (continuing)", "error", trackErr)
		}
	}

	slog.Info("auth.login: successful login",
		"user_id", actorID.String(),
		"email_prefix", email[:min(len(email), 5)],
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"user_id":       actorID.String(),
	})
}

// Refresh serves POST /v1/auth/refresh.
// PR2-03: rotates the refresh token on every call and detects session compromise.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.empty_body", "request body is required", r))
		return
	}

	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.refresh_token_required", "refresh_token is required", r,
			map[string]any{"field": "refresh_token"},
		))
		return
	}

	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if strings.TrimSpace(h.jwtSecret) == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("auth.not_configured", "authentication is not configured", r))
		return
	}

	// PR2-03: hash the incoming raw token for all DB lookups.
	// The DB stores SHA-256(rawToken); the raw token is only held by the client.
	tokenHash := users.TokenHash(req.RefreshToken)

	// PR2-03: always open a read-write transaction — rotation requires INSERTs.
	// The Redis fast-path (feature #118) is intentionally removed here because
	// revoked-token reuse must trigger a DB lookup for compromise detection.
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.refresh: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	row, err := q.GetRefreshToken(ctx, tokenHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.invalid_refresh_token", "refresh token is invalid or does not exist", r))
			return
		}
		logger.Error("auth.refresh: get token failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// PR2-03: reuse of a revoked refresh token is treated as full-session compromise.
	// Revoke all remaining sessions for the account and abort immediately.
	if row.RevokedAt != nil {
		logger.Warn("auth.refresh: revoked token reuse — session compromise detected",
			"user_id", row.UserID.String(),
		)
		// Revoke all active refresh tokens for this user in the same transaction.
		if rErr := q.RevokeAllUserRefreshTokens(ctx, row.UserID); rErr != nil {
			logger.Error("auth.refresh: revoke all sessions failed during compromise", "error", rErr)
		}
		if cErr := tx.Commit(ctx); cErr != nil {
			logger.Error("auth.refresh: commit failed during compromise handling", "error", cErr)
		}
		// Clear the Redis session set for the user.
		if h.sessionStore != nil {
			if sErr := h.sessionStore.ClearUserSessions(ctx, row.UserID.String()); sErr != nil {
				logger.Warn("auth.refresh: clear redis sessions failed during compromise", "error", sErr)
			}
		}
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.session_compromised",
			"this refresh token was previously revoked — all sessions have been terminated for security",
			r,
		))
		return
	}

	if time.Now().UTC().After(row.ExpiresAt.UTC()) {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.refresh_token_expired", "refresh token has expired", r))
		return
	}

	userRow, err := q.GetUserByID(ctx, row.UserID)
	if err != nil {
		if err == pgx.ErrNoRows {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.invalid_refresh_token", "associated user does not exist", r))
			return
		}
		logger.Error("auth.refresh: get user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	actorID := userRow.ID
	accessToken, exp, err := auth.IssueJWT(
		h.jwtSecret,
		actorID,
		nil,
		nil,
		h.jwtIssuer,
		h.jwtAudience,
		accessTokenTTL,
	)
	if err != nil {
		logger.Error("auth.refresh: issue JWT failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_mint_failed", "failed to issue access token", r))
		return
	}

	// PR2-03: rotate the refresh token — revoke the old one and issue a new one.
	newRawToken, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.refresh: generate new refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_generation_failed", "failed to generate new refresh token", r))
		return
	}
	newTokenHash := users.TokenHash(newRawToken)
	newRefreshExp := time.Now().UTC().Add(refreshTokenTTL)

	if err := q.RevokeRefreshToken(ctx, tokenHash); err != nil {
		logger.Error("auth.refresh: revoke old token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.revoke_failed", "failed to revoke old refresh token", r))
		return
	}
	if err := q.InsertRefreshToken(ctx, newTokenHash, actorID, newRefreshExp); err != nil {
		logger.Error("auth.refresh: insert new refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_insert_failed", "failed to save new refresh token", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.refresh: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.transaction_failed", "failed to commit token rotation", r))
		return
	}

	// Feature #118: update Redis session tracking after successful rotation.
	if h.sessionStore != nil {
		userIDStr := actorID.String()
		now := time.Now().UTC()
		// Revoke the old raw token in Redis.
		if rErr := h.sessionStore.RevokeSession(ctx, userIDStr, req.RefreshToken, row.ExpiresAt); rErr != nil {
			logger.Warn("auth.refresh: redis revoke old session failed (DB revocation succeeded)", "error", rErr)
		}
		// Track the new raw token in Redis.
		if tErr := h.sessionStore.TrackSession(ctx, userIDStr, newRawToken, newRefreshExp); tErr != nil {
			logger.Warn("auth.refresh: redis track new session failed", "error", tErr, "user_id", userIDStr)
		}
		_ = now
	}

	slog.Info("auth.refresh: token rotated",
		"user_id", actorID.String(),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": newRawToken,
		"token_type":    "Bearer",
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"user_id":       actorID.String(),
	})
}
