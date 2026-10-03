// session_change.go — the two endpoints a client needs around a session
// change (08_architecture/30_session_change_notifications_ru.md):
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/change-impact
//	    A dry run: what saving a new date / venue / cancellation would do —
//	    kinds, how many orders and tickets, who writes to them, whether the
//	    organizer has a contact, and the default text for the buyers' letter.
//	PUT /v1/organizations/{org_id}/events/{event_id}/contact
//	    Sets the organizer contact buyers can answer to (the event's promoter
//	    when it has one, else the organization).
//
// The decision itself lives in internal/platform/sessionchange; the handlers
// only present it, so every client (bot, event centers, admin) shows the same
// numbers the save will act on.
package hcatalog

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// sessionStateResponse is the buyer-visible slice of a session.
type sessionStateResponse struct {
	StartAt   string `json:"start_at"`
	EndAt     string `json:"end_at"`
	VenueID   string `json:"venue_id"`
	VenueName string `json:"venue_name"`
	Timezone  string `json:"timezone"`
	Status    string `json:"status"`
}

func sessionStateOut(s sessionchange.State) sessionStateResponse {
	return sessionStateResponse{
		StartAt:   s.StartAt.UTC().Format(time.RFC3339),
		EndAt:     s.EndAt.UTC().Format(time.RFC3339),
		VenueID:   s.VenueID.String(),
		VenueName: s.VenueName,
		Timezone:  s.Timezone,
		Status:    s.Status,
	}
}

// ContactResponse is the organizer contact as clients see it.
type ContactResponse struct {
	// Source is "promoter" or "organization"; empty when nobody has an e-mail.
	Source      string `json:"source"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	PhoneHidden bool   `json:"phone_hidden"`
	// Complete is true when buyers have an e-mail address to write to.
	Complete bool `json:"complete"`
	// TargetKind / TargetID / TargetName name the row a client fills in to
	// complete the contact: the event's promoter, or else the organization.
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
}

func contactOut(c sessionchange.Contact) ContactResponse {
	out := ContactResponse{
		Source: c.Source, Name: c.Name, Email: c.Email, Phone: c.Phone,
		PhoneHidden: c.PhoneHidden, Complete: c.Complete(),
		TargetKind: c.TargetKind, TargetName: c.TargetName,
	}
	if c.TargetID != uuid.Nil {
		out.TargetID = c.TargetID.String()
	}
	return out
}

// sessionChangeImpactResponse is the body of GET .../change-impact.
type sessionChangeImpactResponse struct {
	SessionID   string   `json:"session_id"`
	Kinds       []string `json:"kinds"`
	Orders      int      `json:"orders"`
	Tickets     int      `json:"tickets"`
	ArenaOrders int      `json:"arena_orders"`
	SiteOrders  int      `json:"site_orders"`
	NoAddress   int      `json:"no_address"`
	// Blocked is empty when the change may be saved, else
	// "contact_missing" or "site_route_unsupported".
	Blocked string          `json:"blocked"`
	Contact ContactResponse `json:"contact"`
	// DefaultMessage is the text offered to the organizer for the buyers'
	// letter, in the requested locale (the `locale` query parameter).
	DefaultMessage string               `json:"default_message"`
	Current        sessionStateResponse `json:"current"`
	Proposed       sessionStateResponse `json:"proposed"`
}

// HandleSessionChangeImpact serves GET
// /v1/organizations/{org_id}/sessions/{session_id}/change-impact.
// Query: start_at, end_at (RFC 3339), venue_id, status (only "cancelled"),
// locale (language of default_message). Writes nothing.
func (h *Handler) HandleSessionChangeImpact(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	db := h.sessionQueries.DB()
	sess, err := sessionchange.Load(ctx, db, sessionID, false)
	if err != nil || sess.OrgID != orgID || sess.State.Deleted {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: change impact load failed")
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.change_impact_failed", "failed to compute the change impact", r,
		))
		return
	}

	proposed, bad := h.proposedSessionState(w, r, orgID, sess.State)
	if bad {
		return
	}
	impact, err := sessionchange.Preview(ctx, db, sess, proposed)
	if err != nil {
		h.logger.Error("session: change impact failed")
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.change_impact_failed", "failed to compute the change impact", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, sessionChangeImpactResponse{
		SessionID:      sessionID.String(),
		Kinds:          impact.Kinds,
		Orders:         impact.Orders,
		Tickets:        impact.Tickets,
		ArenaOrders:    impact.ArenaOrders,
		SiteOrders:     impact.SiteOrders,
		NoAddress:      impact.NoAddress,
		Blocked:        impact.Blocked,
		Contact:        contactOut(impact.Contact),
		DefaultMessage: sessionchange.DefaultMessage(sessionchange.KindForDefault(impact.Kinds), r.URL.Query().Get("locale")),
		Current:        sessionStateOut(sess.State),
		Proposed:       sessionStateOut(proposed),
	})
}

// proposedSessionState applies the query parameters to the current state. It
// answers the client and reports bad=true on an invalid parameter.
func (h *Handler) proposedSessionState(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, cur sessionchange.State) (sessionchange.State, bool) {
	q := r.URL.Query()
	next := cur
	invalid := func(code, msg, field string) (sessionchange.State, bool) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(code, msg, r, map[string]any{"field": field}))
		return cur, true
	}
	duration := cur.EndAt.Sub(cur.StartAt)
	if raw := strings.TrimSpace(q.Get("start_at")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid("session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", "start_at")
		}
		next.StartAt = t
		next.EndAt = t.Add(duration)
	}
	if raw := strings.TrimSpace(q.Get("end_at")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return invalid("session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", "end_at")
		}
		next.EndAt = t
	}
	if raw := strings.TrimSpace(q.Get("venue_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return invalid("session.invalid_venue_id", "venue_id must be a valid UUID", "venue_id")
		}
		if id != cur.VenueID {
			var name, tz string
			err := h.sessionQueries.DB().QueryRow(r.Context(),
				`SELECT name, COALESCE(timezone, '') FROM venues WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
				id, orgID).Scan(&name, &tz)
			if err != nil {
				return invalid("session.invalid_venue_id", "venue_id is not a venue of this organization", "venue_id")
			}
			next.VenueID, next.VenueName, next.Timezone = id, name, tz
		}
	}
	if raw := strings.TrimSpace(q.Get("status")); raw != "" {
		if raw != sessionchange.StatusCancelled {
			return invalid("session.invalid_status", "status may only be \"cancelled\" in a change preview", "status")
		}
		next.Status = raw
	}
	return next, false
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT /v1/organizations/{org_id}/events/{event_id}/contact
// ─────────────────────────────────────────────────────────────────────────────

type setEventContactRequest struct {
	// Email is required: it is the address buyers write to.
	Email string `json:"email"`
	// Phone is optional; PhoneHidden keeps it out of letters to buyers.
	Phone       *string `json:"phone"`
	PhoneHidden *bool   `json:"phone_hidden"`
}

const maxContactPhoneRunes = 40

// HandleSetEventContact serves PUT /v1/organizations/{org_id}/events/{event_id}/contact.
// The contact is written to the event's promoter when it has one, otherwise to
// the organization, and the response is the contact buyers will now see.
func (h *Handler) HandleSetEventContact(w http.ResponseWriter, r *http.Request) {
	if !h.promoterDepsReady(w, r, true) {
		return
	}
	ctx := r.Context()
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.eventQueries, orgID) {
		return
	}
	if !h.requireEventInOrg(w, r, eventID, orgID) {
		return
	}
	var req setEventContactRequest
	if !readJSONBody(w, r, "contact", &req) {
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil || addr.Address == "" || strings.ContainsAny(addr.Address, " \t\r\n") || !strings.Contains(addr.Address, ".") {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"contact.invalid_email", "email must be a valid e-mail address", r, map[string]any{"field": "email"},
		))
		return
	}
	email := addr.Address
	var phone *string
	if req.Phone != nil {
		if p := strings.TrimSpace(*req.Phone); p != "" {
			if utf8.RuneCountInString(p) > maxContactPhoneRunes {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"contact.invalid_phone", "phone is too long", r, map[string]any{"field": "phone"},
				))
				return
			}
			phone = &p
		}
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope("dependency.database_unavailable", "failed to begin transaction", r))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	hidden := false
	if req.PhoneHidden != nil {
		hidden = *req.PhoneHidden
	}
	var promoterID uuid.UUID
	perr := tx.QueryRow(ctx, `SELECT promoter_id FROM event_promoters WHERE event_id = $1 AND org_id = $2`, eventID, orgID).Scan(&promoterID)
	switch {
	case perr == nil:
		_, err = tx.Exec(ctx,
			`UPDATE org_promoters SET email = $3, phone = $4, phone_hidden = $5, updated_at = now() WHERE id = $1 AND org_id = $2`,
			promoterID, orgID, email, phone, hidden)
	case errors.Is(perr, pgx.ErrNoRows):
		_, err = tx.Exec(ctx,
			`UPDATE organizations SET contact_email = $2, contact_phone = $3, contact_phone_hidden = $4, updated_at = now() WHERE id = $1`,
			orgID, email, phone, hidden)
	default:
		err = perr
	}
	if err != nil {
		h.logger.Error("contact: update failed")
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("contact.update_failed", "failed to save the contact", r))
		return
	}
	// The audit metadata names the row, never the address.
	if err := h.promoterAudit(ctx, tx, r, "v1.event.contact_set", "event", eventID.String(), map[string]any{
		"org_id": orgID.String(), "promoter": promoterID != uuid.Nil,
	}); err != nil {
		h.logger.Error("contact: audit write failed")
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("contact.audit_failed", "failed to write audit event", r))
		return
	}
	contact, err := sessionchange.ResolveContact(ctx, tx, eventID, orgID)
	if err != nil {
		h.logger.Error("contact: read back failed")
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("contact.update_failed", "failed to save the contact", r))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope("contact.commit_failed", "failed to commit transaction", r))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"contact": contactOut(contact)})
}
