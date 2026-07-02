// reconciliation_review.go implements the operator review endpoints:
//
//	PATCH /v1/reconciliation/reports/{id}/review
//	PATCH /v1/reconciliation/reports/{id}/lines/{line_id}
package hreconciliation

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

func (h *Handler) HandleReviewReport(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.invalid_id", "id must be a valid UUID", r,
		))
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	var req reviewReportRequest
	_ = json.Unmarshal(body, &req)

	ctx := r.Context()

	// Verify report exists and is in 'exception' status.
	current, err := h.queries.GetReconciliationReportByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reconciliation.not_found", "reconciliation report not found", r,
			))
			return
		}
		h.logger.Error("reconciliation: get report for review failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.get_failed", "failed to retrieve reconciliation report", r,
		))
		return
	}

	if current.Status == "reviewed" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"reconciliation.already_reviewed", "reconciliation report is already reviewed", r,
		))
		return
	}
	if current.Status != "exception" {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"reconciliation.not_in_exception",
			"only reports in 'exception' status can be reviewed",
			r,
		))
		return
	}

	// Check if any exception lines remain unresolved.
	remaining, err := h.queries.CountExceptionLinesByReport(ctx, id)
	if err != nil {
		h.logger.Error("reconciliation: count exceptions failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.count_failed", "failed to count exception lines", r,
		))
		return
	}
	if remaining > 0 {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"reconciliation.exceptions_pending",
			"all exception lines must be resolved before marking the report as reviewed",
			r,
		))
		return
	}

	report, err := h.queries.UpdateReconciliationReportStatus(ctx, id, "reviewed", req.ReviewedBy)
	if err != nil {
		h.logger.Error("reconciliation: update report status failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.update_failed", "failed to update reconciliation report status", r,
		))
		return
	}

	h.logger.Info("reconciliation: report reviewed",
		slog.String("report_id", id.String()),
		slog.Any("reviewed_by", req.ReviewedBy),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"report": ReportFromRow(report),
	})
}

func (h *Handler) HandleResolveException(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	lineIDStr := chi.URLParam(r, "line_id")
	lineID, err := uuid.Parse(lineIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.invalid_line_id", "line_id must be a valid UUID", r,
		))
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	var req reviewExceptionLineRequest
	_ = json.Unmarshal(body, &req)

	ctx := r.Context()
	line, err := h.queries.UpdateReconciliationLineReview(ctx, lineID, req.OperatorNote)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reconciliation.line_not_found", "reconciliation line not found", r,
			))
			return
		}
		h.logger.Error("reconciliation: resolve exception failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.resolve_failed", "failed to resolve exception line", r,
		))
		return
	}

	h.logger.Info("reconciliation: exception line resolved",
		slog.String("line_id", lineID.String()),
		slog.String("match_status", line.MatchStatus),
	)

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"line": LineFromRow(line),
	})
}
