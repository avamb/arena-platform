-- hold_close.sql — the queries behind closing the provider payment page of an
-- expired seat hold (spec 08_architecture/67 section 5.2), the hold-aware
-- reservation sweep, the late-payment window bounds and the visitor hash
-- purge (spec 67 section 6.1). Migration 0225.

-- name: GetExpiredReservationsHoldAware :many
-- reservation.expire_sweep's candidates: like GetExpiredReservations, but a
-- reservation with an open payment attempt whose provider page is still alive
-- (payment_intents.page_expires_at + grace $2 seconds in the future) is left
-- to hold.close_payment until that hard limit. A hold of the click with an
-- open attempt of a page-closing provider ($3) whose page_expires_at was never
-- recorded is left to it too, up to expires_at + the longest page of its
-- policy (at least $4 seconds) + grace. A row of the old path is a candidate
-- exactly as before.
SELECT r.id, r.org_id, r.channel_id, r.session_id, r.tier_id, r.user_id, r.quantity, r.state,
       r.expires_at, r.created_at, r.updated_at, r.cancelled_at, r.converted_at, r.expired_at
FROM   reservations r
WHERE  r.state IN ('draft', 'active')
  AND  r.expires_at < now()
  AND  NOT EXISTS (
           SELECT 1
           FROM   checkout_sessions cs
           JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
           WHERE  cs.reservation_id = r.id
             AND  pi.state IN ('created', 'requires_action')
             AND  (
                      (pi.page_expires_at IS NOT NULL
                       AND pi.page_expires_at + make_interval(secs => $2::float8) > now())
                   OR (pi.page_expires_at IS NULL
                       AND r.hold_token_hash IS NOT NULL
                       AND pi.provider = ANY($3::text[])
                       AND pi.provider_payment_id IS NOT NULL
                       AND r.expires_at
                           + make_interval(secs => GREATEST(
                                 $4::float8,
                                 COALESCE((r.hold_policy ->> 'payment_seconds')::float8, 0)
                                 + COALESCE((r.hold_policy ->> 'extend_seconds')::float8, 0)
                                   * COALESCE((r.hold_policy ->> 'extend_count')::float8, 0)
                                 + COALESCE((r.hold_policy ->> 'paying_seconds')::float8, 0)))
                           + make_interval(secs => $2::float8) > now())
                  )
       )
ORDER BY r.expires_at ASC
LIMIT  $1
FOR UPDATE OF r SKIP LOCKED;

-- name: ListHoldCloseCandidates :many
-- hold.close_payment's candidates: holds of the click path (hold_token_hash)
-- past expires_at, not paying, not being closed by a live claim (a mark older
-- than $2 is stale), with an open attempt of a provider in $1 that has a
-- provider session. Oldest expires_at first.
SELECT r.id, r.expires_at
FROM   reservations r
WHERE  r.state IN ('draft', 'active')
  AND  r.hold_token_hash IS NOT NULL
  AND  r.expires_at < now()
  AND  (r.paying_until IS NULL OR r.paying_until <= now())
  AND  (r.closing_started_at IS NULL OR r.closing_started_at < $2)
  AND  EXISTS (
           SELECT 1
           FROM   checkout_sessions cs
           JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
           WHERE  cs.reservation_id = r.id
             AND  pi.state IN ('created', 'requires_action')
             AND  pi.provider = ANY($1::text[])
             AND  pi.provider_payment_id IS NOT NULL
       )
ORDER BY r.expires_at ASC
LIMIT  $3;

-- name: ClaimHoldForClose :one
-- Step 1 of section 5.2 in one statement: lock the reservation row
-- (SKIP LOCKED), re-check that it is open, past expires_at, not paying and
-- not claimed by a live mark, and write closing_started_at. No row: skip.
WITH c AS (
    SELECT id
    FROM   reservations
    WHERE  id = $1
      AND  state IN ('draft', 'active')
      AND  expires_at < now()
      AND  (paying_until IS NULL OR paying_until <= now())
      AND  (closing_started_at IS NULL OR closing_started_at < $2)
    FOR UPDATE SKIP LOCKED
)
UPDATE reservations r
SET    closing_started_at = now()
FROM   c
WHERE  r.id = c.id
RETURNING r.closing_started_at, r.expires_at;

-- name: ClearHoldCloseMark :exec
-- Takes the claim back (payment in progress, provider did not answer, page
-- still open): only the mark this claim wrote.
UPDATE reservations
SET    closing_started_at = NULL
WHERE  id = $1
  AND  closing_started_at = $2;

-- name: ListOpenPageIntents :many
-- The open attempts of a reservation whose provider page can be closed.
SELECT pi.id, pi.provider, pi.provider_payment_id, cs.id, cs.org_id, cs.channel_id
FROM   checkout_sessions cs
JOIN   payment_intents pi ON pi.checkout_session_id = cs.id
WHERE  cs.reservation_id = $1
  AND  pi.state IN ('created', 'requires_action')
  AND  pi.provider = ANY($2::text[])
  AND  pi.provider_payment_id IS NOT NULL
ORDER BY pi.created_at ASC;

-- name: GetLatePaymentWindowBounds :one
-- The two later ends of the late-payment window (spec 67 section 5.2): the
-- reservation's paying_until and the attempt's page_expires_at.
SELECT (SELECT paying_until    FROM reservations    WHERE id = $2),
       (SELECT page_expires_at FROM payment_intents WHERE id = $1);

-- name: PurgeVisitorHashes :execrows
-- Spec 67 section 6.1: the visitor hashes of a hold are erased 24 hours
-- after the hold closed ($1 = now - 24 h), at most $2 rows per call.
UPDATE reservations
SET    visitor_device_hash = NULL,
       visitor_ip_hash     = NULL
WHERE  id IN (
           SELECT id
           FROM   reservations
           WHERE  (visitor_device_hash IS NOT NULL OR visitor_ip_hash IS NOT NULL)
             AND  state IN ('expired', 'cancelled', 'converted')
             AND  COALESCE(expired_at, cancelled_at, converted_at, updated_at) < $1
           LIMIT  $2
       );
