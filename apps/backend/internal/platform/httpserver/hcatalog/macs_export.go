// macs_export.go implements GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export
// (AB-50b, feature #438).
//
// The endpoint produces a MACS-import-compatible JSON document containing
// all completed tickets for the session. With ?download=1 it sets
// Content-Disposition: attachment for direct-import use.
package hcatalog

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

// HandleMACSExport serves GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export.
func (h *Handler) HandleMACSExport(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.pool_unavailable", "database pool unavailable", r,
		))
		return
	}

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}
	// Tenant isolation (pass-6 review): the session must belong to the
	// event and the event to the org — otherwise any member of any org
	// could export every ticket on the platform by path-id guessing.
	if _, err := h.sessionQueries.GetSessionByID(r.Context(), sessionID, eventID); err != nil {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"session.not_found", "session not found", r,
		))
		return
	}
	if orgCtx, err := h.sessionQueries.GetSessionOrgContext(r.Context(), sessionID); err != nil || orgCtx.OrgID != orgID {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"session.not_found", "session not found", r,
		))
		return
	}

	export, err := macs.QueryAndBuildExport(r.Context(), pool, sessionID)
	if err != nil {
		h.logger.Error("macs-export: query failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.export_failed", "failed to build MACS export", r,
		))
		return
	}

	download := r.URL.Query().Get("download") == "1"
	if download {
		w.Header().Set("Content-Disposition", fmt.Sprintf(
			`attachment; filename="macs-export-session-%s.json"`, sessionID,
		))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(export)
}
