package hvenueplan

// read.go — the lists, the cards and the two file downloads.

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const (
	listDefaultLimit = 50
	listMaxLimit     = 200
)

type listItemJSON struct {
	ID               string  `json:"id"`
	OrgID            string  `json:"org_id"`
	VenueID          string  `json:"venue_id"`
	VenueName        string  `json:"venue_name"`
	State            string  `json:"state"`
	Channel          string  `json:"channel"`
	EscalationReason *string `json:"escalation_reason"`
	OperatorClaimed  bool    `json:"operator_claimed"`
	CurrentDraft     *int32  `json:"current_draft"`
	PlanID           *string `json:"plan_id"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`

	OrgName           *string `json:"org_name,omitempty"`
	VenueCity         *string `json:"venue_city,omitempty"`
	OperatorID        *string `json:"operator_id,omitempty"`
	OperatorClaimedAt *string `json:"operator_claimed_at,omitempty"`
}

func listItem(in gen.VenuePlanIntakeRow, venueName string) listItemJSON {
	return listItemJSON{
		ID: in.ID.String(), OrgID: in.OrgID.String(), VenueID: in.VenueID.String(), VenueName: venueName,
		State: in.State, Channel: in.Channel, EscalationReason: in.EscalationReason,
		OperatorClaimed: in.OperatorID != nil, CurrentDraft: in.CurrentDraft, PlanID: uuidStr(in.PlanID),
		CreatedAt: fmtTime(in.CreatedAt), UpdatedAt: fmtTime(in.UpdatedAt),
	}
}

// listFilters reads state and limit.
func listFilters(w http.ResponseWriter, r *http.Request) (state *string, limit int32, ok bool) {
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("state")); raw != "" {
		if !venueplan.State(raw).Valid() {
			writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_state",
				"state must be one of received, generating, review, operator, approved, rejected, cancelled",
				map[string]any{"field": "state"})
			return nil, 0, false
		}
		state = &raw
	}
	limit = listDefaultLimit
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > listMaxLimit {
			writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_limit",
				"limit must be a whole number from 1 to 200", map[string]any{"field": "limit"})
			return nil, 0, false
		}
		limit = int32(n) //nolint:gosec // 1..200
	}
	return state, limit, true
}

func uuidQuery(w http.ResponseWriter, r *http.Request, name string) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_"+name,
			name+" must be a valid UUID", map[string]any{"field": name})
		return nil, false
	}
	return &id, true
}

// HandleList answers GET /v1/organizations/{org_id}/plan-intakes.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || refuseAPIKey(w, r) {
		return
	}
	state, limit, ok := listFilters(w, r)
	if !ok {
		return
	}
	venueID, ok := uuidQuery(w, r, "venue_id")
	if !ok {
		return
	}
	ctx := r.Context()
	rows, err := h.queries.ListVenuePlanIntakesByOrg(ctx, orgID, state, venueID, limit)
	if err != nil {
		h.failed(w, r, "list", err)
		return
	}
	names := map[uuid.UUID]string{}
	out := make([]listItemJSON, 0, len(rows))
	for _, in := range rows {
		name, seen := names[in.VenueID]
		if !seen {
			name = h.venueName(ctx, h.queries, in.VenueID)
			names[in.VenueID] = name
		}
		out = append(out, listItem(in, name))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"intakes": out})
}

// HandleAdminList answers GET /v1/admin/plan-intakes.
func (h *Handler) HandleAdminList(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) || !requireSuperadmin(w, r) {
		return
	}
	state, limit, ok := listFilters(w, r)
	if !ok {
		return
	}
	orgID, ok := uuidQuery(w, r, "org_id")
	if !ok {
		return
	}
	unclaimed := false
	if raw := strings.TrimSpace(r.URL.Query().Get("unclaimed")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_unclaimed",
				"unclaimed must be true or false", map[string]any{"field": "unclaimed"})
			return
		}
		unclaimed = b
	}
	rows, err := h.queries.ListVenuePlanIntakesAdmin(r.Context(), state, orgID, unclaimed, limit)
	if err != nil {
		h.failed(w, r, "admin list", err)
		return
	}
	out := make([]listItemJSON, 0, len(rows))
	for _, row := range rows {
		it := listItem(row.VenuePlanIntakeRow, row.VenueName)
		it.OrgName = strPtr(row.OrgName)
		it.VenueCity = row.VenueCity
		it.OperatorID = uuidStr(row.OperatorID)
		it.OperatorClaimedAt = fmtTimePtr(row.OperatorClaimedAt)
		out = append(out, it)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"intakes": out})
}

// requireSuperadmin answers 403 unless the caller is a platform superadmin.
func requireSuperadmin(w http.ResponseWriter, r *http.Request) bool {
	if !auth.HasSuperadminOrgAccess(r.Context()) {
		writeErr(w, r, http.StatusForbidden, "venue_plan_intake.superadmin_only",
			"platform superadmin access is required to work the hall plan intakes")
		return false
	}
	return true
}

// orgIntake loads {intake_id} inside {org_id}: 404 for another
// organization's or an unknown intake, then 403 for an API key.
func (h *Handler) orgIntake(w http.ResponseWriter, r *http.Request) (gen.VenuePlanIntakeRow, bool) {
	if !h.ready(w, r) {
		return gen.VenuePlanIntakeRow{}, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return gen.VenuePlanIntakeRow{}, false
	}
	id, ok := httputil.UUIDPathParam(w, r, "intake_id")
	if !ok {
		return gen.VenuePlanIntakeRow{}, false
	}
	in, err := h.queries.GetVenuePlanIntake(r.Context(), id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.not_found", "hall plan intake not found")
		} else {
			h.failed(w, r, "intake lookup", err)
		}
		return gen.VenuePlanIntakeRow{}, false
	}
	if refuseAPIKey(w, r) {
		return gen.VenuePlanIntakeRow{}, false
	}
	return in, true
}

// adminIntake loads {intake_id} for a platform superadmin.
func (h *Handler) adminIntake(w http.ResponseWriter, r *http.Request, needReason bool) (gen.VenuePlanIntakeRow, string, bool) {
	if !h.ready(w, r) || !requireSuperadmin(w, r) {
		return gen.VenuePlanIntakeRow{}, "", false
	}
	reason := ""
	if needReason {
		var ok bool
		if reason, ok = httputil.RequireAdminReason(w, r); !ok {
			return gen.VenuePlanIntakeRow{}, "", false
		}
	}
	id, ok := httputil.UUIDPathParam(w, r, "intake_id")
	if !ok {
		return gen.VenuePlanIntakeRow{}, "", false
	}
	in, err := h.queries.GetVenuePlanIntakeByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.not_found", "hall plan intake not found")
		} else {
			h.failed(w, r, "intake lookup", err)
		}
		return gen.VenuePlanIntakeRow{}, "", false
	}
	return in, reason, true
}

func (h *Handler) writeCard(w http.ResponseWriter, r *http.Request, status int, in gen.VenuePlanIntakeRow, admin bool) {
	card, err := h.buildCard(r.Context(), h.queries, in, admin)
	if err != nil {
		h.failed(w, r, "card", err)
		return
	}
	httputil.WriteJSON(w, status, map[string]any{"intake": card})
}

// HandleGet answers GET /v1/organizations/{org_id}/plan-intakes/{intake_id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if in, ok := h.orgIntake(w, r); ok {
		h.writeCard(w, r, http.StatusOK, in, false)
	}
}

// HandleAdminGet answers GET /v1/admin/plan-intakes/{intake_id}.
func (h *Handler) HandleAdminGet(w http.ResponseWriter, r *http.Request) {
	if in, _, ok := h.adminIntake(w, r, false); ok {
		h.writeCard(w, r, http.StatusOK, in, true)
	}
}

// HandleSource answers GET .../plan-intakes/{intake_id}/sources/{source_id}.
func (h *Handler) HandleSource(w http.ResponseWriter, r *http.Request) {
	if in, ok := h.orgIntake(w, r); ok {
		h.serveSource(w, r, in)
	}
}

// HandleAdminSource answers GET /v1/admin/plan-intakes/{intake_id}/sources/{source_id}.
func (h *Handler) HandleAdminSource(w http.ResponseWriter, r *http.Request) {
	if in, _, ok := h.adminIntake(w, r, true); ok {
		h.serveSource(w, r, in)
	}
}

// HandleDraftSVG answers GET .../plan-intakes/{intake_id}/drafts/{n}/svg.
func (h *Handler) HandleDraftSVG(w http.ResponseWriter, r *http.Request) {
	if in, ok := h.orgIntake(w, r); ok {
		h.serveDraft(w, r, in)
	}
}

// HandleAdminDraftSVG answers GET /v1/admin/plan-intakes/{intake_id}/drafts/{n}/svg.
func (h *Handler) HandleAdminDraftSVG(w http.ResponseWriter, r *http.Request) {
	if in, _, ok := h.adminIntake(w, r, false); ok {
		h.serveDraft(w, r, in)
	}
}

func (h *Handler) serveSource(w http.ResponseWriter, r *http.Request, in gen.VenuePlanIntakeRow) {
	sid, ok := httputil.UUIDPathParam(w, r, "source_id")
	if !ok {
		return
	}
	src, err := h.queries.GetVenuePlanIntakeSource(r.Context(), sid, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.source_not_found", "file of the intake not found")
		} else {
			h.failed(w, r, "source lookup", err)
		}
		return
	}
	mediaID, name := src.MediaID, src.FileName
	switch strings.TrimSpace(r.URL.Query().Get("format")) {
	case "":
	case "png":
		if src.NormalizedMediaID == nil {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.png_copy_not_found", "this file has no PNG copy")
			return
		}
		mediaID, name = *src.NormalizedMediaID, strings.TrimSuffix(name, pathExt(name))+".png"
	default:
		writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_format",
			"format must be png or left out", map[string]any{"field": "format"})
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		disposition = "attachment"
	}
	h.stream(w, r, mediaID, disposition)
}

func (h *Handler) serveDraft(w http.ResponseWriter, r *http.Request, in gen.VenuePlanIntakeRow) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 1 || n > 1<<20 {
		writeErrDetails(w, r, http.StatusBadRequest, "venue_plan_intake.invalid_draft",
			"the draft number must be a whole number from 1", map[string]any{"field": "n"})
		return
	}
	d, err := h.queries.GetVenuePlanDraft(r.Context(), in.ID, int32(n)) //nolint:gosec // bounded above
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.draft_not_found", "draft of the intake not found")
		} else {
			h.failed(w, r, "draft lookup", err)
		}
		return
	}
	h.stream(w, r, d.SvgMediaID, "")
}

// stream sends a stored object with the closed-file headers: sandboxed, not
// sniffed, never cached. No signed or public link exists for these files.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request, mediaID uuid.UUID, disposition string) {
	ctx := r.Context()
	obj, err := h.media.GetByID(ctx, mediaID)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.file_missing", "the file is no longer available")
			return
		}
		h.failed(w, r, "media lookup", err)
		return
	}
	res, err := h.media.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, r, http.StatusNotFound, "venue_plan_intake.file_missing", "the file is no longer available")
			return
		}
		h.failed(w, r, "storage read", err)
		return
	}
	defer func() { _ = res.Body.Close() }()
	hd := w.Header()
	hd.Set("Content-Type", obj.ContentType)
	hd.Set("Content-Length", strconv.FormatInt(obj.ByteSize, 10))
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "private, no-store")
	if disposition != "" {
		hd.Set("Content-Disposition", disposition)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, res.Body)
}

func pathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}
