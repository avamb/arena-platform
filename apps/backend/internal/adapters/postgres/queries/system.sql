-- name: SelectUUIDv7 :one
SELECT uuidv7() AS id;

-- name: SelectServerTime :one
-- Returns the current PostgreSQL server timestamp. Used by GET /v1/server-info
-- to demonstrate the full router → handler → sqlc → response chain.
SELECT now() AS server_time;
