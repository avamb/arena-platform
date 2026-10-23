//go:build integration

// collect_integration_test.go — the billing.collect job against a live
// database (task B2-2). Connects to DATABASE_URL and skips when it is unset;
// run it on a FRESH migrated database, never the shared dev stand.
//
// The shared database also holds other packages' rows, so every collector here
// sees only the fixture's organization (scopedStore) and keeps its cursors
// under a private prefix: the test never writes a billing fact for anyone
// else's sale and never touches the production cursor keys.
package collect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/collect"
)

// scopedStore narrows every source to one organization.
type scopedStore struct {
	*gen.Queries
	org uuid.UUID
}

// keep filters a page to the fixture's organization and cuts it to the page
// size n, so the collector still sees a short page exactly when its own rows
// ran out.
func (s scopedStore) keep(n int32, rows []gen.CollectCandidate, err error) ([]gen.CollectCandidate, error) {
	var out []gen.CollectCandidate
	for _, r := range rows {
		if r.OrgID == s.org && int32(len(out)) < n {
			out = append(out, r)
		}
	}
	return out, err
}

func (s scopedStore) ListCollectOrderItems(ctx context.Context, at time.Time, id uuid.UUID, n int32, comp bool) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectOrderItems(ctx, at, id, 100000, comp)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectRefunds(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectRefunds(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectCancelledTickets(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectCancelledTickets(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectSessionCancellations(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectSessionCancellations(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectEventPublications(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectEventPublications(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectComplimentaryTickets(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectComplimentaryTickets(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}

type fixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	prefix    string
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	channelID uuid.UUID
	tierID    uuid.UUID
}

func newFixture(t *testing.T, country string) *fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &fixture{
		t: t, pool: pool, prefix: "collect-test-" + uuid.NewString()[:8] + ".",
		orgID: uuid.New(), venueID: uuid.New(), eventID: uuid.New(), sessionID: uuid.New(),
		channelID: uuid.New(), tierID: uuid.New(),
	}
	t.Cleanup(f.cleanup)
	suffix := f.orgID.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "Collect Org " + suffix, "collect-" + suffix}},
		{`INSERT INTO venues (id, org_id, name, timezone, country) VALUES ($1, $2, $3, 'Europe/Prague', $4)`,
			[]any{f.venueID, f.orgID, "Collect Venue " + suffix, country}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`,
			[]any{f.eventID, f.orgID, "Collect Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '20 days', now() + interval '20 days 2 hours', 100, 'scheduled', 'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode) VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{f.channelID, f.orgID, "Collect Channel " + suffix}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, currency) VALUES ($1, $2, 'General', 'fixed', 'EUR')`,
			[]any{f.tierID, f.sessionID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("cleanup (%s): %v", sql, err)
		}
	}
	exec(`DELETE FROM billing_job_cursors WHERE key LIKE $1`, f.prefix+"%")
	exec(`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billable_events WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billing_accounts WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM event_publications WHERE event_id = $1`, f.eventID)
	exec(`DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, f.channelID)
	exec(`DELETE FROM session_changes WHERE org_id = $1`, f.orgID)
	exec(`UPDATE tickets SET refund_id = NULL WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM refunds WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID)
	exec(`UPDATE tickets SET order_id = NULL WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM tickets WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM complimentary_issuances WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM orders WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM reservations WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM sales_channels WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM sessions WHERE id = $1`, f.sessionID)
	exec(`DELETE FROM events WHERE id = $1`, f.eventID)
	exec(`DELETE FROM venues WHERE id = $1`, f.venueID)
	exec(`DELETE FROM organizations WHERE id = $1`, f.orgID)
}

func (f *fixture) must(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

// paidOrder inserts a reservation, a checkout session and a paid order with
// one ticket line per price, in order. A seated line gets a seat place.
type line struct {
	price  int64
	seated bool
}

func (f *fixture) paidOrder(source, currency string, paidAt time.Time, lines ...line) (orderID uuid.UUID, itemIDs, ticketIDs []uuid.UUID) {
	f.t.Helper()
	ctx := context.Background()
	res, err := gen.New(f.pool).InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, int32(len(lines)), time.Now().UTC().Add(time.Hour))
	if err != nil {
		f.t.Fatalf("reservation: %v", err)
	}
	csID := uuid.New()
	f.must(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		csID, f.orgID, f.channelID, res.ID)
	orderID = uuid.New()
	f.must(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id, source, status, currency,
	            subtotal, discount, charge, total, paid_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'paid', $9, 0, 0, 0, 0, $10)`,
		orderID, f.orgID, f.channelID, f.eventID, f.sessionID, csID, res.ID, source, currency, paidAt)
	for i, l := range lines {
		var seatID *uuid.UUID
		if l.seated {
			id := uuid.New()
			f.must(`INSERT INTO session_seats (id, session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
			        VALUES ($1, $2, $3, 'A', '1', $4, $5, 'sold', 'seat')`, id, f.sessionID, "A-1-"+id.String()[:6], fmt.Sprint(i+1), f.tierID)
			seatID = &id
		}
		ticketID := uuid.New()
		f.must(`INSERT INTO tickets (id, checkout_session_id, session_id, status, order_id, ordinal) VALUES ($1, $2, $3, 'active', $4, $5)`,
			ticketID, csID, f.sessionID, orderID, i+1)
		itemID := uuid.New()
		f.must(`INSERT INTO order_items (id, order_id, ordinal, tier_id, session_seat_id, ticket_id, unit_price, discount, charge, total)
		        VALUES ($1, $2, $3, $4, $5, $6, $7, 0, 0, $7)`, itemID, orderID, i+1, f.tierID, seatID, ticketID, l.price)
		itemIDs = append(itemIDs, itemID)
		ticketIDs = append(ticketIDs, ticketID)
	}
	return orderID, itemIDs, ticketIDs
}

type eventRow struct {
	Kind, Source         string
	Seated               *bool
	PricingClass         *string
	Currency, Country    *string
	Unit                 *int64
	Attrs                map[string]any
	OccurredAt           time.Time
	AccountCurrency      string
	SubjectID, AccountID uuid.UUID
}

func (f *fixture) events(kind string) []eventRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT e.kind, e.source, e.seated, e.pricing_class, e.sale_currency, e.venue_country, e.unit_price_minor,
		       e.attributes, e.occurred_at, a.currency, e.subject_id, e.account_id
		FROM   billable_events e JOIN billing_accounts a ON a.id = e.account_id
		WHERE  e.org_id = $1 AND e.kind = $2 ORDER BY e.occurred_at, e.subject_id`, f.orgID, kind)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []eventRow
	for rows.Next() {
		var r eventRow
		var attrs []byte
		if err := rows.Scan(&r.Kind, &r.Source, &r.Seated, &r.PricingClass, &r.Currency, &r.Country, &r.Unit,
			&attrs, &r.OccurredAt, &r.AccountCurrency, &r.SubjectID, &r.AccountID); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal(attrs, &r.Attrs); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (f *fixture) total() int {
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billable_events WHERE org_id = $1`, f.orgID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) collector(since time.Time) *collect.Collector {
	return collect.New(scopedStore{gen.New(f.pool), f.orgID}, collect.Options{Since: since, CursorPrefix: f.prefix})
}

func pass(t *testing.T, c *collect.Collector) int {
	t.Helper()
	res, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	n := 0
	for _, r := range res {
		n += r.Inserted
	}
	return n
}

// A paid order becomes one ticket_sold event per ticket line after ONE pass;
// the seat flag, the low-price class and the account come from the sale; a
// second pass writes nothing; a sale before the date X is never read.
func TestCollect_PaidOrderBecomesEventsOnce(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	since := now.Add(-48 * time.Hour)
	_, items, tickets := f.paidOrder("public_feed", "EUR", now.Add(-time.Hour), line{800, false}, line{1500, true})
	f.paidOrder("public_feed", "EUR", since.Add(-time.Hour), line{900, false}) // before X

	c := f.collector(since)
	if n := pass(t, c); n != 2 {
		t.Fatalf("first pass inserted %d events, want 2", n)
	}
	sold := f.events("ticket_sold")
	if len(sold) != 2 {
		t.Fatalf("%d ticket_sold events, want 2", len(sold))
	}
	byItem := map[uuid.UUID]eventRow{sold[0].SubjectID: sold[0], sold[1].SubjectID: sold[1]}
	cheap, dear := byItem[items[0]], byItem[items[1]]
	if cheap.Seated == nil || *cheap.Seated || dear.Seated == nil || !*dear.Seated {
		t.Errorf("seated flags: cheap=%v dear=%v", cheap.Seated, dear.Seated)
	}
	if cheap.PricingClass == nil || *cheap.PricingClass != "low_price" || dear.PricingClass == nil || *dear.PricingClass != "standard" {
		t.Errorf("pricing classes: cheap=%v dear=%v", cheap.PricingClass, dear.PricingClass)
	}
	if cheap.AccountCurrency != "EUR" || cheap.Source != collect.SourceBackfill || cheap.Country == nil || *cheap.Country != "CZ" {
		t.Errorf("cheap event = %+v", cheap)
	}
	if cheap.Unit == nil || *cheap.Unit != 800 || cheap.Currency == nil || *cheap.Currency != "EUR" {
		t.Errorf("cheap money = %v %v", cheap.Unit, cheap.Currency)
	}
	_ = tickets

	// A second pass changes nothing, and now reads as "collect", not backfill.
	if n := pass(t, c); n != 0 {
		t.Fatalf("second pass inserted %d, want 0", n)
	}
	if f.total() != 2 {
		t.Fatalf("total events = %d, want 2", f.total())
	}
	// A new sale after the first pass is picked up by the next one.
	f.paidOrder("public_feed", "EUR", time.Now().UTC(), line{2000, false})
	if n := pass(t, c); n != 1 {
		t.Fatalf("pass after a new sale inserted %d, want 1", n)
	}
	if got := f.events("ticket_sold"); got[len(got)-1].Source != collect.SourceCollect {
		t.Errorf("late event source = %s, want collect", got[len(got)-1].Source)
	}
}

// A sale in a currency the price list has no threshold for keeps the standard
// class and says why.
func TestCollect_ForeignCurrencyLowPriceIsUnresolved(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	f.paidOrder("public_feed", "CZK", now.Add(-time.Hour), line{800, false})
	pass(t, f.collector(now.Add(-24*time.Hour)))
	sold := f.events("ticket_sold")
	if len(sold) != 1 || sold[0].PricingClass == nil || *sold[0].PricingClass != "standard" || sold[0].Attrs["low_price_unresolved"] != true {
		t.Fatalf("event = %+v", sold)
	}
}

// A refund that cancels its ticket is ONE event; a ticket nobody refunded is a
// ticket_cancelled event; an invitation without an order is
// ticket_complimentary; a complimentary order's lines are too; the session
// journal and the first publication give their own events.
func TestCollect_OtherKinds(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	since := now.Add(-48 * time.Hour)
	orderID, _, tickets := f.paidOrder("public_feed", "EUR", now.Add(-5*time.Hour), line{1500, false}, line{1500, false})

	// Ticket 0: refunded (external refund) and cancelled by it.
	refundID := uuid.New()
	f.must(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, cancel_ticket, settled_at, succeeded_at)
	        VALUES ($1, $2, 1500, 'EUR', 'succeeded', 'external', $3, $4, true, $5, $5)`,
		refundID, f.orgID, orderID, tickets[0], now.Add(-4*time.Hour))
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2, refund_id = $3 WHERE id = $1`, tickets[0], now.Add(-4*time.Hour), refundID)
	// Ticket 1: cancelled with no refund.
	f.must(`UPDATE tickets SET status = 'revoked', cancelled_at = $2 WHERE id = $1`, tickets[1], now.Add(-3*time.Hour))

	// An invitation an operator issued: no order.
	issuanceID := uuid.New()
	f.must(`INSERT INTO complimentary_issuances (id, org_id, session_id, qty, batch_id) VALUES ($1, $2, $3, 1, 'collect-test')`,
		issuanceID, f.orgID, f.sessionID)
	invitationID := uuid.New()
	f.must(`INSERT INTO tickets (id, session_id, status, complimentary_issuance_id, issued_at) VALUES ($1, $2, 'active', $3, $4)`,
		invitationID, f.sessionID, issuanceID, now.Add(-2*time.Hour))
	// A complimentary ORDER.
	_, compItems, _ := f.paidOrder("complimentary", "EUR", now.Add(-time.Hour), line{0, false})

	// The session is cancelled; the event is published.
	f.must(`INSERT INTO session_changes (session_id, event_id, org_id, kinds, old_state, new_state, created_at)
	        VALUES ($1, $2, $3, ARRAY['cancelled'], '{}', '{}', $4)`, f.sessionID, f.eventID, f.orgID, now.Add(-30*time.Minute))
	tokenID := uuid.New()
	f.must(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id) VALUES ($1, $2, $3)`, tokenID, "collect-"+tokenID.String(), f.channelID)
	f.must(`INSERT INTO event_publications (event_id, feed_token_id, published_at) VALUES ($1, $2, $3)`, f.eventID, tokenID, now.Add(-20*time.Hour))

	c := f.collector(since)
	pass(t, c)
	if got := f.events("ticket_refunded"); len(got) != 1 || got[0].SubjectID != refundID {
		t.Errorf("ticket_refunded = %+v, want exactly the refund", got)
	}
	cancelled := f.events("ticket_cancelled")
	if len(cancelled) != 1 || cancelled[0].SubjectID != tickets[1] {
		t.Errorf("ticket_cancelled = %+v, want only the unrefunded ticket", cancelled)
	}
	comp := f.events("ticket_complimentary")
	subjects := map[uuid.UUID]bool{}
	for _, e := range comp {
		subjects[e.SubjectID] = true
	}
	if len(comp) != 2 || !subjects[invitationID] || !subjects[compItems[0]] {
		t.Errorf("ticket_complimentary = %+v, want the invitation and the complimentary order line", comp)
	}
	if sold := f.events("ticket_sold"); len(sold) != 2 {
		t.Errorf("ticket_sold = %d, want the 2 lines of the paid order (the complimentary line is not a sale)", len(sold))
	}
	if got := f.events("session_cancelled"); len(got) != 1 {
		t.Errorf("session_cancelled = %+v", got)
	}
	pub := f.events("event_published")
	if len(pub) != 1 || pub[0].SubjectID != f.eventID || pub[0].OccurredAt.After(now.Add(-19*time.Hour)) {
		t.Errorf("event_published = %+v, want one event at the first publication", pub)
	}

	before := f.total()
	if n := pass(t, c); n != 0 || f.total() != before {
		t.Fatalf("second pass inserted %d (total %d -> %d)", n, before, f.total())
	}
}

// Pages of two rows still reach every row, including two sales paid at the
// very same instant (the keyset is (occurred_at, subject_id), not the time).
func TestCollect_PagesWithEqualTimestamps(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC().Truncate(time.Second)
	same := now.Add(-30 * time.Minute)
	f.paidOrder("public_feed", "EUR", same, line{1500, false}, line{1500, false}, line{1500, false})
	f.paidOrder("public_feed", "EUR", same, line{1500, false}, line{1500, false})
	f.paidOrder("public_feed", "EUR", now.Add(-10*time.Minute), line{1500, false})
	c := collect.New(scopedStore{gen.New(f.pool), f.orgID}, collect.Options{
		Since: now.Add(-time.Hour), CursorPrefix: f.prefix, BatchSize: 2,
	})
	if n := pass(t, c); n != 6 {
		t.Fatalf("inserted %d events, want 6", n)
	}
	if got := len(f.events("ticket_sold")); got != 6 {
		t.Fatalf("%d ticket_sold events, want 6", got)
	}
}
