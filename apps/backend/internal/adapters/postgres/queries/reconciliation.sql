-- reconciliation.sql — typed sqlc queries for reconciliation_reports and reconciliation_lines.
-- Feature #147 — External Reconciliation (Wave 10 External Allocations).
--
-- Flow:
--   Partner submits lines via POST /v1/reconciliation/reports.
--   Platform runs auto-match: each line is scored against barcode_batch_entries
--   for the allocation's partner_org barcodes.
--   Lines with confidence >= 80 → matched; < 80 → exception.
--   Report status set to 'matched' (all lines OK) or 'exception' (some need review).
--   Operator reviews exceptions via PATCH /v1/reconciliation/reports/{id}/review.

-- ── reconciliation_reports ────────────────────────────────────────────────────

-- name: InsertReconciliationReport :one
-- Creates a new reconciliation report in 'processing' status.
INSERT INTO reconciliation_reports (
    allocation_id, partner_org_id, total_lines, matched_lines, exception_lines,
    status, notes
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, allocation_id, partner_org_id, status, total_lines, matched_lines,
          exception_lines, notes, submitted_at, reviewed_at, reviewed_by,
          created_at, updated_at;

-- name: GetReconciliationReportByID :one
-- Fetches a single reconciliation report by UUID.
-- Returns pgx.ErrNoRows when not found.
SELECT id, allocation_id, partner_org_id, status, total_lines, matched_lines,
       exception_lines, notes, submitted_at, reviewed_at, reviewed_by,
       created_at, updated_at
FROM   reconciliation_reports
WHERE  id = $1;

-- name: ListReconciliationReportsByAllocation :many
-- Lists reconciliation reports for an allocation, newest first.
SELECT id, allocation_id, partner_org_id, status, total_lines, matched_lines,
       exception_lines, notes, submitted_at, reviewed_at, reviewed_by,
       created_at, updated_at
FROM   reconciliation_reports
WHERE  allocation_id = $1
ORDER BY submitted_at DESC;

-- name: ListReconciliationExceptions :many
-- Lists all reports in 'exception' status for a partner org (operator queue view).
-- Ordered by submitted_at DESC so oldest unresolved exceptions are visible last.
SELECT id, allocation_id, partner_org_id, status, total_lines, matched_lines,
       exception_lines, notes, submitted_at, reviewed_at, reviewed_by,
       created_at, updated_at
FROM   reconciliation_reports
WHERE  partner_org_id = $1
  AND  status = 'exception'
ORDER BY submitted_at ASC;

-- name: UpdateReconciliationReportStatus :one
-- Transitions a reconciliation report to the given status.
-- Optionally sets reviewed_at and reviewed_by for the 'reviewed' transition.
UPDATE reconciliation_reports
SET    status      = $2,
       reviewed_at = CASE WHEN $2 = 'reviewed' THEN now() ELSE reviewed_at END,
       reviewed_by = CASE WHEN $2 = 'reviewed' AND $3::text IS NOT NULL THEN $3::text ELSE reviewed_by END,
       updated_at  = now()
WHERE  id = $1
RETURNING id, allocation_id, partner_org_id, status, total_lines, matched_lines,
          exception_lines, notes, submitted_at, reviewed_at, reviewed_by,
          created_at, updated_at;

-- ── reconciliation_lines ──────────────────────────────────────────────────────

-- name: InsertReconciliationLine :one
-- Creates a single reconciliation line item.
INSERT INTO reconciliation_lines (
    report_id, external_ref, line_type, qty,
    match_status, confidence_score, matched_barcode_id, exception_reason
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, report_id, external_ref, line_type, qty,
          match_status, confidence_score, matched_barcode_id,
          exception_reason, operator_note, created_at, updated_at;

-- name: ListReconciliationLinesByReport :many
-- Lists all lines for a report, ordered by creation time.
SELECT id, report_id, external_ref, line_type, qty,
       match_status, confidence_score, matched_barcode_id,
       exception_reason, operator_note, created_at, updated_at
FROM   reconciliation_lines
WHERE  report_id = $1
ORDER BY created_at ASC;

-- name: ListExceptionLinesByReport :many
-- Lists only exception lines for a report (operator review queue).
SELECT id, report_id, external_ref, line_type, qty,
       match_status, confidence_score, matched_barcode_id,
       exception_reason, operator_note, created_at, updated_at
FROM   reconciliation_lines
WHERE  report_id = $1
  AND  match_status = 'exception'
ORDER BY created_at ASC;

-- name: UpdateReconciliationLineReview :one
-- Operator resolves an exception line, setting it to 'reviewed'.
UPDATE reconciliation_lines
SET    match_status  = 'reviewed',
       operator_note = $2,
       updated_at    = now()
WHERE  id = $1
RETURNING id, report_id, external_ref, line_type, qty,
          match_status, confidence_score, matched_barcode_id,
          exception_reason, operator_note, created_at, updated_at;

-- name: CountExceptionLinesByReport :one
-- Counts unresolved exception lines for a report. Used to determine if all
-- exceptions have been reviewed and the report can transition to 'reviewed'.
SELECT COUNT(*) AS exception_count
FROM   reconciliation_lines
WHERE  report_id = $1
  AND  match_status = 'exception';

-- name: LookupBarcodeByExternalRef :one
-- Looks up a barcode by its external_ref within the context of a partner's
-- barcode batch entries. Returns the barcode value and ID for confidence scoring.
-- Joins barcode_batch_entries → barcode_batches → barcodes to find active matches.
SELECT b.id AS barcode_id, bbe.external_ref, bb.allocation_id
FROM   barcode_batch_entries bbe
JOIN   barcode_batches bb ON bb.id = bbe.batch_id
LEFT JOIN barcodes b ON b.value = bbe.external_ref
WHERE  bbe.external_ref = $1
  AND  bb.allocation_id = $2
  AND  bb.status = 'active'
  AND  bbe.status = 'active'
LIMIT 1;
