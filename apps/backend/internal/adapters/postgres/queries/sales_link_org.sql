-- Sales link organization routes (QL-4, spec 43 section 4.2).

-- name: CheckSalesLinkTarget :one
SELECT o.slug,
       e.id,
       COALESCE(e.slug, '')  AS event_slug,
       (s.id IS NOT NULL)    AS session_ok,
       (lp.id IS NOT NULL)   AS promoter_ok,
       COALESCE(lp.slug, '') AS promoter_slug
FROM organizations o
LEFT JOIN events e
       ON e.org_id = o.id
      AND e.deleted_at IS NULL
      AND e.id = COALESCE($2::uuid, (SELECT s0.event_id FROM sessions s0
                                      WHERE s0.id = $3::uuid AND s0.deleted_at IS NULL))
LEFT JOIN sessions s
       ON s.id = $3::uuid
      AND s.event_id = e.id
      AND s.deleted_at IS NULL
LEFT JOIN org_promoters lp
       ON lp.id = $4::uuid
      AND lp.org_id = o.id
WHERE o.id = $1
  AND o.deleted_at IS NULL;

-- name: GetSalesLinkByIDForUpdate :one
SELECT * FROM sales_links WHERE id = $1 AND org_id = $2
FOR UPDATE OF sales_links;
