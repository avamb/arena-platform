-- 0098_customer_imports.sql — W1-C7a (feature #519, spec §3.7 / §12.4).
--
-- Storage for the platform.superadmin customer-database import tool (owner
-- decision #11): an uploaded Bil24/WooCommerce/GSheets/Brevo customer/order
-- export is parsed row by row, each row resolved through customers.Resolve
-- (0091) without auto-merge, and the outcome recorded per row for a
-- dry-run preview and an idempotent apply. customer_imports.org_id is NULL
-- for a multi-org file (org resolved per row via `mapping`); NOT NULL pins
-- every row to one organization regardless of `mapping`.
--
-- Note on migration numbering: the spec text names this file
-- "0096_customer_imports.sql", but 0096 was already claimed by
-- 0096_api_keys.sql (a sibling C1 sub-feature landed first) and head is now
-- 0097 — this file takes the next free number, 0098, per AGENTS.md's
-- "Head() just picks the max numeric filename prefix" note.

-- +goose Up
CREATE TABLE customer_imports (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid REFERENCES organizations(id),      -- NULL = multi-org file
    source_label    text NOT NULL,                          -- 'bil24_orders_json' | 'wc_customers_csv' | 'gsheets_csv' | 'brevo_csv' | 'generic_csv'
    file_media_id   uuid NOT NULL REFERENCES media_objects(id),
    mapping         jsonb NOT NULL DEFAULT '{}'::jsonb,      -- spec §12.4
    legal_basis     text NOT NULL CHECK (legal_basis IN ('organizer_contract','legitimate_interest','explicit_consent')),
    status          text NOT NULL DEFAULT 'uploaded' CHECK (status IN
                      ('uploaded','dry_run_running','dry_run_done','applying','applied','failed')),
    dry_run_report  jsonb,
    apply_report    jsonb,
    created_by      uuid NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE customer_imports IS
    'W1-C7a: one uploaded customer/order export awaiting dry-run and/or '
    'apply via the customer.import worker job (spec §12.4). status tracks '
    'the job lifecycle; dry_run_report/apply_report hold the {rows, '
    'created, matched, merge_candidates, skipped, by_org, errors} summary.';

CREATE INDEX customer_imports_org_idx ON customer_imports (org_id);

CREATE TABLE customer_import_rows (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    import_id            uuid NOT NULL REFERENCES customer_imports(id) ON DELETE CASCADE,
    row_no               int  NOT NULL,
    row_hash             text NOT NULL,                     -- sha256 of the normalised row -> idempotency
    raw                  jsonb NOT NULL,
    resolved_customer_id uuid REFERENCES customers(id),
    org_id               uuid REFERENCES organizations(id),
    action               text CHECK (action IN ('created','matched','merge_candidate','skipped')),
    reason               text,
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (import_id, row_hash)
);

COMMENT ON TABLE customer_import_rows IS
    'W1-C7a: per-row outcome of an APPLIED customer_imports run (spec '
    '§12.4). Only apply mode writes rows here — dry_run is read-only and '
    'reports through customer_imports.dry_run_report instead, so a '
    'dry-run preview never blocks the following apply from actually '
    'creating/matching customers. The UNIQUE(import_id, row_hash) '
    'constraint is the re-apply idempotency guard: a second apply of the '
    'same file finds every row already recorded here and reports '
    '"matched" without writing anything else.';

CREATE INDEX customer_import_rows_import_idx ON customer_import_rows (import_id);
CREATE INDEX customer_import_rows_customer_idx ON customer_import_rows (resolved_customer_id);

-- +goose Down
DROP INDEX IF EXISTS customer_import_rows_customer_idx;
DROP INDEX IF EXISTS customer_import_rows_import_idx;
DROP TABLE IF EXISTS customer_import_rows;

DROP INDEX IF EXISTS customer_imports_org_idx;
DROP TABLE IF EXISTS customer_imports;
