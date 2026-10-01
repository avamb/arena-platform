-- 0120_flitt_provider.sql — Flitt joins stripe and allpay as a channel provider.
--
-- A sales channel names WHICH payment provider takes its money
-- (sales_channels.provider); the organization's own credentials live in
-- payment_provider_configs, whose provider column is free text and needs no
-- change. Flitt (https://docs.flitt.com) is a hosted-checkout card gateway:
-- the widget redirects the buyer to a Flitt page and a signed callback
-- reports the outcome (spec 08_architecture/29_flitt_payment_provider_ru.md).
--
-- Only the CHECK on the channel's provider list has to grow; payment_intents
-- .provider is unconstrained text.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE sales_channels DROP CONSTRAINT sales_channels_provider_check;
ALTER TABLE sales_channels
    ADD CONSTRAINT sales_channels_provider_check
    CHECK (provider IN ('stripe', 'allpay', 'flitt'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- A channel already moved to flitt cannot satisfy the narrower list; put it
-- back on the default before restoring the old constraint.
UPDATE sales_channels SET provider = 'stripe' WHERE provider = 'flitt';
ALTER TABLE sales_channels DROP CONSTRAINT sales_channels_provider_check;
ALTER TABLE sales_channels
    ADD CONSTRAINT sales_channels_provider_check
    CHECK (provider IN ('stripe', 'allpay'));

-- +goose StatementEnd
