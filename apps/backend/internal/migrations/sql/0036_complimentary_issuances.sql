-- 0036_complimentary_issuances.sql — Complimentary ticket issuance flow (feature #148).
--
-- Complimentary issuances allow org admins to issue tickets for free to named
-- recipients without going through the checkout/payment flow. The batch_id
-- provides idempotency: re-submitting the same batch_id for the same org is
-- a no-op and returns the existing issuance.
--
-- Inventory is consumed directly (no reservation step): ReserveCapacity +
-- ConfirmCapacity are called atomically in the same transaction so complimentary
-- tickets reduce available capacity the same way paid tickets do.
--
-- The tickets table is extended with a complimentary_issuance_id FK so
-- complimentary tickets can be stored alongside paid tickets in a single table.
-- checkout_session_id is made nullable with a CHECK constraint ensuring exactly
-- one of the two source FKs is set.

-- +goose Up

-- ── complimentary_issuances ───────────────────────────────────────────────────

CREATE TABLE complimentary_issuances (
    id          uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id  uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tier_id     uuid        REFERENCES ticket_tiers(id) ON DELETE SET NULL,
    qty         integer     NOT NULL,
    recipients  text[]      NOT NULL DEFAULT '{}',
    batch_id    text        NOT NULL,
    status      text        NOT NULL DEFAULT 'pending',
    issued_by   text,
    notes       text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    -- qty must be a positive integer
    CONSTRAINT complimentary_qty_positive CHECK (qty > 0),

    -- status must be one of the defined lifecycle values
    CONSTRAINT complimentary_status_check CHECK (status IN ('pending', 'issued', 'failed'))
);

-- Idempotency index: one batch_id per org (enforces idempotent re-submission).
CREATE UNIQUE INDEX complimentary_issuances_org_batch
    ON complimentary_issuances (org_id, batch_id);

-- Fast lookup of all issuances for an org (list endpoint).
CREATE INDEX complimentary_issuances_org_id
    ON complimentary_issuances (org_id);

-- Fast lookup of all issuances for a session (capacity audit).
CREATE INDEX complimentary_issuances_session_id
    ON complimentary_issuances (session_id);

COMMENT ON TABLE complimentary_issuances IS
    'Batch complimentary ticket issuances. Each row represents one issuance batch '
    'identified by (org_id, batch_id). batch_id provides idempotency: re-submitting '
    'the same batch_id for the same org returns the existing row unchanged. '
    'Inventory is consumed directly via ReserveCapacity + ConfirmCapacity. '
    'Feature #148 — Wave 11 Complimentary tickets.';

COMMENT ON COLUMN complimentary_issuances.batch_id IS
    'Caller-supplied idempotency key. Unique per org_id. '
    'Clients should use a stable identifier (e.g. UUID, internal request ID) '
    'so retries on network failure are safe.';

COMMENT ON COLUMN complimentary_issuances.recipients IS
    'Array of recipient email addresses. Empty array means anonymous issuance '
    '(qty anonymous tickets without delivery). Order mirrors ticket order.';

COMMENT ON COLUMN complimentary_issuances.status IS
    'Lifecycle status. pending: created, tickets not yet issued. '
    'issued: tickets successfully created and inventory decremented. '
    'failed: issuance attempted but failed; may be retried via a new batch_id.';

-- ── Extend tickets table to support complimentary source FK ───────────────────
--
-- Complimentary tickets do not have a checkout session, so checkout_session_id
-- is made nullable.  A CHECK constraint ensures exactly one source FK is set:
-- either a checkout session (paid/free checkout) or a complimentary issuance.

ALTER TABLE tickets
    ALTER COLUMN checkout_session_id DROP NOT NULL;

ALTER TABLE tickets
    ADD COLUMN complimentary_issuance_id uuid
        REFERENCES complimentary_issuances(id) ON DELETE CASCADE;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_source_check CHECK (
        (checkout_session_id IS NOT NULL AND complimentary_issuance_id IS NULL) OR
        (checkout_session_id IS NULL     AND complimentary_issuance_id IS NOT NULL)
    );

-- Fast lookup of all tickets for a complimentary issuance.
CREATE INDEX tickets_complimentary_issuance_id
    ON tickets (complimentary_issuance_id)
    WHERE complimentary_issuance_id IS NOT NULL;

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('complimentary.issue', 'Issue complimentary ticket batches to named recipients (feature #148)'),
    ('complimentary.read',  'Read complimentary issuance records for an org (feature #148)')
ON CONFLICT (name) DO NOTHING;

-- Grant both permissions to platform admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('complimentary.issue', 'complimentary.read')
ON CONFLICT DO NOTHING;

-- Grant both permissions to org_admin (manages own org's complimentary issuances).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('complimentary.issue', 'complimentary.read')
ON CONFLICT DO NOTHING;

-- +goose Down

-- Remove ticket extension columns (constraint must drop first).
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_source_check;
DROP INDEX IF EXISTS tickets_complimentary_issuance_id;
ALTER TABLE tickets DROP COLUMN IF EXISTS complimentary_issuance_id;
ALTER TABLE tickets ALTER COLUMN checkout_session_id SET NOT NULL;

-- Remove RBAC seeds.
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('complimentary.issue', 'complimentary.read')
);
DELETE FROM permissions
WHERE name IN ('complimentary.issue', 'complimentary.read');

DROP TABLE IF EXISTS complimentary_issuances;
