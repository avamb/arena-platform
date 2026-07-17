-- tickets.sql — query definitions for ticket issuance (feature #139).
--
-- Tickets are issued after payment.succeeded or free-checkout completion.
-- Idempotency: IssueTicketsForCheckout acquires pg_advisory_xact_lock keyed on
-- the checkout_session_id, then checks existing count vs expected quantity.
-- The UNIQUE (checkout_session_id, ordinal) constraint (migration 0066) is a
-- database-level safety belt against concurrent double-issuance (feature #366).
--
-- SEAT-C3 (feature #311): tickets carry denormalized seat coordinates
-- (seat_key / seat_sector / seat_row / seat_number) copied from
-- session_seats at issuance for assigned-seat sessions. GA tickets keep
-- all four columns NULL.
--
-- ordinal (feature #366): 0-based ticket index within the checkout session.
-- Enables partial-issue recovery: on retry, only missing ordinals are inserted.

-- name: InsertTicket :one
INSERT INTO tickets (
    checkout_session_id, session_id, tier_id, holder_email,
    seat_key, seat_sector, seat_row, seat_number, ordinal
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal;

-- name: ListTicketsByCheckoutSession :many
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal
FROM   tickets
WHERE  checkout_session_id = $1
ORDER BY ordinal ASC, issued_at ASC, id ASC;

-- name: GetTicketByID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal
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
