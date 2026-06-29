-- +goose Up
-- =====================================================================
-- arena_new — Organization bank accounts (feature O-2 / #254)
--
-- Child table of organizations holding bank-account metadata used for
-- legal / billing display purposes. NO FX or settlement logic lives
-- here — Stripe Connect / AllPay payout configuration continues to be
-- held in payment_provider_configs. This table is metadata only:
--
--   * label             — operator-supplied human label
--   * holder_name       — legal account holder (required)
--   * iban / bic_swift  — EU / international identifiers (optional)
--   * account_number /
--     routing_number    — US-style identifiers (optional)
--   * currency          — ISO 4217 alpha-3 (CHAR(3) NOT NULL)
--   * is_default        — at most one default account per org via a
--                         partial unique index that excludes soft-deleted
--                         rows.
--
-- Soft-delete:
--   * deleted_at IS NULL for active rows. The partial unique index on
--     (org_id) WHERE is_default AND deleted_at IS NULL guarantees an
--     org has at most one default account at any time.
-- =====================================================================

CREATE TABLE organization_bank_accounts (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    label           text,
    holder_name     text        NOT NULL,
    iban            text,
    bic_swift       text,
    account_number  text,
    routing_number  text,
    currency        char(3)     NOT NULL,
    is_default      boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);

-- Partial unique index: at most one default bank account per org for
-- active (non-soft-deleted) rows.
CREATE UNIQUE INDEX organization_bank_accounts_one_default_per_org
    ON organization_bank_accounts (org_id)
    WHERE is_default AND deleted_at IS NULL;

-- List active accounts for an org quickly.
CREATE INDEX organization_bank_accounts_org_active
    ON organization_bank_accounts (org_id)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE organization_bank_accounts IS
    'Per-organization bank-account metadata used for legal / billing '
    'display. NO FX or settlement logic; Stripe Connect / AllPay '
    'payout configuration remains in payment_provider_configs. '
    'Feature O-2 (#254).';

COMMENT ON COLUMN organization_bank_accounts.label IS
    'Optional operator-supplied label (e.g. ''Primary EUR'', ''USD ops'').';

COMMENT ON COLUMN organization_bank_accounts.holder_name IS
    'Legal account holder name as reported by the bank. Required.';

COMMENT ON COLUMN organization_bank_accounts.iban IS
    'International Bank Account Number (EU/EEA). Optional; used together '
    'with bic_swift for SEPA-style identifiers.';

COMMENT ON COLUMN organization_bank_accounts.bic_swift IS
    'BIC / SWIFT code identifying the bank branch. Optional.';

COMMENT ON COLUMN organization_bank_accounts.account_number IS
    'Domestic account number (e.g. US ACH). Optional.';

COMMENT ON COLUMN organization_bank_accounts.routing_number IS
    'Domestic routing / sort code (e.g. US ABA). Optional.';

COMMENT ON COLUMN organization_bank_accounts.currency IS
    'ISO 4217 alpha-3 currency code (e.g. ''EUR'', ''USD'', ''RUB''). '
    'Stored as CHAR(3) NOT NULL.';

COMMENT ON COLUMN organization_bank_accounts.is_default IS
    'When true, this account is the org-level default. Enforced by a '
    'partial unique index on (org_id) WHERE is_default AND deleted_at '
    'IS NULL so at most one active default exists per org.';

COMMENT ON COLUMN organization_bank_accounts.deleted_at IS
    'Soft-delete marker. NULL = active. Non-NULL rows are excluded from '
    'the partial unique index, freeing the default slot for reuse.';

-- +goose Down
DROP INDEX IF EXISTS organization_bank_accounts_one_default_per_org;
DROP INDEX IF EXISTS organization_bank_accounts_org_active;
DROP TABLE IF EXISTS organization_bank_accounts;
