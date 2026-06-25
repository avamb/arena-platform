// reconciliation.go implements the external reconciliation HTTP API (feature #147).
//
// External reconciliation allows partner organisations to submit sales/returns
// reports against their active external allocation quota. The platform runs an
// auto-match algorithm that scores each reported line against the allocated
// barcodes in the system. Lines that match with confidence >= 80 are marked
// "matched"; lines below threshold are queued as "exceptions" for operator review.
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
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// reconciliationConfidenceThreshold is the minimum confidence score for
	// a line to be auto-matched. Lines below this threshold become exceptions.
	reconciliationConfidenceThreshold = 80
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response types
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
// POST /v1/reconciliation/reports
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleSubmitReconciliationReport(w http.ResponseWriter, r *http.Request) {
	if s.reconciliationQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req submitReconciliationReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate allocation_id.
	allocationID, err := uuid.Parse(req.AllocationID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_allocation_id", "allocation_id must be a valid UUID", r,
		))
		return
	}

	// Validate lines.
	if len(req.Lines) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.no_lines", "at least one line is required", r,
		))
		return
	}
	for i, line := range req.Lines {
		if strings.TrimSpace(line.ExternalRef) == "" {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"reconciliation.invalid_line",
				"line "+strconv.Itoa(i)+": external_ref is required",
				r,
			))
			return
		}
		if line.LineType != "sale" && line.LineType != "return" {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"reconciliation.invalid_line_type",
				"line "+strconv.Itoa(i)+": line_type must be 'sale' or 'return'",
				r,
			))
			return
		}
		if line.Qty <= 0 {
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"reconciliation.invalid_qty",
				"line "+strconv.Itoa(i)+": qty must be a positive integer",
				r,
			))
			return
		}
	}

	// Verify the allocation exists.
	ctx := r.Context()
	allocation, err := s.reconciliationQueries.GetExternalAllocationByID(ctx, allocationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"reconciliation.allocation_not_found", "external allocation not found", r,
			))
			return
		}
		s.logger.Error("reconciliation: get allocation failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.allocation_fetch_failed", "failed to retrieve allocation", r,
		))
		return
	}

	// Only active/disputed allocations may be reconciled.
	if allocation.Status != "active" && allocation.Status != "disputed" {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"reconciliation.invalid_allocation_status",
			"reconciliation reports can only be submitted for active or disputed allocations",
			r,
		))
		return
	}

	// ── Auto-match loop ───────────────────────────────────────────────────────

	type lineScore struct {
		externalRef     string
		lineType        string
		qty             int32
		matchStatus     string
		confidenceScore int32
		barcodeID       *uuid.UUID
		reason          *string
	}

	scores := make([]lineScore, 0, len(req.Lines))
	matchedCount := int32(0)
	exceptionCount := int32(0)

	for _, line := range req.Lines {
		ls := lineScore{
			externalRef: line.ExternalRef,
			lineType:    line.LineType,
			qty:         line.Qty,
		}

		// Look up barcode by external_ref within the allocation's batches.
		lookup, lookupErr := s.reconciliationQueries.LookupBarcodeByExternalRef(ctx, line.ExternalRef, allocationID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			s.logger.Warn("reconciliation: barcode lookup error",
				slog.String("external_ref", line.ExternalRef),
				slog.String("error", lookupErr.Error()),
			)
		}

		switch {
		case lookupErr == nil && lookup.BarcodeID != nil:
			// Best case: batch entry found AND barcode registered in barcodes table.
			// confidence = 100 → matched (above threshold 80).
			ls.matchStatus = "matched"
			ls.confidenceScore = 100
			ls.barcodeID = lookup.BarcodeID
			matchedCount++

		case lookupErr == nil && lookup.BarcodeID == nil:
			// Batch entry found but barcode not yet registered for scanning.
			// confidence = 60 → exception (below threshold 80).
			reason := "barcode_entry_found_no_scan_registration"
			ls.matchStatus = "exception"
			ls.confidenceScore = 60
			ls.reason = &reason
			exceptionCount++

		default:
			// No matching batch entry found for this external_ref.
			// confidence = 0 → exception.
			reason := "barcode_not_found_in_allocation"
			ls.matchStatus = "exception"
			ls.confidenceScore = 0
			ls.reason = &reason
			exceptionCount++
		}

		scores = append(scores, ls)
	}

	// Determine overall report status.
	reportStatus := "matched"
	if exceptionCount > 0 {
		reportStatus = "exception"
	}

	totalLines := int32(len(req.Lines)) //nolint:gosec // req.Lines length capped upstream by body-size limit

	// ── Persist report + lines in a transaction ───────────────────────────────
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rq := s.reconciliationQueries.WithTx(tx)

	report, err := rq.InsertReconciliationReport(
		ctx,
		allocationID,
		allocation.PartnerOrgID,
		totalLines,
		matchedCount,
		exceptionCount,
		reportStatus,
		req.Notes,
	)
	if err != nil {
		s.logger.Error("reconciliation: insert report failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.insert_failed", "failed to create reconciliation report", r,
		))
		return
	}

	// Insert all lines.
	insertedLines := make([]gen.ReconciliationLineRow, 0, len(scores))
	for _, ls := range scores {
		ln, err := rq.InsertReconciliationLine(
			ctx,
			report.ID,
			ls.externalRef,
			ls.lineType,
			ls.qty,
			ls.matchStatus,
			ls.confidenceScore,
			ls.barcodeID,
			ls.reason,
		)
		if err != nil {
			s.logger.Error("reconciliation: insert line failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"reconciliation.line_insert_failed", "failed to persist reconciliation line", r,
			))
			return
		}
		insertedLines = append(insertedLines, ln)
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.commit_failed", "failed to commit reconciliation transaction", r,
		))
		return
	}

	s.logger.Info("reconciliation: report submitted",
		slog.String("report_id", report.ID.String()),
		slog.String("allocation_id", allocationID.String()),
		slog.String("status", reportStatus),
		slog.Int("total_lines", int(totalLines)),
		slog.Int("matched_lines", int(matchedCount)),
		slog.Int("exception_lines", int(exceptionCount)),
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"report": reconciliationReportFromRow(report),
		"lines":  reconciliationLinesFromRows(insertedLines),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/reconciliation/reports/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleGetReconciliationReport(w http.ResponseWriter, r *http.Request) {
	if s.reconciliationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	report, err := s.reconciliationQueries.GetReconciliationReportByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"reconciliation.not_found", "reconciliation report not found", r,
			))
			return
		}
		s.logger.Error("reconciliation: get report failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.get_failed", "failed to retrieve reconciliation report", r,
		))
		return
	}

	lines, err := s.reconciliationQueries.ListReconciliationLinesByReport(ctx, id)
	if err != nil {
		s.logger.Error("reconciliation: list lines failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.lines_failed", "failed to retrieve reconciliation lines", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"report": reconciliationReportFromRow(report),
		"lines":  reconciliationLinesFromRows(lines),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/reconciliation/exceptions
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleListReconciliationExceptions(w http.ResponseWriter, r *http.Request) {
	if s.reconciliationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// org_id query param is required so the operator can scope the queue.
	orgIDStr := r.URL.Query().Get("org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_org_id", "org_id query parameter must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	reports, err := s.reconciliationQueries.ListReconciliationExceptions(ctx, orgID)
	if err != nil {
		s.logger.Error("reconciliation: list exceptions failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.exceptions_failed", "failed to retrieve exception queue", r,
		))
		return
	}

	reportList := make([]map[string]any, 0, len(reports))
	for _, rep := range reports {
		reportList = append(reportList, reconciliationReportFromRow(rep))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"exceptions": reportList,
		"total":      len(reportList),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/reconciliation/reports/{id}/review
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleReviewReconciliationReport(w http.ResponseWriter, r *http.Request) {
	if s.reconciliationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	var req reviewReportRequest
	_ = json.Unmarshal(body, &req)

	ctx := r.Context()

	// Verify report exists and is in 'exception' status.
	current, err := s.reconciliationQueries.GetReconciliationReportByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"reconciliation.not_found", "reconciliation report not found", r,
			))
			return
		}
		s.logger.Error("reconciliation: get report for review failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.get_failed", "failed to retrieve reconciliation report", r,
		))
		return
	}

	if current.Status == "reviewed" {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"reconciliation.already_reviewed", "reconciliation report is already reviewed", r,
		))
		return
	}
	if current.Status != "exception" {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"reconciliation.not_in_exception",
			"only reports in 'exception' status can be reviewed",
			r,
		))
		return
	}

	// Check if any exception lines remain unresolved.
	remaining, err := s.reconciliationQueries.CountExceptionLinesByReport(ctx, id)
	if err != nil {
		s.logger.Error("reconciliation: count exceptions failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.count_failed", "failed to count exception lines", r,
		))
		return
	}
	if remaining > 0 {
		writeJSON(w, http.StatusConflict, errorEnvelope(
			"reconciliation.exceptions_pending",
			"all exception lines must be resolved before marking the report as reviewed",
			r,
		))
		return
	}

	report, err := s.reconciliationQueries.UpdateReconciliationReportStatus(ctx, id, "reviewed", req.ReviewedBy)
	if err != nil {
		s.logger.Error("reconciliation: update report status failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.update_failed", "failed to update reconciliation report status", r,
		))
		return
	}

	s.logger.Info("reconciliation: report reviewed",
		slog.String("report_id", id.String()),
		slog.Any("reviewed_by", req.ReviewedBy),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"report": reconciliationReportFromRow(report),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/reconciliation/reports/{id}/lines/{line_id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleResolveReconciliationException(w http.ResponseWriter, r *http.Request) {
	if s.reconciliationQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	lineIDStr := chi.URLParam(r, "line_id")
	lineID, err := uuid.Parse(lineIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"reconciliation.invalid_line_id", "line_id must be a valid UUID", r,
		))
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	var req reviewExceptionLineRequest
	_ = json.Unmarshal(body, &req)

	ctx := r.Context()
	line, err := s.reconciliationQueries.UpdateReconciliationLineReview(ctx, lineID, req.OperatorNote)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"reconciliation.line_not_found", "reconciliation line not found", r,
			))
			return
		}
		s.logger.Error("reconciliation: resolve exception failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"reconciliation.resolve_failed", "failed to resolve exception line", r,
		))
		return
	}

	s.logger.Info("reconciliation: exception line resolved",
		slog.String("line_id", lineID.String()),
		slog.String("match_status", line.MatchStatus),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"line": reconciliationLineFromRow(line),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Response helpers
// ─────────────────────────────────────────────────────────────────────────────

// reconciliationReportFromRow converts a ReconciliationReportRow to a JSON map.
func reconciliationReportFromRow(r gen.ReconciliationReportRow) map[string]any {
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

// reconciliationLineFromRow converts a ReconciliationLineRow to a JSON map.
func reconciliationLineFromRow(r gen.ReconciliationLineRow) map[string]any {
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

// reconciliationLinesFromRows converts a slice of ReconciliationLineRow to JSON maps.
func reconciliationLinesFromRows(rows []gen.ReconciliationLineRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, reconciliationLineFromRow(r))
	}
	return out
}
