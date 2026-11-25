-- holds_public.sql — the public seat hold at the moment of the click (spec
-- 08_architecture/67, section 4, migration 0225). Companion hand-written gen
-- file: apps/backend/internal/adapters/postgres/gen/holds_public.sql.go
--
-- A public hold is a reservations row with hold_token_hash set. Only the
-- SHA-256 of the hold token is stored. The visitor hashes are HMAC-SHA256
-- values keyed by HOLD_VISITOR_KEY. The seats a hold held are also kept in
-- hold_policy->'seat_keys', because the expiry path deletes the
-- reservation_seats links and the pause (section 6.3) and "hold again"
-- (section 4.6) need them after the hold has ended.

-- name: LockPublicHoldByTokenHash :one
SELECT id, org_id, channel_id, session_id, quantity, state, expires_at, created_at,
       cancelled_at, expired_at, hold_token_hash, visitor_device_hash, visitor_ip_hash,
       select_deadline, payment_started_at, extensions_used, paying_until, rehold_of,
       closing_started_at, hold_policy
FROM   reservations
WHERE  hold_token_hash = $1
FOR UPDATE;

-- name: GetPublicHoldByTokenHash :one
SELECT id, org_id, channel_id, session_id, quantity, state, expires_at, created_at,
       cancelled_at, expired_at, hold_token_hash, visitor_device_hash, visitor_ip_hash,
       select_deadline, payment_started_at, extensions_used, paying_until, rehold_of,
       closing_started_at, hold_policy
FROM   reservations
WHERE  hold_token_hash = $1;

-- name: LockOpenDeviceHoldOnSession :one
-- The device's open public hold on the session, newest first, row-locked.
SELECT id, org_id, channel_id, session_id, quantity, state, expires_at, created_at,
       cancelled_at, expired_at, hold_token_hash, visitor_device_hash, visitor_ip_hash,
       select_deadline, payment_started_at, extensions_used, paying_until, rehold_of,
       closing_started_at, hold_policy
FROM   reservations
WHERE  session_id = $1
  AND  visitor_device_hash = $2
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now()
  AND  closing_started_at IS NULL
ORDER  BY created_at DESC
LIMIT  1
FOR UPDATE;

-- name: CountOpenPublicHoldsByDevice :one
SELECT count(*)::bigint
FROM   reservations
WHERE  visitor_device_hash = $1
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now();

-- name: CountOpenPublicHoldsByIP :one
SELECT count(*)::bigint
FROM   reservations
WHERE  visitor_ip_hash = $1
  AND  state IN ('draft', 'active')
  AND  hold_token_hash IS NOT NULL
  AND  expires_at > now();

-- name: HoldAdvisoryXactLock :exec
SELECT pg_advisory_xact_lock($1::bigint);

-- name: PublicHoldHasOpenOrder :one
SELECT EXISTS (
    SELECT 1 FROM orders
    WHERE  reservation_id = $1
      AND  status = 'pending_payment'
);

-- name: GetPublicHoldCheckoutToken :one
SELECT cs.checkout_token
FROM   orders o
JOIN   checkout_sessions cs ON cs.id = o.checkout_session_id
WHERE  o.reservation_id = $1
  AND  o.status = 'pending_payment'
ORDER  BY o.created_at DESC
LIMIT  1;

-- name: ListPausedSeatKeysByDevice :many
SELECT k.seat_key, max(r.expired_at)::timestamptz AS expired_at
FROM   reservations r,
       jsonb_array_elements_text(COALESCE(r.hold_policy -> 'seat_keys', '[]'::jsonb)) AS k(seat_key)
WHERE  r.state = 'expired'
  AND  r.visitor_device_hash = $1
  AND  r.session_id = $2
  AND  r.expired_at > $3
GROUP  BY k.seat_key;

-- name: ListPausedSeatKeysByIP :many
SELECT k.seat_key, max(r.expired_at)::timestamptz AS expired_at
FROM   reservations r,
       jsonb_array_elements_text(COALESCE(r.hold_policy -> 'seat_keys', '[]'::jsonb)) AS k(seat_key)
WHERE  r.state = 'expired'
  AND  r.visitor_ip_hash = $1
  AND  r.session_id = $2
  AND  r.expired_at > $3
GROUP  BY k.seat_key;

-- name: GetInheritableSelectDeadline :one
-- The select deadline and extensions of the device's latest public hold on
-- the session that closed (cancelled or expired) after $3 and whose select
-- deadline is still ahead (section 6.3, "remove all and select again").
SELECT select_deadline, extensions_used
FROM   reservations
WHERE  session_id = $1
  AND  visitor_device_hash = $2
  AND  hold_token_hash IS NOT NULL
  AND  state IN ('cancelled', 'expired')
  AND  COALESCE(cancelled_at, expired_at) > $3
  AND  select_deadline > now()
ORDER  BY COALESCE(cancelled_at, expired_at) DESC
LIMIT  1;

-- name: CountReholdsOf :one
SELECT count(*)::bigint FROM reservations WHERE rehold_of = $1;

-- name: SetPublicHoldFields :exec
UPDATE reservations
SET    hold_token_hash     = $2,
       visitor_device_hash = $3,
       visitor_ip_hash     = $4,
       select_deadline     = $5,
       extensions_used     = $6,
       rehold_of           = $7,
       hold_policy         = $8::jsonb,
       updated_at          = now()
WHERE  id = $1;

-- name: SetPublicHoldSeatKeys :exec
UPDATE reservations
SET    hold_policy = jsonb_set(COALESCE(hold_policy, '{}'::jsonb), '{seat_keys}', to_jsonb($2::text[])),
       updated_at  = now()
WHERE  id = $1;

-- name: ExtendPublicHold :one
UPDATE reservations
SET    expires_at      = $2,
       extensions_used = extensions_used + 1,
       select_deadline = CASE WHEN $3::boolean THEN $2 ELSE select_deadline END,
       updated_at      = now()
WHERE  id = $1
  AND  state IN ('draft', 'active')
RETURNING id, org_id, channel_id, session_id, quantity, state, expires_at, created_at,
          cancelled_at, expired_at, hold_token_hash, visitor_device_hash, visitor_ip_hash,
          select_deadline, payment_started_at, extensions_used, paying_until, rehold_of,
          closing_started_at, hold_policy;

-- name: SetPendingOrderExpiryForReservation :execrows
UPDATE orders
SET    expires_at = $2,
       updated_at = now()
WHERE  reservation_id = $1
  AND  status = 'pending_payment';
