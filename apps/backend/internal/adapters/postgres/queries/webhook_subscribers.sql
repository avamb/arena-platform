-- webhook_subscribers.sql — sqlc queries for webhook subscriber management.
--
-- Feature #156 — WordPress webhook receiver / subscriber registration.
-- These queries back the POST /v1/webhooks/subscribers (register),
-- GET /v1/webhooks/subscribers (list), and DELETE /v1/webhooks/subscribers/{id}
-- (deregister) platform endpoints.

-- name: CreateWebhookSubscriber :one
-- Register a new webhook subscriber endpoint.
-- Returns the full row including the generated id and signing_secret.
INSERT INTO webhook_subscribers (
    site_url,
    callback_url,
    signing_secret,
    event_types,
    active
) VALUES (
    $1, -- site_url
    $2, -- callback_url
    $3, -- signing_secret (caller-generated random hex)
    $4, -- event_types (TEXT[], empty = wildcard)
    TRUE
)
RETURNING *;

-- name: ListActiveWebhookSubscribers :many
-- Return all active subscribers, ordered by creation time.
-- Used by the webhook dispatcher to build the fan-out list at runtime.
SELECT *
FROM   webhook_subscribers
WHERE  active = TRUE
ORDER  BY created_at ASC;

-- name: GetWebhookSubscriberByID :one
-- Retrieve a single subscriber by primary key.
SELECT *
FROM   webhook_subscribers
WHERE  id = $1;

-- name: DeactivateWebhookSubscriber :one
-- Soft-delete a subscriber by setting active = FALSE.
-- Returns the updated row so callers can confirm the deactivation.
UPDATE webhook_subscribers
SET    active     = FALSE,
       updated_at = NOW()
WHERE  id = $1
RETURNING *;

-- name: UpdateWebhookSubscriberEventTypes :one
-- Replace the event_types filter for an existing subscriber.
UPDATE webhook_subscribers
SET    event_types = $2,
       updated_at  = NOW()
WHERE  id = $1
RETURNING *;

-- ─────────────────────────────────────────────────────────────────────────────
-- W1-B7c (feature #506, spec §9.2) — bil24_wp subscriber routing.
--
-- The WordPress sites subscribe PER SALES CHANNEL (migration 0094), so the
-- dispatcher routes an order/ticket event by the order's channel and a catalog
-- event by every channel the event is published to.
-- ─────────────────────────────────────────────────────────────────────────────

-- name: GetWPSubscriberByChannel :one
-- The single active bil24_wp subscriber of one sales channel
-- (uq_webhook_subscribers_bil24_wp_per_channel guarantees at most one).
SELECT id, channel_id, callback_url, signing_secret
FROM   webhook_subscribers
WHERE  channel_id = $1
  AND  kind       = 'bil24_wp'
  AND  active     = TRUE;

-- name: ListWPSubscribersForEvent :many
-- Every active bil24_wp subscriber whose sales channel carries a publication
-- of the given event: event_publications -> agent_feed_tokens -> channel.
-- Revoked feed tokens do not carry a live site and are excluded.
SELECT DISTINCT ws.id, ws.channel_id, ws.callback_url, ws.signing_secret
FROM   webhook_subscribers ws
JOIN   agent_feed_tokens   aft ON aft.sales_channel_id = ws.channel_id
JOIN   event_publications  ep  ON ep.feed_token_id     = aft.id
WHERE  ep.event_id  = $1
  AND  ws.kind      = 'bil24_wp'
  AND  ws.active    = TRUE
  AND  aft.is_active = TRUE
  AND  aft.revoked_at IS NULL
ORDER  BY ws.id;

-- name: SetWebhookSubscriberActive :one
-- Toggle the active flag for an existing subscriber.
-- Used by the SuperAdmin webhooks UI (Feature #294 S-3) to re-activate
-- a previously deactivated subscriber or to deactivate via the PATCH
-- route. The DeactivateWebhookSubscriber query above remains the
-- canonical soft-delete operation invoked by the DELETE handler.
UPDATE webhook_subscribers
SET    active     = $2,
       updated_at = NOW()
WHERE  id = $1
RETURNING *;
