-- sales_link_facts.sql — the rows the sales-link redirect looks at (spec 43
-- section 4.3, task QL-2). Read-only, uses existing tables only.

-- name: GetSalesLinkTargetFacts :one
-- GetSalesLinkTargetFacts returns, for the organization $1 and the optional
-- targets $2 (event), $3 (session) and $4 (promoter of the LINK), everything the
-- fall chain needs: the organization slug, whether the event is published and
-- not deleted and its slug, the promoter of the EVENT (slug, archived), whether
-- the session is alive (not deleted, not cancelled, of that event) and the
-- slug and archived flag of the link own promoter. A target of another
-- organization simply produces empty facts. $2 may be NULL when $3 is given:
-- the event is then the session event. pgx.ErrNoRows when the organization
-- does not exist or is deleted.
SELECT o.slug,
       COALESCE(e.slug, '')                                              AS event_slug,
       COALESCE(e.status = 'published' AND e.deleted_at IS NULL, false)  AS event_published,
       COALESCE(ep.slug, '')                                             AS event_promoter_slug,
       COALESCE(ep.archived_at IS NOT NULL, false)                       AS event_promoter_archived,
       (s.id IS NOT NULL)                                                AS session_live,
       COALESCE(lp.slug, '')                                             AS promoter_slug,
       COALESCE(lp.archived_at IS NOT NULL, false)                       AS promoter_archived
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0 WHERE s0.id = $3::uuid))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
      AND s.status <> 'cancelled'
LEFT JOIN event_promoters epr
       ON epr.event_id = e.id
      AND epr.org_id = o.id
LEFT JOIN org_promoters ep
       ON ep.id = epr.promoter_id
      AND ep.org_id = o.id
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL