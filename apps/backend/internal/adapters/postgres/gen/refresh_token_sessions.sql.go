// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: refresh_tokens.sql (migration 0150)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertRefreshTokenSession = `-- name: InsertRefreshTokenSession :exec
INSERT INTO refresh_tokens (token, user_id, expires_at, client_channel, idle_seconds, session_started_at, session_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`

// RefreshTokenSession are the session facts a refresh token carries through
// every rotation (migration 0150). Nil fields are rows issued before 0150.
type RefreshTokenSession struct {
	ClientChannel    *string    `json:"client_channel"`
	IdleSeconds      *int32     `json:"idle_seconds"`
	SessionStartedAt *time.Time `json:"session_started_at"`
	// SessionID is the id of the sign-in; the access token carries it as
	// "sid" and the step-up token is bound to it.
	SessionID *uuid.UUID `json:"session_id"`
}

// InsertRefreshTokenSession inserts a refresh token row with its session
// facts. token is the SHA-256 of the raw token, as InsertRefreshToken stores.
func (q *Queries) InsertRefreshTokenSession(ctx context.Context, token string, userID uuid.UUID, expiresAt time.Time, s RefreshTokenSession) error {
	_, err := q.db.Exec(ctx, insertRefreshTokenSession, token, userID, expiresAt, s.ClientChannel, s.IdleSeconds, s.SessionStartedAt, s.SessionID)
	return err
}

const getRefreshTokenSession = `-- name: GetRefreshTokenSession :one
SELECT client_channel, idle_seconds, session_started_at, session_id
FROM refresh_tokens
WHERE token = $1
`

// GetRefreshTokenSession reads the session facts of a refresh token row.
func (q *Queries) GetRefreshTokenSession(ctx context.Context, token string) (RefreshTokenSession, error) {
	var s RefreshTokenSession
	err := q.db.QueryRow(ctx, getRefreshTokenSession, token).Scan(&s.ClientChannel, &s.IdleSeconds, &s.SessionStartedAt, &s.SessionID)
	return s, err
}

const refreshSessionIsLive = `-- name: RefreshSessionIsLive :one
SELECT EXISTS (
    SELECT 1 FROM refresh_tokens
    WHERE session_id = $1 AND user_id = $2
      AND revoked_at IS NULL AND expires_at > now()
)
`

// RefreshSessionIsLive reports whether the sign-in sessionID of userID still
// has a refresh token that is neither revoked nor expired.
func (q *Queries) RefreshSessionIsLive(ctx context.Context, sessionID, userID uuid.UUID) (bool, error) {
	var live bool
	err := q.db.QueryRow(ctx, refreshSessionIsLive, sessionID, userID).Scan(&live)
	return live, err
}
