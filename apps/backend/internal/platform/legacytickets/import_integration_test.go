//go:build integration

// import_integration_test.go — the Bil24-era tickets of the WordPress shop
// filed as platform orders against a real database: a dry run writes nothing,
// an apply adds sold places, files the orders, tickets and barcodes and emits
// nothing, a rerun changes nothing, a barcode clash is refused, and the guest
// list, "My tickets", the session summary and the WordPress letters see them.
//
// Needs DATABASE_URL of a FRESH migrated scratch database (AGENTS.md); skips
// without it.
package legacytickets_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/guestlist"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/legacytickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/wpletters"
)

func randInt(t *testing.T, n int64) int64 {
	t.Helper()
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		t.Fatal(err)
	}
	return v.Int64()
}

// barcode is a random 13-digit code: the barcodes table is global.
func barcode(t *testing.T) string {
	return fmt.Sprintf("29%011d", randInt(t, 99_999_999_999))
}

type fixture struct {
	pool                *pgxpool.Pool
	org, channel, venue uuid.UUID
	eventA, eventB      uuid.UUID
	eventPast           uuid.UUID
	sessA, sessB, sessP uuid.UUID
	tierA, tierB, tierP uuid.UUID
	actionA, actionB    int64
	actionPast, aeA     int64
	clash               string // a barcode the platform authority already holds
	emailA, emailB      string
	n1, n2, n3, n4, n5  int64 // Bil24 order numbers
	codes               []string
	wc1, wc2, wc3, wc4  string
	wc5                 string
	ticketBase          int64
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	base := 1_000_000 + randInt(t, 8_000_000)
	f := &fixture{
		pool: pool, org: uuid.New(), channel: uuid.New(), venue: uuid.New(),
		eventA: uuid.New(), eventB: uuid.New(), eventPast: uuid.New(),
		sessA: uuid.New(), sessB: uuid.New(), sessP: uuid.New(),
		tierA: uuid.New(), tierB: uuid.New(), tierP: uuid.New(),
		actionA: 200000 + randInt(t, 99999), aeA: 1300000 + randInt(t, 99999),
		clash:  barcode(t),
		emailA: "dana." + uuid.NewString()[:8] + "@example.test",
		emailB: "noa." + uuid.NewString()[:8] + "@example.test",
		n1:     base, n2: base + 1, n3: base + 2, n4: base + 3, n5: base + 4,
		wc1: "3461", wc2: "3470", wc3: "3480", wc4: "3490", wc5: "3495",
		ticketBase: 80_000_000 + randInt(t, 1_000_000),
	}
	f.actionB, f.actionPast = f.actionA+1, f.actionA+2
	for i := 0; i < 6; i++ {
		f.codes = append(f.codes, barcode(t))
	}
	t.Cleanup(func() { f.cleanup(t) })
	sfx := f.org.String()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", strings.Fields(sql)[2], err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.org, "Legacy Org "+sfx, "legacy-"+sfx)
	exec(`INSERT INTO venues (id, org_id, name, timezone, country) VALUES ($1, $2, $3, 'Asia/Jerusalem', 'IL')`, f.venue, f.org, "Legacy Hall "+sfx)
	exec(`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode, settings)
	      VALUES ($1, $2, $3, 'stripe', 'direct_merchant', '{"gateway":{"token_hash":"$2a$10$fixture"}}'::jsonb)`, f.channel, f.org, "Vino WP "+sfx)
	events := []struct {
		id      uuid.UUID
		action  int64
		sess    uuid.UUID
		tier    uuid.UUID
		start   string
		price   int64
		places  int32
		tierNam string
	}{
		{f.eventA, f.actionA, f.sessA, f.tierA, "13 days", 25000, 20, "Входной билет"},
		{f.eventB, f.actionB, f.sessB, f.tierB, "18 days", 18000, 10, "Входной билет"},
		{f.eventPast, f.actionPast, f.sessP, f.tierP, "-2 days", 18000, 5, "Входной билет"},
	}
	for _, e := range events {
		exec(`INSERT INTO events (id, org_id, name, status, visibility, external_bil24_id) VALUES ($1, $2, $3, 'published', 'public', $4)`,
			e.id, f.org, fmt.Sprintf("Legacy %d", e.action), fmt.Sprint(e.action))
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, admission_mode, currency, currency_source)
		      VALUES ($1, $2, $3, now() + $4::interval, now() + $4::interval + interval '2 hours', now() + $4::interval, $5, 'scheduled', 'general_admission', 'ILS', 'override')`,
			e.sess, e.id, f.venue, e.start, e.places)
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency) VALUES ($1, $2, $3, 'fixed', $4, 'ILS')`,
			e.tier, e.sess, e.tierNam, e.price)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := gaquota.CreateCategory(ctx, gen.New(tx), e.sess, e.tier, e.places); err != nil {
			t.Fatalf("create category: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Session A is registered under its Bil24 session id; B is found through its event.
	exec(`INSERT INTO compatibility_id_map (kind, system_id, platform_id, source) VALUES ('action_event', $1, $2, 'bil24')`, f.aeA, f.sessA)
	// A barcode the platform already issued.
	exec(`INSERT INTO barcodes (authority_id, external_ref) SELECT id, $1 FROM barcode_authorities WHERE type = 'platform' LIMIT 1`, f.clash)
	return f
}

func (f *fixture) cleanup(t *testing.T) {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("cleanup %q: %v", sql, err)
		}
	}
	exec(`DELETE FROM email_letters WHERE org_id = $1`, f.org)
	exec(`DELETE FROM wp_order_links WHERE org_id = $1`, f.org)
	exec(`DELETE FROM legacy_ticket_imports WHERE org_id = $1`, f.org)
	exec(`DELETE FROM barcodes WHERE external_ref = ANY($1)`, append([]string{f.clash}, f.codes...))
	exec(`DELETE FROM order_events WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.org)
	exec(`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.org)
	exec(`DELETE FROM tickets WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.org)
	exec(`DELETE FROM orders WHERE org_id = $1`, f.org)
	exec(`DELETE FROM checkout_sessions WHERE org_id = $1`, f.org)
	exec(`DELETE FROM reservations WHERE org_id = $1`, f.org)
	exec(`DELETE FROM customer_org_links WHERE org_id = $1`, f.org)
	exec(`DELETE FROM compatibility_id_map WHERE platform_id = ANY($1)`, []uuid.UUID{f.sessA, f.sessB, f.sessP})
	for _, s := range []uuid.UUID{f.sessA, f.sessB, f.sessP} {
		exec(`DELETE FROM session_seats WHERE session_id = $1`, s)
		exec(`DELETE FROM inventory_ledger WHERE session_id = $1`, s)
		exec(`DELETE FROM ticket_tiers WHERE session_id = $1`, s)
		exec(`DELETE FROM sessions WHERE id = $1`, s)
	}
	exec(`DELETE FROM events WHERE org_id = $1`, f.org)
	exec(`DELETE FROM sales_channels WHERE org_id = $1`, f.org)
	exec(`DELETE FROM venues WHERE org_id = $1`, f.org)
	exec(`DELETE FROM organizations WHERE id = $1`, f.org)
}

// export builds the exporter's document for the fixture: order 1 (session A,
// three tickets, one refunded), order 2 (session B, no live-ids meta), order 3
// (a session that has started), order 4 (a barcode the platform holds) and
// order 5 (a barcode order 1 also carries).
func (f *fixture) export(t *testing.T) legacytickets.Export {
	t.Helper()
	tk := func(n int64, code string, price string, ae int64) string {
		return fmt.Sprintf(`{"id": %d, "seatId": %d, "orderId": 1, "seatLocation": "", "category": "Входной билет",
			"price": %q, "discount": 0, "charge": 0, "totalPrice": %q, "barcode": %q, "barcodeFormat": "EAN13", "actionEvent": {"id": %d}}`,
			n, n+5, price, price, code, ae)
	}
	item := func(action, ae int64) string {
		return fmt.Sprintf(`{"item_id": "1", "product_id": "11", "name": "x", "quantity": 1, "bil24_action_id": %d, "bil24_action_event_id": %d}`, action, ae)
	}
	order := func(wc string, num int64, email, live string, items string, tickets ...string) string {
		return fmt.Sprintf(`{"wc_order_id": %q, "status": "completed", "bil24_order_id": "%d",
			"created_at": "2026-08-30T17:20:00Z", "paid_at": "2026-08-30T17:25:00Z", "currency": "ILS", "language": "ru",
			"billing": {"first_name": "Dana", "last_name": "Example", "email": %q, "phone": "+972500000001"},
			"live_ids": %s, "items": [%s], "tickets": [%s]}`, wc, num, email, live, items, strings.Join(tickets, ","))
	}
	b := f.ticketBase
	doc := `{"generated_at": "2026-11-08T18:00:00Z", "source": "wp_bil24_legacy", "orders": [` + strings.Join([]string{
		order(f.wc1, f.n1, f.emailA, fmt.Sprintf("[%d, %d]", b+1, b+3), item(f.actionA, f.aeA),
			tk(b+1, f.codes[0], "250", f.aeA), tk(b+2, f.codes[1], "250", f.aeA), tk(b+3, f.codes[2], "250", f.aeA)),
		order(f.wc2, f.n2, f.emailB, "null", item(f.actionB, 0), tk(b+10, f.codes[3], "180", 7777777)),
		order(f.wc3, f.n3, f.emailB, "null", item(f.actionPast, 0), tk(b+20, f.codes[4], "180", 0)),
		order(f.wc4, f.n4, f.emailB, "null", item(f.actionA, f.aeA), tk(b+30, f.clash, "250", f.aeA)),
		order(f.wc5, f.n5, f.emailB, "null", item(f.actionA, f.aeA), tk(b+40, f.codes[0], "250", f.aeA)),
	}, ",") + `]}`
	e, err := legacytickets.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func sideEffects(t *testing.T, pool *pgxpool.Pool) [5]int64 {
	return [5]int64{
		count(t, pool, `SELECT count(*) FROM outbox_events`),
		count(t, pool, `SELECT count(*) FROM outbox`),
		count(t, pool, `SELECT count(*) FROM delivery_jobs`),
		count(t, pool, `SELECT count(*) FROM worker_jobs`),
		count(t, pool, `SELECT count(*) FROM billable_events`),
	}
}

func codes(rs []legacytickets.Refusal) map[string]string {
	out := map[string]string{}
	for _, r := range rs {
		out[r.WCOrderID] = r.Code
	}
	return out
}

func TestLegacyImport_DryRunApplyRerunAndSurfaces(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doc := f.export(t)
	opt := legacytickets.Options{OrgID: f.org, OnlyFuture: true, Now: time.Now().UTC()}
	effects := sideEffects(t, f.pool)

	// ── Dry run: plans, writes nothing.
	rep, err := legacytickets.Run(ctx, f.pool, opt, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(rep.Refusals); len(got) != 2 || got[f.wc4] != legacytickets.RefuseBarcodeTaken || got[f.wc5] != legacytickets.RefuseDuplicateInFile {
		t.Fatalf("dry-run refusals = %+v", rep.Refusals)
	}
	if rep.Skipped[legacytickets.SkipPastSession] != 1 {
		t.Fatalf("dry-run skipped = %+v", rep.Skipped)
	}
	byID := map[uuid.UUID]*legacytickets.SessionReport{}
	for _, s := range rep.Sessions {
		byID[s.SessionID] = s
	}
	a := byID[f.sessA]
	if a == nil || a.Orders != 1 || a.Tickets != 2 || a.SumMinor != 50000 || a.ChannelID != f.channel {
		t.Fatalf("dry-run session A = %+v", a)
	}
	if b4, af := a.Tiers[0].Before, a.Tiers[0].After; b4.Places != 20 || b4.Available != 20 || af.Places != 22 || af.Sold != 2 || af.Available != 20 || af.Capacity != 22 {
		t.Fatalf("dry-run places A: before %+v after %+v", b4, af)
	}
	if count(t, f.pool, `SELECT count(*) FROM orders WHERE org_id = $1`, f.org) != 0 ||
		count(t, f.pool, `SELECT count(*) FROM session_seats WHERE session_id = $1`, f.sessA) != 20 {
		t.Fatal("the dry run wrote")
	}

	// ── Apply.
	opt.Apply = true
	rep, err = legacytickets.Run(ctx, f.pool, opt, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(rep.Refusals); len(got) != 2 || got[f.wc4] != legacytickets.RefuseBarcodeTaken {
		t.Fatalf("apply refusals = %+v", rep.Refusals)
	}
	byID = map[uuid.UUID]*legacytickets.SessionReport{}
	for _, s := range rep.Sessions {
		byID[s.SessionID] = s
	}
	if a := byID[f.sessA]; len(a.SystemIDs) != 1 || a.SystemIDs[0] != f.n1 || a.LedgerAfter != [2]int32{22, 2} {
		t.Fatalf("apply session A = %+v", a)
	}
	if b := byID[f.sessB]; b == nil || b.Orders != 1 || len(b.SystemIDs) != 1 || b.SystemIDs[0] != f.n2 {
		t.Fatalf("apply session B = %+v", b)
	}

	// Places: added as sold upstream, nothing available taken; capacity and ledger follow.
	var places, sold, upstream, available int64
	if err := f.pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE status = 'sold'),
       count(*) FILTER (WHERE status = 'sold' AND reservation_id IS NULL),
       count(*) FILTER (WHERE status = 'available')
  FROM session_seats WHERE session_id = $1 AND tier_id = $2 AND kind = 'ga_unit'`, f.sessA, f.tierA).
		Scan(&places, &sold, &upstream, &available); err != nil {
		t.Fatal(err)
	}
	if places != 22 || sold != 2 || upstream != 2 || available != 20 {
		t.Fatalf("places A = %d sold %d upstream %d available %d", places, sold, upstream, available)
	}
	if c := count(t, f.pool, `SELECT capacity FROM ticket_tiers WHERE id = $1`, f.tierA); c != 22 {
		t.Fatalf("tier capacity = %d", c)
	}
	if c := count(t, f.pool, `SELECT capacity_total FROM sessions WHERE id = $1`, f.sessA); c != 22 {
		t.Fatalf("session capacity = %d", c)
	}
	var lt, lh, ls int64
	if err := f.pool.QueryRow(ctx, `SELECT capacity_total, capacity_held, capacity_sold FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`, f.sessA).Scan(&lt, &lh, &ls); err != nil {
		t.Fatal(err)
	}
	if lt != 22 || lh != 0 || ls != 2 {
		t.Fatalf("ledger = %d/%d/%d", lt, lh, ls)
	}

	// The order and its tickets.
	var orderID, customer, channel uuid.UUID
	var source, status, email string
	var total int64
	var paid time.Time
	if err := f.pool.QueryRow(ctx, `SELECT id, source, status, total, buyer_email, customer_id, channel_id, paid_at FROM orders WHERE system_id = $1`, f.n1).
		Scan(&orderID, &source, &status, &total, &email, &customer, &channel, &paid); err != nil {
		t.Fatal(err)
	}
	if source != "legacy_import" || status != "paid" || total != 50000 || email != f.emailA || channel != f.channel ||
		!paid.Equal(time.Date(2026, 8, 30, 17, 25, 0, 0, time.UTC)) || customer == uuid.Nil {
		t.Fatalf("order = %s %s %d %s %s %s", source, status, total, email, channel, paid)
	}
	if n := count(t, f.pool, `
SELECT count(*) FROM tickets t
  JOIN session_seats ss ON ss.session_id = t.session_id AND ss.seat_key = t.seat_key AND ss.status = 'sold' AND ss.reservation_id IS NULL
  JOIN order_items oi ON oi.ticket_id = t.id AND oi.session_seat_id = ss.id
 WHERE t.order_id = $1 AND t.status = 'active' AND t.holder_name = 'Dana Example'`, orderID); n != 2 {
		t.Fatalf("tickets on sold places = %d", n)
	}
	// Each ticket keeps its Bil24 number: MACS reports the scans of these
	// tickets by that number, and a different one is rejected as unknown.
	if n := count(t, f.pool, `
SELECT count(*) FROM tickets t JOIN legacy_ticket_imports l ON l.ticket_id = t.id
 WHERE t.order_id = $1 AND t.system_ticket_id = l.bil24_ticket_id`, orderID); n != 2 {
		t.Fatalf("tickets under their Bil24 number = %d", n)
	}
	if n := count(t, f.pool, `
SELECT count(*) FROM barcodes b
  JOIN barcode_authorities a ON a.id = b.authority_id AND a.type = 'legacy_bil24'
  JOIN ticket_credentials c ON c.ticket_id = b.ticket_id AND c.type = 'ean13' AND c.payload = b.external_ref
 WHERE b.external_ref = ANY($1)`, []string{f.codes[0], f.codes[2]}); n != 2 {
		t.Fatalf("legacy barcodes with credentials = %d", n)
	}
	if n := count(t, f.pool, `SELECT count(*) FROM barcodes WHERE external_ref = $1`, f.codes[1]); n != 0 {
		t.Fatal("the refunded ticket's barcode was filed")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM wp_order_links WHERE org_id = $1 AND wc_order_id = $2 AND bil24_order_id = $3 AND order_id = $4 AND cardinality(ticket_system_ids) = 2`,
		f.org, f.wc1, f.n1, orderID); n != 1 {
		t.Fatal("no wp_order_links row")
	}
	if n := count(t, f.pool, `SELECT count(*) FROM orders WHERE org_id = $1 AND system_id IN ($2, $3, $4)`, f.org, f.n3, f.n4, f.n5); n != 0 {
		t.Fatal("a skipped or refused order was written")
	}

	// Nothing was delivered, queued or billed.
	if got := sideEffects(t, f.pool); got != effects {
		t.Fatalf("side effects before %v after %v (outbox_events, outbox, delivery_jobs, worker_jobs, billable_events)", effects, got)
	}
	q := gen.New(f.pool)
	cands, err := q.ListCollectOrderItems(ctx, time.Time{}, uuid.Nil, 10000, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.OrderID != nil && *c.OrderID == orderID {
			t.Fatal("billing collect picks a legacy order line")
		}
	}

	// ── Rerun: everything unchanged.
	before := count(t, f.pool, `SELECT count(*) FROM tickets WHERE session_id = ANY($1)`, []uuid.UUID{f.sessA, f.sessB})
	rep, err = legacytickets.Run(ctx, f.pool, opt, doc)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Unchanged != 2 || count(t, f.pool, `SELECT count(*) FROM tickets WHERE session_id = ANY($1)`, []uuid.UUID{f.sessA, f.sessB}) != before ||
		count(t, f.pool, `SELECT count(*) FROM session_seats WHERE session_id = $1`, f.sessA) != 22 {
		t.Fatalf("rerun: %+v", rep)
	}

	// ── The guest list shows them, valid, with their barcode.
	rows, err := q.ListGuestListRows(ctx, f.sessA, f.org, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("guest list rows = %d", len(rows))
	}
	for _, r := range rows {
		code, _ := guestlist.StatusOf(r)
		if code != guestlist.StatusValid || r.Barcode == nil || (*r.Barcode != f.codes[0] && *r.Barcode != f.codes[2]) ||
			r.OrderNumber == nil || *r.OrderNumber != f.n1 {
			t.Fatalf("guest row = %+v status %d", r, code)
		}
	}

	// ── "My tickets" finds them by the buyer's e-mail.
	orders, n, err := mytickets.New(f.pool, nil).ListOrders(ctx, mytickets.Session{OrgID: f.org, Email: strings.ToUpper(f.emailA)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(orders) != 1 || orders[0].OrderNumber != f.n1 || len(orders[0].Tickets) != 2 {
		t.Fatalf("my tickets = %d %+v", n, orders)
	}

	// ── The session summary counts them as sold upstream.
	sum, err := q.ListSessionSummaryPlaces(ctx, []uuid.UUID{f.sessA})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 1 || sum[0].Sold != 2 || sum[0].SoldUpstream != 2 || sum[0].Available != 20 {
		t.Fatalf("summary places = %+v", sum)
	}

	// ── The ops watchdog's "paid but no tickets" sees tickets behind the order.
	if n := count(t, f.pool, `SELECT count(*) FROM orders o WHERE o.id = $1 AND NOT EXISTS (SELECT 1 FROM tickets t WHERE t.order_id = o.id)`, orderID); n != 0 {
		t.Fatal("the order looks paid without tickets")
	}

	// ── A WordPress letter of the Bil24-era order is matched through the link.
	lrep, err := wpletters.Import(ctx, f.pool, f.org, []wpletters.Record{{
		WCOrderID: f.wc1, PlatformOrderSystemID: wpletters.FlexString(fmt.Sprint(f.n1)), Recipient: f.emailA,
		SentAt: "2026-08-30T17:26:00Z", MessageID: "<" + uuid.NewString() + "@relay.example.test>", Status: "delivered",
	}}, false)
	if err != nil || lrep.Matched != 1 || lrep.MatchedLegacy != 1 || lrep.Inserted != 1 {
		t.Fatalf("letters: %+v %v", lrep, err)
	}
	if n := count(t, f.pool, `SELECT count(*) FROM email_letters WHERE order_id = $1 AND cardinality(ticket_numbers) = 2`, orderID); n != 1 {
		t.Fatal("the letter is not on the legacy order")
	}

	// Order metadata records where it came from.
	var meta []byte
	if err := f.pool.QueryRow(ctx, `SELECT metadata FROM orders WHERE id = $1`, orderID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(meta, &m); err != nil || m["legacy_import"]["wc_order_id"] != f.wc1 {
		t.Fatalf("metadata = %s", meta)
	}
}

func TestLegacyImport_AmbiguousSessionAndUnknownAction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// A second live session of event B: B's tickets name no registered Bil24 session.
	extra := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', now() + interval '30 days', 10, 'scheduled', 'general_admission', 'ILS', 'override')`,
		extra, f.eventB, f.venue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM sessions WHERE id = $1`, extra) })
	doc := f.export(t)
	doc.Orders = doc.Orders[1:2]
	other := doc.Orders[0]
	other.WCOrderID = "9999"
	other.Items = []legacytickets.Item{{Bil24ActionID: legacytickets.FlexInt(f.actionA + 50)}}
	other.Tickets = []legacytickets.Ticket{other.Tickets[0]}
	other.Tickets[0].ID = legacytickets.FlexInt(f.ticketBase + 99)
	other.Tickets[0].Barcode = legacytickets.FlexString(f.codes[5])
	doc.Orders = append(doc.Orders, other)

	rep, err := legacytickets.Run(ctx, f.pool, legacytickets.Options{OrgID: f.org, OnlyFuture: true}, doc)
	if err != nil {
		t.Fatal(err)
	}
	got := codes(rep.Refusals)
	if got[f.wc2] != legacytickets.RefuseAmbiguousSession || got["9999"] != legacytickets.RefuseUnknownAction {
		t.Fatalf("refusals = %+v", rep.Refusals)
	}
}
