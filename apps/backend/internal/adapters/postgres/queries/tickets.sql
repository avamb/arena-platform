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

-- name: ListTicketsMissingEAN13 :many
-- W1-B6b (feature #503): candidates for the tickets.backfill_ean13 worker
-- job. Only 'active' tickets are backfilled — cancelled/transferred
-- tickets have no scan-time use for a fresh barcode, and
-- RevokeTicketArtifactsTx already revokes ean13 credentials on
-- cancellation, so a cancelled ticket picked up here would immediately be
-- a revoked-on-arrival row. LEFT JOIN + IS NULL finds tickets that never
-- got an ean13 row (pre-#502 data); ORDER BY + LIMIT makes each run a
-- bounded, resumable batch.
SELECT t.id, t.checkout_session_id, t.session_id, t.tier_id, t.holder_email,
       t.status, t.issued_at, t.created_at, t.updated_at,
       t.seat_key, t.seat_sector, t.seat_row, t.seat_number, t.ordinal,
       t.cancelled_at, t.cancellation_reason, t.refund_mode, t.refund_id,
       t.refund_date, t.refund_price, t.review_hold, t.review_hold_reason,
       t.system_ticket_id
FROM   tickets t
LEFT JOIN ticket_credentials c
       ON c.ticket_id = t.id
      AND c.type      = 'ean13'
WHERE  c.id IS NULL
  AND  t.status = 'active'
ORDER BY t.id ASC
LIMIT $1;

-- name: GetTicketPresentationByID :one
-- The presentation projection a ticket e-mail / PDF needs: the human-facing
-- ticket number plus the event / session / venue / category / holder values
-- the buyer actually reads. Resolved at RENDER time by the delivery worker
-- (delivery.Payload's enqueue-time hints were never populated by any
-- production enqueuer, so every live ticket PDF printed "Arena Event" and
-- blank Venue / Category / Holder rows until 2026-09-20).
--
-- Every join below is a LEFT JOIN on purpose: a ticket whose venue row was
-- soft-deleted, whose tier is NULL (GA), or which predates the orders
-- aggregate must still resolve everything else. The query returns exactly
-- one row for an existing ticket, with NULL for whatever is unavailable.
--
-- price_minor is order_items.total — what the buyer actually paid for THIS
-- unit (unit_price minus its share of the order discount plus its share of
-- the service charge), NOT ticket_tiers.price_amount, which is the list
-- price the category happens to carry today. An invitation
-- (orders.source='complimentary') discounts the whole subtotal, so its
-- items total 0 and the renderer drops the price cell rather than printing
-- "0" — see pdf.priceValue.
--
-- poster_media_id follows the documented resolution order of migration
-- 0082: the session's own override first, the event's poster otherwise.
SELECT t.system_ticket_id,
       ord.system_id                 AS order_number,
       e.name                        AS event_name,
       s.start_at                    AS session_start_at,
       v.name                        AS venue_name,
       COALESCE(NULLIF(btrim(v.address_line1), ''),
                NULLIF(btrim(v.address),       '')) AS venue_address,
       COALESCE(t_en.value, ci.slug) AS venue_city,
       v.timezone                    AS venue_timezone,
       tt.name                       AS tier_name,
       COALESCE(NULLIF(btrim(ord.buyer_name), ''), cu.display_name) AS holder_name,
       oi.total                      AS price_minor,
       NULLIF(btrim(ord.currency), '') AS price_currency,
       COALESCE(s.poster_media_id, e.poster_media_id) AS poster_media_id
FROM       tickets t
LEFT JOIN  sessions      s  ON s.id  = t.session_id
LEFT JOIN  events        e  ON e.id  = s.event_id
LEFT JOIN  venues        v  ON v.id  = s.venue_id
LEFT JOIN  cities        ci ON ci.id = v.city_id
LEFT JOIN  i18n_text     t_en ON t_en.namespace = 'geo.cities'
       AND t_en.key = ci.slug AND t_en.locale = 'en'
LEFT JOIN  ticket_tiers  tt ON tt.id = t.tier_id
LEFT JOIN  orders       ord ON ord.id = t.order_id
LEFT JOIN  customers     cu ON cu.id = ord.customer_id
LEFT JOIN  order_items   oi ON oi.ticket_id = t.id
WHERE  t.id = $1;
