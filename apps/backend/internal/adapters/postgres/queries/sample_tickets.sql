-- sample_tickets.sql — the sample e-ticket of a session (migration 0119).
-- GET /v1/organizations/{org_id}/sessions/{session_id}/sample-ticket renders
-- the buyer's layout for an organizer with a real, scannable code that the
-- gate recognises as a sample.

-- name: GetSessionSampleBarcode :one
SELECT session_id, barcode_id, ean13, created_at
FROM   session_sample_barcodes
WHERE  session_id = $1;

-- name: InsertSessionSampleBarcode :execrows
-- Records the session's sample code once; a concurrent second request loses
-- the race quietly (0 rows) and re-reads the winner's code.
INSERT INTO session_sample_barcodes (session_id, barcode_id, ean13)
VALUES ($1, $2, $3)
ON CONFLICT (session_id) DO NOTHING;

-- name: GetSampleTicketPresentation :one
-- Everything the sample PDF prints, scoped to the organization: the event,
-- the session on its venue's clock, the poster (session override first,
-- migration 0082), the promoter's name for the footer and the seller's
-- branding. Every join is a LEFT JOIN so a session without a venue or a
-- poster still renders.
SELECT s.id                                    AS session_id,
       e.id                                    AS event_id,
       e.name                                  AS event_name,
       s.start_at,
       v.name                                  AS venue_name,
       COALESCE(NULLIF(btrim(v.address_line1), ''),
                NULLIF(btrim(v.address),       '')) AS venue_address,
       COALESCE(t_en.value, ci.slug)           AS venue_city,
       v.timezone                              AS venue_timezone,
       COALESCE(s.poster_media_id, e.poster_media_id) AS poster_media_id,
       epr.name                                AS promoter_name,
       o.name                                  AS org_name,
       o.default_locale                        AS org_locale,
       o.website_url, o.legal_name, o.legal_address_line1, o.legal_address_line2,
       o.legal_address_postal_code, o.legal_address_city, o.legal_address_country,
       o.contact_email, o.logo_media_id,
       s.doors_open_at
FROM      sessions s
JOIN      events        e    ON e.id = s.event_id
JOIN      organizations o    ON o.id = e.org_id
LEFT JOIN venues        v    ON v.id = s.venue_id
LEFT JOIN cities        ci   ON ci.id = v.city_id
LEFT JOIN i18n_text     t_en ON t_en.namespace = 'geo.cities'
       AND t_en.key = ci.slug AND t_en.locale = 'en'
LEFT JOIN event_promoters ep  ON ep.event_id = e.id
LEFT JOIN org_promoters   epr ON epr.id = ep.promoter_id
WHERE  s.id = $1
  AND  e.org_id = $2
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;
