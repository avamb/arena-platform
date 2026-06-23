-- feed_tokens.sql — sqlc query definitions for agent_feed_tokens (feature #122).
-- Token lookups by token value (public feed) and by id (management API).

-- name: InsertFeedToken :one
INSERT INTO agent_feed_tokens (token, sales_channel_id, label)
VALUES ($1, $2, $3)
RETURNING id, token, sales_channel_id, label, is_active, revoked_at, last_used_at, created_at, updated_at;

-- name: GetFeedTokenByID :one
SELECT id, token, sales_channel_id, label, is_active, revoked_at, last_used_at, created_at, updated_at
FROM   agent_feed_tokens
WHERE  id = $1
  AND  sales_channel_id = $2;

-- name: ListFeedTokensByChannel :many
SELECT id, token, sales_channel_id, label, is_active, revoked_at, last_used_at, created_at, updated_at
FROM   agent_feed_tokens
WHERE  sales_channel_id = $1
ORDER  BY created_at ASC, id ASC;

-- name: RevokeFeedToken :one
UPDATE agent_feed_tokens
SET    is_active  = false,
       revoked_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  sales_channel_id = $2
RETURNING id, token, sales_channel_id, label, is_active, revoked_at, last_used_at, created_at, updated_at;

-- name: TouchFeedTokenLastUsed :exec
UPDATE agent_feed_tokens
SET    last_used_at = now(),
       updated_at   = now()
WHERE  token = $1
  AND  is_active = true;

-- name: GetFeedTokenByToken :one
SELECT id, token, sales_channel_id, label, is_active, revoked_at, last_used_at, created_at, updated_at
FROM   agent_feed_tokens
WHERE  token = $1;
