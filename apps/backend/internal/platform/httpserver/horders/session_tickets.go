package horders

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// M2: the tickets of one session with what the door did with them, and the
// filter the organizer needs on the evening — "who has not come yet".
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/tickets
//	    ?entered=false|true&limit&offset                       (order.read)
//
// entered=false lists the active tickets nobody entered with, entered=true
// the tickets with an entry, no filter lists every ticket. The page carries
// the buyer's name and e-mail, so an organization API key is refused (403),
// like the letter-interactions route: the data goes to people of the
// organization, never to an integration. The session is loaded inside the
// path organization, a foreign or unknown one is the route's own 404.

type sessionTicketScan struct {
	ScannedAt    string `json:"scanned_at"`
	Result       string `json:"result"`
	Source       string `json:"source"`
	ScannerLabel string `json:"scanner_label"`
	Gate         string `json:"gate"`
}

type sessionTicketItem struct {
	TicketID       string             `json:"ticket_id"`
	SystemTicketID int64              `json:"system_ticket_id"`
	Status         string             `json:"status"`
	TierName       *string            `json:"tier_name"`
	SeatLabel      *string            `json:"seat_label"`
	OrderID        *string            `json:"order_id"`
	OrderNumber    *int64             `json:"order_number"`
	BuyerName      *string            `json:"buyer_name"`
	BuyerEmail     *string            `json:"buyer_email"`
	Entered        bool               `json:"entered"`
	EnteredAt      *string            `json:"entered_at"`
	LastScan       *sessionTicketScan `json:"last_scan"`
}

type sessionTicketList struct {
	SessionID     string              `json:"session_id"`
	VenueTimezone *string             `json:"venue_timezone"`
	Total         int64               `json:"total"`
	HasMore       bool                `json:"has_more"`
	Items         []sessionTicketItem `json:"items"`
}

// parseEnteredFilter reads ?entered=: absent = no filter, true/false = the
// two lists, anything else is false-and-ok=false (the caller answers 400).
func parseEnteredFilter(r *http.Request) (*bool, bool) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("entered"))) {
	case "":
		return nil, true
	case "true":
		v := true
		return &v, true
	case "false":
		v := false
		return &v, true
	}
	return nil, false
}

// sessionTicketEntry shapes one row. A ticket is entered when the query found
// a first admission (EnteredAt); the last scan is shown whatever its result,
// so a ticket the gate turned away shows why it has not come in.
func sessionTicketEntry(r gen.SessionTicketEntryRow) sessionTicketItem {
	item := sessionTicketItem{
		TicketID:       r.TicketID.String(),
		SystemTicketID: r.SystemTicketID,
		Status:         r.Status,
		TierName:       r.TierName,
		SeatLabel:      seatLabel(r.SeatSector, r.SeatRow, r.SeatNumber),
		OrderNumber:    r.OrderNumber,
		BuyerName:      r.BuyerName,
		BuyerEmail:     r.BuyerEmail,
		Entered:        r.EnteredAt != nil,
		EnteredAt:      timePtr(r.EnteredAt),
	}
	if r.OrderID != nil {
		s := r.OrderID.String()
		item.OrderID = &s
	}
	if r.LastScannedAt != nil {
		item.LastScan = &sessionTicketScan{
			ScannedAt:    r.LastScannedAt.UTC().Format(time.RFC3339),
			Result:       deref(r.LastResult),
			Source:       deref(r.LastSource),
			ScannerLabel: deref(r.LastScannerLabel),
			Gate:         deref(r.LastGate),
		}
	}
	return item
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// HandleSessionTickets serves GET .../sessions/{session_id}/tickets.
func (h *Handler) HandleSessionTickets(w http.ResponseWriter, r *http.Request) {
	if actor, ok := auth.ActorFromContext(r.Context()); ok && actor.IsService() {
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			"orders.session_tickets_api_key_forbidden",
			"an API key cannot read the buyers of a session's tickets; a member of the organization reads them signed in",
			r,
		))
		return
	}
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
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
	entered, ok := parseEnteredFilter(r)
	if !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"orders.invalid_entered", "entered must be true or false", r))
		return
	}
	limit, offset, ok := parsePagination(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	header, err := h.queries.GetSessionSummaryHeader(ctx, sessionID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	if err != nil {
		h.sessionTicketsFailure(w, r, "session", err)
		return
	}
	total, err := h.queries.CountSessionTicketsForEntry(ctx, sessionID, orgID, entered)
	if err != nil {
		h.sessionTicketsFailure(w, r, "count", err)
		return
	}
	rows, err := h.queries.ListSessionTicketsForEntry(ctx, sessionID, orgID, entered, limit, offset)
	if err != nil {
		h.sessionTicketsFailure(w, r, "rows", err)
		return
	}
	out := sessionTicketList{
		SessionID:     sessionID.String(),
		VenueTimezone: header.VenueTimezone,
		Total:         total,
		HasMore:       int64(offset)+int64(len(rows)) < total,
		Items:         make([]sessionTicketItem, 0, len(rows)),
	}
	for _, row := range rows {
		out.Items = append(out.Items, sessionTicketEntry(row))
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// sessionTicketsFailure answers 500; the log carries the failing part, never
// a row (the page holds buyer data).
func (h *Handler) sessionTicketsFailure(w http.ResponseWriter, r *http.Request, part string, err error) {
	h.logger.Error("horders: session tickets failed",
		slog.String("part", part), slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError,
		httputil.ErrorEnvelope("orders.internal", "failed to load the session's tickets", r))
}
