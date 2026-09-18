// macs_export.go implements GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export
// (AB-50b, feature #438).
//
// The endpoint produces a MACS-import-compatible JSON document containing
// tickets for the session. With ?download=1 it sets Content-Disposition:
// attachment for direct-import use.
//
// ?tickets=valid|revoked|all (owner decision 2026-09-18) selects which
// tickets are included, because MACS's file importer ignores holderStatus
// and stores every imported ticket as valid: "all" (the default, kept for
// backward compatibility with existing callers) is the pre-existing
// behaviour; "valid" is the file the operator should actually hand to MACS
// import; "revoked" is the door "remove these" list, optionally narrowed to
// only what changed since a previous export via ?revoked_since=<RFC3339>.
package hcatalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

// parseMACSExportParams parses and validates ?tickets= and ?revoked_since=
// from the request query string. It is a pure function (no I/O) so the
// contract can be unit-tested without a database: on success errCode is "";
// on failure errCode/errMsg describe the 400 to write and mode/revokedSince
// are zero-valued.
func parseMACSExportParams(q url.Values) (mode macs.ExportMode, revokedSince *time.Time, errCode, errMsg string) {
	mode, ok := macs.ParseExportMode(q.Get("tickets"))
	if !ok {
		return "", nil, "macs.invalid_tickets_mode", "tickets must be one of valid, revoked, all"
	}

	raw := q.Get("revoked_since")
	if raw == "" {
		return mode, nil, "", ""
	}
	if mode != macs.ExportModeRevoked {
		return "", nil, "macs.revoked_since_requires_revoked", "revoked_since is only valid with tickets=revoked"
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", nil, "macs.invalid_revoked_since", "revoked_since must be an RFC3339 timestamp"
	}
	return mode, &ts, "", ""
}

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

	mode, revokedSince, errCode, errMsg := parseMACSExportParams(r.URL.Query())
	if errCode != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(errCode, errMsg, r))
		return
	}

	export, err := macs.QueryAndBuildExportFiltered(r.Context(), pool, sessionID, mode, revokedSince)
	if err != nil {
		h.logger.Error("macs-export: query failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.export_failed", "failed to build MACS export", r,
		))
		return
	}

	// Validate completeness: MACS will reject the import when cityName is
	// missing. Return 422 so the operator knows they must link the venue to a
	// city before exporting (AB-50g).
	for _, order := range export {
		for _, ticket := range order.TicketList {
			if ticket.ActionEvent.CityName == "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
					"macs.export_incomplete",
					"one or more tickets have no city — link the session venue to a city before exporting",
					r,
				))
				return
			}
		}
	}

	download := r.URL.Query().Get("download") == "1"
	if download {
		w.Header().Set("Content-Disposition", fmt.Sprintf(
			`attachment; filename="macs-export-session-%s-%s.json"`, sessionID, mode,
		))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(export)
}
