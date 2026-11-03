-- 0186_payment_disputes.sql — disputes and chargebacks (spec 51.3, PAY-16).
--
-- A buyer disputes a card payment at their bank; the organizer has days to
-- answer with evidence. Arena records the dispute when the provider tells it
-- (Stripe charge.dispute.*, AllPay type=chargeback) or when the organizer
-- enters it by hand (PayPlus, a letter from the acquirer), builds the
-- evidence package and prepares the answer. Nothing reaches the bank before
-- a person presses "Submit" (decision 2026-10-13).
--
--   * payment_disputes          — one row per (provider, provider dispute id);
--                                 a hand-entered dispute has no provider id.
--   * payment_dispute_events    — append-only journal: provider events and
--                                 every action of a person or a job.
--   * payment_dispute_packages  — every build of the evidence package; an old
--                                 build is never overwritten (what was sent
--                                 is what is kept).
--   * payment_dispute_files     — the PDF files of a build, stored here (not
--                                 in the public media store): they are only
--                                 ever served through the dispute's routes.
--   * payment_dispute_notices   — what the organization was already told, so
--                                 a restart never repeats a reminder.
--   * dispute_settings          — the low-value threshold of decision R-11,
--                                 per currency: org_id NULL is the platform
--                                 default, a row of the organization wins.
--
-- Disputes are evidence: org_id has no cascade and no code deletes a row.
-- Retention: at least 24 and at most 36 months after the dispute closes; no
-- purge job yet.
--
-- Permissions dispute.read (org_admin, organizer) and dispute.respond
-- (org_admin) are granted to platform_superadmin in this same migration
-- (the 532 parity guard). An organization API key never carries
-- dispute.respond (apikeys.ForbiddenScopeExact).

-- +goose Up

-- The foreign keys lock orders, payment_intents and organizations; a deploy
-- that meets a long transaction fails fast instead of queueing every sale.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS payment_disputes (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    provider            text        NOT NULL,
    provider_dispute_id text,
    channel             text        NOT NULL,
    payment_intent_id   uuid        REFERENCES payment_intents(id),
    order_id            uuid        REFERENCES orders(id),
    config_id           uuid        REFERENCES payment_provider_configs(id),
    amount_minor        bigint      NOT NULL,
    currency            text        NOT NULL,
    reason              text        NOT NULL DEFAULT '',
    reason_group        text        NOT NULL DEFAULT 'other',
    provider_status     text        NOT NULL DEFAULT '',
    state               text        NOT NULL DEFAULT 'needs_response',
    is_inquiry          boolean     NOT NULL DEFAULT false,
    is_charge_refundable boolean    NOT NULL DEFAULT false,
    due_by              timestamptz,
    due_by_source       text        NOT NULL DEFAULT 'default',
    respond_to          text        NOT NULL DEFAULT '',
    funds_withdrawn_at  timestamptz,
    funds_reinstated_at timestamptz,
    outcome             text,
    closed_at           timestamptz,
    low_value           boolean     NOT NULL DEFAULT false,
    opened_at           timestamptz NOT NULL DEFAULT now(),
    submitted_at        timestamptz,
    submitted_by        uuid,
    accepted_at         timestamptz,
    accepted_by         uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_disputes_channel_check
        CHECK (channel IN ('api', 'email', 'manual')),
    CONSTRAINT payment_disputes_state_check
        CHECK (state IN ('needs_response', 'draft_ready', 'submitted', 'under_review',
                         'accepted', 'expired', 'closed')),
    CONSTRAINT payment_disputes_reason_group_check
        CHECK (reason_group IN ('fraud', 'not_received', 'not_recognized', 'refund',
                                'quality', 'duplicate', 'other')),
    CONSTRAINT payment_disputes_due_by_source_check
        CHECK (due_by_source IN ('provider', 'organizer', 'default')),
    CONSTRAINT payment_disputes_outcome_check
        CHECK (outcome IS NULL OR outcome IN ('won', 'lost', 'accepted', 'withdrawn', 'expired')),
    CONSTRAINT payment_disputes_amount_check CHECK (amount_minor >= 0),
    CONSTRAINT payment_disputes_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT payment_disputes_respond_to_length CHECK (char_length(respond_to) <= 320)
);

CREATE UNIQUE INDEX IF NOT EXISTS payment_disputes_provider_id_uq
    ON payment_disputes (provider, provider_dispute_id)
    WHERE provider_dispute_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS payment_disputes_org_idx
    ON payment_disputes (org_id, created_at DESC);
CREATE INDEX IF NOT EXISTS payment_disputes_order_idx
    ON payment_disputes (order_id) WHERE order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS payment_disputes_open_due_idx
    ON payment_disputes (due_by) WHERE closed_at IS NULL;

CREATE TABLE IF NOT EXISTS payment_dispute_events (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    dispute_id        uuid        NOT NULL REFERENCES payment_disputes(id),
    org_id            uuid        NOT NULL,
    kind              text        NOT NULL,
    provider_event_id text,
    provider_status   text        NOT NULL DEFAULT '',
    actor_user_id     uuid,
    actor_label       text        NOT NULL DEFAULT '',
    payload           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_dispute_events_kind_check
        CHECK (kind IN ('provider_event', 'opened', 'package_built', 'package_failed',
                        'draft_saved', 'draft_failed', 'submitted', 'submit_failed',
                        'email_queued', 'email_sent', 'email_failed', 'sent_by_organizer',
                        'accepted', 'accept_failed', 'deadline_set', 'notice_sent', 'note'))
);

CREATE UNIQUE INDEX IF NOT EXISTS payment_dispute_events_provider_uq
    ON payment_dispute_events (dispute_id, provider_event_id)
    WHERE provider_event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS payment_dispute_events_dispute_idx
    ON payment_dispute_events (dispute_id, created_at);

CREATE TABLE IF NOT EXISTS payment_dispute_packages (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    dispute_id        uuid        NOT NULL REFERENCES payment_disputes(id),
    org_id            uuid        NOT NULL,
    version           integer     NOT NULL,
    locale            text        NOT NULL DEFAULT 'en',
    fields            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    gaps              jsonb       NOT NULL DEFAULT '[]'::jsonb,
    provider_file_ids jsonb       NOT NULL DEFAULT '{}'::jsonb,
    email_draft       jsonb,
    total_bytes       integer     NOT NULL DEFAULT 0,
    total_pages       integer     NOT NULL DEFAULT 0,
    built_by          uuid,
    built_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_dispute_packages_version_uq UNIQUE (dispute_id, version)
);

CREATE TABLE IF NOT EXISTS payment_dispute_files (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id  uuid        NOT NULL REFERENCES payment_dispute_packages(id),
    kind        text        NOT NULL,
    filename    text        NOT NULL,
    content     bytea       NOT NULL,
    bytes       integer     NOT NULL,
    pages       integer     NOT NULL,
    sha256      text        NOT NULL,
    for_provider boolean    NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_dispute_files_kind_check
        CHECK (kind IN ('receipt', 'service_documentation', 'customer_communication',
                        'refund_policy', 'cancellation_policy', 'uncategorized_file', 'full')),
    CONSTRAINT payment_dispute_files_kind_uq UNIQUE (package_id, kind)
);

CREATE TABLE IF NOT EXISTS payment_dispute_notices (
    dispute_id uuid        NOT NULL REFERENCES payment_disputes(id),
    notice_key text        NOT NULL,
    sent_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dispute_id, notice_key)
);

CREATE TABLE IF NOT EXISTS dispute_settings (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               uuid        REFERENCES organizations(id),
    low_value_enabled    boolean     NOT NULL DEFAULT true,
    low_value_thresholds jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_by           uuid,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS dispute_settings_org_uq
    ON dispute_settings (org_id) WHERE org_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS dispute_settings_platform_uq
    ON dispute_settings ((org_id IS NULL)) WHERE org_id IS NULL;

-- The platform default of decision R-11 (owner decision 2026-11-02): about
-- five US dollars, in each currency's own minor units.
INSERT INTO dispute_settings (org_id, low_value_enabled, low_value_thresholds)
VALUES (NULL, true, '{"USD":500,"EUR":500,"GBP":400,"ILS":2000,"CZK":12000}'::jsonb)
ON CONFLICT DO NOTHING;

INSERT INTO permissions (name, description) VALUES
    ('dispute.read', 'See the payment disputes of the organization and download their evidence'),
    ('dispute.respond', 'Submit or accept a payment dispute and send the evidence to the provider')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'dispute.read'
WHERE  r.name IN ('org_admin', 'organizer', 'platform_superadmin')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'dispute.respond'
WHERE  r.name IN ('org_admin', 'platform_superadmin')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name IN ('dispute.read', 'dispute.respond'));
DELETE FROM permissions WHERE name IN ('dispute.read', 'dispute.respond');

DROP TABLE IF EXISTS dispute_settings;
DROP TABLE IF EXISTS payment_dispute_notices;
DROP TABLE IF EXISTS payment_dispute_files;
DROP TABLE IF EXISTS payment_dispute_packages;
DROP TABLE IF EXISTS payment_dispute_events;
DROP TABLE IF EXISTS payment_disputes;
