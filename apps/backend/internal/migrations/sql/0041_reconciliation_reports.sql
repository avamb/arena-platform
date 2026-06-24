-- 0041_reconciliation_reports.sql — External reconciliation reports (feature #147).
--
-- When a partner organisation submits a sales/returns report against an active
-- external allocation, the platform runs an auto-match algorithm to reconcile
-- each reported line against the allocated barcodes. Lines that match cleanly
-- are confirmed; lines that cannot be matched automatically enter the exception
-- queue for operator review.
--
-- Table overview:
--   reconciliation_reports — one record per report submission from a partner
--   reconciliation_lines   — individual sale/return line items within a report
--
-- Status lifecycle:
--   reconciliation_reports.status:
--     processing → matched   (all lines matched automatically)
--     processing → exception (one or more lines need operator review)
--     exception  → reviewed  (operator resolved all exceptions)
--
--   reconciliation_lines.match_status:
--     pending → matched   (confidence >= threshold, barcode found)
--     pending → exception (confidence too low or barcode not found)
--     exception → reviewed (operator manually resolved)

-- +goose Up

-- ── reconciliation_reports ────────────────────────────────────────────────────

CREATE TABLE reconciliation_reports (
    id              uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    allocation_id   uuid        NOT NULL REFERENCES external_allocations(id) ON DELETE CASCADE,
    partner_org_id  uuid        NOT NULL REFERENCES organizations(id),
    status          text        NOT NULL DEFAULT 'processing'
                    CONSTRAINT reconciliation_reports_status_check
                    CHECK (status IN ('processing', 'matched', 'exception', 'reviewed')),
    total_lines     integer     NOT NULL DEFAULT 0,
    matched_lines   integer     NOT NULL DEFAULT 0,
    exception_lines integer     NOT NULL DEFAULT 0,
    notes           text,
    submitted_at    timestamptz NOT NULL DEFAULT now(),
    reviewed_at     timestamptz,
    reviewed_by     text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT reconciliation_reports_lines_nonneg
        CHECK (total_lines >= 0 AND matched_lines >= 0 AND exception_lines >= 0),
    CONSTRAINT reconciliation_reports_lines_sum
        CHECK (matched_lines + exception_lines <= total_lines)
);

-- Index for listing reports by allocation.
CREATE INDEX reconciliation_reports_allocation_id
    ON reconciliation_reports (allocation_id);

-- Index for listing reports by partner org.
CREATE INDEX reconciliation_reports_partner_org_id
    ON reconciliation_reports (partner_org_id);

-- Partial index for reports in the exception queue (needs operator attention).
CREATE INDEX reconciliation_reports_exception
    ON reconciliation_reports (partner_org_id, submitted_at DESC)
    WHERE status = 'exception';

COMMENT ON TABLE reconciliation_reports IS
    'Partner reconciliation report submissions. Each report is matched against '
    'the allocated quota + barcodes. Unmatched lines enter the exception queue '
    'for operator review. Feature #147 — Wave 10 External Allocations.';

-- ── reconciliation_lines ──────────────────────────────────────────────────────

CREATE TABLE reconciliation_lines (
    id               uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    report_id        uuid        NOT NULL REFERENCES reconciliation_reports(id) ON DELETE CASCADE,
    external_ref     text        NOT NULL,
    line_type        text        NOT NULL DEFAULT 'sale'
                     CONSTRAINT reconciliation_lines_type_check
                     CHECK (line_type IN ('sale', 'return')),
    qty              integer     NOT NULL DEFAULT 1
                     CONSTRAINT reconciliation_lines_qty_positive CHECK (qty > 0),
    match_status     text        NOT NULL DEFAULT 'pending'
                     CONSTRAINT reconciliation_lines_match_status_check
                     CHECK (match_status IN ('pending', 'matched', 'exception', 'reviewed')),
    confidence_score integer     NOT NULL DEFAULT 0
                     CONSTRAINT reconciliation_lines_confidence_range
                     CHECK (confidence_score >= 0 AND confidence_score <= 100),
    matched_barcode_id uuid      REFERENCES barcodes(id) ON DELETE SET NULL,
    exception_reason  text,
    operator_note     text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Index for looking up lines by report.
CREATE INDEX reconciliation_lines_report_id ON reconciliation_lines (report_id);

-- Index for exception lines within a report (operator queue view).
CREATE INDEX reconciliation_lines_exception
    ON reconciliation_lines (report_id)
    WHERE match_status = 'exception';

COMMENT ON TABLE reconciliation_lines IS
    'Individual sale/return line items from a partner reconciliation report. '
    'Each line is matched against the barcode_batch_entries for the allocation. '
    'Lines with confidence_score < 80 are queued as exceptions. Feature #147.';

COMMENT ON COLUMN reconciliation_lines.confidence_score IS
    'Auto-match confidence 0–100. Threshold 80: score >= 80 → matched, < 80 → exception.';

COMMENT ON COLUMN reconciliation_lines.external_ref IS
    'Barcode reference value as reported by the partner. Used to look up matching '
    'barcodes in the barcode_batch_entries table for this allocation.';

-- ── RBAC seeds ────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('reconciliation.submit', 'Submit a reconciliation report for an external allocation (feature #147)'),
    ('reconciliation.read',   'Read reconciliation reports and exception queues (feature #147)'),
    ('reconciliation.review', 'Review and resolve exception lines in reconciliation reports (feature #147)')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('reconciliation.submit', 'reconciliation.read', 'reconciliation.review')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('reconciliation.submit', 'reconciliation.read')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'platform_operator'
  AND  p.name IN ('reconciliation.read', 'reconciliation.review')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('reconciliation.submit', 'reconciliation.read', 'reconciliation.review')
);

DELETE FROM permissions
WHERE name IN ('reconciliation.submit', 'reconciliation.read', 'reconciliation.review');

DROP TABLE IF EXISTS reconciliation_lines;
DROP TABLE IF EXISTS reconciliation_reports;
