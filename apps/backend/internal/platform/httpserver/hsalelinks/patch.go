package hsalelinks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func invalidStatus() *apiError {
	return &apiError{http.StatusBadRequest, "sales_link.invalid_status", "status must be active or archived"}
}

// targetMeta describes a stored or new target for the audit row.
func targetMeta(kind string, eventID, sessionID, promoterID, urlv any) map[string]any {
	return map[string]any{
		"kind": kind, "event_id": eventID, "session_id": sessionID, "promoter_id": promoterID, "url": urlv,
	}
}

func rowTargetMeta(row gen.SalesLinkRow) map[string]any {
	return targetMeta(row.TargetKind, idPtr(row.EventID), idPtr(row.SessionID), idPtr(row.PromoterID), strPtr(row.URL))
}

func newTargetMeta(t resolvedTarget) map[string]any {
	return targetMeta(t.kind, idPtr(t.eventID), idPtr(t.sessionID), idPtr(t.promoterID), strPtr(t.url))
}

// sameTarget reports whether the row already points where t points.
func sameTarget(row gen.SalesLinkRow, t resolvedTarget) bool {
	eq := func(a, b any) bool { return a == b }
	m, n := rowTargetMeta(row), newTargetMeta(t)
	for _, k := range []string{"kind", "event_id", "session_id", "promoter_id", "url"} {
		if !eq(m[k], n[k]) {
			return false
		}
	}
	return true
}

// HandlePatch serves PATCH /v1/organizations/{org_id}/sales-links/{id}. A key
// that is absent keeps the stored value. A target change moves a code that may
// already be printed, so it is audited with the old and the new target.
func (h *Handler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	id, ok := linkID(w, r)
	if !ok {
		return
	}
	var req patchRequest
	if !readBody(w, r, &req) {
		return
	}
	if !req.Label.Present && !req.PlacementKind.Present && !req.UTM.Present && !req.Target.Present && !req.Status.Present {
		writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_body", "send at least one of label, placement_kind, utm, target or status")
		return
	}

	params := gen.UpdateSalesLinkParams{ID: id, OrgID: orgID}
	if req.Label.Present {
		if req.Label.Value == nil {
			invalidLabel().write(w, r)
			return
		}
		label, okLabel := cleanLabel(*req.Label.Value)
		if !okLabel {
			invalidLabel().write(w, r)
			return
		}
		params.SetLabel, params.Label = true, label
	}
	if req.PlacementKind.Present {
		if req.PlacementKind.Value == nil || !oneOf(strings.TrimSpace(*req.PlacementKind.Value), placementKinds) {
			invalidPlacement().write(w, r)
			return
		}
		params.SetPlacementKind, params.PlacementKind = true, strings.TrimSpace(*req.PlacementKind.Value)
	}
	if req.UTM.Present {
		var raw []byte
		if req.UTM.Value != nil {
			raw = *req.UTM.Value
		}
		utm, err := parseUTM(raw)
		if err != nil {
			invalidUTM().write(w, r)
			return
		}
		params.SetUTM, params.UTM = true, marshalUTM(utm)
	}
	if req.Status.Present {
		if req.Status.Value == nil || !oneOf(strings.TrimSpace(*req.Status.Value), linkStatuses) {
			invalidStatus().write(w, r)
			return
		}
		params.SetStatus, params.Status = true, strings.TrimSpace(*req.Status.Value)
	}

	ctx := r.Context()
	// A link of another organization is a 404 before anything else is judged,
	// so a foreign id never answers with a validation error about the body.
	if _, err := h.queries.GetSalesLinkByID(ctx, id, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "load", err)
		return
	}
	var newTarget resolvedTarget
	if req.Target.Present {
		if req.Target.Value == nil {
			invalidTarget("target must be an object").write(w, r)
			return
		}
		t, aerr, err := h.resolveTarget(ctx, orgID, *req.Target.Value)
		if aerr != nil {
			aerr.write(w, r)
			return
		}
		if err != nil {
			h.failed(w, r, "target check", err)
			return
		}
		newTarget = t
	}

	tx, q, err := h.beginTx(ctx)
	if err != nil {
		writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "failed to begin transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := q.GetSalesLinkByIDForUpdate(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "load", err)
		return
	}
	retarget := req.Target.Present && !sameTarget(current, newTarget)
	if retarget {
		params.SetTarget, params.TargetKind = true, newTarget.kind
		params.EventID, params.SessionID, params.PromoterID, params.URL =
			newTarget.eventID, newTarget.sessionID, newTarget.promoterID, newTarget.url
	}
	updated, err := q.UpdateSalesLink(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
			return
		}
		h.failed(w, r, "update", err)
		return
	}

	if err := h.auditPatch(ctx, tx, r, current, updated, params, retarget, newTarget); err != nil {
		h.failed(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.failed(w, r, "commit", err)
		return
	}
	writeJSON(w, http.StatusOK, h.present(ctx, h.queries, updated))
}
