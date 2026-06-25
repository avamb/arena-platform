// auth_verify.go implements GET /v1/auth/verify?token=<tok> (feature #114).
//
// Flow:
//  1. Extract the token query parameter.
//  2. Begin a PostgreSQL transaction.
//  3. Fetch the token row — 404 when not found.
//  4. Check that used_at IS NULL — 410 Gone when already consumed.
//  5. Check that expires_at is in the future — 410 Gone when expired.
//  6. UPDATE users SET email_verified_at = now() WHERE id = token.user_id.
//  7. UPDATE email_verification_tokens SET used_at = now() WHERE token = $1.
//  8. COMMIT.
//  9. Return 200 OK with user_id, email, and email_verified_at.
package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// handleAuthVerifyEmail serves GET /v1/auth/verify?token=<tok>.
//
// This endpoint is intentionally PUBLIC — no Authorization header required.
// The token arrives in the verification link emailed (logged) to the user
// after registration.
func (s *Server) handleAuthVerifyEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// --- 1. Extract token ---
	token := r.URL.Query().Get("token")
	if token == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"validation.token_required",
			"query parameter 'token' is required",
			r,
			map[string]any{"param": "token"},
		))
		return
	}

	// --- 2. Ensure pool is available ---
	if s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}

	// --- 3. Begin transaction ---
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		logger.Error("auth.verify: begin tx failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database is not available", r))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := gen.New(tx)

	// --- 4. Fetch token row ---
	tokenRow, err := q.GetEmailVerificationToken(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("auth.token_not_found", "verification token not found or already expired", r))
			return
		}
		logger.Error("auth.verify: fetch token failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "database error", r))
		return
	}

	// --- 5. Check already-used ---
	if tokenRow.UsedAt != nil {
		writeJSON(w, http.StatusGone, errorEnvelope("auth.token_already_used", "this verification token has already been used", r))
		return
	}

	// --- 6. Check expiry ---
	if time.Now().UTC().After(tokenRow.ExpiresAt.UTC()) {
		writeJSON(w, http.StatusGone, errorEnvelope("auth.token_expired", "this verification token has expired; please request a new one", r))
		return
	}

	// --- 7. Mark user as verified ---
	verifiedRow, err := q.MarkEmailVerified(ctx, tokenRow.UserID)
	if err != nil {
		logger.Error("auth.verify: mark email verified failed", "error", err, "user_id", tokenRow.UserID)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.update_failed", "failed to mark email as verified", r))
		return
	}

	// --- 8. Consume token ---
	if err := q.MarkVerificationTokenUsed(ctx, token); err != nil {
		logger.Error("auth.verify: mark token used failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal.update_failed", "failed to consume verification token", r))
		return
	}

	// --- 9. Commit ---
	if err := tx.Commit(ctx); err != nil {
		logger.Error("auth.verify: commit failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "failed to save verification", r))
		return
	}

	// --- 10. Return 200 OK ---
	verifiedAt := time.Now().UTC() // fallback
	if verifiedRow.EmailVerifiedAt != nil {
		verifiedAt = verifiedRow.EmailVerifiedAt.UTC()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":           verifiedRow.ID.String(),
		"email":             verifiedRow.Email,
		"email_verified_at": verifiedAt.Format(time.RFC3339Nano),
		"message":           "Email address verified successfully.",
	})
}
