-- 0028_refunds.sql — refund state machine (feature #138).
--
-- A refund wraps a provider refund operation into a stateful object
-- that tracks the full lifecycle from request to completion.
--
-- State machine:
--
--   requested
--     │ (approved)   → approved
--     │ (rejected)   → rejected  (terminal)
--     ▼
--   approved
--     │ (sent to provider) → provider_pending
--     ▼
--   provider_pending
--     │ (provider confirms)    → succeeded  (terminal)
--     │ (provider rejects)     → failed     (terminal)
--     │ (needs manual review)  → manual_review
--     ▼
--   manual_review
--     │ (admin approves) → succeeded  (terminal)
--     │ (admin rejects)  → failed     (terminal)
--
--   rejected / succeeded / failed  (terminal states)

-- +goose Up

-- ── refunds ───────────────────────────────────────────────────────────────────
--
-- Tracks one refund request per row. amount is in minor units (e.g. cents).
-- provider_refund_id is set when the refund has been submitted to the payment provider.
-- failure_reason is set when state transitions to 'failed'.

CREATE TABLE refunds (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    payment_intent_id   uuid        NOT NULL REFERENCES payment_intents(id),
    org_id              uuid        NOT NULL,
    amount              bigint      NOT NULL,
    currency            text        NOT NULL,
    reason              text,
    requested_by        text,
    state               text        NOT NULL DEFAULT 'requested'
                        CONSTRAINT refunds_state_check CHECK (state IN (
                            'requested',
                            'approved',
                            'rejected',
                            'provider_pending',
                            'succeeded',
                            'failed',
                            'manual_review'
                        )),
    provider_refund_id  text,
    failure_reason      text,
    requested_at        timestamptz NOT NULL DEFAULT now(),
    approved_at         timestamptz,
    succeeded_at        timestamptz,
    failed_at           timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Index for lookups by payment_intent_id (list all refunds for a payment).
CREATE INDEX refunds_payment_intent_id ON refunds (payment_intent_id);

-- Index for worker queries that poll for refunds in a specific state.
CREATE INDEX refunds_state_idx ON refunds (state);

-- ── refund_events ─────────────────────────────────────────────────────────────
--
-- Records each processed provider webhook event for idempotency.
-- The UNIQUE constraint on (provider_refund_id, event_type) ensures that
-- duplicate webhook deliveries are detected and rejected without reprocessing.

CREATE TABLE refund_events (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    refund_id           uuid        NOT NULL REFERENCES refunds(id),
    provider_refund_id  text        NOT NULL,
    event_type          text        NOT NULL,
    event_payload       jsonb,
    resulting_state     text,
    processed_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_refund_id, event_type)
);

-- Index for fast lookups by provider_refund_id when processing webhooks.
CREATE INDEX refund_events_provider_refund_id ON refund_events (provider_refund_id);

-- ── RBAC seeds ────────────────────────────────────────────────────────────────
--
-- Permissions:
--   refund.create  — request a new refund
--   refund.read    — read refund state
--   refund.approve — approve or reject a refund
--
-- Role grants:
--   admin     → refund.create, refund.read, refund.approve
--   org_admin → refund.create, refund.read, refund.approve
--   member    → refund.read

INSERT INTO permissions (name) VALUES
    ('refund.create'),
    ('refund.read'),
    ('refund.approve')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_name, permission_name) VALUES
    ('admin',     'refund.create'),
    ('admin',     'refund.read'),
    ('admin',     'refund.approve'),
    ('org_admin', 'refund.create'),
    ('org_admin', 'refund.read'),
    ('org_admin', 'refund.approve'),
    ('member',    'refund.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions WHERE permission_name IN ('refund.create', 'refund.read', 'refund.approve');
DELETE FROM permissions WHERE name IN ('refund.create', 'refund.read', 'refund.approve');

DROP TABLE IF EXISTS refund_events;
DROP TABLE IF EXISTS refunds;
