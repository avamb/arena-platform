-- channels.sql — sqlc query definitions for the sales_channels table
-- (features #121 and #236). All queries filter WHERE deleted_at IS NULL
-- to respect the soft-delete policy.

-- name: InsertSalesChannel :one
INSERT INTO sales_channels (org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8::jsonb, '{}'::jsonb))
RETURNING id, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;

-- name: GetSalesChannelByID :one
SELECT id, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at
FROM   sales_channels
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: ListSalesChannelsByOrg :many
SELECT id, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at
FROM   sales_channels
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: UpdateSalesChannel :one
UPDATE sales_channels
SET    name                     = COALESCE(NULLIF($3, ''), name),
       payment_mode             = COALESCE(NULLIF($4, ''), payment_mode),
       provider                 = COALESCE(NULLIF($5, ''), provider),
       provider_account_id      = CASE WHEN $6::text IS NOT NULL THEN $6::text ELSE provider_account_id END,
       fee_percent              = CASE WHEN $7::numeric IS NOT NULL THEN $7::numeric ELSE fee_percent END,
       reservation_ttl_override = $8,
       settings                 = CASE WHEN $9::jsonb IS NOT NULL THEN $9::jsonb ELSE settings END,
       updated_at               = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;

-- name: SoftDeleteSalesChannel :one
UPDATE sales_channels
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;
