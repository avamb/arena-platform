-- 0033_billing_ledger.sql — service billing ledger (feature #161).
--
-- Implements the platform-level billing subsystem that tracks per-org
-- service fees (distinct from customer ticket payments).
--
-- Tables:
--   tariffs         — versioned tariff plans (per-ticket, per-event, monthly fees)
--   usage_records   — monthly usage metrics per org (tickets sold, etc.)
--   invoices        — billing invoice headers per org per period
--   invoice_lines   — line items for each invoice (links to tariff version used)
--
-- Invoice state machine:
--   draft → issued → paid (terminal)
--   draft → void   (terminal)
--   issued → void  (terminal)

-- +goose Up

-- ── tariffs ───────────────────────────────────────────────────────────────────
--
-- Versioned tariff plans. org_id = NULL means the global default tariff.
-- org_id != NULL means an org-specific override tariff.
-- effective_from marks when this version takes effect.
-- Only one version per (org_id, effective_from) is allowed.
-- The active tariff for a billing period is the one with the largest
-- effective_from that is <= the period start date.

CREATE TABLE tariffs (
    id                      uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id                  uuid,           -- NULL = global default
    plan_name               text        NOT NULL DEFAULT 'standard',
    effective_from          date        NOT NULL,
    per_ticket_fee_minor    bigint      NOT NULL DEFAULT 0
                            CONSTRAINT tariffs_per_ticket_fee_non_negative CHECK (per_ticket_fee_minor >= 0),
    per_event_fee_minor     bigint      NOT NULL DEFAULT 0
                            CONSTRAINT tariffs_per_event_fee_non_negative CHECK (per_event_fee_minor >= 0),
    monthly_fee_minor       bigint      NOT NULL DEFAULT 0
                            CONSTRAINT tariffs_monthly_fee_non_negative CHECK (monthly_fee_minor >= 0),
    currency                text        NOT NULL DEFAULT 'EUR',
    notes                   text,
    created_at              timestamptz NOT NULL DEFAULT now(),
    created_by              text,
    UNIQUE (org_id, effective_from)
);

-- Index for looking up the active tariff as of a given date (for all orgs).
CREATE INDEX tariffs_org_id_effective_from ON tariffs (org_id, effective_from DESC);

-- ── usage_records ─────────────────────────────────────────────────────────────
--
-- Cumulative per-org monthly usage counters (one row per org per billing period).
-- billing_period is 'YYYY-MM' (e.g. '2026-06'). Counters are incremented by
-- usage capture hooks (ticket issuance, complimentary issuance, event publish).
-- UPSERT pattern: INSERT ... ON CONFLICT DO UPDATE SET ... += delta.

CREATE TABLE usage_records (
    id                      uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id                  uuid        NOT NULL,
    billing_period          text        NOT NULL,   -- 'YYYY-MM'
    tickets_sold            bigint      NOT NULL DEFAULT 0,
    complimentary_issued    bigint      NOT NULL DEFAULT 0,
    events_published        bigint      NOT NULL DEFAULT 0,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, billing_period)
);

-- Index for month-end batch: find all orgs with usage in a given period.
CREATE INDEX usage_records_billing_period ON usage_records (billing_period);

-- ── invoices ──────────────────────────────────────────────────────────────────
--
-- One invoice per org per billing period. State machine: draft → issued → paid.
-- Voiding is allowed from draft or issued states.

CREATE TABLE invoices (
    id                      uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id                  uuid        NOT NULL,
    billing_period          text        NOT NULL,   -- 'YYYY-MM'
    state                   text        NOT NULL DEFAULT 'draft'
                            CONSTRAINT invoices_state_check CHECK (state IN (
                                'draft', 'issued', 'paid', 'void'
                            )),
    total_amount_minor      bigint      NOT NULL DEFAULT 0,
    currency                text        NOT NULL DEFAULT 'EUR',
    issued_at               timestamptz,
    paid_at                 timestamptz,
    voided_at               timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, billing_period)
);

-- Index for looking up all invoices for an org.
CREATE INDEX invoices_org_id ON invoices (org_id);

-- Index for month-end batch: find invoices in a specific state.
CREATE INDEX invoices_state_idx ON invoices (state);

-- ── invoice_lines ─────────────────────────────────────────────────────────────
--
-- Line items for each invoice. Each line references the tariff version that
-- was used to calculate it (for auditability — even if the tariff is later versioned).

CREATE TABLE invoice_lines (
    id                      uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    invoice_id              uuid        NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    tariff_id               uuid        REFERENCES tariffs(id),
    description             text        NOT NULL,
    quantity                bigint      NOT NULL DEFAULT 1,
    unit_amount_minor       bigint      NOT NULL DEFAULT 0,
    total_amount_minor      bigint      NOT NULL DEFAULT 0,
    currency                text        NOT NULL DEFAULT 'EUR',
    created_at              timestamptz NOT NULL DEFAULT now()
);

-- Index for listing lines by invoice.
CREATE INDEX invoice_lines_invoice_id ON invoice_lines (invoice_id);

-- ── RBAC seeds ────────────────────────────────────────────────────────────────
--
-- Permissions:
--   billing.read   — read tariffs, usage records, invoices
--   billing.admin  — create/update tariffs, trigger month-end batch, update invoice state
--
-- Role grants:
--   admin     → billing.read, billing.admin
--   org_admin → billing.read

INSERT INTO permissions (name, description) VALUES
    ('billing.read',  'Read billing tariffs, usage records, and invoices (feature #161)'),
    ('billing.admin', 'Manage billing tariffs and trigger invoice generation (feature #161)')
ON CONFLICT (name) DO NOTHING;

-- Grant all billing permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('billing.read', 'billing.admin')
ON CONFLICT DO NOTHING;

-- Grant billing.read to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('billing.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('billing.read', 'billing.admin')
);

DELETE FROM permissions WHERE name IN ('billing.read', 'billing.admin');

DROP TABLE IF EXISTS invoice_lines;
DROP TABLE IF EXISTS invoices;
DROP TABLE IF EXISTS usage_records;
DROP TABLE IF EXISTS tariffs;
