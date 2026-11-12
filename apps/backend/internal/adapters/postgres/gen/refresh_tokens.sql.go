// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: refresh_tokens.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertRefreshToken = `-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (token, user_id, expires_at)
VALUES ($1, $2, $3)
`

// InsertRefreshToken inserts a new refresh token row.
// token is a 64-character hex string (32 random bytes).
// expiresAt should be now()+30d, set by the application layer.
func (q *Queries) InsertRefreshToken(ctx context.Context, token string, userID uuid.UUID, expiresAt time.Time) error {
	_, err := q.db.Exec(ctx, insertRefreshToken, token, userID, expiresAt)
	return err
}

const getRefreshToken = `-- name: GetRefreshToken :one
SELECT token, user_id, expires_at, revoked_at, created_at, revoke_reason
FROM refresh_tokens
WHERE token = $1
`

// Revoke reasons of a refresh token (migration 0214, refresh_tokens.revoke_reason).
const (
	RefreshTokenRevokeReasonRotated    = "rotated"
	RefreshTokenRevokeReasonSignedOut  = "signed_out"
	RefreshTokenRevokeReasonRevokedAll = "revoked_all"
)

// GetRefreshTokenRow is the result type for GetRefreshToken.
// RevokeReason is nil on an active token and on a token revoked before
// migration 0214.
type GetRefreshTokenRow struct {
	Token        string     `json:"token"`
	UserID       uuid.UUID  `json:"user_id"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokeReason *string    `json:"revoke_reason"`
}

// GetRefreshToken fetches a refresh token row by token string.
// Returns pgx.ErrNoRows when no matching token exists.
func (q *Queries) GetRefreshToken(ctx context.Context, token string) (GetRefreshTokenRow, error) {
	row := q.db.QueryRow(ctx, getRefreshToken, token)
	var i GetRefreshTokenRow
	err := row.Scan(
		&i.Token,
		&i.UserID,
		&i.ExpiresAt,
		&i.RevokedAt,
		&i.CreatedAt,
		&i.RevokeReason,
	)
	return i, err
}

const revokeRefreshToken = `-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'signed_out'
WHERE token = $1 AND revoked_at IS NULL
`

// RevokeRefreshToken soft-revokes a refresh token on a sign-out or a session
// eviction: revoked_at = now(), revoke_reason = 'signed_out'. A token that is
// already revoked keeps its first time and reason.
func (q *Queries) RevokeRefreshToken(ctx context.Context, token string) error {
	_, err := q.db.Exec(ctx, revokeRefreshToken, token)
	return err
}

const rotateRefreshToken = `-- name: RotateRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'rotated'
WHERE token = $1
`

// RotateRefreshToken revokes a refresh token replaced by a newer one on
// refresh: revoked_at = now(), revoke_reason = 'rotated'.
func (q *Queries) RotateRefreshToken(ctx context.Context, token string) error {
	_, err := q.db.Exec(ctx, rotateRefreshToken, token)
	return err
}

const revokeAllUserRefreshTokens = `-- name: RevokeAllUserRefreshTokens :exec
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'revoked_all'
WHERE user_id = $1 AND revoked_at IS NULL
`

// RevokeAllUserRefreshTokens revokes every active refresh token for a user.
// Called when a password reset succeeds or a session compromise is detected.
func (q *Queries) RevokeAllUserRefreshTokens(ctx context.Context, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, revokeAllUserRefreshTokens, userID)
	return err
}

const getUserByID = `-- name: GetUserByID :one
SELECT id, email, password_hash, preferred_locale, created_at, email_verified_at, deactivated_at
FROM users
WHERE id = $1
`

// GetUserByID fetches a user row by UUID primary key.
// Returns pgx.ErrNoRows when no matching user exists.
func (q *Queries) GetUserByID(ctx context.Context, id uuid.UUID) (GetUserByEmailRow, error) {
	row := q.db.QueryRow(ctx, getUserByID, id)
	var i GetUserByEmailRow
	err := row.Scan(
		&i.ID,
		&i.Email,
		&i.PasswordHash,
		&i.PreferredLocale,
		&i.CreatedAt,
		&i.EmailVerifiedAt,
		&i.DeactivatedAt,
	)
	return i, err
}
