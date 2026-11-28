package hscannerapp

// devices.go — the organizer's own pairing code and the scanner devices of an
// organization (spec 69 A.6, MA-11). Routes under a person's JWT inside
// /v1/organizations/{org_id}/scanner/; the router checked scan.perform and
// the membership of {org_id}.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// DeviceItem is one live scanner device of an organization.
type DeviceItem struct {
	ID             uuid.UUID  `json:"id"`
	Kind           string     `json:"kind"`
	Label          string     `json:"label"`
	Platform       string     `json:"platform"`
	ControllerName string     `json:"controller_name"`
	PassID         *uuid.UUID `json:"pass_id"`
	PassLabel      *string    `json:"pass_label"`
	UserID         *uuid.UUID `json:"user_id"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

// HandleCreatePairingCode serves POST
// /v1/organizations/{org_id}/scanner/pairing-codes (no body): a one-time
// code for the caller's own phone, 15 minutes.
//
//	201 PairingLink (code, link, expires_at, qr_png)
//	403 scanner.membership_required / scanner.user_only
func (h *Handler) HandleCreatePairingCode(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	orgID, ok := pathOrg(w, r)
	if !ok {
		return
	}
	userID, ok := callerUser(w, r)
	if !ok {
		return
	}
	issued, err := scannerapp.IssueOrganizerCode(r.Context(), h.Pool, orgID, userID, h.now())
	if errors.Is(err, scannerapp.ErrMembershipRequired) {
		writeErr(w, r, http.StatusForbidden, "scanner.membership_required",
			"a pairing code is issued only to a member of the organization whose role may check tickets")
		return
	}
	if err != nil {
		h.Logger.Error("hscannerapp: issue organizer code failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner.code_failed", "failed to issue a pairing code")
		return
	}
	h.writeAudit(r, "user", userID.String(), "v1.scanner.pairing_code_issue", "organization", orgID.String(),
		map[string]any{"kind": scannerapp.KindOrganizer, "expires_at": issued.ExpiresAt.UTC().Format(time.RFC3339)})
	out := h.pairingLink(issued.Code)
	exp := issued.ExpiresAt.UTC()
	out.ExpiresAt = &exp
	httputil.WriteJSON(w, http.StatusCreated, out)
}

// HandleListDevices serves GET /v1/organizations/{org_id}/scanner/devices:
// the live devices of the caller (scan.perform), every live device of the
// organization with scanner_pass.manage. 200 {devices: [...]}, newest first.
func (h *Handler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	orgID, ok := pathOrg(w, r)
	if !ok {
		return
	}
	userID, ok := callerUser(w, r)
	if !ok {
		return
	}
	var only *uuid.UUID
	if !h.canManage(r) {
		only = &userID
	}
	list, err := scannerapp.ListDevices(r.Context(), h.Pool, orgID, only)
	if err != nil {
		h.Logger.Error("hscannerapp: list devices failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner.read_failed", "failed to read the devices")
		return
	}
	items := make([]DeviceItem, 0, len(list))
	for _, d := range list {
		items = append(items, DeviceItem{
			ID: d.ID, Kind: d.Kind, Label: d.Label, Platform: d.Platform, ControllerName: d.ControllerName,
			PassID: d.PassID, PassLabel: d.PassLabel, UserID: d.UserID,
			CreatedAt: d.CreatedAt.UTC(), LastSeenAt: utcPtr(d.LastSeenAt),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"devices": items})
}

// HandleRevokeDevice serves DELETE
// /v1/organizations/{org_id}/scanner/devices/{device_id}: the caller's own
// device (scan.perform) or any device of the organization
// (scanner_pass.manage). 204, an already revoked one too; 404
// scanner.device_not_found for a device of another organization, an unknown
// one or one of another person.
func (h *Handler) HandleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	orgID, ok := pathOrg(w, r)
	if !ok {
		return
	}
	userID, ok := callerUser(w, r)
	if !ok {
		return
	}
	deviceID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "device_id")))
	if err != nil {
		writeDeviceNotFound(w, r)
		return
	}
	var only *uuid.UUID
	if !h.canManage(r) {
		only = &userID
	}
	by := userID
	just, err := scannerapp.RevokeDeviceOf(r.Context(), h.Pool, orgID, deviceID, only, &by, h.now())
	if errors.Is(err, scannerapp.ErrDeviceNotFound) {
		writeDeviceNotFound(w, r)
		return
	}
	if err != nil {
		h.Logger.Error("hscannerapp: revoke device failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner.revoke_failed", "failed to revoke the device")
		return
	}
	if just {
		h.writeAudit(r, "user", userID.String(), "v1.scanner_app.device_revoke", "scanner_device", deviceID.String(),
			map[string]any{"org_id": orgID.String(), "reason": scannerapp.ReasonRevokedByUser})
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDeviceNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusNotFound, "scanner.device_not_found", "scanner device not found")
}
