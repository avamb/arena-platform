-- gateway_cart.sql — reservations viewed as the Bil24-gateway session cart.
-- Feature #484 (W1-A5b), spec section 7.4.
--
-- The Bil24 compatibility gateway models a shopping cart as the set of open
-- reservations (state draft|active, TTL not yet elapsed) that carry the same
-- reservations.gateway_session_id, with at most ONE such reservation per event
-- session. Migration 0091 added the gateway_session_id / customer_id columns
-- plus the partial index reservations_gateway_session_active_idx.
--
-- Every query below selects the SAME 14-column list as reservations.sql so the
-- shared scanReservationRow helper stays valid.

-- name: BindReservationToGatewaySession :exec
-- Attaches a freshly created hold to the gateway session that requested it so
-- later RESERVE / UN_RESERVE / UN_RESERVE_ALL commands can find the cart.
-- customerID may be NULL for anonymous gateway sessions.
UPDATE reservations
SET    gateway_session_id = $2,
       customer_id        = COALESCE($3, customer_id),
       updated_at         = now()
WHERE  id = $1;

-- name: GetActiveGatewayCartReservation :one
-- Returns the single open reservation of the gateway session for one event
-- session — the row a RESERVE extends and an UN_RESERVE shrinks. Returns
-- pgx.ErrNoRows when the cart has no line for that event session yet.
-- Ordered newest-first so a legacy duplicate (pre-0091 data) resolves
-- deterministically to the most recent hold.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  gateway_session_id = $1
  AND  session_id = $2
  AND  state IN ('draft', 'active')
  AND  expires_at > now()
ORDER  BY created_at DESC, id DESC
LIMIT  1;

-- name: ListActiveGatewayCartReservations :many
-- Returns every open reservation of the gateway session across all event
-- sessions — the whole cart projected by the RESERVATION response and the set
-- cancelled by UN_RESERVE_ALL. Ordered oldest-first so the response seatList
-- is stable across calls.
SELECT id, org_id, channel_id, session_id, tier_id, user_id, quantity, state,
       expires_at, created_at, updated_at, cancelled_at, converted_at, expired_at
FROM   reservations
WHERE  gateway_session_id = $1
  AND  state IN ('draft', 'active')
  AND  expires_at > now()
ORDER  BY created_at ASC, id ASC;
