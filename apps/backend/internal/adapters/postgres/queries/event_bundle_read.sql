-- event_bundle_read.sql — queries behind GET
-- /v1/organizations/{org_id}/events/{event_id}/event-bundle (task 3-2).
-- Companion hand-written gen file: gen/event_bundle_read.sql.go

-- name: ListEventPublicationChannelIDs :many
-- The sales channels of the organization the event is published into
-- through a feed token that can still carry the publication (active and not
-- revoked: the same predicate ensureChannelFeedToken and the WordPress
-- fan-out use), oldest publication first. A deleted channel is left out.
SELECT sc.id
FROM   event_publications ep
JOIN   agent_feed_tokens  aft ON aft.id = ep.feed_token_id
JOIN   sales_channels     sc  ON sc.id  = aft.sales_channel_id
WHERE  ep.event_id = $1
  AND  sc.org_id   = $2
  AND  sc.deleted_at IS NULL
  AND  aft.is_active
  AND  aft.revoked_at IS NULL
GROUP  BY sc.id
ORDER  BY MIN(ep.published_at), sc.id;
