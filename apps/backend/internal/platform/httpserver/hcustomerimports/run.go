package hcustomerimports

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/customerimport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleDryRunCustomerImport serves POST
// /v1/admin/customer-imports/{id}/dry-run. It runs customerimport.RunImport
// in dry_run mode synchronously and returns the resulting report; no
// customer_import_rows are ever written by this mode (see job.go's file
// doc comment).
func (h *Handler) HandleDryRunCustomerImport(w http.ResponseWriter, r *http.Request) {
	h.runMode(w, r, customerimport.ModeDryRun, "customer_import.dry_run")
}

// HandleApplyCustomerImport serves POST
// /v1/admin/customer-imports/{id}/apply. It runs customerimport.RunImport
// in apply mode synchronously and returns the resulting report. Re-applying
// the same file is idempotent at the database level (UNIQUE(import_id,
// row_hash) on customer_import_rows) — RunImport reports already-applied
// rows by their previously recorded action rather than re-running Resolve.
func (h *Handler) HandleApplyCustomerImport(w http.ResponseWriter, r *http.Request) {
	h.runMode(w, r, customerimport.ModeApply, "customer_import.apply")
}

func (h *Handler) runMode(w http.ResponseWriter, r *http.Request, mode, auditAction string) {
	if h.queries == nil || h.pgxPool == nil || h.media == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database or media storage is not available", r,
		))
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	importID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	ctx := r.Context()
	report, err := customerimport.RunImport(ctx, customerimport.Options{
		Pool:   h.pgxPool,
		Media:  h.media,
		Logger: h.logger,
	}, importID, mode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"customer_import.not_found", "customer import not found", r,
			))
			return
		}
		h.logger.Error("customer_import: run failed", slog.String("mode", mode), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"customer_import.run_failed", "failed to run customer import: "+err.Error(), r,
			map[string]any{"mode": mode},
		))
		return
	}

	h.writeAudit(r, auditAction, importID.String(), reason, map[string]any{
		"mode": mode, "rows": report.Rows, "created": report.Created, "matched": report.Matched,
	})

	httputil.WriteJSON(w, http.StatusOK, report)
}
