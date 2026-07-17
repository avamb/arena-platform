// register.go implements POST /v1/auth/register (feature #114).
//
// Flow:
//  1. Parse and validate the request body (email, password, optional locale).
//  2. Normalise email to lowercase + trim.
//  3. Validate password length (8–72 chars).
//  4. Hash password with bcrypt at cost 12.
//  5. Begin a PostgreSQL transaction.
//  6. INSERT INTO users — if the email already exists, pgx returns a unique
//     violation (code 23505) which is mapped to 409 Conflict.
//  7. Generate a 64-char hex verification token (32 random bytes).
//  8. INSERT INTO email_verification_tokens with expires_at = now()+24h.
//  9. INSERT INTO worker_jobs (auth.email_verification job) — atomically with
//     the token row so job and token are always committed together.
// 10. COMMIT.
// 11. Return 201 Created with the user_id and a human-readable message.
//
// Logs contain user_id and job_id; they never contain the raw token or URL.
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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// pgUniqueViolation is the PostgreSQL error code for unique-constraint violations.
const pgUniqueViolation = "23505"

// emailVerificationTTL is the lifetime of a newly-issued email verification token.
const emailVerificationTTL = 24 * time.Hour

// Register serves POST /v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
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
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Locale   *string `json:"locale,omitempty"`
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
	if !strings.Contains(email, "@") {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.email_invalid", "email address is invalid", r,
			map[string]any{"field": "email"},
		))
		return
	}

	if req.Password == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_required", "password is required", r,
			map[string]any{"field": "password"},
		))
		return
	}
	if len(req.Password) < users.MinPasswordLength {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_too_short", "password must be at least 8 characters", r,
			map[string]any{"field": "password", "min_length": users.MinPasswordLength},
		))
		return
	}
	if len(req.Password) > 72 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"validation.password_too_long", "password must not exceed 72 characters", r,
			map[string]any{"field": "password", "max_length": 72},
		))
		return
	}

	hash, err := users.HashPassword(req.Password)
	if err != nil {
		logger.Error("auth.register: bcrypt failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.password_hash_failed", "failed to hash password", r))
		return
	}

	locale := "en"
	if req.Locale != nil && strings.TrimSpace(*req.Locale) != "" {
		locale = strings.TrimSpace(*req.Locale)
	}

	if h.db == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.register: begin tx failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	userRow, err := q.InsertUser(ctx, email, hash, locale)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
				"auth.email_already_registered", "this email address is already registered", r,
				map[string]any{"field": "email"},
			))
			return
		}
		logger.Error("auth.register: insert user failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to create user", r))
		return
	}

	token, err := users.GenerateVerificationToken()
	if err != nil {
		logger.Error("auth.register: generate token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_generation_failed", "failed to generate verification token", r))
		return
	}

	// PR2-03: store SHA-256(rawToken) in DB; send raw token in the email URL only.
	tokenHash := users.TokenHash(token)
	expiresAt := time.Now().UTC().Add(emailVerificationTTL)
	if err := q.InsertEmailVerificationToken(ctx, tokenHash, userRow.ID, expiresAt); err != nil {
		logger.Error("auth.register: insert token failed", "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("internal.token_insert_failed", "failed to save verification token", r))
		return
	}

	// Enqueue the verification email job WITHIN the same transaction so that
	// the job row and the token row are committed atomically. A rollback
	// removes both. Only correlation/job/user identifiers are logged — the
	// raw token and complete URL are never written to structured logs.
	// The raw token (not hash) goes in the email payload so it can be embedded
	// in the verification URL; the verify handler will hash it on receipt.
	jobPayload := authemail.VerificationEmailPayload{
		UserID:    userRow.ID.String(),
		Email:     email,
		Token:     token,
		Locale:    locale,
		ExpiresAt: expiresAt,
	}
	jobID, enqErr := worker.EnqueueInTx(ctx, tx, authemail.JobTypeEmailVerification, jobPayload, 5)
	if enqErr != nil {
		logger.Error("auth.register: failed to enqueue verification email job",
			slog.String("user_id", userRow.ID.String()),
			slog.String("request_id", logging.RequestID(ctx)),
			slog.String("error", enqErr.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError,
			httputil.ErrorEnvelope("internal.enqueue_failed", "failed to schedule verification email", r))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.register: commit failed", "error", err)
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to save registration", r))
		return
	}

	// Log identifiers only — no token, no URL.
	logger.Info("auth.register: verification email job enqueued",
		slog.String("user_id", userRow.ID.String()),
		slog.String("job_id", jobID),
		slog.String("request_id", logging.RequestID(ctx)),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"user_id":    userRow.ID.String(),
		"email":      userRow.Email,
		"created_at": userRow.CreatedAt.UTC().Format(time.RFC3339Nano),
		"message":    "Registration successful. Please check your email to verify your address.",
	})
}
