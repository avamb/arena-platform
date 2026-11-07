-- name: ListTestOnlyEventIDsByOrg :many
-- ListTestOnlyEventIDsByOrg returns the published events of an organization
-- whose publications (event_publications -> agent_feed_tokens -> sales_channels)
-- all sit in TEST channels (sales_channels.is_test, migration 0202, spec 62
-- 6.3). An event with no publication row at all is NOT returned (it keeps the
-- historical "listed" behaviour). The Bil24 gateway removes these events from
-- the catalogue of every non-test channel.
SELECT e.id
FROM   events e
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  EXISTS (
        SELECT 1
        FROM   event_publications ep
        JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
        JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
        WHERE  ep.event_id = e.id
          AND  sc.deleted_at IS NULL
          AND  sc.is_test = true)
  AND  NOT EXISTS (
        SELECT 1
        FROM   event_publications ep
        JOIN   agent_feed_tokens ft ON ft.id = ep.feed_token_id
        JOIN   sales_channels sc ON sc.id = ft.sales_channel_id
        WHERE  ep.event_id = e.id
          AND  sc.deleted_at IS NULL
          AND  sc.is_test = false)
ORDER BY e.id;
