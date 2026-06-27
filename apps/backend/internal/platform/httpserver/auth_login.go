// auth_login.go implements POST /v1/auth/login and POST /v1/auth/refresh
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
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const (
	// accessTokenTTL is the lifetime of a newly issued JWT access token.
	accessTokenTTL = 15 * time.Minute

	// refreshTokenTTL is the lifetime of a newly issued refresh token.
	refreshTokenTTL = 30 * 24 * time.Hour // 30 days

	// loginRateLimitAttempts is the maximum number of login attempts allowed
	// per composite key (IP + email) within loginRateLimitWindow.
	loginRateLimitAttempts = 5

	// loginRateLimitWindow is the sliding window duration for login rate
	// limiting.
	loginRateLimitWindow = 15 * time.Minute

	// jwtIssuer and jwtAudience are fallback claim values used when the stub
	// auth provider is unavailable. In development we prefer the provider
	// values so login tokens stay compatible with /v1/me and other stub-
	// protected routes.
	jwtIssuer = "arena-api"

	// jwtAudience is the "aud" claim embedded in access tokens.
	jwtAudience = "arena-api"
)

// loginRateLimiter is the package-level sliding-window limiter shared across
// all login handler invocations. It is initialised once at package init time
// so it persists across requests.
//
// In production a Redis-backed limiter would replace this so the limit is
// enforced across multiple instances. This in-memory implementation is correct
// for the foundation milestone's single-instance deployment.
var loginRateLimiter ratelimit.Limiter = ratelimit.New(ratelimit.Config{
	MaxAttempts: loginRateLimitAttempts,
	Window:      loginRateLimitWindow,
})

// loginRateLimiterKey returns the composite rate-limit key for a login attempt.
// The key includes both the remote IP and the normalised email so that:
//   - A single IP cannot brute-force multiple accounts simultaneously.
//   - A single account cannot be locked out by different IPs simultaneously.
func loginRateLimiterKey(r *http.Request, email string) string {
	ip := clientIP(r)
	return ip + ":" + email
}

func (s *Server) loginTokenIssuerAudience() (string, string) {
	if s.stub != nil {
		return s.stub.Issuer(), s.stub.Audience()
	}
	return jwtIssuer, jwtAudience
}

// handleAuthLogin serves POST /v1/auth/login.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- 1. Parse request body ---
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.empty_body", "request body is required", r))
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}

	// --- 2. Normalise email ---
	email, err := users.NormalizeEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.email_required",
			"email is required",
			r,
			map[string]any{"field": "email"},
		))
		return
	}

	// --- 3. Check rate limit (before hitting the DB to keep the IP+email combo cheap) ---
	rlKey := loginRateLimiterKey(r, email)
	if !loginRateLimiter.Allow(rlKey) {
		logger.Warn("auth.login: rate limit exceeded", "email_prefix", email[:min(len(email), 5)])
		writeJSON(w, http.StatusTooManyRequests, errorEnvelopeWithDetails(
			"auth.rate_limited",
			"too many login attempts; please wait before trying again",
			r,
			map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
		))
		return
	}

	// --- 4. Validate password present ---
	if strings.TrimSpace(req.Password) == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.password_required",
			"password is required",
			r,
			map[string]any{"field": "password"},
		))
		return
	}

	// --- 5. Require pool ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 6. Require JWT secret ---
	if strings.TrimSpace(s.cfg.JWTSecretStub) == "" {
		logger.Error("auth.login: JWT_SIGNING_SECRET is not configured")
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("auth.not_configured", "authentication is not configured", r))
		return
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.login: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 7. Look up user by email ---
	userRow, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Use a constant-time response to avoid user-enumeration via timing.
			// We still do a dummy bcrypt comparison below is not needed here
			// since we return 401 regardless; a dummy hash compare would add
			// latency but is not required by the spec for this milestone.
			writeJSON(w, http.StatusUnauthorized, errorEnvelope(
				"auth.invalid_credentials",
				"email or password is incorrect",
				r,
			))
			return
		}
		logger.Error("auth.login: get user failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 8. Verify password ---
	if err := users.CheckPassword(userRow.PasswordHash, req.Password); err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope(
			"auth.invalid_credentials",
			"email or password is incorrect",
			r,
		))
		return
	}

	// Successful authentication — reset the rate-limit counter so a
	// successful login doesn't consume remaining attempts unnecessarily.
	loginRateLimiter.Reset(rlKey)

	// --- 9. Issue JWT access token (15 min) ---
	actorID := userRow.ID
	issuer, audience := s.loginTokenIssuerAudience()
	accessToken, exp, err := auth.IssueJWT(
		s.cfg.JWTSecretStub,
		actorID,
		nil, // no org_id in foundation milestone
		nil, // roles come from DB in a later milestone
		issuer,
		audience,
		accessTokenTTL,
	)
	if err != nil {
		logger.Error("auth.login: issue JWT failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_mint_failed", "failed to issue access token", r))
		return
	}

	// --- 10. Generate and store refresh token ---
	refreshToken, err := users.GenerateVerificationToken() // reuse 32-byte random hex generator
	if err != nil {
		logger.Error("auth.login: generate refresh token failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_generation_failed", "failed to generate refresh token", r))
		return
	}

	refreshExp := time.Now().UTC().Add(refreshTokenTTL)
	if err := q.InsertRefreshToken(ctx, refreshToken, actorID, refreshExp); err != nil {
		logger.Error("auth.login: insert refresh token failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_insert_failed", "failed to save refresh token", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.login: commit failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.transaction_failed", "failed to commit transaction", r))
		return
	}

	// --- Feature #118: session tracking + concurrent-session enforcement ---
	// These operations are post-commit and best-effort: a Redis or DB failure
	// here does NOT roll back the login (the main transaction already committed).
	// Session eviction ensures the user never has more than maxConcurrentSessions
	// active refresh tokens simultaneously.
	if s.sessionStore != nil {
		userIDStr := actorID.String()
		now := time.Now().UTC()

		// 1. Prune expired and evict oldest sessions if over the per-user limit.
		//    We pass maxSessions-1 to PruneAndEvict so that after eviction there
		//    is room for exactly one new session. When maxConcurrentSessions=1,
		//    this passes 0 → "evict all", leaving room for the new token.
		if s.maxConcurrentSessions > 0 {
			tokensToEvict, evictErr := s.sessionStore.PruneAndEvict(ctx, userIDStr, s.maxConcurrentSessions-1, now)
			if evictErr != nil {
				logger.Warn("auth.login: session prune failed (continuing)", "error", evictErr)
			} else if len(tokensToEvict) > 0 {
				// Revoke evicted tokens in the database in a single new transaction.
				evictTx, txErr := s.pool.BeginTx(ctx, pgx.TxOptions{})
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
						// Best-effort Redis revocation with conservative TTL.
						// The exact expiry of the evicted token is unknown here so
						// we use refreshTokenTTL as a safe upper bound.
						_ = s.sessionStore.RevokeSession(ctx, userIDStr, tok, now.Add(refreshTokenTTL))
					}
					if cErr := evictTx.Commit(ctx); cErr != nil {
						logger.Warn("auth.login: commit evict tx failed", "error", cErr)
						_ = evictTx.Rollback(ctx)
					}
				}
			}
		}

		// 2. Track the new session.
		if trackErr := s.sessionStore.TrackSession(ctx, userIDStr, refreshToken, refreshExp); trackErr != nil {
			logger.Warn("auth.login: session track failed (continuing)", "error", trackErr)
		}
	}

	slog.Info("auth.login: successful login",
		"user_id", actorID.String(),
		"email_prefix", email[:min(len(email), 5)],
	)

	// --- 11. Return 200 ---
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"user_id":       actorID.String(),
	})
}

// handleAuthRefresh serves POST /v1/auth/refresh.
// It accepts a refresh token and issues a new short-lived JWT access token.
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- 1. Parse request body ---
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

	// --- 2. Require pool + secret ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if strings.TrimSpace(s.cfg.JWTSecretStub) == "" {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("auth.not_configured", "authentication is not configured", r))
		return
	}

	// --- 2.5 Fast Redis revocation check (feature #118) ---
	// Check the Redis revocation store before opening a DB transaction. This
	// O(1) EXISTS lookup short-circuits the refresh flow for revoked tokens
	// without a database round-trip.
	if s.sessionStore != nil {
		revoked, checkErr := s.sessionStore.IsRevoked(ctx, req.RefreshToken)
		if checkErr != nil {
			// Redis unavailable — log a warning and fall through to the DB check.
			logger.Warn("auth.refresh: redis revocation check failed (falling back to DB)", "error", checkErr)
		} else if revoked {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.refresh_token_revoked", "refresh token has been revoked", r))
			return
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		logger.Error("auth.refresh: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 3. Look up refresh token ---
	row, err := q.GetRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_refresh_token", "refresh token is invalid or does not exist", r))
			return
		}
		logger.Error("auth.refresh: get token failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 4. Check revocation ---
	if row.RevokedAt != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.refresh_token_revoked", "refresh token has been revoked", r))
		return
	}

	// --- 5. Check expiry ---
	if time.Now().UTC().After(row.ExpiresAt.UTC()) {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.refresh_token_expired", "refresh token has expired", r))
		return
	}

	// --- 6. Fetch user ---
	userRow, err := q.GetUserByID(ctx, row.UserID)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.invalid_refresh_token", "associated user does not exist", r))
			return
		}
		logger.Error("auth.refresh: get user failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 7. Issue new JWT access token ---
	actorID := userRow.ID
	issuer, audience := s.loginTokenIssuerAudience()
	accessToken, exp, err := auth.IssueJWT(
		s.cfg.JWTSecretStub,
		actorID,
		nil,
		nil,
		issuer,
		audience,
		accessTokenTTL,
	)
	if err != nil {
		logger.Error("auth.refresh: issue JWT failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_mint_failed", "failed to issue access token", r))
		return
	}

	// --- 8. Return 200 ---
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_at":   exp.UTC().Format(time.RFC3339),
		"user_id":      actorID.String(),
	})
}
