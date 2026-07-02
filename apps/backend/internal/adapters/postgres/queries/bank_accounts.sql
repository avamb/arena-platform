-- bank_accounts.sql — sqlc query definitions for the
-- organization_bank_accounts table (feature #255).
--
-- Column mapping between the table (migration 0048 + 0056) and the API
-- contract (openapi.yaml BankAccountItem):
--
--   label      → bank_name   (free-form bank name for operator display)
--   bic_swift  → bic         (BIC / SWIFT code)
--   is_default → is_primary  (exactly one primary per org, enforced by
--                             the partial unique index
--                             organization_bank_accounts_one_default_per_org)
--
-- All write queries are scoped by org_id to enforce owner-gated mutation
-- policy. All queries filter WHERE deleted_at IS NULL to respect the
-- soft-delete policy.

-- name: InsertOrganizationBankAccount :one
INSERT INTO organization_bank_accounts (
    org_id, label, holder_name, iban, bic_swift,
    account_number, routing_number, currency, country, is_default
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, org_id, label AS bank_name, holder_name, iban, bic_swift AS bic, account_number, routing_number, currency, country, is_default AS is_primary, created_at, updated_at, deleted_at;

-- name: GetOrganizationBankAccountByID :one
SELECT id, org_id, label AS bank_name, holder_name, iban, bic_swift AS bic, account_number, routing_number, currency, country, is_default AS is_primary, created_at, updated_at, deleted_at
FROM   organization_bank_accounts
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: ListOrganizationBankAccountsByOrg :many
SELECT id, org_id, label AS bank_name, holder_name, iban, bic_swift AS bic, account_number, routing_number, currency, country, is_default AS is_primary, created_at, updated_at, deleted_at
FROM   organization_bank_accounts
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY is_default DESC, created_at ASC, id ASC;

-- name: UpdateOrganizationBankAccount :one
UPDATE organization_bank_accounts
SET    label          = $3,
       holder_name    = $4,
       iban           = $5,
       bic_swift      = $6,
       account_number = $7,
       routing_number = $8,
       currency       = $9,
       country        = $10,
       is_default     = $11,
       updated_at     = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, label AS bank_name, holder_name, iban, bic_swift AS bic, account_number, routing_number, currency, country, is_default AS is_primary, created_at, updated_at, deleted_at;

-- name: SoftDeleteOrganizationBankAccount :one
UPDATE organization_bank_accounts
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, org_id, label AS bank_name, holder_name, iban, bic_swift AS bic, account_number, routing_number, currency, country, is_default AS is_primary, created_at, updated_at, deleted_at;

-- name: DemoteOrganizationBankAccountDefault :exec
UPDATE organization_bank_accounts
SET    is_default = false,
       updated_at = now()
WHERE  org_id = $1
  AND  is_default
  AND  deleted_at IS NULL
  AND  id <> $2;

-- name: CountOtherActiveOrganizationBankAccounts :one
SELECT count(*)
FROM   organization_bank_accounts
WHERE  org_id = $1
  AND  deleted_at IS NULL
  AND  id <> $2;
