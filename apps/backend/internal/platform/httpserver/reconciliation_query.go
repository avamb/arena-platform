// reconciliation_query.go implements the read-side reconciliation endpoints:
//
//	GET /v1/reconciliation/reports/{id}
//	GET /v1/reconciliation/exceptions
//
// Extracted from reconciliation.go as part of feature #175.
package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

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
