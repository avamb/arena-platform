-- Public venue addresses for the hosted page and single-event feed.
-- Hand-maintained wrapper: gen/venues_public.sql.go.
-- name: ListEventVenueDetails :many
SELECT s.event_id,
       v.id,
       v.name,
       v.address_line1,
       v.address_line2,
       v.postal_code,
       v.address,
       v.country,
       v.timezone,
       t_en.value AS city_name,
       array_agg(s.start_at ORDER BY s.start_at) AS starts
FROM sessions s
JOIN venues v ON v.id = s.venue_id
LEFT JOIN cities ci ON ci.id = v.city_id
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
    AND t_en.key = ci.slug
    AND t_en.locale = 'en'
WHERE s.event_id = ANY($1::uuid[])
  AND s.deleted_at IS NULL
  AND s.status IN ('scheduled', 'completed')
GROUP BY s.event_id, v.id, v.name, v.address_line1, v.address_line2,
         v.postal_code, v.address, v.country, v.timezone, t_en.value
ORDER BY s.event_id, min(s.start_at), v.name;
