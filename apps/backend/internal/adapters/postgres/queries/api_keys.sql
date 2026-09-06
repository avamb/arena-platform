-- api_keys.sql — sqlc query definitions for the api_keys table
-- (migration 0096, feature #512, spec §3.6 / §13.1).
--
-- key_prefix is the 12-char lookup segment of the wire token
-- ak_<prefix12>_<secret43>; key_hash is bcrypt of the secret half. Every
-- read query is scoped so callers cannot leak another org's row.

-- name: InsertAPIKey :one
INSERT INTO api_keys (
    org_id, channel_id, name, key_prefix, key_hash, scopes, created_by, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
          created_at, last_used_at, expires_at, revoked_at;

-- name: GetAPIKeyByPrefix :one
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  key_prefix = $1;

-- name: GetAPIKeyByID :one
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  id = $1
  AND  org_id = $2;

-- name: ListAPIKeysByOrg :many
SELECT id, org_id, channel_id, name, key_prefix, key_hash, scopes, created_by,
       created_at, last_used_at, expires_at, revoked_at
FROM   api_keys
WHERE  org_id = $1
ORDER  BY created_at DESC, id ASC;

-- name: TouchAPIKeyLastUsed :exec
UPDATE api_keys
SET    last_used_at = $2
WHERE  id = $1;

-- name: RevokeAPIKey :exec
UPDATE api_keys
SET    revoked_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  revoked_at IS NULL;
