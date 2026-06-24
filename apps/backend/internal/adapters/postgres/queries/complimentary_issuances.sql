-- complimentary_issuances.sql — typed sqlc queries for the complimentary_issuances table.
-- Feature #148 — Wave 11 Complimentary tickets.
--
-- Complimentary issuances track batches of tickets issued without payment.
-- batch_id provides idempotency: callers must supply a unique batch_id; if a row
-- with the same (org_id, batch_id) already exists, the handler returns it unchanged.
--
-- InsertComplimentaryTicket inserts directly into the tickets table using
-- complimentary_issuance_id (no checkout_session_id required, see migration 0036).

-- name: InsertComplimentaryIssuance :one
-- Creates a new complimentary issuance batch record in 'pending' status.
INSERT INTO complimentary_issuances (org_id, session_id, tier_id, qty, recipients, batch_id, issued_by, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, session_id, tier_id, qty, recipients, batch_id, status, issued_by, notes,
          created_at, updated_at;

-- name: GetComplimentaryIssuanceByBatchID :one
-- Fetches an issuance by (org_id, batch_id) — used for idempotency check.
-- Returns pgx.ErrNoRows when no matching row exists.
SELECT id, org_id, session_id, tier_id, qty, recipients, batch_id, status, issued_by, notes,
       created_at, updated_at
FROM   complimentary_issuances
WHERE  org_id = $1
  AND  batch_id = $2;

-- name: GetComplimentaryIssuanceByID :one
-- Fetches an issuance by UUID primary key.
-- Returns pgx.ErrNoRows when not found.
SELECT id, org_id, session_id, tier_id, qty, recipients, batch_id, status, issued_by, notes,
       created_at, updated_at
FROM   complimentary_issuances
WHERE  id = $1;

-- name: ListComplimentaryIssuancesByOrg :many
-- Lists all issuances for an org, newest first.
SELECT id, org_id, session_id, tier_id, qty, recipients, batch_id, status, issued_by, notes,
       created_at, updated_at
FROM   complimentary_issuances
WHERE  org_id = $1
ORDER BY created_at DESC;

-- name: UpdateComplimentaryIssuanceStatus :one
-- Transitions an issuance to a new status (pending → issued | failed).
-- Returns pgx.ErrNoRows when the issuance does not exist.
UPDATE complimentary_issuances
SET    status     = $2,
       updated_at = now()
WHERE  id = $1
RETURNING id, org_id, session_id, tier_id, qty, recipients, batch_id, status, issued_by, notes,
          created_at, updated_at;

-- name: InsertComplimentaryTicket :one
-- Inserts a ticket row sourced from a complimentary issuance.
-- Uses complimentary_issuance_id instead of checkout_session_id (see migration 0036
-- which makes checkout_session_id nullable and adds the complimentary_issuance_id FK).
INSERT INTO tickets (complimentary_issuance_id, session_id, tier_id, holder_email)
VALUES ($1, $2, $3, $4)
RETURNING id, complimentary_issuance_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at;

-- name: ListTicketsByComplimentaryIssuance :many
-- Lists all tickets for a complimentary issuance (idempotency check + read).
SELECT id, complimentary_issuance_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at
FROM   tickets
WHERE  complimentary_issuance_id = $1
ORDER BY issued_at ASC, id ASC;
