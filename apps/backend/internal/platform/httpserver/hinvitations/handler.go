package hinvitations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// CancelTicketFunc is htickets.Handler.CancelTicketTx: the one ticket
// cancellation transaction (inventory release to seat_origin, capacity,
// barcode revocation, audit, v1.ticket.cancelled).
type CancelTicketFunc func(ctx context.Context, p htickets.CancelTicketParams) (htickets.CancelTicketOutcome, *htickets.CancelTicketError)

// Handler serves the staff invitation routes of spec 46 (task INV-02).
type Handler struct {
	pool         hcheckout.TxStarter
	queries      *gen.Queries
	audit        audit.Writer
	logger       *slog.Logger
	cancelTicket CancelTicketFunc
}

// New builds the handler. audit may be nil (no audit rows); cancelTicket
// nil makes the cancel route answer 503.
func New(pool hcheckout.TxStarter, queries *gen.Queries, auditWriter audit.Writer, logger *slog.Logger, cancelTicket CancelTicketFunc) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, queries: queries, audit: auditWriter, logger: logger, cancelTicket: cancelTicket}
}

const (
	maxBodyBytes  = 64 << 10
	maxNameRunes  = 200
	maxNoteRunes  = 1000
	listLimit     = 500
	cancelReason  = "invitation_cancelled"
	ticketsIssued = "issued"
	ticketsIssue  = "issuing"
)

type seatRef struct {
	SeatKey string `json:"seat_key"`
}

type gaRef struct {
	TierID string `json:"tier_id"`
	Qty    int    `json:"qty"`
}

type guestBody struct {
	Name   string  `json:"name"`
	Email  *string `json:"email"`
	Phone  *string `json:"phone"`
	Locale *string `json:"locale"`
}

type issueBody struct {
	RequestID string    `json:"request_id"`
	Seats     []seatRef `json:"seats"`
	GA        []gaRef   `json:"ga"`
	Guest     guestBody `json:"guest"`
	Delivery  string    `json:"delivery"`
	Note      *string   `json:"note"`
}

// lineResponse is one place of an invitation.
type lineResponse struct {
	SeatKey   *string `json:"seat_key"`
	Sector    *string `json:"sector"`
	Row       *string `json:"row"`
	Seat      *string `json:"seat"`
	Kind      *string `json:"kind"`
	TierID    string  `json:"tier_id"`
	TierName  string  `json:"tier_name"`
	FaceValue int64   `json:"face_value"`
	Origin    *string `json:"origin"`
	TicketID  *string `json:"ticket_id"`
}

type issueResponse struct {
	OrderID       string         `json:"order_id"`
	SystemID      int64          `json:"system_id"`
	Status        string         `json:"status"`
	GuestName     *string        `json:"guest_name"`
	Delivery      string         `json:"delivery"`
	Places        []lineResponse `json:"places"`
	FromReserve   int            `json:"from_reserve"`
	TicketsStatus string         `json:"tickets_status"`
	// Charge is the billing preview (spec 46, section 3.6) — INV-07.
	Charge   any  `json:"charge"`
	Replayed bool `json:"replayed"`
}

// HandleIssue serves POST /v1/organizations/{org_id}/sessions/{session_id}/invitations.
func (h *Handler) HandleIssue(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil || h.queries == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	var body issueBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil {
		writeErr(w, r, http.StatusBadRequest, "invitation.invalid_body", "the request body is not valid JSON", nil)
		return
	}
	in, ierr := validateIssue(body)
	if ierr != nil {
		writeIssueErr(w, r, ierr)
		return
	}
	ctx := r.Context()
	actor, _ := auth.ActorFromContext(ctx)
	in.OrgID, in.SessionID = orgID, sessionID
	in.Actor = "system"
	auditType := "service"
	if actor.Type == auth.ActorTypeUser || actor.Type == auth.ActorTypeStubUser {
		if uid, err := uuid.Parse(actor.ID); err == nil {
			in.IssuedBy = &uid
			in.Actor = "user:" + uid.String()
			auditType = "user"
		}
	}
	if in.Delivery == DeliveryMyEmail && in.IssuedBy == nil {
		writeErr(w, r, http.StatusUnprocessableEntity, "invitation.my_email_requires_user",
			"delivery my_email needs a signed-in user; an API key has no e-mail", nil)
		return
	}
	if h.audit != nil {
		in.Audit = func(ctx context.Context, tx pgx.Tx, order gen.OrderRow, meta map[string]any) error {
			actorID := ""
			if in.IssuedBy != nil {
				actorID = in.IssuedBy.String()
			}
			return h.audit.WriteTx(ctx, tx, audit.Event{
				OccurredAt:   time.Now().UTC(),
				ActorType:    auditType,
				ActorID:      actorID,
				Action:       "v1.invitation.issue",
				ResourceType: "order",
				ResourceID:   order.ID.String(),
				RequestID:    logging.RequestID(ctx),
				TraceID:      logging.TraceID(ctx),
				Metadata:     meta,
			})
		}
	}

	res, err := Issue(ctx, h.pool, h.queries, in)
	if err != nil {
		var ie *IssueError
		if errors.As(err, &ie) {
			writeIssueErr(w, r, ie)
			return
		}
		h.logger.Error("invitation.issue failed", slog.String("session_id", sessionID.String()), slog.String("error", err.Error()))
		writeErr(w, r, http.StatusInternalServerError, "invitation.issue_failed", "the invitation could not be issued", nil)
		return
	}
	resp, err := h.loadIssued(ctx, res.OrderID, orgID)
	if err != nil {
		h.logger.Error("invitation.issue: read back failed", slog.String("order_id", res.OrderID.String()), slog.String("error", err.Error()))
		writeErr(w, r, http.StatusInternalServerError, "invitation.issue_failed", "the invitation was issued but could not be read back", nil)
		return
	}
	resp.Replayed = res.Replayed
	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
	}
	httputil.WriteJSON(w, status, resp)
}

// validateIssue checks the request's shape (spec 46, section 3.1).
func validateIssue(b issueBody) (IssueInput, *IssueError) {
	var in IssueInput
	rid, err := uuid.Parse(strings.TrimSpace(b.RequestID))
	if err != nil || rid == uuid.Nil {
		return in, refuse(http.StatusBadRequest, "invitation.invalid_request_id", "request_id must be a UUID", nil)
	}
	in.RequestID = rid

	seen := map[string]bool{}
	for _, s := range b.Seats {
		k := strings.TrimSpace(s.SeatKey)
		if k == "" || seen[k] {
			return in, refuse(http.StatusBadRequest, "invitation.invalid_seats",
				"every seat_key must be non-empty and named once", nil)
		}
		seen[k] = true
		in.SeatKeys = append(in.SeatKeys, k)
	}
	total := len(in.SeatKeys)
	for _, g := range b.GA {
		tid, err := uuid.Parse(strings.TrimSpace(g.TierID))
		if err != nil || g.Qty < 1 {
			return in, refuse(http.StatusBadRequest, "invitation.invalid_ga",
				"every ga line needs a tier_id and a qty of at least 1", nil)
		}
		total += g.Qty
		if total > MaxPlaces {
			break
		}
		in.GA = append(in.GA, GALine{TierID: tid, Quantity: int32(g.Qty)}) //nolint:gosec // bounded by MaxPlaces
	}
	if total == 0 {
		return in, refuse(http.StatusBadRequest, "invitation.empty", "name at least one seat or one ga place", nil)
	}
	if total > MaxPlaces {
		return in, refuse(http.StatusBadRequest, "invitation.too_many",
			"one invitation takes at most 50 places, upload a CSV for more", map[string]any{"max": MaxPlaces})
	}

	name := strings.TrimSpace(b.Guest.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return in, refuse(http.StatusBadRequest, "invitation.invalid_guest_name",
			"guest.name is required, 1 to 200 characters", nil)
	}
	in.Guest.Name = name
	in.Guest.Email = trimPtr(b.Guest.Email)
	in.Guest.Phone = trimPtr(b.Guest.Phone)
	in.Guest.Locale = trimPtr(b.Guest.Locale)
	if in.Guest.Email != "" && !strings.Contains(in.Guest.Email, "@") {
		return in, refuse(http.StatusBadRequest, "invitation.invalid_guest_email", "guest.email is not an e-mail address", nil)
	}

	switch b.Delivery {
	case DeliveryGuestEmail:
		if in.Guest.Email == "" {
			return in, refuse(http.StatusUnprocessableEntity, "invitation.guest_email_required",
				"delivery guest_email needs guest.email", nil)
		}
	case DeliveryMyEmail, DeliveryPDFOnly:
	default:
		return in, refuse(http.StatusBadRequest, "invitation.invalid_delivery",
			"delivery must be guest_email, my_email or pdf_only", nil)
	}
	in.Delivery = b.Delivery
	in.Note = trimPtr(b.Note)
	if utf8.RuneCountInString(in.Note) > maxNoteRunes {
		return in, refuse(http.StatusBadRequest, "invitation.note_too_long", "note is at most 1000 characters", nil)
	}
	return in, nil
}

// loadIssued builds the issue answer from the committed rows, so a replay
// and a fresh issue answer the same shape.
func (h *Handler) loadIssued(ctx context.Context, orderID, orgID uuid.UUID) (issueResponse, error) {
	o, err := h.queries.GetInvitationOrder(ctx, orderID, orgID)
	if err != nil {
		return issueResponse{}, err
	}
	lines, err := h.queries.ListInvitationOrderLines(ctx, []uuid.UUID{orderID})
	if err != nil {
		return issueResponse{}, err
	}
	resp := issueResponse{
		OrderID:       o.ID.String(),
		SystemID:      o.SystemID,
		Status:        o.Status,
		GuestName:     o.BuyerName,
		Delivery:      deliveryOf(o.Metadata),
		Places:        make([]lineResponse, 0, len(lines)),
		TicketsStatus: ticketsIssue,
		Charge:        nil,
	}
	issued := len(lines) > 0
	for _, l := range lines {
		resp.Places = append(resp.Places, toLine(l))
		if l.SeatOrigin != nil && *l.SeatOrigin == originUnavailable {
			resp.FromReserve++
		}
		if l.TicketID == nil {
			issued = false
		}
	}
	if issued {
		resp.TicketsStatus = ticketsIssued
	}
	return resp, nil
}

func toLine(l gen.InvitationOrderLineRow) lineResponse {
	out := lineResponse{
		SeatKey: l.SeatKey, Sector: l.SectorName, Row: l.RowName, Seat: l.SeatNumber, Kind: l.Kind,
		TierID: l.TierID.String(), TierName: l.TierName, FaceValue: l.UnitPrice, Origin: l.SeatOrigin,
	}
	if l.TicketID != nil {
		s := l.TicketID.String()
		out.TicketID = &s
	}
	return out
}

// deliveryOf reads orders.metadata.invitation.delivery.
func deliveryOf(raw json.RawMessage) string {
	var m struct {
		Invitation struct {
			Delivery string `json:"delivery"`
		} `json:"invitation"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Invitation.Delivery
}

type issuerResponse struct {
	UserID    *string `json:"user_id"`
	Email     *string `json:"email"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
}

type listItem struct {
	OrderID       string         `json:"order_id"`
	SystemID      int64          `json:"system_id"`
	Status        string         `json:"status"`
	GuestName     *string        `json:"guest_name"`
	Delivery      string         `json:"delivery"`
	IssuedVia     string         `json:"issued_via"`
	IssuedBy      issuerResponse `json:"issued_by"`
	CreatedAt     time.Time      `json:"created_at"`
	TicketsTotal  int64          `json:"tickets_total"`
	TicketsActive int64          `json:"tickets_active"`
	Places        []lineResponse `json:"places"`
}

// HandleList serves GET /v1/organizations/{org_id}/sessions/{session_id}/invitations:
// every staff invitation of the session, whoever issued it (spec 46 R-5).
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()
	target, err := h.queries.GetInvitationSessionTarget(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && target.OrgID != orgID) {
		writeErr(w, r, http.StatusNotFound, "session.not_found", "session not found", nil)
		return
	}
	if err != nil {
		h.internal(w, r, "list: session lookup", err)
		return
	}
	rows, err := h.queries.ListSessionInvitations(ctx, orgID, sessionID, listLimit)
	if err != nil {
		h.internal(w, r, "list", err)
		return
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	byOrder := map[uuid.UUID][]lineResponse{}
	if len(ids) > 0 {
		lines, err := h.queries.ListInvitationOrderLines(ctx, ids)
		if err != nil {
			h.internal(w, r, "list: lines", err)
			return
		}
		for _, l := range lines {
			byOrder[l.OrderID] = append(byOrder[l.OrderID], toLine(l))
		}
	}
	items := make([]listItem, 0, len(rows))
	for _, row := range rows {
		it := listItem{
			OrderID: row.ID.String(), SystemID: row.SystemID, Status: row.Status, GuestName: row.BuyerName,
			Delivery: deliveryOf(row.Metadata), IssuedVia: row.IssuedVia, CreatedAt: row.CreatedAt,
			TicketsTotal: row.TicketsTotal, TicketsActive: row.TicketsActive,
			IssuedBy: issuerResponse{Email: row.IssuerEmail, FirstName: row.IssuerFirst, LastName: row.IssuerLast},
			Places:   byOrder[row.ID],
		}
		if it.Places == nil {
			it.Places = []lineResponse{}
		}
		if row.IssuedBy != nil {
			s := row.IssuedBy.String()
			it.IssuedBy.UserID = &s
		}
		items = append(items, it)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

type cancelBody struct {
	TicketIDs   []string `json:"ticket_ids"`
	NotifyGuest *bool    `json:"notify_guest"`
}

type cancelResponse struct {
	OrderID            string   `json:"order_id"`
	OrderStatus        string   `json:"order_status"`
	CancelledTicketIDs []string `json:"cancelled_ticket_ids"`
	SkippedTicketIDs   []string `json:"skipped_ticket_ids"`
	NotifyGuest        bool     `json:"notify_guest"`
}

// HandleCancel serves POST /v1/organizations/{org_id}/invitations/{order_id}/cancel.
// Every active ticket named (or all of them) is cancelled through the one
// ticket cancellation transaction with no money (refund_mode none), so each
// seat goes back where it came from (order_items.seat_origin, INV-01).
func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.cancelTicket == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	orderID, ok := httputil.UUIDPathParam(w, r, "order_id")
	if !ok {
		return
	}
	var body cancelBody
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, r, http.StatusBadRequest, "invitation.invalid_body", "the request body is not valid JSON", nil)
		return
	}
	notify := body.NotifyGuest == nil || *body.NotifyGuest
	ctx := r.Context()

	o, err := h.queries.GetInvitationOrder(ctx, orderID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, r, http.StatusNotFound, "invitation.not_found", "invitation not found", nil)
		return
	}
	if err != nil {
		h.internal(w, r, "cancel: order lookup", err)
		return
	}
	tickets, err := h.queries.ListTicketsByCheckoutSession(ctx, o.CheckoutSessionID)
	if err != nil {
		h.internal(w, r, "cancel: tickets", err)
		return
	}
	if len(tickets) == 0 {
		writeErr(w, r, http.StatusConflict, "invitation.tickets_not_issued",
			"the tickets of this invitation are still being issued, try again in a moment", nil)
		return
	}
	byID := make(map[uuid.UUID]gen.TicketRow, len(tickets))
	for _, t := range tickets {
		byID[t.ID] = t
	}
	targets := tickets
	if len(body.TicketIDs) > 0 {
		targets = targets[:0:0]
		for _, raw := range body.TicketIDs {
			id, err := uuid.Parse(strings.TrimSpace(raw))
			t, found := byID[id]
			if err != nil || !found {
				writeErr(w, r, http.StatusBadRequest, "invitation.unknown_ticket",
					"a ticket_id is not a ticket of this invitation", map[string]any{"ticket_id": raw})
				return
			}
			targets = append(targets, t)
		}
	}

	actor, _ := auth.ActorFromContext(ctx)
	actorType, actorID := "service", ""
	if actor.Type == auth.ActorTypeUser || actor.Type == auth.ActorTypeStubUser {
		if _, err := uuid.Parse(actor.ID); err == nil {
			actorType, actorID = "user", actor.ID
		}
	}
	resp := cancelResponse{OrderID: o.ID.String(), CancelledTicketIDs: []string{}, SkippedTicketIDs: []string{}, NotifyGuest: notify}
	for _, t := range targets {
		if t.Status != "active" {
			resp.SkippedTicketIDs = append(resp.SkippedTicketIDs, t.ID.String())
			continue
		}
		_, cerr := h.cancelTicket(ctx, htickets.CancelTicketParams{
			TicketID:   t.ID,
			Reason:     cancelReason,
			RefundMode: htickets.RefundModeNone,
			ActorType:  actorType,
			ActorID:    actorID,
		})
		if cerr != nil {
			if cerr.Code == "ticket.not_active" {
				resp.SkippedTicketIDs = append(resp.SkippedTicketIDs, t.ID.String())
				continue
			}
			h.logger.Error("invitation.cancel: ticket cancel failed",
				slog.String("order_id", o.ID.String()), slog.String("ticket_id", t.ID.String()), slog.String("error", cerr.Error()))
			writeErr(w, r, cerr.Status, cerr.Code, cerr.Message, map[string]any{
				"cancelled_ticket_ids": resp.CancelledTicketIDs,
			})
			return
		}
		resp.CancelledTicketIDs = append(resp.CancelledTicketIDs, t.ID.String())
	}

	resp.OrderStatus = h.projectCancel(ctx, o, orgID, resp, actorType, actorID)
	// TODO(INV-03): send the guest the `cancel` letter when notify_guest is
	// true and the order carries the guest's e-mail (spec 46, section 3.4,
	// decision R-9). The choice is recorded on order_events above so the
	// letter job can read it.
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// projectCancel moves the order to 'cancelled' once none of its tickets is
// active, records the cancellation on order_events and audits it. A failure
// here is logged: the tickets are already cancelled and their seats back.
func (h *Handler) projectCancel(ctx context.Context, o gen.InvitationOrderRow, orgID uuid.UUID, resp cancelResponse, actorType, actorID string) string {
	status := o.Status
	tickets, err := h.queries.ListTicketsByCheckoutSession(ctx, o.CheckoutSessionID)
	if err == nil && len(resp.CancelledTicketIDs) > 0 {
		active := 0
		for _, t := range tickets {
			if t.Status == "active" {
				active++
			}
		}
		if active == 0 && status != "cancelled" {
			now := time.Now().UTC()
			if row, uerr := h.queries.UpdateOrderStatus(ctx, o.ID, orgID, "cancelled", nil, &now); uerr == nil {
				status = row.Status
			} else {
				h.logger.Warn("invitation.cancel: order status update failed",
					slog.String("order_id", o.ID.String()), slog.String("error", uerr.Error()))
			}
		}
	}
	if len(resp.CancelledTicketIDs) == 0 {
		return status
	}
	payload, _ := json.Marshal(map[string]any{
		"ticket_ids":   resp.CancelledTicketIDs,
		"notify_guest": resp.NotifyGuest,
	})
	actor := "system"
	if actorType == "user" {
		actor = "user:" + actorID
	}
	if _, eerr := h.queries.InsertOrderEvent(ctx, o.ID, "invitation_cancelled", actor, payload); eerr != nil {
		h.logger.Warn("invitation.cancel: order event insert failed",
			slog.String("order_id", o.ID.String()), slog.String("error", eerr.Error()))
	}
	if h.audit != nil {
		if aerr := h.audit.Write(ctx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    actorType,
			ActorID:      actorID,
			Action:       "v1.invitation.cancel",
			ResourceType: "order",
			ResourceID:   o.ID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			Metadata: map[string]any{
				"session_id":   o.SessionID.String(),
				"tickets":      len(resp.CancelledTicketIDs),
				"notify_guest": resp.NotifyGuest,
				"order_status": status,
			},
		}); aerr != nil {
			h.logger.Warn("invitation.cancel: audit failed", slog.String("error", aerr.Error()))
		}
	}
	return status
}

func trimPtr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string, details map[string]any) {
	if details != nil {
		httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(code, msg, r, details))
		return
	}
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func writeIssueErr(w http.ResponseWriter, r *http.Request, e *IssueError) {
	writeErr(w, r, e.Status, e.Code, e.Message, e.Details)
}

func unavailable(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusServiceUnavailable, "dependency.database_unavailable", "database is not available", nil)
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("invitation."+what+" failed", slog.String("error", err.Error()))
	writeErr(w, r, http.StatusInternalServerError, "invitation.internal", "the invitations could not be read", nil)
}
