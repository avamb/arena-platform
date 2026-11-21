-- +goose Up
-- +goose StatementBegin

-- Spec 62 sections 8.1 and 8.3 (П-8, П-9): the buyer's browser context kept with
-- a public checkout, per-organization server-side conversion settings and the
-- delivery journal of those conversions.
--
-- checkout_buyer_contexts   what the site sends in checkout/start as
--                           buyer_context (fbp, fbc, fbclid, gclid, GA4
--                           client id, tracking consent) plus the client IP
--                           and User-Agent the server saw. One row per
--                           checkout session, written best effort (behind a
--                           SAVEPOINT), never read by any buyer-facing route.
-- org_conversion_settings   Meta pixel id + CAPI token, GA4 measurement id +
--                           Measurement Protocol secret. The two secrets are
--                           write-only: no route returns them.
-- conversion_deliveries     one row per (order, provider, kind), the outbox leg
--                           queues it, ops.conversions.sweep sends it with
--                           backoff. last_error is scrubbed of secrets.

CREATE TABLE checkout_buyer_contexts (
    checkout_session_id uuid        PRIMARY KEY REFERENCES checkout_sessions (id) ON DELETE CASCADE,
    order_id            uuid        REFERENCES orders (id) ON DELETE CASCADE,
    context             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT checkout_buyer_contexts_object_chk CHECK (jsonb_typeof(context) = 'object')
);
CREATE UNIQUE INDEX checkout_buyer_contexts_order_uq
    ON checkout_buyer_contexts (order_id) WHERE order_id IS NOT NULL;

CREATE TABLE org_conversion_settings (
    org_id               uuid        PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    meta_pixel_id        text,
    meta_capi_token      text,
    meta_test_event_code text,
    ga4_measurement_id   text,
    ga4_api_secret       text,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE conversion_deliveries (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid        NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    org_id          uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    provider        text        NOT NULL,
    kind            text        NOT NULL DEFAULT 'purchase',
    status          text        NOT NULL DEFAULT 'pending',
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text,
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conversion_deliveries_provider_chk CHECK (provider IN ('meta', 'ga4')),
    CONSTRAINT conversion_deliveries_kind_chk CHECK (kind IN ('purchase')),
    CONSTRAINT conversion_deliveries_status_chk CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    CONSTRAINT conversion_deliveries_uq UNIQUE (order_id, provider, kind)
);
CREATE INDEX conversion_deliveries_due_idx
    ON conversion_deliveries (next_attempt_at) WHERE status = 'pending';

-- No new permission: the settings route is gated on org.update (the owner),
-- which platform_superadmin already holds.

-- +goose StatementEnd

-- +goose Down
DROP TABLE conversion_deliveries;
DROP TABLE org_conversion_settings;
DROP TABLE checkout_buyer_contexts;
