-- orgs.sql — sqlc query definitions for the organizations table (feature #119).
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertOrganization :one
INSERT INTO organizations (name, slug, country, default_locale, reservation_ttl_seconds)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at;

-- name: GetOrganizationByID :one
SELECT id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at
FROM   organizations
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetOrganizationBySlug :one
SELECT id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at
FROM   organizations
WHERE  slug = $1
  AND  deleted_at IS NULL;

-- name: ListOrganizations :many
SELECT id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: UpdateOrganization :one
UPDATE organizations
SET    name                    = COALESCE(NULLIF($2, ''), name),
       slug                    = COALESCE(NULLIF($3, ''), slug),
       country                 = COALESCE(NULLIF($4, ''), country),
       default_locale          = COALESCE(NULLIF($5, ''), default_locale),
       reservation_ttl_seconds = CASE WHEN $6::integer > 0 THEN $6::integer ELSE reservation_ttl_seconds END,
       updated_at              = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at;

-- name: SoftDeleteOrganization :one
UPDATE organizations
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, name, slug, country, default_locale, reservation_ttl_seconds, created_at, updated_at, deleted_at;
