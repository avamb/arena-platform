-- +goose Up
-- =====================================================================
-- arena_new — Sales channel settings + credential-masking guard
-- Feature #236.
--
-- Adds the free-form per-channel `settings` jsonb column used by the
-- channel handler. Settings carry provider-specific knobs (e.g. Stripe
-- statement descriptor, AllPay terminal id) that the platform must
-- persist but doesn't model with first-class columns.
--
-- The provider_account_id column is left intact — its credential-masking
-- behaviour is implemented in the API serialization layer
-- (channels.go:maskProviderAccountID) rather than at the database
-- level so background callers (e.g. reservations TTL lookup) keep
-- access to the raw merchant identifier required for payment routing.
-- =====================================================================

ALTER TABLE sales_channels
    ADD COLUMN IF NOT EXISTS settings jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN sales_channels.settings IS
    'Provider-specific free-form configuration (Stripe statement descriptor, '
    'AllPay terminal id, channel-level feature flags, etc). Always a JSON '
    'object — never null. Feature #236.';

-- +goose Down
ALTER TABLE sales_channels
    DROP COLUMN IF EXISTS settings;
