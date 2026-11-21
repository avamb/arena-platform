package ticketreissue

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TicketOption is one ticket of the order on the check screen.
type TicketOption struct {
	TicketID   uuid.UUID
	Number     int64
	Seat       string
	Category   string
	Status     string
	Reissuable bool
	// Reason is why an active ticket blocks the operation ("review_hold",
	// "refund_in_progress"), or "not_active" for a ticket the reissue skips.
	Reason    string
	ScannedAt *time.Time
}

// Options is what the check screen shows (GET .../reissue-options).
type Options struct {
	// Route: "arena", or "seller_site" while a live selling site writes the
	// order's letters.
	Route       string
	OrderID     uuid.UUID
	OrderNumber int64
	OrderStatus string
	OrderEmail  string
	BuyerName   string
	BuyerPhone  string
	// VenueTimezone is the IANA zone to print scanned_at in.
	VenueTimezone string
	Tickets       []TicketOption
	// Blocked is the error code a reissue would answer now, "" when it may
	// run.
	Blocked  string
	Open     *Open
	Provider Provider
}

// LoadOptions reads the order and its tickets without locking anything.
func LoadOptions(ctx context.Context, q Querier, orgID, orderID uuid.UUID) (Options, error) {
	o, err := loadOrder(ctx, q, orgID, orderID, false)
	if err != nil {
		return Options{}, err
	}
	out := Options{
		Route: "arena", OrderID: o.ID, OrderNumber: o.Number, OrderStatus: o.Status,
		OrderEmail: o.BuyerEmail, BuyerName: o.BuyerName, BuyerPhone: o.BuyerPhone,
		VenueTimezone: o.VenueTimezone,
	}
	site, err := siteRoute(ctx, q, o.ChannelID)
	if err != nil {
		return Options{}, err
	}
	if site {
		out.Route = "seller_site"
	}
	tickets, err := loadTickets(ctx, q, o, false)
	if err != nil {
		return Options{}, err
	}
	activeCount := 0
	for _, t := range tickets {
		opt := TicketOption{TicketID: t.ID, Number: t.Number, Seat: t.SeatLabel(), Category: t.Category,
			Status: t.Status, ScannedAt: t.ScannedAt}
		switch {
		case t.Status != "active":
			opt.Reason = "not_active"
		case t.refusal() != "":
			opt.Reason = t.refusal()
			if out.Blocked == "" {
				out.Blocked = "reissue.ticket_not_reissuable"
			}
		default:
			opt.Reissuable = true
		}
		if t.Status == "active" {
			activeCount++
		}
		out.Tickets = append(out.Tickets, opt)
	}
	if out.Open, err = loadOpen(ctx, q, o.ID); err != nil {
		return Options{}, err
	}
	if out.Provider, err = loadProvider(ctx, q, o.CheckoutSessionID); err != nil {
		return Options{}, err
	}
	// The first refusal of the route's own order (spec 63 section 4.3).
	switch {
	case o.Status != "paid" && o.Status != "partially_refunded":
		out.Blocked = "order.not_paid"
	case site:
		out.Blocked = "order.seller_site_order"
	case o.SessionClosed:
		out.Blocked = "reissue.session_closed"
	case out.Open != nil:
		out.Blocked = "reissue.in_progress"
	case activeCount == 0:
		out.Blocked = "reissue.nothing_to_reissue"
	}
	return out, nil
}

// Entry is one operation in the order's history.
type Entry struct {
	ID                 uuid.UUID
	State              string
	OldEmail           string
	NewEmail           string
	Reason             string
	Note               string
	OldCodeScannedAt   *time.Time
	AppliedAt          *time.Time
	LetterFailedAt     *time.Time
	ProviderEmailState string
	ActorType          string
	ActorID            string
	Via                string
	CreatedAt          time.Time
	Pairs              []Pair
}

// History lists the order's reissues, newest first. The organization is part
// of the filter: a foreign order lists nothing.
func History(ctx context.Context, q Querier, orgID, orderID uuid.UUID) ([]Entry, error) {
	return history(ctx, q, orgID, "order_id", orderID)
}

// history lists the reissues whose column (order_id or
// complimentary_issuance_id, never input from a request) equals id.
func history(ctx context.Context, q Querier, orgID uuid.UUID, column string, id uuid.UUID) ([]Entry, error) {
	if column != "order_id" && column != "complimentary_issuance_id" {
		return nil, fmt.Errorf("ticketreissue: history by %q", column)
	}
	rows, err := q.Query(ctx, `
		SELECT id, state, COALESCE(old_email, ''), new_email, reason, COALESCE(note, ''),
		       old_code_scanned_at, applied_at, letter_failed_at, COALESCE(provider_email_state, ''),
		       actor_type, COALESCE(actor_id, ''), COALESCE(via, ''), created_at
		FROM   ticket_reissues
		WHERE  org_id = $1 AND `+column+` = $2
		ORDER  BY created_at DESC`, orgID, id)
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: history: %w", err)
	}
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.State, &e.OldEmail, &e.NewEmail, &e.Reason, &e.Note,
			&e.OldCodeScannedAt, &e.AppliedAt, &e.LetterFailedAt, &e.ProviderEmailState,
			&e.ActorType, &e.ActorID, &e.Via, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Pairs, err = loadPairs(ctx, q, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get reads one operation of the order.
func Get(ctx context.Context, q Querier, orgID, orderID, reissueID uuid.UUID) (Entry, error) {
	all, err := History(ctx, q, orgID, orderID)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range all {
		if e.ID == reissueID {
			return e, nil
		}
	}
	return Entry{}, ErrReissueNotFound
}
