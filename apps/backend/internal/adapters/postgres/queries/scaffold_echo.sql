-- scaffold_echo queries used by POST /v1/scaffold/echo
-- (feature #105 — scaffolding example, not a real domain query)

-- name: InsertScaffoldEcho :one
-- InsertScaffoldEcho inserts a new row into scaffold_echo and returns the
-- generated id and created_at timestamp. actor_id is cast from text to uuid
-- so callers can pass the string UUID from the auth context without converting
-- on the Go side.
INSERT INTO scaffold_echo (actor_id, message)
VALUES ($1::uuid, $2)
RETURNING id, actor_id, message, created_at;
