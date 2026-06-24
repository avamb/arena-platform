-- 0030_delivery_jobs.sql — email delivery jobs table (feature #141).
--
-- A delivery_job represents a request to send an email to a ticket holder.
-- One delivery_job is created per issued ticket when an email address is
-- known (ticket.holder_email or checkout session user email).
--
-- The job is picked up by the ticket.deliver worker handler which:
--   1. Renders a transactional email with the PDF ticket as an attachment.
--   2. Sends via the configured SMTP/transactional provider.
--   3. Updates this row to status='sent' on success.
--   4. On transient failure, the worker retries (status stays 'pending').
--   5. After max_attempts exhausted, status is set to 'failed'.
--
-- State machine:
--   pending → sent     (terminal)
--   pending → failed   (terminal, after worker dead-letter)

-- +goose Up

CREATE TABLE delivery_jobs (
    id              uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    -- The ticket this delivery is for.
    ticket_id       uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    -- Recipient email address. NULL when email was not available at issuance time;
    -- the worker resolves it from ticket.holder_email at delivery time.
    recipient_email text,
    -- Delivery status.
    status          text        NOT NULL DEFAULT 'pending',
    CONSTRAINT delivery_jobs_status_check CHECK (
        status IN ('pending', 'sent', 'failed')
    ),
    -- Attempt counter (incremented by the worker on each retry).
    attempts        int         NOT NULL DEFAULT 0,
    -- Error message from the last failed attempt.
    last_error      text,
    -- When the job was first enqueued.
    queued_at       timestamptz NOT NULL DEFAULT now(),
    -- When the email was successfully delivered (set on status='sent').
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Fast lookup for delivery status reporting and the worker handler.
CREATE INDEX delivery_jobs_ticket_id ON delivery_jobs (ticket_id);

-- Efficient poll for pending jobs ordered by enqueue time.
CREATE INDEX delivery_jobs_status_pending ON delivery_jobs (queued_at)
    WHERE status = 'pending';

COMMENT ON TABLE delivery_jobs IS
    'Email delivery jobs for issued tickets. One row per ticket delivery attempt. '
    'The ticket.deliver worker handler reads pending rows and sends transactional '
    'emails with the PDF ticket attached. Status: pending → sent | failed.';

COMMENT ON COLUMN delivery_jobs.recipient_email IS
    'Target email address. NULL when not known at enqueue time; the worker '
    'resolves it from ticket.holder_email at delivery time. If still unresolved, '
    'the worker skips delivery and logs a warning.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('delivery.read',   'Read delivery job status for tickets (feature #141)'),
    ('delivery.manage', 'Manage ticket delivery jobs: retry, inspect (feature #141)')
ON CONFLICT (name) DO NOTHING;

-- Grant both permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('delivery.read', 'delivery.manage')
ON CONFLICT DO NOTHING;

-- Grant both permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('delivery.read', 'delivery.manage')
ON CONFLICT DO NOTHING;

-- Grant read-only to member (buyers can check delivery status of their tickets).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('delivery.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('delivery.read', 'delivery.manage')
);

DELETE FROM permissions
WHERE name IN ('delivery.read', 'delivery.manage');

DROP TABLE IF EXISTS delivery_jobs;
