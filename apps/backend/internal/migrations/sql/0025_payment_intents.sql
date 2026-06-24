-- 0025_payment_intents.sql — payment intent state machine (feature #137).
--
-- A payment_intent wraps a provider payment operation into a stateful object
-- that tracks the full lifecycle including SCA/3DS challenges.
--
-- State machine:
--
--   created
--     │ (no SCA needed) → processing
--     │ (SCA needed)    → requires_action
--     ▼
--   requires_action
--     │ (challenge completed) → processing
--     │ (challenge failed)    → failed
--     ▼
--   processing
--     │ (auth, manual capture) → authorized
--     │ (immediate capture)    → succeeded
--     │ (declined)             → failed
--     │ (fraud flag)           → manual_review
--     ▼
--   authorized
--     │ (capture) → succeeded
--     │ (void)    → failed
--     ▼
--   manual_review
--     │ (approve) → succeeded
--     │ (decline) → failed
--     ▼
--   succeeded / failed  (terminal states)
--
-- Idempotency: payment_intent_events deduplicates provider webhook events on
-- (provider_payment_id, event_type) so the same event cannot be processed more
-- than once even if the provider retries delivery.

-- +goose Up

CREATE TABLE payment_intents (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    -- Optional link to a checkout session that triggered this intent.
    checkout_session_id uuid        REFERENCES checkout_sessions(id),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    -- Payment provider name (e.g. "stripe", "paypal", "mock").
    provider            text        NOT NULL,
    -- Provider's own payment intent / charge identifier (nullable until the
    -- provider confirms intent creation and returns its ID).
    provider_payment_id text,
    amount              bigint      NOT NULL,   -- smallest currency unit (cents)
    currency            text        NOT NULL,   -- ISO 4217 (e.g. "USD")
    -- State machine column.
    state               text        NOT NULL DEFAULT 'created',
    CONSTRAINT payment_intents_state_check CHECK (
        state IN (
            'created',
            'requires_action',
            'processing',
            'authorized',
            'succeeded',
            'failed',
            'manual_review'
        )
    ),
    -- SCA / 3DS fields — populated when state transitions to requires_action.
    sca_redirect_url    text,       -- URL to redirect the customer for 3DS challenge
    client_secret       text,       -- provider's client secret (for front-end SDK usage)
    -- Error information — populated on the failed transition.
    failure_code        text,
    failure_message     text,
    -- Terminal-state timestamps.
    authorized_at       timestamptz,
    succeeded_at        timestamptz,
    failed_at           timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Efficient lookups by checkout session (one active intent per session).
CREATE INDEX payment_intents_checkout_session_id ON payment_intents (checkout_session_id)
    WHERE checkout_session_id IS NOT NULL;

-- Webhook handlers locate an intent by the provider's own identifier.
-- UNIQUE ensures no two intents share the same provider_payment_id.
CREATE UNIQUE INDEX payment_intents_provider_payment_id ON payment_intents (provider_payment_id)
    WHERE provider_payment_id IS NOT NULL;

-- Active-intent scan by org for monitoring dashboards.
CREATE INDEX payment_intents_org_id_state ON payment_intents (org_id, state)
    WHERE state NOT IN ('succeeded', 'failed');

COMMENT ON TABLE payment_intents IS
    'One payment intent per checkout payment attempt. Tracks the full provider '
    'lifecycle including SCA/3DS challenges. Terminal states: succeeded, failed.';

COMMENT ON COLUMN payment_intents.state IS
    'State machine: created → requires_action|processing → authorized|succeeded|'
    'failed|manual_review. Terminal states: succeeded, failed.';

COMMENT ON COLUMN payment_intents.provider_payment_id IS
    'The payment provider''s own identifier for this intent (e.g. Stripe''s pi_… ID). '
    'Set once the provider confirms intent creation. Used for webhook routing.';

COMMENT ON COLUMN payment_intents.sca_redirect_url IS
    'URL to redirect the customer for SCA/3DS challenge. Non-null when state='
    'requires_action and the provider uses a redirect-based SCA flow.';

-- ── Payment intent event log (webhook idempotency) ───────────────────────────
--
-- Records every processed provider webhook event. The UNIQUE constraint on
-- (provider_payment_id, event_type) guarantees that each provider event is
-- processed at most once, even when the provider retries delivery.

CREATE TABLE payment_intent_events (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    payment_intent_id   uuid        NOT NULL REFERENCES payment_intents(id) ON DELETE CASCADE,
    provider_payment_id text        NOT NULL,
    event_type          text        NOT NULL,
    -- Optional raw webhook payload for audit / replay (jsonb for flexible schema).
    event_payload       jsonb,
    -- New state after processing this event (useful for audit trail).
    resulting_state     text,
    processed_at        timestamptz NOT NULL DEFAULT now(),
    -- Idempotency guard: each (provider_payment_id, event_type) pair is unique.
    CONSTRAINT payment_intent_events_unique UNIQUE (provider_payment_id, event_type)
);

CREATE INDEX payment_intent_events_intent_id ON payment_intent_events (payment_intent_id);

COMMENT ON TABLE payment_intent_events IS
    'Webhook event log for payment intents. Each (provider_payment_id, event_type) '
    'is deduplicated so provider retries cannot cause double-processing.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('payment_intent.create', 'Create a payment intent linked to a checkout session (feature #137)'),
    ('payment_intent.read',   'Read payment intent state and details (feature #137)'),
    ('payment_intent.update', 'Transition payment intent state (feature #137)')
ON CONFLICT (name) DO NOTHING;

-- Grant all payment_intent permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('payment_intent.create', 'payment_intent.read', 'payment_intent.update')
ON CONFLICT DO NOTHING;

-- Grant all payment_intent permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('payment_intent.create', 'payment_intent.read', 'payment_intent.update')
ON CONFLICT DO NOTHING;

-- Grant create/read to member (buyers create and view their own intents).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('payment_intent.create', 'payment_intent.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('payment_intent.create', 'payment_intent.read', 'payment_intent.update')
);

DELETE FROM permissions
WHERE name IN ('payment_intent.create', 'payment_intent.read', 'payment_intent.update');

DROP TABLE IF EXISTS payment_intent_events;
DROP TABLE IF EXISTS payment_intents;
