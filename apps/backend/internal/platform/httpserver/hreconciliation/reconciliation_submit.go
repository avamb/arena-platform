// reconciliation_submit.go implements POST /v1/reconciliation/reports
// (feature #147).
package hreconciliation

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

func (h *Handler) HandleSubmitReport(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.read_body_failed", "failed to read request body", r,
		))
		return
	}

	var req submitReconciliationReportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate allocation_id.
	allocationID, err := uuid.Parse(req.AllocationID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.invalid_allocation_id", "allocation_id must be a valid UUID", r,
		))
		return
	}

	// Validate lines.
	if len(req.Lines) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.no_lines", "at least one line is required", r,
		))
		return
	}
	for i, line := range req.Lines {
		if strings.TrimSpace(line.ExternalRef) == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"reconciliation.invalid_line",
				"line "+strconv.Itoa(i)+": external_ref is required",
				r,
			))
			return
		}
		if line.LineType != "sale" && line.LineType != "return" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"reconciliation.invalid_line_type",
				"line "+strconv.Itoa(i)+": line_type must be 'sale' or 'return'",
				r,
			))
			return
		}
		if line.Qty <= 0 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"reconciliation.invalid_qty",
				"line "+strconv.Itoa(i)+": qty must be a positive integer",
				r,
			))
			return
		}
	}

	// Verify the allocation exists.
	ctx := r.Context()
	allocation, err := h.queries.GetExternalAllocationByID(ctx, allocationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reconciliation.allocation_not_found", "external allocation not found", r,
			))
			return
		}
		h.logger.Error("reconciliation: get allocation failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.allocation_fetch_failed", "failed to retrieve allocation", r,
		))
		return
	}

	// Only active/disputed allocations may be reconciled.
	if allocation.Status != "active" && allocation.Status != "disputed" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
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
		lookup, lookupErr := h.queries.LookupBarcodeByExternalRef(ctx, line.ExternalRef, allocationID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			h.logger.Warn("reconciliation: barcode lookup error",
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
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rq := h.queries.WithTx(tx)

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
		h.logger.Error("reconciliation: insert report failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
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
			h.logger.Error("reconciliation: insert line failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"reconciliation.line_insert_failed", "failed to persist reconciliation line", r,
			))
			return
		}
		insertedLines = append(insertedLines, ln)
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.commit_failed", "failed to commit reconciliation transaction", r,
		))
		return
	}

	h.logger.Info("reconciliation: report submitted",
		slog.String("report_id", report.ID.String()),
		slog.String("allocation_id", allocationID.String()),
		slog.String("status", reportStatus),
		slog.Int("total_lines", int(totalLines)),
		slog.Int("matched_lines", int(matchedCount)),
		slog.Int("exception_lines", int(exceptionCount)),
	)

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"report": ReportFromRow(report),
		"lines":  LinesFromRows(insertedLines),
	})
}
