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
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason;

-- name: ListTicketsByCheckoutSession :many
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal,
       cancelled_at, cancellation_reason, refund_mode, refund_id,
       refund_date, refund_price, review_hold, review_hold_reason
FROM   tickets
WHERE  checkout_session_id = $1
ORDER BY ordinal ASC, issued_at ASC, id ASC;

-- name: GetTicketByID :one
SELECT id, checkout_session_id, session_id, tier_id, holder_email,
       status, issued_at, created_at, updated_at,
       seat_key, seat_sector, seat_row, seat_number, ordinal,
       cancelled_at, cancellation_reason, refund_mode, refund_id,
       refund_date, refund_price, review_hold, review_hold_reason
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

-- ─────────────────────────────────────────────────────────────────────
-- AB-49: ticket cancellation
-- ─────────────────────────────────────────────────────────────────────

-- name: CancelTicket :one
-- Conditional 'active' -> 'cancelled' transition for the operator
-- cancellation action. Records the reason and the refund-mode decision
-- taken at cancellation time. Returns pgx.ErrNoRows when the ticket is
-- not active — the caller MUST surface that as a 409 (already
-- cancelled / transferred / revoked), never as a silent success.
UPDATE tickets
SET    status              = 'cancelled',
       cancelled_at        = now(),
       cancellation_reason = $2,
       refund_mode         = $3,
       updated_at          = now()
WHERE  id     = $1
  AND  status = 'active'
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason;

-- name: SetTicketRefundRecord :one
-- Records the financial side of a cancellation on the ticket: the
-- refunds-row link (mode=automatic), the refund date and the refunded
-- amount (Bil24 export shape refundDate/refundPrice). Never gates
-- anything — called AFTER the cancellation transaction committed.
UPDATE tickets
SET    refund_id    = $2,
       refund_date  = $3,
       refund_price = $4,
       updated_at   = now()
WHERE  id = $1
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason;

-- name: SetTicketsReviewHoldByCheckoutSession :execrows
-- AB-49: a PARTIAL inbound provider refund cannot be attributed to
-- specific tickets — auto-cancel nothing, flag every ticket of the
-- order for human review. The hold FLAGS, never blocks admission, and
-- is never propagated to MACS as a gate status.
UPDATE tickets
SET    review_hold        = true,
       review_hold_reason = $2,
       updated_at         = now()
WHERE  checkout_session_id = $1
  AND  status = 'active'
  AND  review_hold = false;

-- name: ClearTicketReviewHold :one
-- Operator resolves a review hold on one ticket (either by cancelling
-- it via the cancel endpoint — which clears the flag as part of the
-- resolution — or by confirming it stays valid).
UPDATE tickets
SET    review_hold        = false,
       review_hold_reason = NULL,
       updated_at         = now()
WHERE  id = $1
  AND  review_hold = true
RETURNING id, checkout_session_id, session_id, tier_id, holder_email,
          status, issued_at, created_at, updated_at,
          seat_key, seat_sector, seat_row, seat_number, ordinal,
          cancelled_at, cancellation_reason, refund_mode, refund_id,
          refund_date, refund_price, review_hold, review_hold_reason;

-- name: SetTicketOrder :exec
-- Links a freshly issued ticket to the order it belongs to (column added
-- by migration 0092; W1-A6c, feature #488, spec §7.9 step 5 / §14.3
-- invariant "tickets.order_id filled at issuance for every source that
-- has an order"). Deliberately a narrow :exec rather than a RETURNING
-- query: order_id is not part of TicketRow, so widening the shared
-- scanTicketRow would ripple through every SELECT that feeds it.
UPDATE tickets
SET    order_id   = $2,
       updated_at = now()
WHERE  id = $1;

-- name: CountActiveTicketsForSeat :one
-- Guard for ReleaseSoldSessionSeat: how many ACTIVE tickets still
-- reference (session_id, seat_key). Uses tickets_active_seat_idx.
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1
  AND  seat_key   = $2
  AND  status     = 'active';
