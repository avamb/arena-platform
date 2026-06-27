-- payment_provider_configs.sql — sqlc query definitions for the
-- payment_provider_configs table (feature #237).
--
-- All write queries are scoped by org_id to enforce owner-gated mutation
-- policy. All queries filter WHERE deleted_at IS NULL to respect the
-- soft-delete policy.

-- name: InsertPaymentProviderConfig :one
INSERT INTO payment_provider_configs (
    org_id, provider, mode, provider_account_id,
    public_config, secrets, status, is_active
)
VALUES (
    $1, $2, $3, $4,
    COALESCE($5::jsonb, '{}'::jsonb),
    COALESCE($6::jsonb, '{}'::jsonb),
    $7, $8
)
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at;

-- name: GetPaymentProviderConfigByID :one
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at
FROM   payment_provider_configs
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: ListPaymentProviderConfigsByOrg :many
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at
FROM   payment_provider_configs
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY provider ASC, mode ASC, created_at ASC, id ASC;

-- name: UpdatePaymentProviderConfig :one
UPDATE payment_provider_configs
SET    provider_account_id = CASE WHEN $3::text  IS NOT NULL THEN $3::text  ELSE provider_account_id END,
       public_config       = CASE WHEN $4::jsonb IS NOT NULL THEN $4::jsonb ELSE public_config END,
       secrets             = CASE WHEN $5::jsonb IS NOT NULL THEN $5::jsonb ELSE secrets END,
       status              = COALESCE(NULLIF($6, ''), status),
       is_active           = COALESCE($7, is_active),
       updated_at          = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at;

-- name: SoftDeletePaymentProviderConfig :one
UPDATE payment_provider_configs
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at;
