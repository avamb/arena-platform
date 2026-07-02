-- +goose Up
-- =====================================================================
-- arena_new — Organization bank accounts: country column (feature #255)
--
-- The OpenAPI contract for the bank-accounts CRUD surface
-- (BankAccountItem / CreateBankAccountRequest in openapi.yaml, Wave O)
-- requires a mandatory ISO 3166-1 alpha-2 `country` field on every
-- bank-account row: the country where the account is held. Migration
-- 0048 created organization_bank_accounts without that column, so this
-- follow-up adds it.
--
-- The column is added NOT NULL with a transient DEFAULT 'ZZ' (the ISO
-- 3166-1 user-assigned "unknown or unspecified" code) so the statement
-- is safe even if rows were inserted out-of-band before the HTTP
-- surface existed; the default is dropped immediately afterwards so
-- application code must always supply an explicit country.
-- =====================================================================

ALTER TABLE organization_bank_accounts
    ADD COLUMN country char(2) NOT NULL DEFAULT 'ZZ';

ALTER TABLE organization_bank_accounts
    ALTER COLUMN country DROP DEFAULT;

COMMENT ON COLUMN organization_bank_accounts.country IS
    'ISO 3166-1 alpha-2 country code where the account is held '
    '(e.g. ''DE'', ''US''). Required by the bank-accounts API contract '
    '(feature #255).';

-- +goose Down
ALTER TABLE organization_bank_accounts DROP COLUMN IF EXISTS country;
