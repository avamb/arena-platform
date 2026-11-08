package hexport

// guest_list.go — the organizer's guest list of a session (owner decision
// 2026-11-02, spec 51 section 4.4, package guestlist):
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/guest-list.xlsx (order.read + session.read)
//	GET /v1/organizations/{org_id}/sessions/{session_id}/guest-list.csv  (order.read + session.read)
//	GET /v1/organizations/{org_id}/events/{event_id}/guest-list.xlsx     (order.read + session.read)
//	GET /v1/organizations/{org_id}/guest-list-subscription               (order.read)
//	PUT /v1/organizations/{org_id}/guest-list-subscription               (order.read)
//
// The file is built by guestlist.Build, the same code the worker mails. The
// subscription belongs to the PERSON who calls (a member, never an API key):
// when it is on, the guest_list.schedule job sends them the list of every
// session 24 hours and 3 hours before the start and at doors open, to their
// private chat with the event center bot or, without the bot, by e-mail.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// HandleSessionGuestListXLSX serves GET .../sessions/{session_id}/guest-list.xlsx,
// the Excel workbook people open.
func (h *Handler) HandleSessionGuestListXLSX(w http.ResponseWriter, r *http.Request) {
	h.serveGuestList(w, r, guestlist.XLSX)
}

// HandleSessionGuestList serves GET .../sessions/{session_id}/guest-list.csv,
// the same rows as a CSV for imports into other systems.
func (h *Handler) HandleSessionGuestList(w http.ResponseWriter, r *http.Request) {
	h.serveGuestList(w, r, guestlist.CSV)
}

// HandleEventGuestListXLSX serves GET .../events/{event_id}/guest-list.xlsx:
// the tickets of every session of the event in one sheet, a Session column
// (date and time in the venue's zone) first.
func (h *Handler) HandleEventGuestListXLSX(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := uuidParam(w, r, "event_id")
	if !ok {
		return
	}
	if guestListForbiddenForKey(w, r) {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetExportEventHeader(ctx, eventID, orgID)
	if isNoRows(err) {
		notFound(w, r, "event.not_found", "event not found")
		return
	}
	if err != nil {
		h.internal(w, r, "event", err)
		return
	}
	file, err := guestlist.BuildEvent(ctx, h.queries, orgID, eventID, csvexport.LocaleFromRequest(r), time.UTC, h.maxRows, guestlist.XLSX)
	if errors.Is(err, guestlist.ErrTooManyRows) {
		n, _ := h.queries.CountEventGuestListRows(ctx, eventID, orgID)
		h.tooManyRows(w, r, n)
		return
	}
	if err != nil {
		h.internal(w, r, "guest list", err)
		return
	}
	slug := ""
	if header.Slug != nil {
		slug = *header.Slug
	}
	h.writeGuestListFile(w, file, guestlist.FileName(header.Name, slug, time.Now(), time.UTC, guestlist.XLSX))
}

// guestListForbiddenForKey refuses an organization API key: the file names
// buyers and their contacts, like the per-date ticket list.
func guestListForbiddenForKey(w http.ResponseWriter, r *http.Request) bool {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"guest_list.api_key_forbidden", "the guest list holds buyers' contacts and is not served to an API key", r))
		return true
	}
	return false
}

func (h *Handler) writeGuestListFile(w http.ResponseWriter, file guestlist.File, name string) {
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("X-Row-Count", strconv.Itoa(file.Rows))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Body)
}

// serveGuestList writes one row per ticket (every status), sorted by the
// guest's last name. X-Row-Count carries the number of data rows, so a client (the bot)
// can say how many tickets the file holds without reading it.
func (h *Handler) serveGuestList(w http.ResponseWriter, r *http.Request, format guestlist.Format) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r, "session_id")
	if !ok {
		return
	}
	if guestListForbiddenForKey(w, r) {
		return
	}
	ctx := r.Context()
	header, err := h.queries.GetExportSessionHeader(ctx, sessionID, orgID)
	if isNoRows(err) {
		notFound(w, r, "session.not_found", "session not found")
		return
	}
	if err != nil {
		h.internal(w, r, "session", err)
		return
	}
	loc := venueLocation(header.VenueTimezone)
	file, err := guestlist.Build(ctx, h.queries, orgID, sessionID, csvexport.LocaleFromRequest(r), loc, h.maxRows, format)
	if errors.Is(err, guestlist.ErrTooManyRows) {
		n, _ := h.queries.CountGuestListRows(ctx, sessionID, orgID)
		h.tooManyRows(w, r, n)
		return
	}
	if err != nil {
		h.internal(w, r, "guest list", err)
		return
	}
	slug := ""
	if header.EventSlug != nil {
		slug = *header.EventSlug
	}
	name := guestlist.FileName(header.EventName, slug, header.StartAt, loc, format)
	h.writeGuestListFile(w, file, name)
}

// GuestListSubscription is the answer of the subscription routes. channel
// says how the automatic list reaches the person: "telegram" (their private
// chat with the bot), "email" (no bot link) or "none" (neither: nothing can
// be sent). eligible is false for a role that never receives the list (only
// owners and managers do); such a person cannot turn it on.
type GuestListSubscription struct {
	Enabled  bool   `json:"enabled"`
	Channel  string `json:"channel"`
	Eligible bool   `json:"eligible"`
}

// callerUser is the member a subscription belongs to. An API key has no
// person behind it and is refused.
func callerUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	actor, ok := auth.ActorFromContext(r.Context())
	if ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"guest_list.api_key_forbidden", "the guest list subscription belongs to a person, not to an API key", r))
		return uuid.Nil, false
	}
	id, err := uuid.Parse(actor.ID)
	if !ok || err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"auth.unauthenticated", "sign in to change the guest list subscription", r))
		return uuid.Nil, false
	}
	return id, true
}

// HandleGetGuestListSubscription serves GET .../guest-list-subscription.
func (h *Handler) HandleGetGuestListSubscription(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	userID, ok := callerUser(w, r)
	if !ok {
		return
	}
	h.writeSubscription(w, r, orgID, userID)
}

// HandlePutGuestListSubscription serves PUT .../guest-list-subscription with
// {"enabled": true|false}. A repeat of the current value changes nothing.
func (h *Handler) HandlePutGuestListSubscription(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	userID, ok := callerUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Enabled == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"guest_list.invalid_body", `send {"enabled": true} or {"enabled": false}`, r))
		return
	}
	var err error
	if *body.Enabled {
		row, gerr := h.queries.GetGuestListSubscription(r.Context(), orgID, userID)
		if gerr != nil {
			h.internal(w, r, "subscription", gerr)
			return
		}
		if !row.Eligible {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"guest_list.role_not_eligible", "only the owners and managers of the organization receive the guest list", r))
			return
		}
		err = h.queries.InsertGuestListSubscription(r.Context(), orgID, userID)
	} else {
		err = h.queries.DeleteGuestListSubscription(r.Context(), orgID, userID)
	}
	if err != nil {
		h.internal(w, r, "subscription", err)
		return
	}
	h.writeSubscription(w, r, orgID, userID)
}

func (h *Handler) writeSubscription(w http.ResponseWriter, r *http.Request, orgID, userID uuid.UUID) {
	row, err := h.queries.GetGuestListSubscription(r.Context(), orgID, userID)
	if err != nil {
		h.internal(w, r, "subscription", err)
		return
	}
	channel := "none"
	switch {
	case row.TelegramLinked:
		channel = "telegram"
	case row.HasEmail:
		channel = "email"
	}
	httputil.WriteJSON(w, http.StatusOK, GuestListSubscription{Enabled: row.Enabled, Channel: channel, Eligible: row.Eligible})
}

// GuestListSettings is the organization-level switch of the team guest list
// (guest_list.team_send): the current list goes to the owners, the managers
// and the organization's sales Telegram groups 24 h and 3 h before every
// session. On by default.
type GuestListSettings struct {
	AutoSend bool `json:"auto_send"`
}

// HandleGetGuestListSettings serves GET .../guest-list-settings.
func (h *Handler) HandleGetGuestListSettings(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	h.writeGuestListSettings(w, r, orgID)
}

// HandlePutGuestListSettings serves PUT .../guest-list-settings with
// {"auto_send": true|false}; a repeat of the current value changes nothing.
func (h *Handler) HandlePutGuestListSettings(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r) {
		return
	}
	orgID, ok := uuidParam(w, r, "org_id")
	if !ok {
		return
	}
	var body struct {
		AutoSend *bool `json:"auto_send"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.AutoSend == nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"guest_list.invalid_body", `send {"auto_send": true} or {"auto_send": false}`, r))
		return
	}
	if _, err := h.queries.GetOrgGuestListAutoSend(r.Context(), orgID); isNoRows(err) {
		notFound(w, r, "org.not_found", "organization not found")
		return
	} else if err != nil {
		h.internal(w, r, "settings", err)
		return
	}
	if err := h.queries.SetOrgGuestListAutoSend(r.Context(), orgID, *body.AutoSend); err != nil {
		h.internal(w, r, "settings", err)
		return
	}
	h.writeGuestListSettings(w, r, orgID)
}

func (h *Handler) writeGuestListSettings(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) {
	on, err := h.queries.GetOrgGuestListAutoSend(r.Context(), orgID)
	if isNoRows(err) {
		notFound(w, r, "org.not_found", "organization not found")
		return
	}
	if err != nil {
		h.internal(w, r, "settings", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, GuestListSettings{AutoSend: on})
}
