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
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error;

-- name: GetPaymentProviderConfigByID :one
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: GetPaymentProviderConfigByIDUnscoped :one
-- Org-LESS lookup by primary key, for the per-config payment webhook route
-- POST /v1/payment-intents/webhook/{config_id}. The caller there is the
-- payment provider, which has no arena identity and cannot supply an org —
-- the config id in the path IS the routing key, and the org it belongs to is
-- what this read establishes. A single-row primary-key read, so an
-- unauthenticated caller cannot make it expensive.
--
-- Every other reader must keep using the org-scoped GetPaymentProviderConfigByID.
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListPaymentProviderConfigsByOrg :many
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
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
       -- Changing the credential invalidates what the provider said about
       -- the OLD one. Anything else would leave a green "verified" badge
       -- standing over a key nobody has ever tried.
       verification_status = CASE WHEN $5::jsonb IS NOT NULL THEN 'unverified' ELSE verification_status END,
       verified_at         = CASE WHEN $5::jsonb IS NOT NULL THEN NULL         ELSE verified_at         END,
       verification_error  = CASE WHEN $5::jsonb IS NOT NULL THEN NULL         ELSE verification_error  END,
       updated_at          = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error;

-- name: SoftDeletePaymentProviderConfig :one
UPDATE payment_provider_configs
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error;

-- name: SetPaymentProviderConfigVerification :one
-- Records what the provider answered when we last sent it this credential.
--
-- Separate from UpdatePaymentProviderConfig because it is a different kind of
-- write: it touches no configuration the operator entered, only the verdict
-- on it, and it must be callable on its own (the "check again" button) as
-- well as right after a save. It deliberately does NOT bump updated_at —
-- that column means "an operator changed this row", and a verification is
-- not an edit.
UPDATE payment_provider_configs
SET    verification_status = $3,
       verified_at         = CASE WHEN $3 = 'ok' THEN now() ELSE verified_at END,
       verification_error  = NULLIF($4, '')
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error;
