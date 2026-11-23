-- 0219_payment_method_domains.sql — wallet domains for embedded Stripe checkout
-- (spec 65 section 5, task E3).
--
-- Apple Pay and Google Pay work on a page only when its domain is registered
-- in the organization's OWN Stripe account (Payment method domains). The
-- platform domain and the organizer's own sites are registered per Stripe
-- configuration:
--
--   sales_channel_site_domains  the organizer's site hosts of one sales
--                               channel (the explicit list, because the
--                               CORS list is shared by every organization)
--   payment_method_domains      what we registered in a configuration's
--                               Stripe account and what Stripe said about it
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE sales_channel_site_domains (
    channel_id uuid        NOT NULL REFERENCES sales_channels(id) ON DELETE CASCADE,
    org_id     uuid        NOT NULL REFERENCES organizations(id),
    domain     text        NOT NULL
        CHECK (length(domain) BETWEEN 3 AND 253
               AND domain = lower(domain)
               AND domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, domain)
);

CREATE INDEX sales_channel_site_domains_org_idx ON sales_channel_site_domains (org_id);

CREATE TABLE payment_method_domains (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    payment_config_id  uuid        NOT NULL REFERENCES payment_provider_configs(id) ON DELETE CASCADE,
    org_id             uuid        NOT NULL REFERENCES organizations(id),
    domain             text        NOT NULL
        CHECK (length(domain) BETWEEN 3 AND 253
               AND domain = lower(domain)
               AND domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    source             text        NOT NULL CHECK (source IN ('platform', 'site')),
    provider_domain_id text,
    enabled            boolean,
    apple_pay_status   text CHECK (apple_pay_status  IS NULL OR apple_pay_status  ~ '^[a-z_]{1,32}$'),
    google_pay_status  text CHECK (google_pay_status IS NULL OR google_pay_status ~ '^[a-z_]{1,32}$'),
    link_status        text CHECK (link_status       IS NULL OR link_status       ~ '^[a-z_]{1,32}$'),
    status_detail      text,
    last_error         text,
    checked_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (payment_config_id, domain)
);

CREATE INDEX payment_method_domains_org_idx ON payment_method_domains (org_id);

COMMENT ON TABLE sales_channel_site_domains IS
    'Site hosts of an organizer sales channel, registered as wallet domains in the organization''s Stripe account (spec 65 section 5).';
COMMENT ON TABLE payment_method_domains IS
    'Wallet domains registered in the Stripe account of one payment configuration and the statuses Stripe reported. last_error is Stripe''s own wording and is operator-facing.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TABLE IF EXISTS payment_method_domains;
DROP TABLE IF EXISTS sales_channel_site_domains;
