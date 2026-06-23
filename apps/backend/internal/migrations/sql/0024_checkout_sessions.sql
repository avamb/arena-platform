-- 0024_checkout_sessions.sql — checkout session state machine (feature #132).
--
-- A checkout_session wraps a reservation + pricing snapshot + payment intent
-- into a single stateful object.  The state machine is:
--
--   created
--     │ POST /v1/checkout/{id}/confirm  (pricing snapshot locked in)
--     ▼
--   pricing_confirmed
--     │ POST /v1/checkout/{id}/complete (payment confirmed externally)
--     ▼
--   completed                (terminal)
--
--   Any non-terminal state → abandoned  (POST /v1/checkout/{id}/abandon)
--   Any non-terminal state → expired    (TTL worker / reservation expiry hook)
--   payment_started         → manual_review  (payment provider flag)
--
-- The pricing snapshot columns (subtotal … total, currency) are written once
-- during the pricing_confirmed transition and must not change afterwards.

-- +goose Up

CREATE TABLE checkout_sessions (
    id                uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    channel_id        uuid        NOT NULL REFERENCES sales_channels(id),
    reservation_id    uuid        NOT NULL REFERENCES reservations(id),
    user_id           uuid        REFERENCES users(id),

    -- State machine column.
    state             text        NOT NULL DEFAULT 'created',
    CONSTRAINT checkout_sessions_state_check CHECK (
        state IN (
            'created',
            'pricing_confirmed',
            'payment_started',
            'completed',
            'abandoned',
            'expired',
            'manual_review'
        )
    ),

    -- Pricing snapshot — written once on pricing_confirmed transition.
    -- All amounts in smallest currency unit (cents).  Null until confirmed.
    subtotal          bigint,
    discount          bigint,
    platform_fee      bigint,
    provider_fee      bigint,
    tax               bigint,
    total             bigint,
    currency          text,
    promo_code_id     uuid        REFERENCES promo_codes(id),

    -- Payment intent — written on payment_started transition.
    payment_intent_id text,
    payment_provider  text,

    -- Terminal-state timestamps.
    completed_at      timestamptz,
    abandoned_at      timestamptz,
    expired_at        timestamptz,

    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Index for reservation-based expiry hook lookups.
CREATE INDEX checkout_sessions_reservation_id ON checkout_sessions (reservation_id);

-- Index to find active (non-terminal) sessions efficiently.
CREATE INDEX checkout_sessions_state_active ON checkout_sessions (created_at)
    WHERE state NOT IN ('completed', 'abandoned', 'expired');

COMMENT ON TABLE checkout_sessions IS
    'One checkout session per reservation attempt. Tracks the full lifecycle '
    'from cart to payment. Terminal states: completed, abandoned, expired.';

COMMENT ON COLUMN checkout_sessions.state IS
    'State machine: created → pricing_confirmed → completed|abandoned|expired. '
    'payment_started and manual_review are intermediate states before completion.';

COMMENT ON COLUMN checkout_sessions.subtotal IS
    'Snapshot of tier_price × quantity at the time of pricing_confirmed. '
    'Null until the pricing_confirmed transition fires.';

COMMENT ON COLUMN checkout_sessions.total IS
    'All-in total (subtotal − discount + platform_fee + provider_fee + tax). '
    'Null until pricing_confirmed. This is the amount charged to the customer.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('checkout.start',   'Create a checkout session from a reservation (feature #132)'),
    ('checkout.confirm', 'Lock in pricing and advance to pricing_confirmed (feature #132)'),
    ('checkout.complete','Mark a checkout session as completed (feature #132)'),
    ('checkout.abandon', 'Abandon a checkout session and release inventory (feature #132)'),
    ('checkout.read',    'Read checkout session details (feature #132)')
ON CONFLICT (name) DO NOTHING;

-- Grant all checkout permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('checkout.start', 'checkout.confirm', 'checkout.complete',
                   'checkout.abandon', 'checkout.read')
ON CONFLICT DO NOTHING;

-- Grant all checkout permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('checkout.start', 'checkout.confirm', 'checkout.complete',
                   'checkout.abandon', 'checkout.read')
ON CONFLICT DO NOTHING;

-- Grant start/confirm/complete/abandon/read to member (buyers).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('checkout.start', 'checkout.confirm', 'checkout.complete',
                   'checkout.abandon', 'checkout.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('checkout.start', 'checkout.confirm', 'checkout.complete',
                   'checkout.abandon', 'checkout.read')
);

DELETE FROM permissions
WHERE name IN ('checkout.start', 'checkout.confirm', 'checkout.complete',
               'checkout.abandon', 'checkout.read');

DROP TABLE IF EXISTS checkout_sessions;
