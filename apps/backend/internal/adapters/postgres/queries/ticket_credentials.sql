-- ticket_credentials.sql — sqlc queries for the ticket_credentials table (feature #140).
--
-- Credential lifecycle:
--   1. InsertTicketCredential — create (or regenerate) a credential for a ticket.
--      Uses ON CONFLICT DO UPDATE so callers can regenerate without checking first.
--   2. GetCredentialByTicketID — fetch the credential for a specific (ticket, type).
--      Returns pgx.ErrNoRows when not yet generated.
--   3. RevokeCredential — mark all credentials for a ticket as revoked.
--      Called on ticket cancellation or refund.
--   4. ListCredentialsByTicketID — list all credentials for a ticket (for audit/display).

-- name: InsertTicketCredential :one
INSERT INTO ticket_credentials (ticket_id, type, payload)
VALUES ($1, $2, $3)
ON CONFLICT (ticket_id, type) DO UPDATE
    SET payload    = EXCLUDED.payload,
        revoked_at = NULL
RETURNING id, ticket_id, type, payload, issued_at, revoked_at;

-- name: GetCredentialByTicketID :one
SELECT id, ticket_id, type, payload, issued_at, revoked_at
FROM   ticket_credentials
WHERE  ticket_id = $1
  AND  type      = $2;

-- name: RevokeCredential :one
UPDATE ticket_credentials
SET    revoked_at = now()
WHERE  ticket_id  = $1
  AND  type       = $2
RETURNING id, ticket_id, type, payload, issued_at, revoked_at;

-- name: ListCredentialsByTicketID :many
SELECT id, ticket_id, type, payload, issued_at, revoked_at
FROM   ticket_credentials
WHERE  ticket_id = $1
ORDER BY issued_at ASC;
