-- 0032_event_reports.sql — post-event report generation (feature #159).
--
-- After event.end_at + configurable cutoff window, a worker job generates an
-- event_reports row with event_report_lines aggregating sales, refunds,
-- complimentary tickets, scans, commissions, and payouts.
--
-- Table design:
--   event_reports        — one row per report request; tracks generation state.
--   event_report_lines   — one row per category per report; contains the aggregated
--                          quantities and amounts.
--
-- State machine for event_reports.state:
--   pending → generating → ready (terminal, success)
--                        → failed (terminal, error)

-- +goose Up

-- ── event_reports ─────────────────────────────────────────────────────────────
--
-- Tracks one post-event report per row.
-- report_window_start = event.end_at
-- report_window_end   = event.end_at + cutoff_duration (configurable)
-- error_msg is set when state transitions to 'failed'.
-- generated_at is set when state transitions to 'ready'.

CREATE TABLE event_reports (
    id                   uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    event_id             uuid        NOT NULL REFERENCES events(id),
    org_id               uuid        NOT NULL,
    state                text        NOT NULL DEFAULT 'pending'
                         CONSTRAINT event_reports_state_check CHECK (state IN (
                             'pending',
                             'generating',
                             'ready',
                             'failed'
                         )),
    report_window_start  timestamptz,
    report_window_end    timestamptz,
    error_msg            text,
    generated_at         timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Index for lookups by event_id.
CREATE INDEX event_reports_event_id ON event_reports (event_id);

-- Index for worker queries that poll for reports in a specific state.
CREATE INDEX event_reports_state_idx ON event_reports (state);

-- ── event_report_lines ────────────────────────────────────────────────────────
--
-- One row per (report_id, category). Categories mirror the spec:
--   sales         — paid ticket revenue
--   refunds       — succeeded refunds
--   complimentary — free/zero-price tickets
--   scans         — successful entry scans
--   commissions   — platform fee amounts
--   payouts       — net amount due to organizer
--
-- gross_amount and net_amount are in minor currency units (e.g. cents).
-- quantity is the number of units (tickets or scans).

CREATE TABLE event_report_lines (
    id           uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    report_id    uuid        NOT NULL REFERENCES event_reports(id) ON DELETE CASCADE,
    category     text        NOT NULL
                 CONSTRAINT event_report_lines_category_check CHECK (category IN (
                     'sales',
                     'refunds',
                     'complimentary',
                     'scans',
                     'commissions',
                     'payouts'
                 )),
    quantity     bigint      NOT NULL DEFAULT 0,
    gross_amount bigint      NOT NULL DEFAULT 0,
    net_amount   bigint      NOT NULL DEFAULT 0,
    currency     text        NOT NULL DEFAULT 'usd',
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Index for fast lookups by report_id (list all lines for a report).
CREATE INDEX event_report_lines_report_id ON event_report_lines (report_id);

-- Unique constraint: each category appears at most once per report.
CREATE UNIQUE INDEX event_report_lines_report_category ON event_report_lines (report_id, category);

-- ── RBAC seeds ────────────────────────────────────────────────────────────────
--
-- Permissions:
--   report.read     — view event reports and their lines
--   report.generate — trigger on-demand report generation
--
-- Role grants:
--   admin     → report.read, report.generate
--   org_admin → report.read, report.generate

INSERT INTO permissions (name, description) VALUES
    ('report.read',     'View event reports and aggregated financial lines (feature #159)'),
    ('report.generate', 'Trigger on-demand post-event report generation (feature #159)')
ON CONFLICT (name) DO NOTHING;

-- Grant all report permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('report.read', 'report.generate')
ON CONFLICT DO NOTHING;

-- Grant all report permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('report.read', 'report.generate')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('report.read', 'report.generate')
);

DELETE FROM permissions WHERE name IN ('report.read', 'report.generate');

DROP TABLE IF EXISTS event_report_lines;
DROP TABLE IF EXISTS event_reports;
