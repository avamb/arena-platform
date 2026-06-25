// auth_password_reset.go implements the password-reset flow (feature #116).
//
// Endpoints:
//
//	POST /v1/auth/password-reset/request
//	  1. Parse and validate the request body (email).
//	  2. Look up the user by normalised email (silently succeed if not found
//	     to prevent user enumeration).
//	  3. Generate a 64-char hex reset token with a 1-hour TTL.
//	  4. INSERT into password_reset_tokens + write audit event (same tx).
//	  5. Log the reset link to stdout (dev-mode email delivery).
//	  6. Return 202 Accepted regardless of whether the email was found.
//
//	POST /v1/auth/password-reset/confirm
//	  1. Parse and validate the request body (token, new_password).
//	  2. Validate password length (8–72 chars).
//	  3. Fetch the token row — 404 when not found.
//	  4. Check that used_at IS NULL — 410 Gone when already consumed.
//	  5. Check that expires_at is in the future — 410 Gone when expired.
//	  6. Hash the new password with bcrypt (cost 12).
//	  7. UPDATE users SET password_hash = … WHERE id = token.user_id.
//	  8. Mark the token as used (single-use guarantee).
//	  9. Write audit event (same tx).
//	 10. COMMIT.
//	 11. Return 200 OK with user_id and message.
//
// Both endpoints are intentionally PUBLIC — no Authorization header required.
package httpserver

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const (
	// passwordResetTokenTTL is the lifetime of a password-reset token.
	// Deliberately short (1 hour) per the security policy.
	passwordResetTokenTTL = time.Hour
)

// handleAuthPasswordResetRequest serves POST /v1/auth/password-reset/request.
//
// Returns 202 Accepted in ALL cases (email found or not) to prevent user
// enumeration via the response. The reset link is logged to stdout in
// dev-mode so it can be retrieved from server logs during testing.
func (s *Server) handleAuthPasswordResetRequest(w http.ResponseWriter, r *http.Request) {
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
		Email string `json:"email"`
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

	// --- 3. Require pool ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 4. Begin transaction ---
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.password_reset.request: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 5. Look up user by email (silently succeed if not found) ---
	userRow, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// User not found — return 202 without creating a token.
			// Do NOT reveal that the email is unregistered.
			writeJSON(w, http.StatusAccepted, map[string]any{
				"message": "If that email address is registered, you will receive a password reset link.",
			})
			return
		}
		logger.Error("auth.password_reset.request: get user failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 6. Generate reset token ---
	token, err := users.GenerateVerificationToken() // 32 random bytes, hex-encoded
	if err != nil {
		logger.Error("auth.password_reset.request: generate token failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_generation_failed", "failed to generate reset token", r))
		return
	}

	// --- 7. Insert reset token ---
	expiresAt := time.Now().UTC().Add(passwordResetTokenTTL)
	if err := q.InsertPasswordResetToken(ctx, token, userRow.ID, expiresAt); err != nil {
		logger.Error("auth.password_reset.request: insert token failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.token_insert_failed", "failed to save reset token", r))
		return
	}

	// --- 8. Write audit event (inside the transaction) ---
	if s.audit != nil {
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "anonymous",
			ActorID:      "",
			Action:       "auth.password_reset_request",
			ResourceType: "user",
			ResourceID:   userRow.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"email_prefix": email[:min(len(email), 5)],
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			logger.Error("auth.password_reset.request: audit write failed", "error", err)
			// Audit failure is non-fatal for this public endpoint — we still
			// issue the token so the user is not left unable to reset their password.
		}
	}

	// --- 9. Commit ---
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.password_reset.request: commit failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "failed to save reset token", r))
		return
	}

	// --- 10. Log reset email (dev-mode delivery) ---
	// In production this would call a webhook / email delivery service.
	// Per the email integration spec, we log the link to stdout so it can be
	// retrieved from server logs during testing without an external mail service.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	resetURL := scheme + "://" + host + "/v1/auth/password-reset/confirm?token=" + token

	slog.Info("EMAIL DELIVERY (dev-mode): password reset",
		"to", email,
		"subject", "Reset your Arena Platform password",
		"reset_url", resetURL,
		"expires_at", expiresAt.Format(time.RFC3339),
		"user_id", userRow.ID.String(),
	)

	// --- 11. Return 202 Accepted ---
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message": "If that email address is registered, you will receive a password reset link.",
	})
}

// handleAuthPasswordResetConfirm serves POST /v1/auth/password-reset/confirm.
//
// Accepts a reset token + new password, validates the token, and updates
// the user's password. The token is consumed (single-use) on success.
func (s *Server) handleAuthPasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
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
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("http.invalid_json", "request body is not valid JSON", r))
		return
	}

	// --- 2. Validate token present ---
	if strings.TrimSpace(req.Token) == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.token_required",
			"token is required",
			r,
			map[string]any{"field": "token"},
		))
		return
	}

	// --- 3. Validate new password ---
	if req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.password_required",
			"new_password is required",
			r,
			map[string]any{"field": "new_password"},
		))
		return
	}
	if len(req.NewPassword) < users.MinPasswordLength {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.password_too_short",
			"new_password must be at least 8 characters",
			r,
			map[string]any{"field": "new_password", "min_length": users.MinPasswordLength},
		))
		return
	}
	if len(req.NewPassword) > 72 {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.password_too_long",
			"new_password must not exceed 72 characters",
			r,
			map[string]any{"field": "new_password", "max_length": 72},
		))
		return
	}

	// --- 4. Require pool ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 5. Begin transaction ---
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.password_reset.confirm: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 6. Fetch token row ---
	tokenRow, err := q.GetPasswordResetToken(ctx, req.Token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("auth.token_not_found", "reset token not found or already expired", r))
			return
		}
		logger.Error("auth.password_reset.confirm: fetch token failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database error", r))
		return
	}

	// --- 7. Check already-used ---
	if tokenRow.UsedAt != nil {
		writeJSON(w, http.StatusGone, errorEnvelope("auth.token_already_used", "this reset token has already been used", r))
		return
	}

	// --- 8. Check expiry ---
	if time.Now().UTC().After(tokenRow.ExpiresAt.UTC()) {
		writeJSON(w, http.StatusGone, errorEnvelope("auth.token_expired", "this reset token has expired; please request a new one", r))
		return
	}

	// --- 9. Hash new password ---
	hash, err := users.HashPassword(req.NewPassword)
	if err != nil {
		logger.Error("auth.password_reset.confirm: bcrypt failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.password_hash_failed", "failed to hash password", r))
		return
	}

	// --- 10. Update user password ---
	if err := q.UpdateUserPassword(ctx, tokenRow.UserID, hash); err != nil {
		logger.Error("auth.password_reset.confirm: update password failed", "error", err, "user_id", tokenRow.UserID)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.update_failed", "failed to update password", r))
		return
	}

	// --- 11. Consume token (single-use guarantee) ---
	if err := q.MarkPasswordResetTokenUsed(ctx, req.Token); err != nil {
		logger.Error("auth.password_reset.confirm: mark token used failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.update_failed", "failed to consume reset token", r))
		return
	}

	// --- 12. Write audit event (inside the transaction) ---
	if s.audit != nil {
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      tokenRow.UserID.String(),
			Action:       "auth.password_reset_confirm",
			ResourceType: "user",
			ResourceID:   tokenRow.UserID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata:     map[string]any{},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			logger.Error("auth.password_reset.confirm: audit write failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"auth.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	// --- 13. Commit ---
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.password_reset.confirm: commit failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "failed to save password update", r))
		return
	}

	slog.Info("auth.password_reset.confirm: password reset successful",
		"user_id", tokenRow.UserID.String(),
	)

	// --- 14. Return 200 OK ---
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": tokenRow.UserID.String(),
		"message": "Password has been reset successfully. Please log in with your new password.",
	})
}
