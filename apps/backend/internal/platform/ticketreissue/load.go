package ticketreissue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// orderFacts is the order as the checks need it.
type orderFacts struct {
	ID                uuid.UUID
	OrgID             uuid.UUID
	Number            int64
	Status            string
	Source            string
	ChannelID         uuid.UUID
	SessionID         uuid.UUID
	CheckoutSessionID uuid.UUID
	BuyerEmail        string
	BuyerName         string
	BuyerPhone        string
	CustomerID        *uuid.UUID
	SessionClosed     bool
	// VenueTimezone is the IANA zone the event's times are told in.
	VenueTimezone string
}

const loadOrderSQL = `
SELECT o.id, o.org_id, o.system_id, o.status, o.source, o.channel_id, o.session_id,
       o.checkout_session_id, o.buyer_email, o.buyer_name, o.buyer_phone, o.customer_id,
       (s.status = 'cancelled' OR s.deleted_at IS NOT NULL
        OR COALESCE(s.end_at, s.start_at + interval '6 hours') < now()) AS session_closed,
       COALESCE((SELECT v.timezone FROM venues v WHERE v.id = s.venue_id), '') AS venue_timezone
FROM   orders o
JOIN   sessions s ON s.id = o.session_id
WHERE  o.id = $1 AND o.org_id = $2`

func loadOrder(ctx context.Context, q Querier, orgID, orderID uuid.UUID, lock bool) (orderFacts, error) {
	sql := loadOrderSQL
	if lock {
		sql += "\nFOR UPDATE OF o"
	}
	var (
		o                  orderFacts
		email, name, phone *string
	)
	err := q.QueryRow(ctx, sql, orderID, orgID).Scan(&o.ID, &o.OrgID, &o.Number, &o.Status, &o.Source,
		&o.ChannelID, &o.SessionID, &o.CheckoutSessionID, &email, &name, &phone, &o.CustomerID, &o.SessionClosed, &o.VenueTimezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, ErrOrderNotFound
	}
	if err != nil {
		return o, fmt.Errorf("ticketreissue: load order: %w", err)
	}
	o.BuyerEmail, o.BuyerName, o.BuyerPhone = deref(email), deref(name), deref(phone)
	return o, nil
}

// ticketFacts is one ticket of the order.
type ticketFacts struct {
	ID         uuid.UUID
	Number     int64
	Status     string
	ReviewHold bool
	Sector     string
	Row        string
	Seat       string
	Category   string
	RefundOpen bool
	ScannedAt  *time.Time
	Barcode    string
}

const loadTicketsSQL = `
SELECT t.id, t.system_ticket_id, t.status, t.review_hold,
       COALESCE(t.seat_sector, ''), COALESCE(t.seat_row, ''), COALESCE(t.seat_number, ''),
       COALESCE(tt.name, ''),
       EXISTS (SELECT 1 FROM refunds r
               WHERE (r.ticket_id = t.id OR (r.ticket_id IS NULL AND r.order_id = $1))
                 AND r.state IN ('requested', 'approved', 'provider_pending', 'manual_review')) AS refund_open,
       COALESCE(t.used_at,
                (SELECT min(se.scanned_at) FROM scan_events se
                 WHERE se.ticket_id = t.id AND se.result = 'admitted'),
                (SELECT min(b.scanned_at) FROM barcodes b
                 WHERE b.ticket_id = t.id AND b.scanned_at IS NOT NULL)) AS scanned_at,
       COALESCE((SELECT c.payload FROM ticket_credentials c
                 WHERE c.ticket_id = t.id AND c.type = 'ean13'), '') AS barcode
FROM   tickets t
LEFT   JOIN ticket_tiers tt ON tt.id = t.tier_id
WHERE  t.order_id = $1 OR (t.order_id IS NULL AND t.checkout_session_id = $2)
ORDER  BY t.id`

func loadTickets(ctx context.Context, q Querier, o orderFacts, lock bool) ([]ticketFacts, error) {
	sql := loadTicketsSQL
	if lock {
		sql += "\nFOR UPDATE OF t"
	}
	rows, err := q.Query(ctx, sql, o.ID, o.CheckoutSessionID)
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: load tickets: %w", err)
	}
	defer rows.Close()
	var out []ticketFacts
	for rows.Next() {
		var t ticketFacts
		if err := rows.Scan(&t.ID, &t.Number, &t.Status, &t.ReviewHold, &t.Sector, &t.Row, &t.Seat,
			&t.Category, &t.RefundOpen, &t.ScannedAt, &t.Barcode); err != nil {
			return nil, fmt.Errorf("ticketreissue: scan ticket: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// refusal is why one active ticket blocks the operation, "" when it does not.
func (t ticketFacts) refusal() string {
	switch {
	case t.ReviewHold:
		return "review_hold"
	case t.RefundOpen:
		return "refund_in_progress"
	}
	return ""
}

// SeatLabel is "sector, row, seat" with the empty parts left out.
func (t ticketFacts) SeatLabel() string {
	out := ""
	for _, p := range []string{t.Sector, t.Row, t.Seat} {
		if p == "" {
			continue
		}
		if out != "" {
			out += " · "
		}
		out += p
	}
	return out
}

// Open is a reissue of the order that has not been applied yet.
type Open struct {
	ID        uuid.UUID
	State     string
	NewEmail  string
	CreatedAt time.Time
}

func loadOpen(ctx context.Context, q Querier, orderID uuid.UUID) (*Open, error) {
	var op Open
	err := q.QueryRow(ctx, `
		SELECT id, state, new_email, created_at FROM ticket_reissues
		WHERE  order_id = $1 AND state IN ('pending', 'send_failed')
		ORDER  BY created_at DESC LIMIT 1`, orderID).Scan(&op.ID, &op.State, &op.NewEmail, &op.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: load open reissue: %w", err)
	}
	return &op, nil
}

// Provider is the payment provider of the order (spec 63 section 4.10).
type Provider struct {
	// Name is the registry name ("stripe"), "" when the order has no payment.
	Name string
	// Title is the human name ("Stripe").
	Title string
	// EmailUpdatable: the module can move the provider's receipt address
	// (decision R-17, П-2). Otherwise the card shows a hint (П-3).
	EmailUpdatable bool
}

func loadProvider(ctx context.Context, q Querier, checkoutSessionID uuid.UUID) (Provider, error) {
	var name string
	err := q.QueryRow(ctx, `
		SELECT provider FROM payment_intents
		WHERE  checkout_session_id = $1 AND state = 'succeeded'
		ORDER  BY created_at DESC LIMIT 1`, checkoutSessionID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Provider{}, nil
	}
	if err != nil {
		return Provider{}, fmt.Errorf("ticketreissue: load payment provider: %w", err)
	}
	return ProviderOf(name), nil
}

// ProviderOf describes a provider by its registry name.
func ProviderOf(name string) Provider {
	p := Provider{Name: name, Title: name}
	if e, ok := paymodules.Registry().Get(name); ok {
		d := e.Descriptor
		if d.Title != "" {
			p.Title = d.Title
		}
		p.EmailUpdatable = d.Capabilities.ReceiptEmailUpdate
	}
	return p
}

// siteRoute reports whether a live selling site writes this order's letters
// (decision R-8: the live site decides, not the order's source).
func siteRoute(ctx context.Context, q Querier, channelID uuid.UUID) (bool, error) {
	return sessionchange.ChannelHasSite(ctx, q, channelID)
}
