-- reservation_seats.sql — sqlc-style query definitions for the
-- reservation_seats join (feature #305, Wave SEAT-B1). Companion
-- hand-written gen file:
--   apps/backend/internal/adapters/postgres/gen/reservation_seats.sql.go
--
-- The table is a pure join between reservations and session_seats.
-- Rows are written inside the transactional seat-hold path of the
-- seated checkout (SEAT-B5) and walked during ticket issuance to
-- hydrate the denormalized seat_* columns onto emitted tickets.

-- name: InsertReservationSeat :exec
-- Records the (reservation_id, session_seat_id) link. Idempotency is
-- provided by the composite PRIMARY KEY: a duplicate write raises
-- 23505 (unique violation) which the caller MUST translate into a
-- 409 conflict.
INSERT INTO reservation_seats (reservation_id, session_seat_id)
VALUES ($1, $2);

-- name: ListReservationSeats :many
-- Returns every seat linked to a reservation joined with its
-- session_seats row. Ordered by session_seats.seat_key for
-- deterministic ticket iteration during issuance.
SELECT ss.id, ss.session_id, ss.seat_key, ss.sector_name, ss.row_name,
       ss.seat_number, ss.tier_id, ss.status, ss.reservation_id,
       ss.status_version, ss.updated_at
FROM   reservation_seats rs
JOIN   session_seats     ss ON ss.id = rs.session_seat_id
WHERE  rs.reservation_id = $1
ORDER  BY ss.seat_key ASC, ss.id ASC;

-- name: DeleteReservationSeats :exec
-- Removes every seat link for a reservation. Called on cancel /
-- expire paths before / together with the ReleaseSessionSeat
-- transitions so a stale link never outlives its hold.
DELETE FROM reservation_seats
WHERE  reservation_id = $1;

-- name: DeleteReservationSeatsBySession :execrows
-- Removes every reservation_seats link whose seat belongs to the given
-- session. Called on the SEAT-B2 rebind path (after the
-- zero-reservations / zero-tickets guardrail) immediately before
-- DeleteSessionSeatsBySession so the FK from reservation_seats to
-- session_seats never dangles.
DELETE FROM reservation_seats
WHERE  session_seat_id IN (
    SELECT id FROM session_seats WHERE session_id = $1
);

-- name: CountReservationSeats :one
-- Returns the number of seats currently linked to a reservation.
SELECT COUNT(*)::bigint AS count
FROM   reservation_seats
WHERE  reservation_id = $1;
