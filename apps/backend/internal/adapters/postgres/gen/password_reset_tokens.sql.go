// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: password_reset_tokens.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertPasswordResetToken = `-- name: InsertPasswordResetToken :exec
INSERT INTO password_reset_tokens (token, user_id, expires_at)
VALUES ($1, $2, $3)
`

// InsertPasswordResetToken inserts a new password-reset token row.
// token is a 64-character hex string (32 random bytes).
// expiresAt should be now()+1h, set by the application layer.
func (q *Queries) InsertPasswordResetToken(ctx context.Context, token string, userID uuid.UUID, expiresAt time.Time) error {
	_, err := q.db.Exec(ctx, insertPasswordResetToken, token, userID, expiresAt)
	return err
}

const getPasswordResetToken = `-- name: GetPasswordResetToken :one
SELECT token, user_id, expires_at, used_at
FROM password_reset_tokens
WHERE token = $1
`

// GetPasswordResetTokenRow is the result type for GetPasswordResetToken.
type GetPasswordResetTokenRow struct {
	Token     string     `json:"token"`
	UserID    uuid.UUID  `json:"user_id"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
}

// GetPasswordResetToken fetches a password-reset token row by token string.
// Returns pgx.ErrNoRows when no matching token exists.
func (q *Queries) GetPasswordResetToken(ctx context.Context, token string) (GetPasswordResetTokenRow, error) {
	row := q.db.QueryRow(ctx, getPasswordResetToken, token)
	var i GetPasswordResetTokenRow
	err := row.Scan(
		&i.Token,
		&i.UserID,
		&i.ExpiresAt,
		&i.UsedAt,
	)
	return i, err
}

const consumePasswordResetToken = `-- name: ConsumePasswordResetToken :one
UPDATE password_reset_tokens
SET used_at = now()
WHERE token = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id
`

// ConsumePasswordResetToken stamps used_at on an unused, unexpired token and
// returns its owner. A token is
// consumed at most once. Returns pgx.ErrNoRows when the token is unknown,
// already used or expired.
func (q *Queries) ConsumePasswordResetToken(ctx context.Context, token string) (uuid.UUID, error) {
	row := q.db.QueryRow(ctx, consumePasswordResetToken, token)
	var userID uuid.UUID
	err := row.Scan(&userID)
	return userID, err
}
