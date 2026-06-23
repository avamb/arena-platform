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

-- name: GetUserByID :one
SELECT id, email, password_hash, preferred_locale, created_at, email_verified_at
FROM users
WHERE id = $1;
