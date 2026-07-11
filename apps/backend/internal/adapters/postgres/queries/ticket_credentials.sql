-- ticket_credentials.sql — sqlc queries for the ticket_credentials table (feature #140).
--
-- Credential lifecycle:
--   1. InsertTicketCredential — create (or regenerate) a credential for a ticket.
--      Uses ON CONFLICT DO UPDATE so callers can regenerate without checking first.
--   2. InsertTicketCredentialWithHumanCode — static_qr variant that also stores
--      the SEAT-C4 human-readable code. Callers must retry (bounded) on a 23505
--      unique violation of ticket_credentials_human_code_unique.
--   3. GetCredentialByTicketID — fetch the credential for a specific (ticket, type).
--      Returns pgx.ErrNoRows when not yet generated.
--   4. GetCredentialByHumanCode — resolve a canonical human code (SEAT-C4) to its
--      static_qr credential row. Callers normalize input first (humancode.Normalize).
--   5. SetTicketCredentialHumanCode — backfill a human code onto a legacy
--      static_qr row that predates SEAT-C4 (human_code IS NULL guard keeps the
--      write race-safe).
--   6. RevokeCredential — mark all credentials for a ticket as revoked.
--      Called on ticket cancellation or refund.
--   7. ListCredentialsByTicketID — list all credentials for a ticket (for audit/display).

-- name: InsertTicketCredential :one
INSERT INTO ticket_credentials (ticket_id, type, payload)
VALUES ($1, $2, $3)
ON CONFLICT (ticket_id, type) DO UPDATE
    SET payload    = EXCLUDED.payload,
        revoked_at = NULL
RETURNING id, ticket_id, type, payload, human_code, issued_at, revoked_at;

-- name: InsertTicketCredentialWithHumanCode :one
INSERT INTO ticket_credentials (ticket_id, type, payload, human_code)
VALUES ($1, $2, $3, $4)
ON CONFLICT (ticket_id, type) DO UPDATE
    SET payload    = EXCLUDED.payload,
        human_code = EXCLUDED.human_code,
        revoked_at = NULL
RETURNING id, ticket_id, type, payload, human_code, issued_at, revoked_at;

-- name: GetCredentialByTicketID :one
SELECT id, ticket_id, type, payload, human_code, issued_at, revoked_at
FROM   ticket_credentials
WHERE  ticket_id = $1
  AND  type      = $2;

-- name: GetCredentialByHumanCode :one
SELECT id, ticket_id, type, payload, human_code, issued_at, revoked_at
FROM   ticket_credentials
WHERE  type       = 'static_qr'
  AND  human_code = $1;

-- name: SetTicketCredentialHumanCode :one
UPDATE ticket_credentials
SET    human_code = $3
WHERE  ticket_id  = $1
  AND  type       = $2
  AND  human_code IS NULL
RETURNING id, ticket_id, type, payload, human_code, issued_at, revoked_at;

-- name: RevokeCredential :one
UPDATE ticket_credentials
SET    revoked_at = now()
WHERE  ticket_id  = $1
  AND  type       = $2
RETURNING id, ticket_id, type, payload, human_code, issued_at, revoked_at;

-- name: ListCredentialsByTicketID :many
SELECT id, ticket_id, type, payload, human_code, issued_at, revoked_at
FROM   ticket_credentials
WHERE  ticket_id = $1
ORDER BY issued_at ASC;
