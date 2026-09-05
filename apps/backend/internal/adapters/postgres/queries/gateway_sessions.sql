-- gateway_sessions.sql — sqlc query definitions for the Bil24-compat
-- gateway session table introduced by migration 0091 (W1-A4a,
-- feature #479).
--
-- A gateway_session is the persistent Bil24 sessionId cookie a WordPress
-- site keeps for a customer within one (org, channel). session_token is
-- 43 base64url chars (32 random bytes) minted by the shim in the
-- bil24compat adapter. Expiry beyond expires_at causes the gateway to
-- return resultCode=1 (session expired) so the site re-creates the
-- session (spec §5 / §6).

-- name: InsertGatewaySession :one
-- Creates a session row. Callers pre-compute the token (32 bytes
-- crypto/rand → base64url) and expiry. promo_codes is optional and
-- normally starts empty; ADD_PROMO_CODES appends to it via
-- AppendGatewaySessionPromoCode.
INSERT INTO gateway_sessions
    (session_token, customer_id, org_id, channel_id, locale, promo_codes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, session_token, customer_id, org_id, channel_id, locale,
          promo_codes, created_at, last_seen_at, expires_at;

-- name: GetGatewaySessionByToken :one
-- Loads a session by its wire token. Returns pgx.ErrNoRows when the
-- token is unknown; callers translate that (plus "expired") to
-- resultCode=1 per spec §6.
SELECT id, session_token, customer_id, org_id, channel_id, locale,
       promo_codes, created_at, last_seen_at, expires_at
FROM   gateway_sessions
WHERE  session_token = $1;

-- name: GetGatewaySessionByID :one
-- Loads a session by platform uuid. Used by admin/debug surfaces —
-- production traffic always keys off session_token.
SELECT id, session_token, customer_id, org_id, channel_id, locale,
       promo_codes, created_at, last_seen_at, expires_at
FROM   gateway_sessions
WHERE  id = $1;

-- name: TouchGatewaySession :exec
-- Bumps last_seen_at on a session after a successful wire dispatch.
-- Does NOT extend expires_at — expiry is anchored at creation time so
-- the site rotates its cookie predictably.
UPDATE gateway_sessions
SET    last_seen_at = now()
WHERE  id = $1;

-- name: ExtendGatewaySessionExpiry :exec
-- Advances expires_at (and last_seen_at) for the rare case where the
-- gateway needs to refresh a still-active session (spec §5). Callers
-- pass the desired expires_at explicitly to keep TTL policy in Go.
UPDATE gateway_sessions
SET    expires_at   = $2,
       last_seen_at = now()
WHERE  id = $1;

-- name: AppendGatewaySessionPromoCode :exec
-- Appends a promo code to gateway_sessions.promo_codes if it is not
-- already present. Idempotent — spec §7 (ADD_PROMO_CODES) treats an
-- already-registered code as "exist" not an error.
UPDATE gateway_sessions
SET    promo_codes = ARRAY(SELECT DISTINCT unnest(promo_codes || $2::text[]))
WHERE  id = $1;

-- name: DeleteExpiredGatewaySessions :exec
-- Reaper query: drops rows whose expires_at is in the past. Intended
-- for a periodic housekeeping job; keeps the reservations FK healthy
-- because reservations.gateway_session_id is ON DELETE SET NULL.
DELETE FROM gateway_sessions
WHERE expires_at < now();
