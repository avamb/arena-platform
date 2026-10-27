package hauth

// telegram_miniapp.go — sign-in of the Telegram Mini App (task S3b).
//
//	POST /v1/auth/telegram-miniapp   public: the signed launch data of a Mini
//	                                 opened from the event-center bot becomes
//	                                 a session of channel event_center_tg.
//
// The page sends Telegram.WebApp.initData in the body. The signature is
// checked with TELEGRAM_MINIAPP_SECRET_KEY (telegram_initdata.go), the data
// must be at most five minutes old, and the Telegram account must be linked
// to a person through the bot (bot_telegram_links) — the same person the
// bot's "Open in browser" link would sign in. Nobody is created or linked
// here. The session is the short one of the mini app (30 minutes, never
// refreshed past that, not counted in the concurrent-session limit), and its
// access token carries chn=event_center_tg, so a money route still asks for
// the password again.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// MiniappMetrics counts sign-ins by a closed outcome set (see
// observability.Metrics.RecordTelegramMiniappLogin). A nil value counts
// nothing.
type MiniappMetrics interface {
	RecordTelegramMiniappLogin(outcome string)
}

// newMiniappLimiter allows 30 requests per client address in 15 minutes. A
// forged signature cannot succeed, so this only keeps a broken or hostile
// client from spending the server's time; a success resets it.
func newMiniappLimiter() ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{MaxAttempts: 30, Window: loginRateLimitWindow})
}

// SetTelegramMiniapp turns the route on: secretKey is the configured
// TELEGRAM_MINIAPP_SECRET_KEY (64 hex characters). An empty or malformed key
// leaves the route answering 503.
func (h *Handler) SetTelegramMiniapp(secretKey string, m MiniappMetrics) {
	h.miniappKey = MiniappSecretKey(secretKey)
	h.miniappMetrics = m
}

func (h *Handler) miniappOutcome(outcome string) {
	if h.miniappMetrics != nil {
		h.miniappMetrics.RecordTelegramMiniappLogin(outcome)
	}
}

func (h *Handler) miniappClock() time.Time {
	if h.miniappNow != nil {
		return h.miniappNow()
	}
	return time.Now()
}

func (h *Handler) miniappRefused(w http.ResponseWriter, r *http.Request, status int, outcome, code, msg string) {
	h.miniappOutcome(outcome)
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func (h *Handler) miniappUnavailable(w http.ResponseWriter, r *http.Request) {
	h.miniappRefused(w, r, http.StatusServiceUnavailable, "error",
		"dependency.database_unavailable", "database is not available")
}

// TelegramMiniapp serves POST /v1/auth/telegram-miniapp. Body
// {"init_data": "<Telegram.WebApp.initData>"}.
//
//	200 the login answer (access_token, refresh_token, ...) of a 30-minute
//	    session of channel event_center_tg
//	400 http.invalid_json
//	401 auth.telegram_init_data_invalid   malformed data or a wrong signature
//	401 auth.telegram_init_data_expired   a genuine signature older than five
//	                                      minutes: reopen the Mini App
//	403 auth.telegram_not_linked          a real Telegram account that the
//	                                      bot has not linked to a person
//	403 auth.user_deactivated / auth.web_link_superadmin
//	429 auth.rate_limited
//	503 auth.telegram_miniapp_not_configured
func (h *Handler) TelegramMiniapp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	if len(h.miniappKey) == 0 {
		h.miniappRefused(w, r, http.StatusServiceUnavailable, "not_configured",
			"auth.telegram_miniapp_not_configured", "Telegram Mini App sign-in is not configured")
		return
	}
	limitKey := "telegram-miniapp:" + httputil.ClientKey(httputil.TrustedClientIP(r, h.trustedProxyCount))
	if h.miniappLimiter != nil && !h.miniappLimiter.Allow(limitKey) {
		h.miniappOutcome("rate_limited")
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
			"auth.rate_limited", "too many attempts; please wait before trying again", r,
			map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
		))
		return
	}

	var req struct {
		InitData string `json:"init_data"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 3*maxInitDataLen))
	if err != nil || json.Unmarshal(body, &req) != nil {
		h.miniappRefused(w, r, http.StatusBadRequest, "invalid", "http.invalid_json", "request body is not valid JSON")
		return
	}
	user, err := VerifyInitData(strings.TrimSpace(req.InitData), h.miniappKey, h.miniappClock(), InitDataMaxAge)
	switch {
	case errors.Is(err, ErrInitDataExpired):
		h.miniappRefused(w, r, http.StatusUnauthorized, "expired", "auth.telegram_init_data_expired",
			"the launch data is too old; close the Mini App and open it again from the bot")
		return
	case err != nil:
		h.miniappRefused(w, r, http.StatusUnauthorized, "invalid", "auth.telegram_init_data_invalid",
			"the launch data is not valid; open the Mini App from the bot")
		return
	}

	if h.db == nil || strings.TrimSpace(h.jwtSecret) == "" {
		h.miniappUnavailable(w, r)
		return
	}
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.telegram_miniapp: begin tx failed", "error", err)
		h.miniappUnavailable(w, r)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)

	// The Telegram account must be linked by the bot to a person. An account
	// that is not linked, or whose link was revoked, gets one answer.
	link, err := q.GetBotTelegramLink(ctx, user.TelegramID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		logger.Error("auth.telegram_miniapp: read telegram link failed", "error", err)
		h.miniappUnavailable(w, r)
		return
	}
	if err != nil || link.RevokedAt != nil {
		h.miniappRefused(w, r, http.StatusForbidden, "not_linked", "auth.telegram_not_linked",
			"this Telegram account is not linked to a person; ask the organization owner to invite you in the bot")
		return
	}
	userRow, err := q.GetUserByID(ctx, link.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.miniappRefused(w, r, http.StatusForbidden, "not_linked", "auth.telegram_not_linked",
			"this Telegram account is not linked to a person; ask the organization owner to invite you in the bot")
		return
	}
	if err != nil {
		logger.Error("auth.telegram_miniapp: get user failed", "error", err)
		h.miniappUnavailable(w, r)
		return
	}
	// A GDPR-erased account keeps its row with a placeholder address.
	if userRow.DeactivatedAt != nil || strings.HasSuffix(userRow.Email, "@arena.invalid") {
		h.miniappRefused(w, r, http.StatusForbidden, "forbidden", "auth.user_deactivated", "this account has been deactivated")
		return
	}
	platform, err := q.HasPlatformRole(ctx, link.UserID)
	if err != nil {
		logger.Error("auth.telegram_miniapp: read roles failed", "error", err)
		h.miniappUnavailable(w, r)
		return
	}
	if platform {
		h.miniappRefused(w, r, http.StatusForbidden, "forbidden", "auth.web_link_superadmin",
			"a platform superadmin works in the admin console")
		return
	}

	actorID := link.UserID
	now := time.Now().UTC()
	session, refreshExp := h.newMiniappSession(now)
	accessToken, exp, err := auth.IssueSessionJWT(h.jwtSecret, actorID, h.jwtIssuer, h.jwtAudience, accessTTL(refreshExp, now), sessionClaims(session))
	if err != nil {
		logger.Error("auth.telegram_miniapp: issue JWT failed", "error", err)
		h.miniappRefused(w, r, http.StatusInternalServerError, "error", "internal.token_mint_failed", "failed to issue access token")
		return
	}
	refreshToken, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.telegram_miniapp: generate refresh token failed", "error", err)
		h.miniappRefused(w, r, http.StatusInternalServerError, "error", "internal.token_generation_failed", "failed to generate refresh token")
		return
	}
	if err := q.InsertRefreshTokenSession(ctx, users.TokenHash(refreshToken), actorID, refreshExp, session); err != nil {
		logger.Error("auth.telegram_miniapp: insert refresh token failed", "error", err)
		h.miniappRefused(w, r, http.StatusInternalServerError, "error", "internal.token_insert_failed", "failed to save refresh token")
		return
	}
	if h.audit != nil {
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   now,
			ActorType:    "user",
			ActorID:      actorID.String(),
			Action:       "v1.auth.telegram_miniapp_login",
			ResourceType: "user",
			ResourceID:   actorID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"via":              audit.ChannelEventCenterTG,
				"telegram_user_id": user.TelegramID,
				"auth_age_seconds": int(now.Sub(user.AuthDate).Seconds()),
			},
		}); err != nil {
			logger.Error("auth.telegram_miniapp: audit write failed", "error", err)
			h.miniappRefused(w, r, http.StatusInternalServerError, "error", "auth.audit_failed", "failed to write audit event")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.telegram_miniapp: commit failed", "error", err)
		h.miniappRefused(w, r, http.StatusInternalServerError, "error", "internal.transaction_failed", "failed to commit transaction")
		return
	}
	if h.miniappLimiter != nil {
		h.miniappLimiter.Reset(limitKey)
	}
	h.trackNewSession(ctx, actorID, refreshToken, refreshExp, session)
	h.miniappOutcome("ok")

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"token_type":    "Bearer",
		"expires_at":    exp.UTC().Format(time.RFC3339),
		"user_id":       actorID.String(),
	})
}
