package hauth

// step_up.go — POST /v1/auth/step-up: the signed-in person types their
// password again and gets a short step-up token (auth.StepUpTTL) that the
// money routes ask for when the request comes from the web event center
// (spec 08_architecture/37 section 4.5, owner decision 2026-10-14). The bot
// never asks: Telegram already proves the person.
//
// The token is bound to the sign-in of the access token ("sid") and to the
// organization named in the body: it is worthless in another session, after
// a sign-out and in another organization.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

// newStepUpLimiter allows 5 wrong passwords per person in 15 minutes, the
// login limit: a stolen access token cannot be used to guess the password.
func newStepUpLimiter() ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{MaxAttempts: loginRateLimitAttempts, Window: loginRateLimitWindow})
}

// StepUp serves POST /v1/auth/step-up. Body {"password": "...", "org_id": "..."}.
//
//	200 {step_up_token, expires_at}  the password is right
//	400 validation.org_id_required   no organization named
//	401 auth.session_ended           the access token comes from no sign-in,
//	                                 or that sign-in has ended: sign in again
//	403 auth.step_up_wrong_password  the password is wrong (not 401: the
//	                                 access token is fine, and a client must
//	                                 not refresh and retry)
//	429 auth.rate_limited            too many attempts
func (h *Handler) StepUp(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Password string `json:"password"`
		OrgID    string `json:"org_id"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_required", "password is required", r,
			map[string]any{"field": "password"},
		))
		return
	}
	orgID, err := uuid.Parse(strings.TrimSpace(req.OrgID))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.org_id_required", "org_id must name the organization of the action", r,
			map[string]any{"field": "org_id"},
		))
		return
	}
	sessionID, err := uuid.Parse(actor.SessionID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.session_ended", "this sign-in has ended; sign in again", r))
		return
	}
	key := "step-up:" + userID.String()
	if !h.stepUpLimiter.Allow(key) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
			"auth.rate_limited", "too many attempts; please wait before trying again", r,
			map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
		))
		return
	}
	if h.db == nil || strings.TrimSpace(h.jwtSecret) == "" {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.step_up: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gen.New(tx)
	userRow, err := q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope("auth.unauthenticated", "authentication required", r))
			return
		}
		logger.Error("auth.step_up: get user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if userRow.DeactivatedAt != nil {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope("auth.user_deactivated", "this account has been deactivated", r))
		return
	}
	live, err := q.RefreshSessionIsLive(ctx, sessionID, userID)
	if err != nil {
		logger.Error("auth.step_up: session check failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	if !live {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.session_ended", "this sign-in has ended; sign in again", r))
		return
	}
	passwordOK := users.CheckPassword(userRow.PasswordHash, req.Password) == nil
	if h.audit != nil {
		action := "v1.auth.step_up"
		if !passwordOK {
			action = "v1.auth.step_up_failed"
		}
		if err := h.audit.WriteTx(ctx, tx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      userID.String(),
			Action:       action,
			ResourceType: "user",
			ResourceID:   userID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{"org_id": orgID.String()},
		}); err != nil {
			logger.Error("auth.step_up: audit write failed", "error", err)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("auth.audit_failed", "failed to write audit event", r))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.step_up: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.transaction_failed", "failed to commit", r))
		return
	}
	if !passwordOK {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"auth.step_up_wrong_password", "the password is not right; type it again", r))
		return
	}
	h.stepUpLimiter.Reset(key)
	token, exp, err := auth.IssueStepUp(h.jwtSecret, h.jwtIssuer, userID, sessionID.String(), orgID.String())
	if err != nil {
		logger.Error("auth.step_up: issue token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_mint_failed", "failed to issue step-up token", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"step_up_token": token,
		"expires_at":    exp.UTC().Format(time.RFC3339),
	})
}
