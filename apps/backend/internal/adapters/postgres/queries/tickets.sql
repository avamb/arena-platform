-- tickets.sql — query definitions for ticket issuance (feature #139).
--
-- Tickets are issued after payment.succeeded or free-checkout completion.
-- Idempotency: before issuing, call ListTicketsByCheckoutSession — if non-empty,
-- the checkout_session_id has already been issued tickets; return existing rows.
--
-- SEAT-C3 (feature #311): tickets carry denormalized seat coordinates
-- (seat_key / seat_sector / seat_row / seat_number) copied from
-- session_seats at issuance for assigned-seat sessions. GA tickets keep
-- all four columns NULL.

-- name: InsertTicket :one
INSERT INTO tickets (
    checkout_session_id, session_id, tier_id, holder_email,
    seat_key, seat_sector, seat_row, seat_number
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number;

-- name: ListTicketsByCheckoutSession :many
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number
FROM   tickets
WHERE  checkout_session_id = $1
ORDER BY issued_at ASC, id ASC;

-- name: GetTicketByID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number
FROM   tickets
WHERE  id = $1;

-- name: CountTicketsByCheckoutSession :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  checkout_session_id = $1;

-- name: CountTicketsBySession :one
-- CountTicketsBySession returns the number of tickets issued against a
-- session. Powers the seating-plan rebind gate (feature #306, Wave SEAT-B2)
-- alongside CountReservationsBySession.
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1;
