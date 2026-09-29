-- promoters.sql — promoters of an organization's events (migration 0113).
-- Wrappers are hand-maintained in gen/promoters.sql.go.

-- name: ListOrgPromoters :many
SELECT id, org_id, name, legal_id, phone, email, slug, archived_at, created_at, updated_at
FROM   org_promoters
WHERE  org_id = $1
  AND  ($2::boolean OR archived_at IS NULL)
ORDER  BY lower(name), id;

-- name: GetOrgPromoter :one
SELECT id, org_id, name, legal_id, phone, email, slug, archived_at, created_at, updated_at
FROM   org_promoters
WHERE  id = $1
  AND  org_id = $2;

-- name: InsertOrgPromoter :one
INSERT INTO org_promoters (org_id, name, legal_id, phone, email, slug)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, org_id, name, legal_id, phone, email, slug, archived_at, created_at, updated_at;

-- name: UpdateOrgPromoter :one
UPDATE org_promoters
SET    name        = $3,
       legal_id    = $4,
       phone       = $5,
       email       = $6,
       archived_at = CASE WHEN $7::boolean THEN COALESCE(archived_at, now()) ELSE NULL END,
       slug        = $8,
       updated_at  = now()
WHERE  id = $1
  AND  org_id = $2
RETURNING id, org_id, name, legal_id, phone, email, slug, archived_at, created_at, updated_at;

-- name: PromoterSlugTaken :one
-- A promoter slug shares its namespace with organization slugs (the public
-- resolver tries the organization first), so both tables are checked.
SELECT EXISTS (SELECT 1 FROM org_promoters WHERE lower(slug) = lower($1))
    OR EXISTS (SELECT 1 FROM organizations WHERE lower(slug) = lower($1) AND deleted_at IS NULL);

-- name: SetEventPromoter :exec
INSERT INTO event_promoters (event_id, org_id, promoter_id)
VALUES ($1, $2, $3)
ON CONFLICT (event_id) DO UPDATE
SET    promoter_id = EXCLUDED.promoter_id,
       org_id      = EXCLUDED.org_id,
       updated_at  = now();

-- name: DeleteEventPromoter :exec
DELETE FROM event_promoters WHERE event_id = $1 AND org_id = $2;

-- name: ListEventPromoters :many
SELECT ep.event_id, p.id, p.name
FROM   event_promoters ep
JOIN   org_promoters   p ON p.id = ep.promoter_id
WHERE  ep.event_id = ANY($1::uuid[]);
