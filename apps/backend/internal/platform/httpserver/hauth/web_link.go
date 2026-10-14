package hauth

// web_link.go — the web event center from the Telegram bot (spec
// 08_architecture/37 section 4.3, task WEB-AUTH-2, migration 0151).
//
//	POST /v1/auth/web/link             public: a one-time code from the
//	                                   bot's "Open in browser" button becomes
//	                                   a web session.
//	POST /v1/auth/web-sessions/revoke  signed in: "Sign out everywhere" ends
//	                                   every web session of the caller.
//
// The bot asks for the code under its service token (hbot). The code is in
// the address after "#", so it never reaches a server log; the web sends it
// in the body.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// PlatformSuperadminRole works in admin-web: no web link is issued to it
// (spec 37 section 1, item 4).
const PlatformSuperadminRole = "platform_superadmin"

// newWebLinkLimiter allows 10 exchanges per client address in 15 minutes.
// A code is 256 random bits, so this only keeps a broken client from
// hammering the database.
func newWebLinkLimiter() ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{MaxAttempts: 10, Window: loginRateLimitWindow})
}

// newWebSession is newSession for a sign-in that is a web session whatever
// header the request carries: a link from the bot always opens the web.
func (h *Handler) newWebSession(now time.Time) (gen.RefreshTokenSession, time.Time) {
	channel := audit.ChannelEventCenterWeb
	s := gen.RefreshTokenSession{SessionStartedAt: &now, ClientChannel: &channel}
	if h.webSessionIdle > 0 {
		idle := int32(h.webSessionIdle / time.Second) // #nosec G115 -- capped at maxSessionAge (30 days)
		s.IdleSeconds = &idle
	}
	return s, sessionEnd(s, now)
}

func (h *Handler) webLinkInvalid(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
		"auth.web_link_invalid",
		"this link does not work any more: it works once and for five minutes; press Open in browser in the bot again", r))
}

// WebLink serves POST /v1/auth/web/link. Body {"code": "..."}.
//
//	200 the login answer (access_token, refresh_token, ...)
//	400 auth.web_link_invalid   unknown, used, taken back or expired code,
//	                            or the Telegram account is no longer linked
//	                            to the same person (one answer for all)
//	403 auth.user_deactivated / auth.web_link_superadmin
//	429 auth.rate_limited
func (h *Handler) WebLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	var req struct {
		Code string `json:"code"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}
	code := strings.TrimSpace(req.Code)
	key := "web-link:" + httputil.TrustedClientIP(r, h.trustedProxyCount)
	if !h.webLinkLimiter.Allow(key) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
			"auth.rate_limited", "too many attempts; please wait before trying again", r,
			map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
		))
		return
	}
	if code == "" || len(code) > 128 {
		h.webLinkInvalid(w, r)
		return
	}
	if h.db == nil || strings.TrimSpace(h.jwtSecret) == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.web_link: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)

	used, err := q.UseWebLoginCode(ctx, users.TokenHash(code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.webLinkInvalid(w, r)
			return
		}
		logger.Error("auth.web_link: use code failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	// The code belongs to the Telegram account that pressed the button: it
	// is void once that account is unlinked or linked to someone else.
	link, err := q.GetBotTelegramLink(ctx, used.TelegramUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		logger.Error("auth.web_link: read telegram link failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if err != nil || link.RevokedAt != nil || link.UserID != used.UserID {
		h.webLinkInvalid(w, r)
		return
	}
	userRow, err := q.GetUserByID(ctx, used.UserID)
	if err != nil {
		logger.Error("auth.web_link: get user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if userRow.DeactivatedAt != nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("auth.user_deactivated", "this account has been deactivated", r))
		return
	}
	roles, err := q.GetActiveRolesForUser(ctx, used.UserID)
	if err != nil {
		logger.Error("auth.web_link: read roles failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if slices.Contains(roles, PlatformSuperadminRole) {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"auth.web_link_superadmin", "a platform superadmin works in the admin console", r))
		return
	}

	actorID := used.UserID
	accessToken, exp, err := auth.IssueJWT(h.jwtSecret, actorID, nil, nil, h.jwtIssuer, h.jwtAudience, accessTokenTTL)
	if err != nil {
		logger.Error("auth.web_link: issue JWT failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_mint_failed", "failed to issue access token", r))
		return
	}
	refreshToken, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.web_link: generate refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_generation_failed", "failed to generate refresh token", r))
		return
	}
	session, refreshExp := h.newWebSession(time.Now().UTC())
	if err := q.InsertRefreshTokenSession(ctx, users.TokenHash(refreshToken), actorID, refreshExp, session); err != nil {
		logger.Error("auth.web_link: insert refresh token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_insert_failed", "failed to save refresh token", r))
		return
	}
	if h.audit != nil {
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actorID.String(),
			Action:       "v1.auth.web_link_login",
			ResourceType: "user",
			ResourceID:   actorID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"via": audit.ChannelEventCenterWeb, "code_id": used.ID.String()},
		}); err != nil {
			logger.Error("auth.web_link: audit write failed", "error", err)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("auth.audit_failed", "failed to write audit event", r))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.web_link: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.transaction_failed", "failed to commit transaction", r))
		return
	}
	h.webLinkLimiter.Reset(key)
	h.trackNewSession(ctx, actorID, refreshToken, refreshExp)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"user_id":       actorID.String(),
	})
}

// RevokeWebSessions serves POST /v1/auth/web-sessions/revoke: every web
// event center session of the caller ends, and every unused link code is
// taken back. Sessions of admin-web and the bot stay. An access token the
// web already holds keeps working until it expires (15 minutes).
func (h *Handler) RevokeWebSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() || actor.IsService() {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.unauthenticated", "authentication required", r))
		return
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.unauthenticated", "authentication required", r))
		return
	}
	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)
	revoked, err := q.RevokeWebRefreshTokens(ctx, userID)
	if err == nil {
		err = q.RevokeOpenWebLoginCodes(ctx, userID)
	}
	if err != nil {
		logger.Error("auth.web_sessions_revoke: revoke failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if h.audit != nil {
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      userID.String(),
			Action:       "v1.auth.web_sessions_revoke",
			ResourceType: "user",
			ResourceID:   userID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"revoked_sessions": revoked},
		}); err != nil {
			logger.Error("auth.web_sessions_revoke: audit write failed", "error", err)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("auth.audit_failed", "failed to write audit event", r))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.transaction_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"revoked_sessions": revoked})
}
