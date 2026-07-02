// reconciliation.go declares the shared request/response types and row
// mappers for the external reconciliation HTTP API (feature #147).
//
// The HTTP handlers themselves live in sibling files in this same package:
//
//   - reconciliation_submit.go  – POST  /v1/reconciliation/reports
//   - reconciliation_query.go   – GET   /v1/reconciliation/reports/{id}
//     – GET   /v1/reconciliation/exceptions
//   - reconciliation_review.go  – PATCH /v1/reconciliation/reports/{id}/review
//     – PATCH /v1/reconciliation/reports/{id}/lines/{line_id}
//
// External reconciliation allows partner organisations to submit sales/returns
// reports against their active external allocation quota. The platform runs an
// auto-match algorithm that scores each reported line against the allocated
// barcodes in the system. Lines that match with confidence >= 80 are marked
// "matched"; lines below threshold are queued as "exceptions" for operator
// review.
//
// # Auto-match algorithm
//
// For each submitted line:
//  1. Look up the external_ref in barcode_batch_entries for the allocation
//     (LookupBarcodeByExternalRef).
//  2. If a match is found AND a barcodes row exists: confidence = 100 → matched.
//  3. If a batch entry is found but barcodes row is absent: confidence = 60 → exception.
//  4. If no batch entry found at all: confidence = 0 → exception.
//
// # Exception queue
//
// After all lines are processed:
//   - If all lines matched → report.status = "matched"
//   - If any line is an exception → report.status = "exception"
//
// Operators can review exceptions via PATCH /v1/reconciliation/reports/{id}/review.
// When all exception lines are resolved, the report transitions to "reviewed".
//
// # Endpoints (all require JWT auth)
//
//	POST  /v1/reconciliation/reports                        — submit report (reconciliation.submit)
//	GET   /v1/reconciliation/reports/{id}                   — get report + lines (reconciliation.read)
//	GET   /v1/reconciliation/exceptions                     — exception queue (reconciliation.review)
//	PATCH /v1/reconciliation/reports/{id}/review            — mark report reviewed (reconciliation.review)
//	PATCH /v1/reconciliation/reports/{id}/lines/{line_id}   — resolve exception line (reconciliation.review)
package hreconciliation

import (
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response types (package-private — only used within hreconciliation)
// ─────────────────────────────────────────────────────────────────────────────

// reconciliationLineInput is a single line item in a partner reconciliation report.
type reconciliationLineInput struct {
	ExternalRef string `json:"external_ref"`
	LineType    string `json:"line_type"` // "sale" or "return"
	Qty         int32  `json:"qty"`
}

// submitReconciliationReportRequest is the request body for POST /v1/reconciliation/reports.
type submitReconciliationReportRequest struct {
	AllocationID string                    `json:"allocation_id"`
	Lines        []reconciliationLineInput `json:"lines"`
	Notes        *string                   `json:"notes"`
}

// reviewExceptionLineRequest is the request body for resolving a single exception line.
type reviewExceptionLineRequest struct {
	OperatorNote *string `json:"operator_note"`
}

// reviewReportRequest is the request body for PATCH /v1/reconciliation/reports/{id}/review.
type reviewReportRequest struct {
	ReviewedBy *string `json:"reviewed_by"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers (exported so reconciliation_shims.go can re-export the
// original lowercase names to package httpserver)
// ─────────────────────────────────────────────────────────────────────────────

// ReportFromRow converts a ReconciliationReportRow to a JSON map.
func ReportFromRow(r gen.ReconciliationReportRow) map[string]any {
	return map[string]any{
		"id":              r.ID,
		"allocation_id":   r.AllocationID,
		"partner_org_id":  r.PartnerOrgID,
		"status":          r.Status,
		"total_lines":     r.TotalLines,
		"matched_lines":   r.MatchedLines,
		"exception_lines": r.ExceptionLines,
		"notes":           r.Notes,
		"submitted_at":    r.SubmittedAt,
		"reviewed_at":     r.ReviewedAt,
		"reviewed_by":     r.ReviewedBy,
		"created_at":      r.CreatedAt,
		"updated_at":      r.UpdatedAt,
	}
}

// LineFromRow converts a ReconciliationLineRow to a JSON map.
func LineFromRow(r gen.ReconciliationLineRow) map[string]any {
	return map[string]any{
		"id":                 r.ID,
		"report_id":          r.ReportID,
		"external_ref":       r.ExternalRef,
		"line_type":          r.LineType,
		"qty":                r.Qty,
		"match_status":       r.MatchStatus,
		"confidence_score":   r.ConfidenceScore,
		"matched_barcode_id": r.MatchedBarcodeID,
		"exception_reason":   r.ExceptionReason,
		"operator_note":      r.OperatorNote,
		"created_at":         r.CreatedAt,
		"updated_at":         r.UpdatedAt,
	}
}

// LinesFromRows converts a slice of ReconciliationLineRow to JSON maps.
func LinesFromRows(rows []gen.ReconciliationLineRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, LineFromRow(r))
	}
	return out
}
