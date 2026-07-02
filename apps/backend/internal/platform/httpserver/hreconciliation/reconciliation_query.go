// reconciliation_query.go implements the read-side reconciliation endpoints:
//
//	GET /v1/reconciliation/reports/{id}
//	GET /v1/reconciliation/exceptions
package hreconciliation

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

func (h *Handler) HandleGetReport(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	report, err := h.queries.GetReconciliationReportByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"reconciliation.not_found", "reconciliation report not found", r,
			))
			return
		}
		h.logger.Error("reconciliation: get report failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.get_failed", "failed to retrieve reconciliation report", r,
		))
		return
	}

	lines, err := h.queries.ListReconciliationLinesByReport(ctx, id)
	if err != nil {
		h.logger.Error("reconciliation: list lines failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.lines_failed", "failed to retrieve reconciliation lines", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"report": ReportFromRow(report),
		"lines":  LinesFromRows(lines),
	})
}

func (h *Handler) HandleListExceptions(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	// org_id query param is required so the operator can scope the queue.
	orgIDStr := r.URL.Query().Get("org_id")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"reconciliation.invalid_org_id", "org_id query parameter must be a valid UUID", r,
		))
		return
	}

	ctx := r.Context()
	reports, err := h.queries.ListReconciliationExceptions(ctx, orgID)
	if err != nil {
		h.logger.Error("reconciliation: list exceptions failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reconciliation.exceptions_failed", "failed to retrieve exception queue", r,
		))
		return
	}

	reportList := make([]map[string]any, 0, len(reports))
	for _, rep := range reports {
		reportList = append(reportList, ReportFromRow(rep))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"exceptions": reportList,
		"total":      len(reportList),
	})
}
