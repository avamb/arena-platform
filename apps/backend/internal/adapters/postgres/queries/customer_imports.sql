-- customer_imports.sql — sqlc query definitions for the customer-import
-- job (migration 0098, feature #519, spec §3.7 / §12.4).
--
-- customer_imports / customer_import_rows back the platform.superadmin
-- "import a customer/order export" tool: an uploaded file is parsed row by
-- row and each row resolved through customers.Resolve (0091). dry_run mode
-- NEVER writes to customer_import_rows — see the migration's table comment
-- for the full contract. Two more queries at the bottom
-- (InsertCustomerConsent, UpsertCustomerOrgLinkStats) extend the customers
-- aggregate introduced by migration 0091 with writes the apply path needs
-- that no earlier feature required.

-- name: InsertCustomerImport :one
-- Creates a customer_imports row for POST /v1/admin/customer-imports
-- (feature #520, W1-C7b). Status always starts at 'uploaded' (migration
-- 0098's customer_imports_status_check) — RunImport advances it as the
-- dry-run/apply flow progresses.
INSERT INTO customer_imports
    (org_id, source_label, file_media_id, mapping, legal_basis, status, created_by)
VALUES ($1, $2, $3, $4::jsonb, $5, 'uploaded', $6)
RETURNING id, org_id, source_label, file_media_id, mapping, legal_basis,
          status, dry_run_report, apply_report, created_by, created_at, updated_at;

-- name: GetCustomerImportByID :one
-- Loads a customer_imports row for the job handler. Returns pgx.ErrNoRows
-- when absent.
SELECT id, org_id, source_label, file_media_id, mapping, legal_basis,
       status, dry_run_report, apply_report, created_by, created_at, updated_at
FROM   customer_imports
WHERE  id = $1;

-- name: UpdateCustomerImportStatus :exec
-- Bumps status (and updated_at) with no report change — used for the
-- 'dry_run_running' / 'applying' transitional states at the start of a run.
UPDATE customer_imports
SET    status     = $2,
       updated_at = now()
WHERE  id = $1;

-- name: SetCustomerImportDryRunReport :exec
-- Persists the dry-run summary and flips status to 'dry_run_done'. Never
-- called from inside the dry-run transaction itself — the transaction is
-- always rolled back; this write happens afterwards, outside it.
UPDATE customer_imports
SET    dry_run_report = $2::jsonb,
       status         = 'dry_run_done',
       updated_at     = now()
WHERE  id = $1;

-- name: SetCustomerImportApplyReport :exec
-- Persists the apply summary and flips status to 'applied'.
UPDATE customer_imports
SET    apply_report = $2::jsonb,
       status       = 'applied',
       updated_at   = now()
WHERE  id = $1;

-- name: MarkCustomerImportFailed :exec
-- Flips status to 'failed' when the job handler returns before producing a
-- report (parse error, missing media, unresolvable org, etc).
UPDATE customer_imports
SET    status     = 'failed',
       updated_at = now()
WHERE  id = $1;

-- name: InsertCustomerImportRow :one
-- Records the outcome of one applied row. ON CONFLICT (import_id,
-- row_hash) DO NOTHING makes a second apply of the same file a no-op at
-- the database level; the Go caller detects the conflict (no row
-- returned) and falls back to GetCustomerImportRowByHash to report
-- "already applied" without re-running the resolver.
INSERT INTO customer_import_rows
    (import_id, row_no, row_hash, raw, resolved_customer_id, org_id, action, reason)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
ON CONFLICT (import_id, row_hash) DO NOTHING
RETURNING id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
          action, reason, created_at;

-- name: GetCustomerImportRowByHash :one
-- Idempotency lookup for a re-applied row: if a row with this (import_id,
-- row_hash) already exists, the apply loop reports it as already-applied
-- without touching the resolver, consents or org-link counters again.
-- Returns pgx.ErrNoRows when this exact row has not been applied before.
SELECT id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
       action, reason, created_at
FROM   customer_import_rows
WHERE  import_id = $1
  AND  row_hash = $2;

-- name: ListCustomerImportRows :many
-- Lists customer_import_rows for GET /v1/admin/customer-imports/{id}/rows,
-- optionally filtered by action (sqlc.narg pattern: pass an empty string for
-- "no filter" since action is nullable and empty is not a valid action
-- value). Ordered by row_no for stable pagination-free listing.
SELECT id, import_id, row_no, row_hash, raw, resolved_customer_id, org_id,
       action, reason, created_at
FROM   customer_import_rows
WHERE  import_id = $1
  AND  ($2::text = '' OR action = $2::text)
ORDER BY row_no;

-- ─── Customer-aggregate additions the apply path needs (migration 0091) ────

-- name: InsertCustomerConsent :exec
-- Records a consent grant for (customer, org, kind). First import wins:
-- ON CONFLICT DO NOTHING means a re-applied row (or a second import that
-- happens to touch the same customer/org/kind) never overwrites
-- given_at/source of an existing consent record. customer_consents has no
-- "verified" concept — consent verification is not modelled here, and
-- import NEVER calls customers.MarkVerified on the identities it attaches,
-- so a marketing consent sourced from an import is never conflated with a
-- verified identity.
INSERT INTO customer_consents (customer_id, org_id, kind, given_at, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (customer_id, org_id, kind) DO NOTHING;

-- name: UpsertCustomerOrgLinkStats :exec
-- Ensures the (customer, org) rollup exists and bumps its order/ticket
-- counters and first/last-order timestamps for one imported order. Unlike
-- UpsertCustomerOrgLink (0091, insert-only-if-absent), this query is
-- meant to be called once per imported order row and always advances the
-- counters — orders_count/tickets_count are cumulative, first_order_at
-- takes the earliest date seen and last_order_at the latest.
INSERT INTO customer_org_links
    (customer_id, org_id, source, first_order_at, last_order_at, orders_count, tickets_count)
VALUES ($1, $2, 'import', $3, $3, 1, $4)
ON CONFLICT (customer_id, org_id) DO UPDATE
SET orders_count  = customer_org_links.orders_count + 1,
    tickets_count = customer_org_links.tickets_count + EXCLUDED.tickets_count,
    first_order_at = LEAST(customer_org_links.first_order_at, EXCLUDED.first_order_at),
    last_order_at  = GREATEST(customer_org_links.last_order_at, EXCLUDED.last_order_at);
