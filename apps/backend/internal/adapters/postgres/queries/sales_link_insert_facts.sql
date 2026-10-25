-- sales_link_insert_facts.sql — the names the printable insert of a sales link
-- shows (spec 43 section 6.5, task QL-13). Read-only, existing tables only.

-- name: GetSalesLinkInsertFacts :one
-- GetSalesLinkInsertFacts returns, for the organization $1 and the optional
-- targets $2 (event), $3 (session) and $4 (promoter of the LINK), what the
-- insert prints: the organization name, logo media id and default locale, the
-- event name (of $2, or of the session's event when only $3 is given), the
-- session start with its venue's time zone, and the promoter name. A target of
-- another organization, a deleted event or a deleted session simply produce
-- empty values (the insert then falls back to the organization name).
-- pgx.ErrNoRows when the organization does not exist or is deleted.
SELECT o.name                                   AS org_name,
       o.logo_media_id                          AS org_logo_media_id,
       o.default_locale                         AS org_default_locale,
       COALESCE(e.name, '')                     AS event_name,
       s.start_at                               AS session_start_at,
       COALESCE(v.timezone, '')                 AS venue_timezone,
       COALESCE(lp.name, '')                    AS promoter_name
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.deleted_at IS NULL
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0 WHERE s0.id = $3::uuid))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
LEFT JOIN venues v
       ON v.id = s.venue_id
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL
