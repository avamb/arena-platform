-- refresh_tokens.sql — SQL queries for refresh token management (feature #115).

-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (token, user_id, expires_at)
VALUES ($1, $2, $3);

-- name: GetRefreshToken :one
SELECT token, user_id, expires_at, revoked_at, created_at, revoke_reason
FROM refresh_tokens
WHERE token = $1;

-- name: RevokeRefreshToken :exec
-- A sign-out or a session eviction (migration 0214: revoke_reason). A token
-- that is already revoked keeps its first time and reason.
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'signed_out'
WHERE token = $1 AND revoked_at IS NULL;

-- name: RotateRefreshToken :exec
-- The token was replaced by a newer one on refresh.
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'rotated'
WHERE token = $1;

-- name: RevokeAllUserRefreshTokens :exec
-- Revokes every active refresh token for a user in one sweep.
-- Called when a password reset succeeds or a session-compromise is detected.
UPDATE refresh_tokens
SET revoked_at = now(), revoke_reason = 'revoked_all'
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetUserByID :one
SELECT id, email, password_hash, preferred_locale, created_at, email_verified_at, deactivated_at
FROM users
WHERE id = $1;

-- name: InsertRefreshTokenSession :exec
-- A refresh token with its session facts (migration 0150): the client that
-- signed in, the idle window (NULL = fixed lifetime), the sign-in time and
-- the sign-in id.
INSERT INTO refresh_tokens (token, user_id, expires_at, client_channel, idle_seconds, session_started_at, session_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetRefreshTokenSession :one
SELECT client_channel, idle_seconds, session_started_at, session_id
FROM refresh_tokens
WHERE token = $1;

-- name: RefreshSessionIsLive :one
-- A sign-in is live while one of its refresh tokens is neither revoked nor
-- expired. A sign-out revokes the token, "sign out everywhere" expires it.
SELECT EXISTS (
    SELECT 1 FROM refresh_tokens
    WHERE session_id = $1 AND user_id = $2
      AND revoked_at IS NULL AND expires_at > now()
);
