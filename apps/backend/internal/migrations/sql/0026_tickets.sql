-- 0026_tickets.sql — tickets table and issuance support (feature #139).
--
-- A ticket is the atomic unit of entitlement issued after payment.succeeded or
-- free-checkout completion.  One checkout session may produce N tickets (one per
-- unit in the reservation quantity).
--
-- Idempotency is implemented at the application layer:
--   - Before issuing, the handler calls ListTicketsByCheckoutSession.
--   - If any rows already exist for that checkout_session_id, the existing
--     tickets are returned without inserting new ones.
--   - This prevents double-issuance on webhook replay or handler retry.
--
-- State machine:
--
--   active
--     │ (cancelled by org or buyer)  → cancelled   (terminal)
--     │ (transferred to new holder)  → transferred (terminal)

-- +goose Up

CREATE TABLE tickets (
    id                  uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    -- The checkout session that triggered this issuance.
    -- Acts as the idempotency scope: check ListTicketsByCheckoutSession
    -- before inserting to prevent double-issuance.
    checkout_session_id uuid        NOT NULL REFERENCES checkout_sessions(id),
    -- The event session the ticket grants access to.
    session_id          uuid        NOT NULL REFERENCES sessions(id),
    -- Optional tier association; NULL for GA / untiered sessions.
    tier_id             uuid        REFERENCES ticket_tiers(id),
    -- Optional holder email for delivery; NULL for anonymous purchases.
    holder_email        text,
    -- State machine column.
    status              text        NOT NULL DEFAULT 'active',
    CONSTRAINT tickets_status_check CHECK (status IN ('active', 'cancelled', 'transferred')),
    -- Issuance timestamp (when payment confirmed or free checkout completed).
    issued_at           timestamptz NOT NULL DEFAULT now(),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Fast lookup of all tickets for a checkout session (idempotency + read).
CREATE INDEX tickets_checkout_session_id ON tickets (checkout_session_id);

-- Lookup tickets by session (capacity reporting, scanner integration).
CREATE INDEX tickets_session_id ON tickets (session_id);

-- Active-ticket scan by status (exclude terminals for operational views).
CREATE INDEX tickets_status_active ON tickets (checkout_session_id)
    WHERE status = 'active';

COMMENT ON TABLE tickets IS
    'Atomic entitlements issued after payment.succeeded or free checkout completion. '
    'One row per unit (one checkout may issue many tickets based on reservation quantity). '
    'Idempotent per checkout_session_id: check existence before inserting.';

COMMENT ON COLUMN tickets.checkout_session_id IS
    'The checkout session that triggered this issuance. '
    'Application-level idempotency key: ListTicketsByCheckoutSession returns non-empty '
    'if tickets were already issued for this session.';

COMMENT ON COLUMN tickets.status IS
    'State machine: active → cancelled | transferred. Terminal states: cancelled, transferred.';

COMMENT ON COLUMN tickets.holder_email IS
    'Delivery email for the ticket holder. NULL for anonymous purchases '
    'or when email is not yet collected at time of issuance.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('ticket.read',   'Read tickets for a checkout session (feature #139)'),
    ('ticket.issue',  'Issue tickets on payment success or free checkout (feature #139)'),
    ('ticket.cancel', 'Cancel an issued ticket (feature #139)')
ON CONFLICT (name) DO NOTHING;

-- Grant all ticket permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('ticket.read', 'ticket.issue', 'ticket.cancel')
ON CONFLICT DO NOTHING;

-- Grant all ticket permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('ticket.read', 'ticket.issue', 'ticket.cancel')
ON CONFLICT DO NOTHING;

-- Grant read to member (buyers can view their own tickets).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('ticket.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('ticket.read', 'ticket.issue', 'ticket.cancel')
);

DELETE FROM permissions
WHERE name IN ('ticket.read', 'ticket.issue', 'ticket.cancel');

DROP TABLE IF EXISTS tickets;
