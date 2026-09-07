package hcustomerimports

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleGetCustomerImport serves GET /v1/admin/customer-imports/{id}.
func (h *Handler) HandleGetCustomerImport(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	_, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	importID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	row, err := h.queries.GetCustomerImportByID(r.Context(), importID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"customer_import.not_found", "customer import not found", r,
			))
			return
		}
		h.logger.Error("customer_import: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to load customer import", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, toCustomerImportDTO(row))
}

// allowedRowActions mirrors customer_import_rows_action_check (migration 0098).
var allowedRowActions = map[string]bool{
	"created":         true,
	"matched":         true,
	"merge_candidate": true,
	"skipped":         true,
}

type customerImportRowDTO struct {
	ID                 string  `json:"id"`
	ImportID           string  `json:"import_id"`
	RowNo              int32   `json:"row_no"`
	RowHash            string  `json:"row_hash"`
	ResolvedCustomerID *string `json:"resolved_customer_id"`
	OrgID              *string `json:"org_id"`
	Action             *string `json:"action"`
	Reason             *string `json:"reason"`
	CreatedAt          string  `json:"created_at"`
}

func toCustomerImportRowDTO(row gen.CustomerImportRowRow) customerImportRowDTO {
	dto := customerImportRowDTO{
		ID:        row.ID.String(),
		ImportID:  row.ImportID.String(),
		RowNo:     row.RowNo,
		RowHash:   row.RowHash,
		Action:    row.Action,
		Reason:    row.Reason,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.ResolvedCustomerID != nil {
		s := row.ResolvedCustomerID.String()
		dto.ResolvedCustomerID = &s
	}
	if row.OrgID != nil {
		s := row.OrgID.String()
		dto.OrgID = &s
	}
	return dto
}

// HandleListCustomerImportRows serves GET
// /v1/admin/customer-imports/{id}/rows, optionally filtered by
// ?action=created|matched|merge_candidate|skipped.
func (h *Handler) HandleListCustomerImportRows(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	_, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	importID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	actionFilter := strings.TrimSpace(r.URL.Query().Get("action"))
	if actionFilter != "" && !allowedRowActions[actionFilter] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"customer_import.invalid_action", "action must be one of the supported row outcomes", r,
			map[string]any{"field": "action", "allowed": sortedKeys(allowedRowActions)},
		))
		return
	}

	// Confirm the parent import exists so a typo'd id 404s instead of
	// silently returning an empty rows list.
	if _, err := h.queries.GetCustomerImportByID(r.Context(), importID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"customer_import.not_found", "customer import not found", r,
			))
			return
		}
		h.logger.Error("customer_import: rows lookup parent failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to load customer import", r,
		))
		return
	}

	rows, err := h.queries.ListCustomerImportRows(r.Context(), importID, actionFilter)
	if err != nil {
		h.logger.Error("customer_import: list rows failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to list customer import rows", r,
		))
		return
	}

	items := make([]customerImportRowDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toCustomerImportRowDTO(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"rows": items, "total": len(items)})
}
