-- 0132_sales_channel_provider_registry.sql — a channel's provider is checked
-- by the payment module registry, not by a list in the database (spec
-- 08_architecture/36_payment_modules_refunds_acquiring_ru.md §5, PAY-02).
--
-- sales_channels_provider_check enumerated the providers ('stripe', 'allpay',
-- 'flitt' since 0120), so every new payment module needed a migration and a
-- matching edit in three hand-kept Go copies of the same list. The handlers
-- now validate against internal/app/payments.Registry() (a provider with a
-- module behind it, spelled as its descriptor names it), so the database
-- only keeps the value well-formed: the same shape the registry demands of a
-- descriptor name (lower-case, starts with a letter, [a-z0-9_]), at most 64
-- characters. A new provider is one module plus one line in
-- internal/app/payments/modules.go — no migration.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE sales_channels DROP CONSTRAINT sales_channels_provider_check;
ALTER TABLE sales_channels
    ADD CONSTRAINT sales_channels_provider_format_check
    CHECK (provider ~ '^[a-z][a-z0-9_]{0,63}$');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- A channel on a provider the old list does not know cannot satisfy it; put
-- it back on the default before restoring the list, as 0120's Down did.
UPDATE sales_channels SET provider = 'stripe' WHERE provider NOT IN ('stripe', 'allpay', 'flitt');
ALTER TABLE sales_channels DROP CONSTRAINT sales_channels_provider_format_check;
ALTER TABLE sales_channels
    ADD CONSTRAINT sales_channels_provider_check
    CHECK (provider IN ('stripe', 'allpay', 'flitt'));

-- +goose StatementEnd
