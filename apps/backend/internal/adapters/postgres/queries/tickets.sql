-- tickets.sql — query definitions for ticket issuance (feature #139).
--
-- Tickets are issued after payment.succeeded or free-checkout completion.
-- Idempotency: before issuing, call ListTicketsByCheckoutSession — if non-empty,
-- the checkout_session_id has already been issued tickets; return existing rows.

-- name: InsertTicket :one
INSERT INTO tickets (checkout_session_id, session_id, tier_id, holder_email)
VALUES ($1, $2, $3, $4)
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at;

-- name: ListTicketsByCheckoutSession :many
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at
FROM   tickets
WHERE  checkout_session_id = $1
ORDER BY issued_at ASC, id ASC;

-- name: GetTicketByID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at
FROM   tickets
WHERE  id = $1;

-- name: CountTicketsByCheckoutSession :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  checkout_session_id = $1;
