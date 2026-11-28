// Package hscannerapp holds the handlers of the door-control app routes
// /v1/scanner-app/* (spec 69, MA-10). Every handler reads the device that the
// device-token middleware put in the context; nothing about the device, its
// organization or its pass comes from the request.
package hscannerapp

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// Handler serves the scanner-app routes.
type Handler struct {
	DB     gen.DBTX
	Logger *slog.Logger

	// The fields below serve the pairing, refresh and organizer routes
	// (MA-11 / MA-12); see Configure.

	// Pool opens the transactions of the store; nil answers 503.
	Pool scannerapp.TxDB
	// Audit records the changes; nil skips the audit rows (unit tests).
	Audit audit.Writer
	// Perms decides, inside the path organization, whether the caller also
	// holds scanner_pass.manage (all devices of the organization).
	Perms permissions.Checker
	// APIBaseURL is the public origin of the API, the host of the pairing
	// link.
	APIBaseURL string
	// TrustedProxies is forwarded to httputil.TrustedClientIP.
	TrustedProxies int
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// New builds a Handler. db may be nil: every route then answers 503.
func New(db gen.DBTX, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{DB: db, Logger: logger}
}

// Deps are the dependencies of the pairing, refresh and organizer routes.
type Deps struct {
	Pool           scannerapp.TxDB
	Audit          audit.Writer
	Perms          permissions.Checker
	APIBaseURL     string
	TrustedProxies int
	Now            func() time.Time
}

// Configure sets the dependencies of the MA-11 / MA-12 routes and returns h.
func (h *Handler) Configure(d Deps) *Handler {
	h.Pool, h.Audit, h.Perms = d.Pool, d.Audit, d.Perms
	h.APIBaseURL, h.TrustedProxies, h.Now = d.APIBaseURL, d.TrustedProxies, d.Now
	return h
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// MeResponse is the body of GET /v1/scanner-app/me.
type MeResponse struct {
	Device       MeDevice       `json:"device"`
	Organization MeOrganization `json:"organization"`
	Kind         string         `json:"kind"`
	Permissions  []string       `json:"permissions"`
	Pass         *MePass        `json:"pass,omitempty"`
}

// MeDevice is the device itself.
type MeDevice struct {
	ID       uuid.UUID `json:"id"`
	Label    string    `json:"label"`
	Platform string    `json:"platform"`
}

// MeOrganization is the organization of the device.
type MeOrganization struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// MePass is the controller pass of a pass device.
type MePass struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"`
	ValidFrom  *time.Time `json:"valid_from"`
	ValidUntil *time.Time `json:"valid_until"`
	// State is "active" or "closed" (turned off or its window ended); a
	// closed pass leaves its devices "upload only" until UploadUntil.
	State       string          `json:"state"`
	ClosedAt    *time.Time      `json:"closed_at"`
	UploadUntil *time.Time      `json:"upload_until"`
	Sessions    []MePassSession `json:"sessions"`
}

// MePassSession is one date the pass may check. AllTiers true: every
// category (TierIDs empty); false: ONLY TierIDs, nothing when it is empty.
type MePassSession struct {
	SessionID uuid.UUID   `json:"session_id"`
	EventID   uuid.UUID   `json:"event_id"`
	AllTiers  bool        `json:"all_tiers"`
	TierIDs   []uuid.UUID `json:"tier_ids"`
}

// HandleMe serves GET /v1/scanner-app/me: what the authenticated device is.
func (h *Handler) HandleMe(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is unavailable", r))
		return
	}
	d, ok := scannerapp.DeviceFromContext(r.Context())
	if !ok {
		writeUnauthorized(w, r)
		return
	}
	me, err := scannerapp.Describe(r.Context(), h.DB, d)
	if errors.Is(err, scannerapp.ErrPassNotFound) {
		h.Logger.Warn("scannerapp: pass of an authenticated device not found", "device_id", d.ID.String())
		writeUnauthorized(w, r)
		return
	}
	if err != nil {
		h.Logger.Error("scannerapp: describe device failed", "device_id", d.ID.String(), "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"scanner_app.read_failed", "failed to read the device", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, BuildMeResponse(d, me))
}

// BuildMeResponse maps a device and its description to the wire body.
func BuildMeResponse(d scannerapp.Device, me scannerapp.Me) MeResponse {
	out := MeResponse{
		Device:       MeDevice{ID: d.ID, Label: d.Label, Platform: d.Platform},
		Organization: MeOrganization{ID: me.OrgID, Name: me.OrgName},
		Kind:         me.Kind,
		Permissions:  auth.DevicePermissions(),
	}
	if me.Pass != nil {
		p := &MePass{
			ID: me.Pass.ID, Label: me.Pass.Label, Kind: me.Pass.Kind,
			ValidFrom: utcPtr(me.Pass.ValidFrom), ValidUntil: utcPtr(me.Pass.ValidUntil),
			State: me.Pass.State, ClosedAt: utcPtr(me.Pass.ClosedAt), UploadUntil: utcPtr(me.Pass.UploadUntil),
			Sessions: make([]MePassSession, 0, len(me.Pass.Sessions)),
		}
		if p.State == "" {
			p.State = scannerapp.PassStateActive
		}
		for _, s := range me.Pass.Sessions {
			tiers := s.TierIDs
			if tiers == nil || s.AllTiers {
				tiers = []uuid.UUID{}
			}
			p.Sessions = append(p.Sessions, MePassSession{SessionID: s.SessionID, EventID: s.EventID, AllTiers: s.AllTiers, TierIDs: tiers})
		}
		out.Pass = p
	}
	return out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(auth.HeaderWWWAuthenticate, auth.WWWAuthenticateBearer)
	httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
		"auth.invalid_token", "the presented device token is not valid", r))
}
