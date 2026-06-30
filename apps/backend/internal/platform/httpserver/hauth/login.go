// login.go implements POST /v1/auth/login and POST /v1/auth/refresh
// (feature #115).
//
// Login flow:
//  1. Parse and validate the request body (email, password).
//  2. Check the per-IP+email rate limit (sliding window, 5 attempts / 15 min).
//  3. Look up the user by normalised email.
//  4. Verify the bcrypt password hash.
//  5. Issue an HS256 JWT access token (15-minute TTL) via auth.IssueJWT.
//  6. Generate a 64-char hex refresh token and store it in the DB (30-day TTL).
//  7. If a session store is wired (feature #118): evict excess sessions and
//     track the new session in Redis.
//  8. Return 200 with {access_token, refresh_token, token_type, expires_at}.
//
// Refresh flow:
//  1. Parse the refresh token from the request body.
//  2. If a session store is wired (feature #118): check the Redis revocation
//     store before querying PostgreSQL (fast O(1) path).
//  3. Fetch the token row from DB; return 401 when not found.
//  4. Verify the token is not revoked (revoked_at IS NULL) → 401.
//  5. Verify the token has not expired → 401.
//  6. Fetch the owning user to populate the new JWT claims.
//  7. Issue a new JWT access token (15-minute TTL).
//  8. Return 200 with {access_token, token_type, expires_at}.
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

func loginRateLimiterKey(r *http.Request, email string) string {
	return httputil.ClientIP(r) + ":" + email
}

// LoginRateLimiterKey is the exported form of loginRateLimiterKey, for use by
// the httpserver shim layer (auth_login_test.go calls loginRateLimiterKey directly
// from package httpserver via the shim forwarder in auth_shims.go).
func LoginRateLimiterKey(r *http.Request, email string) string {
	return loginRateLimiterKey(r, email)
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

	rlKey := loginRateLimiterKey(r, email)
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

	h.rateLimiter.Reset(rlKey)

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

	refreshExp := time.Now().UTC().Add(refreshTokenTTL)
	if err := q.InsertRefreshToken(ctx, refreshToken, actorID, refreshExp); err != nil {
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

	// Feature #118: fast Redis revocation check before opening a DB transaction.
	if h.sessionStore != nil {
		revoked, checkErr := h.sessionStore.IsRevoked(ctx, req.RefreshToken)
		if checkErr != nil {
			logger.Warn("auth.refresh: redis revocation check failed (falling back to DB)", "error", checkErr)
		} else if revoked {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.refresh_token_revoked", "refresh token has been revoked", r))
			return
		}
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		logger.Error("auth.refresh: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	row, err := q.GetRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		if err == pgx.ErrNoRows {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.invalid_refresh_token", "refresh token is invalid or does not exist", r))
			return
		}
		logger.Error("auth.refresh: get token failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	if row.RevokedAt != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.refresh_token_revoked", "refresh token has been revoked", r))
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_at":   exp.UTC().Format(time.RFC3339),
		"user_id":      actorID.String(),
	})
}
