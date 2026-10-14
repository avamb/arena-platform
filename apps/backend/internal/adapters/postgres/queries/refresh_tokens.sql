-- refresh_tokens.sql — SQL queries for refresh token management (feature #115).

-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (token, user_id, expires_at)
VALUES ($1, $2, $3);

-- name: GetRefreshToken :one
SELECT token, user_id, expires_at, revoked_at, created_at
FROM refresh_tokens
WHERE token = $1;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE token = $1;

-- name: RevokeAllUserRefreshTokens :exec
-- Revokes every active refresh token for a user in one sweep.
-- Called when a password reset succeeds or a session-compromise is detected.
UPDATE refresh_tokens
SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetUserByID :one
SELECT id, email, password_hash, preferred_locale, created_at, email_verified_at, deactivated_at
FROM users
WHERE id = $1;

-- name: InsertRefreshTokenSession :exec
-- A refresh token with its session facts (migration 0150): the client that
-- signed in, the idle window (NULL = fixed lifetime) and the sign-in time.
INSERT INTO refresh_tokens (token, user_id, expires_at, client_channel, idle_seconds, session_started_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetRefreshTokenSession :one
SELECT client_channel, idle_seconds, session_started_at
FROM refresh_tokens
WHERE token = $1;
