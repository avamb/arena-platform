// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: refresh_tokens.sql (migration 0150)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertRefreshTokenSession = `-- name: InsertRefreshTokenSession :exec
INSERT INTO refresh_tokens (token, user_id, expires_at, client_channel, idle_seconds, session_started_at)
VALUES ($1, $2, $3, $4, $5, $6)
`

// RefreshTokenSession are the session facts a refresh token carries through
// every rotation (migration 0150). Nil fields are rows issued before 0150.
type RefreshTokenSession struct {
	ClientChannel    *string    `json:"client_channel"`
	IdleSeconds      *int32     `json:"idle_seconds"`
	SessionStartedAt *time.Time `json:"session_started_at"`
}

// InsertRefreshTokenSession inserts a refresh token row with its session
// facts. token is the SHA-256 of the raw token, as InsertRefreshToken stores.
func (q *Queries) InsertRefreshTokenSession(ctx context.Context, token string, userID uuid.UUID, expiresAt time.Time, s RefreshTokenSession) error {
	_, err := q.db.Exec(ctx, insertRefreshTokenSession, token, userID, expiresAt, s.ClientChannel, s.IdleSeconds, s.SessionStartedAt)
	return err
}

const getRefreshTokenSession = `-- name: GetRefreshTokenSession :one
SELECT client_channel, idle_seconds, session_started_at
FROM refresh_tokens
WHERE token = $1
`

// GetRefreshTokenSession reads the session facts of a refresh token row.
func (q *Queries) GetRefreshTokenSession(ctx context.Context, token string) (RefreshTokenSession, error) {
	var s RefreshTokenSession
	err := q.db.QueryRow(ctx, getRefreshTokenSession, token).Scan(&s.ClientChannel, &s.IdleSeconds, &s.SessionStartedAt)
	return s, err
}
