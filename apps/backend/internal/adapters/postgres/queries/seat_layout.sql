-- seat_layout.sql — read-only inputs of the seat-layout allocator
-- (POST /v1/organizations/{org_id}/sessions/{session_id}/seat-layout/suggest,
-- task S1e). Nothing here writes.

-- name: ListSessionSeatLayoutStates :many
-- Every seat (kind 'seat', never a GA unit) of a session with its status and
-- the two prices the allocator may need for a frozen seat: what its order
-- line says (order_items.total of the latest order of the seat's CURRENT
-- reservation) and its category's list price. A sold seat is counted "by
-- fact" at the order price; a held seat without an order yet, and a seat
-- sold upstream (no reservation), fall back to the list price. Orders are
-- reached through orders_session_status_idx; order_items has no index on
-- session_seat_id, so the join never starts from it.
--
-- hold_expired is true for a 'held' seat whose reservation is no longer
-- live (state not draft/active, or expires_at passed — the same rule as
-- hbil24's orderOldHoldIsLive). reservation.expire_sweep releases such a
-- seat within about 30 seconds, so the allocator treats it as FREE: it gets
-- a category and counts at the proposed price. A held seat without a
-- reservation row stays frozen (nothing proves it is dead).
SELECT ss.seat_key,
       ss.status,
       p.total         AS order_price,
       tt.price_amount AS tier_price,
       (ss.status = 'held' AND r.id IS NOT NULL
        AND (r.state NOT IN ('draft', 'active') OR r.expires_at <= now())) AS hold_expired
FROM      session_seats ss
LEFT JOIN ticket_tiers  tt ON tt.id = ss.tier_id
LEFT JOIN reservations  r  ON r.id = ss.reservation_id
LEFT JOIN (
    SELECT DISTINCT ON (oi.session_seat_id, o.reservation_id)
           oi.session_seat_id, o.reservation_id, oi.total
    FROM   orders o
    JOIN   order_items oi ON oi.order_id = o.id
    WHERE  o.session_id = $1
      AND  oi.session_seat_id IS NOT NULL
    ORDER  BY oi.session_seat_id, o.reservation_id, o.created_at DESC, oi.ordinal DESC
) p ON  p.session_seat_id = ss.id
    AND p.reservation_id  = ss.reservation_id
    AND ss.status IN ('sold', 'held')
WHERE  ss.session_id = $1
  AND  ss.kind = 'seat'
ORDER  BY ss.seat_key;
