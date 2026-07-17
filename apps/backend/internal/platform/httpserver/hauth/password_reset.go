// password_reset.go implements the password-reset flow (feature #116, hardened
// in feature #359 / PR2-03).
//
// Endpoints:
//
//	POST /v1/auth/password-reset/request
//	  1. Parse and validate the request body (email).
//	  2. Look up the user by normalised email (silently succeed if not found
//	     to prevent user enumeration).
//	  3. Generate a 64-char hex reset token with a 1-hour TTL.
//	  4. INSERT SHA-256(token) into password_reset_tokens + write audit event (same tx).
//	     The raw token is included in the email payload for the URL only.
//	  5. INSERT into worker_jobs (auth.password_reset_email) — atomically
//	     with the token so enqueue and token are always committed together.
//	  6. Return 202 Accepted regardless of whether the email was found.
//	     Logs contain user_id and job_id; never the token or URL.
//
//	POST /v1/auth/password-reset/confirm
//	  1. Parse and validate the request body (token, new_password).
//	  2. Validate password length (8–72 chars).
//	  3. Fetch the token row by SHA-256(token) — 404 when not found.
//	  4. Check that used_at IS NULL — 410 Gone when already consumed.
//	  5. Check that expires_at is in the future — 410 Gone when expired.
//	  6. Hash the new password with bcrypt (cost 12).
//	  7. UPDATE users SET password_hash = … WHERE id = token.user_id.
//	  8. Mark the token as used (single-use guarantee).
//	  9. PR2-03: Revoke ALL active refresh tokens for the user (same tx)
//	     so that old sessions cannot persist after account recovery.
//	 10. Write audit event (same tx).
//	 11. COMMIT.
//	 12. PR2-03: Clear Redis session tracking set for the user (best-effort).
//	 13. Return 200 OK with user_id and message.
//
// Both endpoints are intentionally PUBLIC — no Authorization header required.
package hauth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// passwordResetTokenTTL is the lifetime of a password-reset token (1 hour per security policy).
const passwordResetTokenTTL = time.Hour

// PasswordResetRequest serves POST /v1/auth/password-reset/request.
// Returns 202 Accepted in ALL cases to prevent user enumeration.
func (h *Handler) PasswordResetRequest(w http.ResponseWriter, r *http.Request) {
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
		Email string `json:"email"`
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

	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.password_reset.request: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	userRow, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
				"message": "If that email address is registered, you will receive a password reset link.",
			})
			return
		}
		logger.Error("auth.password_reset.request: get user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	token, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.password_reset.request: generate token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_generation_failed", "failed to generate reset token", r))
		return
	}

	// PR2-03: store SHA-256(rawToken) in DB; send raw token in the email URL only.
	tokenHash := users.TokenHash(token)
	expiresAt := time.Now().UTC().Add(passwordResetTokenTTL)
	if err := q.InsertPasswordResetToken(ctx, tokenHash, userRow.ID, expiresAt); err != nil {
		logger.Error("auth.password_reset.request: insert token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_insert_failed", "failed to save reset token", r))
		return
	}

	if h.audit != nil {
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "anonymous",
			ActorID:      "",
			Action:       "auth.password_reset_request",
			ResourceType: "user",
			ResourceID:   userRow.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"email_prefix": email[:min(len(email), 5)],
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			logger.Error("auth.password_reset.request: audit write failed", "error", err)
			// Audit failure is non-fatal — still issue the token.
		}
	}

	// Enqueue the password-reset email job WITHIN the same transaction so that
	// the job row and the token row are committed atomically.
	// Logs contain user_id and job_id only — never the raw token or URL.
	jobPayload := authemail.PasswordResetEmailPayload{
		UserID:    userRow.ID.String(),
		Email:     email,
		Token:     token,
		ExpiresAt: expiresAt,
	}
	jobID, enqErr := worker.EnqueueInTx(ctx, tx, authemail.JobTypePasswordResetEmail, jobPayload, 5)
	if enqErr != nil {
		// Enqueue failure is treated as a transaction error: roll back so no
		// orphaned token exists without a delivery job, then return 202 to
		// preserve enumeration safety.
		logger.Error("auth.password_reset.request: failed to enqueue reset email job",
			slog.String("user_id", userRow.ID.String()),
			slog.String("request_id", logging.RequestID(ctx)),
			slog.String("error", enqErr.Error()),
		)
		// Rollback is handled by the deferred tx.Rollback above.
		// Return 202 to preserve enumeration safety — don't reveal the failure.
		httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
			"message": "If that email address is registered, you will receive a password reset link.",
		})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.password_reset.request: commit failed", "error", err)
		// Preserve enumeration safety even on commit failure.
		httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
			"message": "If that email address is registered, you will receive a password reset link.",
		})
		return
	}

	// Log identifiers only — no token, no URL.
	logger.Info("auth.password_reset.request: reset email job enqueued",
		slog.String("user_id", userRow.ID.String()),
		slog.String("job_id", jobID),
		slog.String("request_id", logging.RequestID(ctx)),
	)

	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"message": "If that email address is registered, you will receive a password reset link.",
	})
}

// PasswordResetConfirm serves POST /v1/auth/password-reset/confirm.
func (h *Handler) PasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
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
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}

	if strings.TrimSpace(req.Token) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.token_required", "token is required", r,
			map[string]any{"field": "token"},
		))
		return
	}

	if req.NewPassword == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_required", "new_password is required", r,
			map[string]any{"field": "new_password"},
		))
		return
	}
	if len(req.NewPassword) < users.MinPasswordLength {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_too_short", "new_password must be at least 8 characters", r,
			map[string]any{"field": "new_password", "min_length": users.MinPasswordLength},
		))
		return
	}
	if len(req.NewPassword) > 72 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_too_long", "new_password must not exceed 72 characters", r,
			map[string]any{"field": "new_password", "max_length": 72},
		))
		return
	}

	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.password_reset.confirm: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// PR2-03: look up token by SHA-256 hash; client sent raw token.
	resetTokenHash := users.TokenHash(req.Token)
	tokenRow, err := q.GetPasswordResetToken(ctx, resetTokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("auth.token_not_found", "reset token not found or already expired", r))
			return
		}
		logger.Error("auth.password_reset.confirm: fetch token failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database error", r))
		return
	}

	if tokenRow.UsedAt != nil {
		httputil.WriteJSON(w, http.StatusGone, httputil.ErrorEnvelope("auth.token_already_used", "this reset token has already been used", r))
		return
	}

	if time.Now().UTC().After(tokenRow.ExpiresAt.UTC()) {
		httputil.WriteJSON(w, http.StatusGone, httputil.ErrorEnvelope("auth.token_expired", "this reset token has expired; please request a new one", r))
		return
	}

	pwHash, err := users.HashPassword(req.NewPassword)
	if err != nil {
		logger.Error("auth.password_reset.confirm: bcrypt failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.password_hash_failed", "failed to hash password", r))
		return
	}

	if err := q.UpdateUserPassword(ctx, tokenRow.UserID, pwHash); err != nil {
		logger.Error("auth.password_reset.confirm: update password failed", "error", err, "user_id", tokenRow.UserID)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.update_failed", "failed to update password", r))
		return
	}

	if err := q.MarkPasswordResetTokenUsed(ctx, resetTokenHash); err != nil {
		logger.Error("auth.password_reset.confirm: mark token used failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.update_failed", "failed to consume reset token", r))
		return
	}

	// PR2-03: revoke ALL active refresh tokens for this user so that old
	// sessions cannot survive account recovery. This is atomic with the
	// password update — both succeed or both roll back.
	if err := q.RevokeAllUserRefreshTokens(ctx, tokenRow.UserID); err != nil {
		logger.Error("auth.password_reset.confirm: revoke all refresh tokens failed", "error", err, "user_id", tokenRow.UserID)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.revoke_failed", "failed to revoke existing sessions", r))
		return
	}

	if h.audit != nil {
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      tokenRow.UserID.String(),
			Action:       "auth.password_reset_confirm",
			ResourceType: "user",
			ResourceID:   tokenRow.UserID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata:     map[string]any{},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			logger.Error("auth.password_reset.confirm: audit write failed", "error", err)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"auth.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.password_reset.confirm: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to save password update", r))
		return
	}

	// PR2-03: clear Redis session tracking for the user (best-effort, non-fatal).
	// The DB already has revoked_at set for all tokens, so the Redis clear is
	// an optimistic cleanup — clients will be rejected on next DB check regardless.
	if h.sessionStore != nil {
		if sErr := h.sessionStore.ClearUserSessions(ctx, tokenRow.UserID.String()); sErr != nil {
			logger.Warn("auth.password_reset.confirm: clear redis sessions failed (DB revocation succeeded)",
				"error", sErr, "user_id", tokenRow.UserID.String())
		}
	}

	slog.Info("auth.password_reset.confirm: password reset successful; all sessions revoked",
		"user_id", tokenRow.UserID.String(),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id": tokenRow.UserID.String(),
		"message": "Password has been reset successfully. Please log in with your new password.",
	})
}
