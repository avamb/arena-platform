// loader.go — the Postgres half of the bil24_wp dispatcher (W1-B7c, feature
// #506).
//
// dispatcher.go owns the mapping, the envelope and the HTTP delivery and never
// touches a pool; every database fact it needs arrives through the Loader
// interface, whose production implementation is here. The split is what lets
// every mapping be unit-tested against a fake Loader and an httptest receiver.
//
// The EncodeContext recipe below (frontend = the sales channel's
// display_number/name, agent = the same channel's name, legalOwnerInn =
// organizations.tax_id, charge = orders.charge, four compatibility_id_map
// lookups) is deliberately the same one hbil24 applies to GET_ORDER_INFO: a
// webhook and a pull of the same order must describe it identically.
package bil24wire

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// PoolLoader implements Loader over a pgx pool.
type PoolLoader struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPoolLoader builds the production Loader.
func NewPoolLoader(pool *pgxpool.Pool) *PoolLoader {
	return &PoolLoader{pool: pool, q: gen.New(pool)}
}

// NewDispatcher builds the dispatcher the worker registers (third member of
// multiDispatcher). Returns nil for a nil pool so the caller can wire it
// unconditionally and let the worker skip it when there is no database.
func NewDispatcher(pool *pgxpool.Pool) *Dispatcher {
	if pool == nil {
		return nil
	}
	return NewDispatcherWithLoader(NewPoolLoader(pool))
}

// ─────────────────────────────────────────────────────────────────────────────
// Subscriber routing
// ─────────────────────────────────────────────────────────────────────────────

// SubscriberByChannel implements Loader. A channel with no registered site is
// (Subscriber{}, false, nil): a skip, not a failure.
func (l *PoolLoader) SubscriberByChannel(ctx context.Context, channelID uuid.UUID) (Subscriber, bool, error) {
	if channelID == uuid.Nil {
		return Subscriber{}, false, nil
	}
	row, err := l.q.GetWPSubscriberByChannel(ctx, channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscriber{}, false, nil
	}
	if err != nil {
		return Subscriber{}, false, fmt.Errorf("bil24wire loader: subscriber by channel: %w", err)
	}
	return Subscriber{CallbackURL: row.CallbackURL, SigningSecret: row.SigningSecret}, true, nil
}

// SubscribersForEvent implements Loader.
func (l *PoolLoader) SubscribersForEvent(ctx context.Context, eventID uuid.UUID) ([]Subscriber, error) {
	rows, err := l.q.ListWPSubscribersForEvent(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("bil24wire loader: subscribers for event: %w", err)
	}
	out := make([]Subscriber, 0, len(rows))
	for _, r := range rows {
		out = append(out, Subscriber{CallbackURL: r.CallbackURL, SigningSecret: r.SigningSecret})
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Order + ticket payloads
// ─────────────────────────────────────────────────────────────────────────────

// orderRefSQL answers "which integer does the site know this order by, and who
// sold it".
//
// The id is COALESCE(MIN(system_ticket_id), system_id) rather than plainly
// system_id because orderexport derives an order's wire id from its cheapest
// invariant — the minimum system_ticket_id of its tickets. An order.cancelled
// must repeat the id the site already saw in order.paid; only an order that
// never issued a ticket (the common cancellation) falls back to the
// aggregate's own system_id.
const orderRefSQL = `
SELECT COALESCE(MIN(t.system_ticket_id), o.system_id), o.channel_id
FROM   orders o
LEFT   JOIN tickets t
       ON t.order_id = o.id
      AND t.status IN ('active', 'cancelled', 'revoked')
WHERE  o.id = $1
GROUP  BY o.id, o.system_id, o.channel_id`

// OrderRef implements Loader.
func (l *PoolLoader) OrderRef(ctx context.Context, orderID uuid.UUID) (int64, uuid.UUID, error) {
	var (
		systemID  int64
		channelID uuid.UUID
	)
	err := l.pool.QueryRow(ctx, orderRefSQL, orderID).Scan(&systemID, &channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, uuid.Nil, nil
	}
	if err != nil {
		return 0, uuid.Nil, fmt.Errorf("bil24wire loader: order ref: %w", err)
	}
	return systemID, channelID, nil
}

// Order implements Loader.
func (l *PoolLoader) Order(ctx context.Context, orderID uuid.UUID) (*Order, error) {
	projected, err := orderexport.QueryOrder(ctx, l.pool, orderID)
	if err != nil {
		return nil, err
	}
	if projected == nil || len(projected.Tickets) == 0 {
		return nil, nil
	}
	ec, _, err := l.encodeContext(ctx, orderContextSQL, orderID, *projected)
	if err != nil {
		return nil, err
	}
	out := EncodeOrder(*projected, ec)
	return &out, nil
}

// RefundedTicket implements Loader.
func (l *PoolLoader) RefundedTicket(ctx context.Context, ticketID uuid.UUID) (*RefundedTicket, uuid.UUID, error) {
	ticket, order, err := orderexport.QueryTicket(ctx, l.pool, ticketID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if ticket == nil || order == nil {
		return nil, uuid.Nil, nil
	}
	ec, channelID, err := l.encodeContext(ctx, ticketContextSQL, ticketID, *order)
	if err != nil {
		return nil, uuid.Nil, err
	}
	out := EncodeTicketRefunded(*ticket, ec)
	return &out, channelID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// EncodeContext assembly
// ─────────────────────────────────────────────────────────────────────────────

// The two context queries differ only in how they reach the order: by its own
// id, or through one of its tickets (tickets.order_id is the modern link;
// checkout_session_id keeps pre-#488 rows reachable).
const orderContextSQL = `
SELECT o.channel_id,
       COALESCE(sc.display_number, 0),
       sc.name,
       COALESCE(org.tax_id, ''),
       o.charge
FROM   orders o
JOIN   sales_channels sc  ON sc.id  = o.channel_id
JOIN   organizations  org ON org.id = o.org_id
WHERE  o.id = $1`

const ticketContextSQL = `
SELECT o.channel_id,
       COALESCE(sc.display_number, 0),
       sc.name,
       COALESCE(org.tax_id, ''),
       o.charge
FROM   tickets t
JOIN   orders o ON o.id = t.order_id
                OR o.checkout_session_id = t.checkout_session_id
JOIN   sales_channels sc  ON sc.id  = o.channel_id
JOIN   organizations  org ON org.id = o.org_id
WHERE  t.id = $1
LIMIT  1`

// encodeContext assembles everything the neutral projection cannot carry, and
// reports the sales channel that sold the order — the routing key its webhook
// needs. A missing selling context is NOT fatal: the wire object then reports
// a zero frontend, which is a degraded payload rather than an undelivered
// sale. Only a real query failure propagates, because that one is worth a
// retry.
func (l *PoolLoader) encodeContext(
	ctx context.Context,
	sql string,
	id uuid.UUID,
	projected orderexport.Order,
) (EncodeContext, uuid.UUID, error) {
	var (
		channelID   uuid.UUID
		frontendID  int64
		channelName string
		taxID       string
		charge      int64
	)
	err := l.pool.QueryRow(ctx, sql, id).Scan(&channelID, &frontendID, &channelName, &taxID, &charge)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return EncodeContext{}, uuid.Nil, fmt.Errorf("bil24wire loader: selling context: %w", err)
	}

	ec := EncodeContext{
		// The arena organization sells; the sales channel is the frontend.
		// Neither has a compatibility_id_map kind (spec §3.1), so the agent id
		// stays 0 and the frontend reports its display_number — the same
		// integer the WordPress site authenticates with.
		Frontend:      Frontend{ID: frontendID, Name: channelName},
		Agent:         Agent{Name: channelName},
		Email:         projected.BuyerEmail,
		LegalOwnerInn: taxID,
		Charge:        &charge,
	}

	var sessions, events, venues, cities []uuid.UUID
	for _, t := range projected.Tickets {
		sessions = append(sessions, t.Event.SessionID)
		events = append(events, t.Event.EventID)
		venues = append(venues, t.Event.VenueID)
		if t.Event.CityID != nil {
			cities = append(cities, *t.Event.CityID)
		}
	}
	ec.ActionEventIDs = l.compatIDs(ctx, compatids.KindActionEvent, sessions)
	ec.ActionIDs = l.compatIDs(ctx, compatids.KindAction, events)
	ec.VenueIDs = l.compatIDs(ctx, compatids.KindVenue, venues)
	ec.CityIDs = l.compatIDs(ctx, compatids.KindCity, cities)
	return ec, channelID, nil
}

// compatIDs resolves (minting on first read) the bigint wire ids of one kind.
// A failure yields nil, so the encoder emits 0 for that kind.
func (l *PoolLoader) compatIDs(ctx context.Context, kind compatids.Kind, ids []uuid.UUID) map[uuid.UUID]int64 {
	if len(ids) == 0 {
		return nil
	}
	out, err := compatids.EnsureMany(ctx, l.pool, kind, ids)
	if err != nil {
		return nil
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Catalog notifications
// ─────────────────────────────────────────────────────────────────────────────

// eventSessionsSQL lists every session of an event in show order — what an
// `event.created` names when the producer did not enumerate sessions itself.
const eventSessionsSQL = `
SELECT id
FROM   sessions
WHERE  event_id = $1
ORDER  BY start_at, id`

// ActionEventIDs implements Loader.
func (l *PoolLoader) ActionEventIDs(ctx context.Context, eventID uuid.UUID, sessionIDs []uuid.UUID) ([]int64, error) {
	if len(sessionIDs) == 0 {
		rows, err := l.pool.Query(ctx, eventSessionsSQL, eventID)
		if err != nil {
			return nil, fmt.Errorf("bil24wire loader: event sessions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("bil24wire loader: scan session: %w", err)
			}
			sessionIDs = append(sessionIDs, id)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("bil24wire loader: event sessions: %w", err)
		}
	}
	if len(sessionIDs) == 0 {
		return nil, nil
	}

	mapped, err := compatids.EnsureMany(ctx, l.pool, compatids.KindActionEvent, sessionIDs)
	if err != nil {
		return nil, fmt.Errorf("bil24wire loader: action event ids: %w", err)
	}
	// Preserve the caller's order, and drop nothing silently: an id that could
	// not be minted would be a 0 the site cannot resolve.
	out := make([]int64, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		if wireID, ok := mapped[sid]; ok && wireID != 0 {
			out = append(out, wireID)
		}
	}
	return out, nil
}

// Compile-time interface guard.
var _ Loader = (*PoolLoader)(nil)
