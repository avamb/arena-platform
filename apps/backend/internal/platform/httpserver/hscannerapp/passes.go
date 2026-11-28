package hscannerapp

// passes.go — controller passes of an organization (spec 69 A.6, MA-12).
// Every route needs scanner_pass.manage and the membership of {org_id}
// (checked by the router); every pass is read and written by (id, org_id),
// so a pass of another organization is the route's own 404.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// PassSessionInput is one date of a pass in a request. No tier_ids (or an
// empty list) means every category of the date.
type PassSessionInput struct {
	SessionID uuid.UUID   `json:"session_id"`
	TierIDs   []uuid.UUID `json:"tier_ids"`
}

// CreatePassRequest is the body of POST .../scanner/passes.
type CreatePassRequest struct {
	Label      string             `json:"label"`
	Kind       string             `json:"kind"`
	Sessions   []PassSessionInput `json:"sessions"`
	MaxDevices *int               `json:"max_devices"`
	ValidFrom  *time.Time         `json:"valid_from"`
	ValidUntil *time.Time         `json:"valid_until"`
}

// PatchPassRequest is the body of PATCH .../scanner/passes/{pass_id}; an
// absent field stays as it is, sessions replaces the dates as a whole.
type PatchPassRequest struct {
	Label      *string             `json:"label"`
	ValidFrom  *time.Time          `json:"valid_from"`
	ValidUntil *time.Time          `json:"valid_until"`
	MaxDevices *int                `json:"max_devices"`
	Sessions   *[]PassSessionInput `json:"sessions"`
}

// PassBody is a controller pass as the organizer sees it.
type PassBody struct {
	ID         uuid.UUID       `json:"id"`
	Label      string          `json:"label"`
	Kind       string          `json:"kind"`
	ValidFrom  *time.Time      `json:"valid_from"`
	ValidUntil *time.Time      `json:"valid_until"`
	MaxDevices int             `json:"max_devices"`
	Accepted   int             `json:"accepted"`
	CreatedAt  time.Time       `json:"created_at"`
	RevokedAt  *time.Time      `json:"revoked_at"`
	Sessions   []MePassSession `json:"sessions"`
	Devices    []PassDevice    `json:"devices"`
}

// PassDevice is a live device of a pass.
type PassDevice struct {
	ID             uuid.UUID  `json:"id"`
	ControllerName string     `json:"controller_name"`
	Label          string     `json:"label"`
	Platform       string     `json:"platform"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

// CreatePassResponse is the 201 body of POST .../scanner/passes.
type CreatePassResponse struct {
	Pass    PassBody    `json:"pass"`
	Pairing PairingLink `json:"pairing"`
}

func buildPass(p scannerapp.Pass) PassBody {
	out := PassBody{
		ID: p.ID, Label: p.Label, Kind: p.Kind,
		ValidFrom: utcPtr(p.ValidFrom), ValidUntil: utcPtr(p.ValidUntil),
		MaxDevices: p.MaxDevices, Accepted: p.Accepted,
		CreatedAt: p.CreatedAt.UTC(), RevokedAt: utcPtr(p.RevokedAt),
		Sessions: make([]MePassSession, 0, len(p.Sessions)),
		Devices:  make([]PassDevice, 0, len(p.Devices)),
	}
	for _, s := range p.Sessions {
		tiers := s.TierIDs
		if tiers == nil || s.AllTiers {
			tiers = []uuid.UUID{}
		}
		out.Sessions = append(out.Sessions, MePassSession{SessionID: s.SessionID, EventID: s.EventID, AllTiers: s.AllTiers, TierIDs: tiers})
	}
	for _, d := range p.Devices {
		out.Devices = append(out.Devices, PassDevice{
			ID: d.ID, ControllerName: d.ControllerName, Label: d.Label, Platform: d.Platform,
			CreatedAt: d.CreatedAt.UTC(), LastSeenAt: utcPtr(d.LastSeenAt),
		})
	}
	return out
}

func passSessions(in []PassSessionInput) []scannerapp.CreatePassSession {
	out := make([]scannerapp.CreatePassSession, 0, len(in))
	for _, s := range in {
		out = append(out, scannerapp.CreatePassSession{SessionID: s.SessionID, TierIDs: s.TierIDs})
	}
	return out
}

// writePassErr answers a refusal of the store; false when err is not one.
func writePassErr(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, scannerapp.ErrPassNotFound):
		writePassNotFound(w, r)
	case errors.Is(err, scannerapp.ErrPassRevoked):
		writeErr(w, r, http.StatusConflict, "scanner_pass.revoked",
			"the pass is turned off; create a new link to turn it on before you change it")
	case errors.Is(err, scannerapp.ErrInvalidWindow):
		writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.invalid_window",
			"valid_until must be after valid_from, and a demo pass has no time window")
	case errors.Is(err, scannerapp.ErrMaxDevicesBelowAccepted):
		writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.max_devices_below_accepted",
			"max_devices is lower than the number of devices that already use the pass; revoke a device first")
	case errors.Is(err, scannerapp.ErrForeignSession):
		writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.invalid_session",
			"a session of the pass is not a session of this organization")
	case errors.Is(err, scannerapp.ErrForeignTier):
		writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.invalid_tier",
			"a price category of the pass is not a category of its session")
	case errors.Is(err, scannerapp.ErrPassWindowEnded):
		writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.window_ended",
			"the time window of the pass is over; move valid_until first")
	case errors.Is(err, scannerapp.ErrInvalidPass):
		writeInvalidPass(w, r)
	default:
		return false
	}
	return true
}

func writeInvalidPass(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusUnprocessableEntity, "scanner_pass.invalid",
		"label must be 1 to 80 characters, kind standard or demo, max_devices 1 to 50, and the pass needs at least one session")
}

func writePassNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusNotFound, "scanner_pass.not_found", "controller pass not found")
}

func passID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "pass_id")))
	if err != nil {
		writePassNotFound(w, r)
		return uuid.Nil, false
	}
	return id, true
}

// passCaller is the organization of the path and the person who calls.
func (h *Handler) passCaller(w http.ResponseWriter, r *http.Request) (orgID, userID uuid.UUID, ok bool) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return uuid.Nil, uuid.Nil, false
	}
	if orgID, ok = pathOrg(w, r); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if userID, ok = callerUser(w, r); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, userID, true
}

func (h *Handler) passFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.Logger.Error("hscannerapp: "+what+" failed", "error", err.Error())
	writeErr(w, r, http.StatusInternalServerError, "scanner_pass.failed", "failed to "+what)
}

// HandleCreatePass serves POST /v1/organizations/{org_id}/scanner/passes.
//
//	201 CreatePassResponse (the pass and its pairing link)
//	400 scanner_pass.invalid_body
//	403 scanner_pass.demo_forbidden (a demo pass, not a platform superadmin)
//	422 scanner_pass.invalid / invalid_window / invalid_session / invalid_tier / window_ended
func (h *Handler) HandleCreatePass(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	var req CreatePassRequest
	if !readJSON(r, &req) {
		writeErr(w, r, http.StatusBadRequest, "scanner_pass.invalid_body",
			"the request body is not a JSON object with label, kind, sessions, max_devices, valid_from and valid_until")
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = scannerapp.PassKindStandard
	}
	if kind != scannerapp.PassKindStandard && kind != scannerapp.PassKindDemo {
		writeInvalidPass(w, r)
		return
	}
	if kind == scannerapp.PassKindDemo && !auth.HasSuperadminOrgAccess(r.Context()) {
		writeErr(w, r, http.StatusForbidden, "scanner_pass.demo_forbidden",
			"only a platform superadmin creates a demo pass")
		return
	}
	maxDevices := scannerapp.DefaultPassDevices
	if req.MaxDevices != nil {
		maxDevices = *req.MaxDevices
	}
	label := strings.TrimSpace(req.Label)
	if label == "" || len([]rune(label)) > scannerapp.MaxLabelLen || len(req.Sessions) == 0 ||
		maxDevices < 1 || maxDevices > scannerapp.MaxPassDevices {
		writeInvalidPass(w, r)
		return
	}
	from, until := req.ValidFrom, req.ValidUntil
	if (from == nil) != (until == nil) || (kind == scannerapp.PassKindDemo && from != nil) ||
		(from != nil && !until.After(*from)) {
		writePassErr(w, r, scannerapp.ErrInvalidWindow)
		return
	}
	ctx := r.Context()
	now := h.now()
	sessions := passSessions(req.Sessions)
	if kind == scannerapp.PassKindStandard && from == nil {
		ids := make([]uuid.UUID, 0, len(sessions))
		for _, s := range sessions {
			ids = append(ids, s.SessionID)
		}
		f, u, err := scannerapp.DefaultPassWindow(ctx, h.Pool, orgID, ids)
		if err != nil {
			if !writePassErr(w, r, err) {
				h.passFailed(w, r, "compute the pass window", err)
			}
			return
		}
		from, until = &f, &u
	}

	by := userID
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		h.passFailed(w, r, "create the pass", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := scannerapp.CreatePass(ctx, tx, scannerapp.CreatePassParams{
		OrgID: orgID, Label: label, Kind: kind, ValidFrom: from, ValidUntil: until,
		MaxDevices: maxDevices, CreatedBy: &by, Sessions: sessions,
	}, now)
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "create the pass", err)
		}
		return
	}
	issued, err := scannerapp.IssuePassCode(ctx, tx, orgID, id, &by, now)
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "create the pass", err)
		}
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.passFailed(w, r, "create the pass", err)
		return
	}
	p, err := scannerapp.GetPass(ctx, h.Pool, orgID, id)
	if err != nil {
		h.passFailed(w, r, "read the pass", err)
		return
	}
	h.writeAudit(r, "user", userID.String(), "v1.scanner_pass.create", "scanner_pass", id.String(), map[string]any{
		"org_id": orgID.String(), "kind": kind, "max_devices": maxDevices, "sessions": len(sessions),
	})
	httputil.WriteJSON(w, http.StatusCreated, CreatePassResponse{Pass: buildPass(p), Pairing: h.pairingLink(issued.Code)})
}

// HandleListPasses serves GET /v1/organizations/{org_id}/scanner/passes
// (?event_id= keeps the passes with a session of that event): up to 100,
// newest first, turned-off ones included. 200 {passes: [...]}.
func (h *Handler) HandleListPasses(w http.ResponseWriter, r *http.Request) {
	orgID, _, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	var eventID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("event_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"scanner_pass.invalid_filter", "event_id must be a UUID", r, map[string]any{"field": "event_id"}))
			return
		}
		eventID = &id
	}
	list, err := scannerapp.ListPasses(r.Context(), h.Pool, orgID, eventID)
	if err != nil {
		h.passFailed(w, r, "read the passes", err)
		return
	}
	out := make([]PassBody, 0, len(list))
	for _, p := range list {
		out = append(out, buildPass(p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"passes": out})
}

// HandleGetPass serves GET /v1/organizations/{org_id}/scanner/passes/{pass_id}.
func (h *Handler) HandleGetPass(w http.ResponseWriter, r *http.Request) {
	orgID, _, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	id, ok := passID(w, r)
	if !ok {
		return
	}
	p, err := scannerapp.GetPass(r.Context(), h.Pool, orgID, id)
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "read the pass", err)
		}
		return
	}
	httputil.WriteJSON(w, http.StatusOK, buildPass(p))
}

// HandlePatchPass serves PATCH /v1/organizations/{org_id}/scanner/passes/{pass_id}.
//
//	200 PassBody
//	400 scanner_pass.invalid_body (also: no field to change)
//	404 scanner_pass.not_found
//	409 scanner_pass.revoked
//	422 scanner_pass.invalid / invalid_window / max_devices_below_accepted / invalid_session / invalid_tier
func (h *Handler) HandlePatchPass(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	id, ok := passID(w, r)
	if !ok {
		return
	}
	var req PatchPassRequest
	if !readJSON(r, &req) {
		writeErr(w, r, http.StatusBadRequest, "scanner_pass.invalid_body",
			"the request body is not a JSON object with label, valid_from, valid_until, max_devices or sessions")
		return
	}
	if req.Label == nil && req.ValidFrom == nil && req.ValidUntil == nil && req.MaxDevices == nil && req.Sessions == nil {
		writeErr(w, r, http.StatusBadRequest, "scanner_pass.invalid_body",
			"send at least one of label, valid_from, valid_until, max_devices or sessions")
		return
	}
	params := scannerapp.UpdatePassParams{
		OrgID: orgID, PassID: id, Label: req.Label,
		ValidFrom: req.ValidFrom, ValidUntil: req.ValidUntil, MaxDevices: req.MaxDevices,
	}
	changed := []string{}
	if req.Label != nil {
		changed = append(changed, "label")
	}
	if req.ValidFrom != nil {
		changed = append(changed, "valid_from")
	}
	if req.ValidUntil != nil {
		changed = append(changed, "valid_until")
	}
	if req.MaxDevices != nil {
		changed = append(changed, "max_devices")
	}
	if req.Sessions != nil {
		s := passSessions(*req.Sessions)
		params.Sessions = &s
		changed = append(changed, "sessions")
	}
	ctx := r.Context()
	if err := scannerapp.UpdatePass(ctx, h.Pool, params); err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "change the pass", err)
		}
		return
	}
	p, err := scannerapp.GetPass(ctx, h.Pool, orgID, id)
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "read the pass", err)
		}
		return
	}
	h.writeAudit(r, "user", userID.String(), "v1.scanner_pass.update", "scanner_pass", id.String(),
		map[string]any{"org_id": orgID.String(), "fields": changed})
	httputil.WriteJSON(w, http.StatusOK, buildPass(p))
}

// HandleRevokePass serves DELETE /v1/organizations/{org_id}/scanner/passes/{pass_id}:
// the pass is turned off, its live devices are revoked (pass_revoked) and its
// codes deleted, in one transaction. 204, an already turned-off pass too.
func (h *Handler) HandleRevokePass(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	id, ok := passID(w, r)
	if !ok {
		return
	}
	by := userID
	res, err := scannerapp.RevokePassWithDevices(r.Context(), h.Pool, orgID, id, &by, h.now())
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "turn off the pass", err)
		}
		return
	}
	if res.JustRevoked || len(res.RevokedDeviceIDs) > 0 {
		ids := make([]string, 0, len(res.RevokedDeviceIDs))
		for _, d := range res.RevokedDeviceIDs {
			ids = append(ids, d.String())
		}
		h.writeAudit(r, "user", userID.String(), "v1.scanner_pass.revoke", "scanner_pass", id.String(),
			map[string]any{"org_id": orgID.String(), "revoked_device_ids": ids, "reason": scannerapp.ReasonPassRevoked})
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleLinkPass serves POST /v1/organizations/{org_id}/scanner/passes/{pass_id}/link:
// a new pairing link of the pass; the old codes stop working and a turned-off
// pass is turned on again.
//
//	200 PairingLink (code, link, qr_png)
//	404 scanner_pass.not_found
//	422 scanner_pass.window_ended
func (h *Handler) HandleLinkPass(w http.ResponseWriter, r *http.Request) {
	orgID, userID, ok := h.passCaller(w, r)
	if !ok {
		return
	}
	id, ok := passID(w, r)
	if !ok {
		return
	}
	by := userID
	issued, err := scannerapp.IssuePassCode(r.Context(), h.Pool, orgID, id, &by, h.now())
	if err != nil {
		if !writePassErr(w, r, err) {
			h.passFailed(w, r, "issue a link of the pass", err)
		}
		return
	}
	h.writeAudit(r, "user", userID.String(), "v1.scanner_pass.link", "scanner_pass", id.String(),
		map[string]any{"org_id": orgID.String(), "reenabled": issued.PassReenabled})
	httputil.WriteJSON(w, http.StatusOK, h.pairingLink(issued.Code))
}
