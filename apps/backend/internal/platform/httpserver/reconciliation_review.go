// reconciliation_review.go implements the operator review endpoints:
//
//	PATCH /v1/reconciliation/reports/{id}/review
//	PATCH /v1/reconciliation/reports/{id}/lines/{line_id}
//
// Extracted from reconciliation.go as part of feature #175.
package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

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
