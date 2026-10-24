//go:build integration

package gen_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestSalesLinks_LiveDB runs the sales-link queries of migration 0167 against
// a real schema: the code race, paging, the tri-state update, the counters,
// the CHECK constraints, the order aggregate and the two scanners that gained
// columns (orders, checkout_sessions).
func TestSalesLinks_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var (
		orgID, otherOrgID, venueID, evtID = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		sessID, chanID, resvID, csID      = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		orderID, tktID, refID, tierID     = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		nonce                             = orgID.String()[:8]
	)
	// Codes are global, so every code of this run carries the run's nonce
	// characters (hex digits 2-9 and a-f are in the alphabet).
	codeNonce := strings.NewReplacer("0", "2", "1", "3").Replace(nonce)[:5]
	code := func(n string) string { return "q" + n + codeNonce }

	cleanup := func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM refunds WHERE org_id = $1`, orgID},
			{`DELETE FROM tickets WHERE id = $1`, tktID},
			{`DELETE FROM orders WHERE id = $1`, orderID},
			{`DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, orgID},
			{`DELETE FROM sales_links WHERE org_id = $1`, orgID},
			{`DELETE FROM checkout_sessions WHERE id = $1`, csID},
			{`DELETE FROM reservations WHERE id = $1`, resvID},
			{`DELETE FROM ticket_tiers WHERE id = $1`, tierID},
			{`DELETE FROM sales_channels WHERE id = $1`, chanID},
			{`DELETE FROM sessions WHERE id = $1`, sessID},
			{`DELETE FROM events WHERE id = $1`, evtID},
			{`DELETE FROM venues WHERE id = $1`, venueID},
			{`DELETE FROM organizations WHERE id = $1`, orgID},
			{`DELETE FROM organizations WHERE id = $1`, otherOrgID},
		} {
			if _, err := pool.Exec(context.Background(), step.sql, step.arg); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	}
	defer cleanup()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Links Org "+nonce, "links-"+nonce)
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, otherOrgID, "Links Other "+nonce, "links-other-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Links Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Links Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)
	exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1, $2, 'Standard', 'fixed', 50000, 'CZK')`, tierID, sessID)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)

	q := gen.New(pool)
	label := func(s string) string { return s }

	// ── Insert: a free code, then the same code again ───────────────────────
	first, ok, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
		OrgID: orgID, Code: code("a"), TargetKind: "event", EventID: &evtID,
		Label: label("Poster at the theatre"),
	})
	if err != nil || !ok {
		t.Fatalf("first insert: ok=%v err=%v", ok, err)
	}
	if first.ShortHost != gen.DefaultSalesLinkShortHost || first.PlacementKind != "poster" ||
		first.Status != "active" || string(first.UTM) != "{}" || first.OrdersCount != 0 {
		t.Errorf("defaults: %+v", first)
	}
	dup, ok, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
		OrgID: orgID, Code: code("a"), TargetKind: "org_page", Label: "Other",
	})
	if err != nil || ok || dup.ID != uuid.Nil {
		t.Fatalf("repeat with the same code must be a quiet miss: ok=%v err=%v row=%+v", ok, err, dup)
	}
	if got, err := q.GetSalesLinkByCode(ctx, "Q"+code("a")[1:]); err != nil || got.ID != first.ID {
		t.Errorf("GetSalesLinkByCode is case-insensitive: %v %v", err, got.ID)
	}
	if _, err := q.GetSalesLinkByID(ctx, first.ID, otherOrgID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another organization's link: got %v, want pgx.ErrNoRows", err)
	}

	// ── Paging: three links, limit 2 → 2 then 1 ─────────────────────────────
	sessionURL := "https://example.test/landing"
	second, _, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
		OrgID: orgID, Code: code("b"), TargetKind: "session", SessionID: &sessID, EventID: &evtID,
		Label: "Flyer", PlacementKind: "flyer", UTM: json.RawMessage(`{"utm_source":"qr"}`),
	})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	third, _, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
		OrgID: orgID, Code: code("c"), TargetKind: "url", URL: &sessionURL, Label: "Own page",
	})
	if err != nil {
		t.Fatalf("third insert: %v", err)
	}
	page1, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: orgID, Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("page 1: n=%d err=%v", len(page1), err)
	}
	if page1[0].ID != third.ID || page1[1].ID != second.ID {
		t.Errorf("page 1 order (newest first): %v %v", page1[0].Code, page1[1].Code)
	}
	last := page1[1]
	page2, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{
		OrgID: orgID, Limit: 2, CursorCreatedAt: &last.CreatedAt, CursorID: &last.ID,
	})
	if err != nil || len(page2) != 1 || page2[0].ID != first.ID {
		t.Fatalf("page 2: n=%d err=%v", len(page2), err)
	}
	if half, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: orgID, Limit: 2, CursorCreatedAt: &last.CreatedAt}); err != nil || len(half) != 2 {
		t.Errorf("a half cursor must be ignored, not return an empty page: n=%d err=%v", len(half), err)
	}
	if bySession, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: orgID, SessionID: &sessID, Limit: 10}); err != nil || len(bySession) != 1 || bySession[0].ID != second.ID {
		t.Errorf("session filter: n=%d err=%v", len(bySession), err)
	}
	if byEvent, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: orgID, EventID: &evtID, Limit: 10}); err != nil || len(byEvent) != 2 {
		t.Errorf("event filter (event + session link): n=%d err=%v", len(byEvent), err)
	}
	if foreign, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: otherOrgID, Limit: 10}); err != nil || len(foreign) != 0 {
		t.Errorf("another organization lists nothing: n=%d err=%v", len(foreign), err)
	}

	// ── Tri-state update: unset fields stay ─────────────────────────────────
	upd, err := q.UpdateSalesLink(ctx, gen.UpdateSalesLinkParams{
		ID: first.ID, OrgID: orgID, SetLabel: true, Label: "Poster, new label",
	})
	if err != nil {
		t.Fatalf("UpdateSalesLink label: %v", err)
	}
	if upd.Label != "Poster, new label" || upd.PlacementKind != "poster" || upd.Status != "active" ||
		upd.TargetKind != "event" || upd.EventID == nil || *upd.EventID != evtID || !upd.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("label-only update touched other fields: %+v", upd)
	}
	upd, err = q.UpdateSalesLink(ctx, gen.UpdateSalesLinkParams{
		ID: first.ID, OrgID: orgID, SetStatus: true, Status: "archived",
		SetUTM: true, UTM: json.RawMessage(`{"utm_campaign":"autumn"}`),
	})
	if err != nil || upd.Status != "archived" || string(upd.UTM) != `{"utm_campaign": "autumn"}` || upd.Label != "Poster, new label" {
		t.Errorf("status+utm update: %v %+v", err, upd)
	}
	// A retarget writes the whole group: event → session clears nothing stale.
	upd, err = q.UpdateSalesLink(ctx, gen.UpdateSalesLinkParams{
		ID: first.ID, OrgID: orgID, SetTarget: true, TargetKind: "session", EventID: &evtID, SessionID: &sessID,
	})
	if err != nil || upd.TargetKind != "session" || upd.SessionID == nil || upd.Label != "Poster, new label" {
		t.Errorf("retarget: %v %+v", err, upd)
	}
	// A target that breaks a CHECK is rejected as a whole.
	if _, err := q.UpdateSalesLink(ctx, gen.UpdateSalesLinkParams{
		ID: first.ID, OrgID: orgID, SetTarget: true, TargetKind: "url",
	}); err == nil {
		t.Errorf("an url target without url must be refused")
	}
	if _, err := q.UpdateSalesLink(ctx, gen.UpdateSalesLinkParams{
		ID: first.ID, OrgID: otherOrgID, SetLabel: true, Label: "hijack",
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("update of another organization's link: got %v, want pgx.ErrNoRows", err)
	}
	if active, err := q.ListSalesLinks(ctx, gen.ListSalesLinksParams{OrgID: orgID, Status: "active", Limit: 10}); err != nil || len(active) != 2 {
		t.Errorf("status filter active: n=%d err=%v", len(active), err)
	}

	// ── Counters ────────────────────────────────────────────────────────────
	if err := q.BumpSalesLinkScan(ctx, first.ID, false); err != nil {
		t.Fatalf("BumpSalesLinkScan: %v", err)
	}
	if err := q.BumpSalesLinkScan(ctx, first.ID, true); err != nil {
		t.Fatalf("BumpSalesLinkScan suspect: %v", err)
	}
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := q.UpsertSalesLinkHit(ctx, first.ID, day, "CZ", false); err != nil {
			t.Fatalf("UpsertSalesLinkHit: %v", err)
		}
	}
	if err := q.UpsertSalesLinkHit(ctx, first.ID, day, "CZ", true); err != nil {
		t.Fatalf("UpsertSalesLinkHit suspect: %v", err)
	}
	if err := q.UpsertSalesLinkHit(ctx, first.ID, day.AddDate(0, 0, 1), "", false); err != nil {
		t.Fatalf("UpsertSalesLinkHit unknown country: %v", err)
	}
	got, err := q.GetSalesLinkByID(ctx, first.ID, orgID)
	if err != nil || got.ScanCount != 1 || got.SuspectScanCount != 1 || got.LastScannedAt == nil {
		t.Errorf("scan counters: %v %+v", err, got)
	}
	var hits, suspect int64
	if err := pool.QueryRow(ctx, `SELECT hits, suspect_hits FROM sales_link_hits WHERE sales_link_id = $1 AND day = $2 AND country = 'CZ'`,
		first.ID, day).Scan(&hits, &suspect); err != nil || hits != 2 || suspect != 1 {
		t.Errorf("hit row: hits=%d suspect=%d err=%v", hits, suspect, err)
	}
	byDay, err := q.SalesLinkHitsByDay(ctx, first.ID, orgID, day, day.AddDate(0, 0, 1))
	if err != nil || len(byDay) != 2 || byDay[0].Hits != 2 || byDay[0].SuspectHits != 1 || byDay[1].Hits != 1 {
		t.Errorf("hits by day: %v %+v", err, byDay)
	}
	byCountry, err := q.SalesLinkHitsByCountry(ctx, first.ID, orgID, day, day.AddDate(0, 0, 1))
	if err != nil || len(byCountry) != 2 || byCountry[0].Country != "CZ" || byCountry[0].Hits != 2 || byCountry[1].Country != "" {
		t.Errorf("hits by country: %v %+v", err, byCountry)
	}
	if foreign, err := q.SalesLinkHitsByDay(ctx, first.ID, otherOrgID, day, day); err != nil || len(foreign) != 0 {
		t.Errorf("hits of another organization's link: n=%d err=%v", len(foreign), err)
	}

	// ── CHECK constraints ───────────────────────────────────────────────────
	for name, p := range map[string]gen.InsertSalesLinkParams{
		"session without session_id": {OrgID: orgID, Code: code("d"), TargetKind: "session", EventID: &evtID, Label: "x"},
		"url kind without url":       {OrgID: orgID, Code: code("e"), TargetKind: "url", Label: "x"},
		"event without event_id":     {OrgID: orgID, Code: code("f"), TargetKind: "event", Label: "x"},
		"event with a session":       {OrgID: orgID, Code: code("g"), TargetKind: "event", EventID: &evtID, SessionID: &sessID, Label: "x"},
		"promoter page no promoter":  {OrgID: orgID, Code: code("h"), TargetKind: "promoter_page", Label: "x"},
		"org page with an event":     {OrgID: orgID, Code: code("j"), TargetKind: "org_page", EventID: &evtID, Label: "x"},
		"empty label":                {OrgID: orgID, Code: code("k"), TargetKind: "org_page", Label: ""},
		"bad code":                   {OrgID: orgID, Code: "ab1de0f", TargetKind: "org_page", Label: "x"},
		"bad placement":              {OrgID: orgID, Code: code("m"), TargetKind: "org_page", Label: "x", PlacementKind: "billboard"},
	} {
		if _, ok, err := q.InsertSalesLinkIfCodeFree(ctx, p); err == nil || ok {
			t.Errorf("%s: want a CHECK violation, got ok=%v err=%v", name, ok, err)
		}
	}
	orgPage, ok, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
		OrgID: orgID, Code: code("n"), TargetKind: "org_page", Label: "All events",
	})
	if err != nil || !ok {
		t.Fatalf("org_page link needs no target: ok=%v err=%v", ok, err)
	}
	_ = orgPage

	// ── Orders: the scanner columns and the aggregate ───────────────────────
	if rows, err := q.SalesLinkOrdersAggregate(ctx, first.ID, orgID); err != nil || len(rows) != 0 {
		t.Fatalf("aggregate of a link without orders: n=%d err=%v", len(rows), err)
	}
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email,
	                          sales_link_id, attribution)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'CZK', 50000, 0, 0, 50000, 'x', 'x@example.test',
	              $8, '{"ref":"abc"}'::jsonb)`,
		orderID, orgID, chanID, evtID, sessID, csID, resvID, first.ID)
	exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id)
	      VALUES ($1, $2, $3, $4, 'x@example.test', $5)`, tktID, csID, sessID, tierID, orderID)
	exec(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, succeeded_at)
	      VALUES ($1, $2, 20000, 'CZK', 'succeeded', 'external', $3, $4, now())`, refID, orgID, orderID, tktID)

	agg, err := q.SalesLinkOrdersAggregate(ctx, first.ID, orgID)
	if err != nil || len(agg) != 1 {
		t.Fatalf("aggregate: n=%d err=%v", len(agg), err)
	}
	if a := agg[0]; a.Currency != "CZK" || a.Orders != 1 || a.Tickets != 1 || a.RevenueMinor != 30000 {
		t.Errorf("aggregate: %+v (want 1 order, 1 ticket, 50000-20000)", a)
	}
	if foreign, err := q.SalesLinkOrdersAggregate(ctx, first.ID, otherOrgID); err != nil || len(foreign) != 0 {
		t.Errorf("aggregate of another organization's link: n=%d err=%v", len(foreign), err)
	}
	withCount, err := q.GetSalesLinkByID(ctx, first.ID, orgID)
	if err != nil || withCount.OrdersCount != 1 {
		t.Errorf("orders_count: %v %d", err, withCount.OrdersCount)
	}
	ord, err := q.GetOrderByID(ctx, orderID, orgID)
	if err != nil {
		t.Fatalf("GetOrderByID: %v", err)
	}
	if ord.SalesLinkID == nil || *ord.SalesLinkID != first.ID || string(ord.Attribution) != `{"ref": "abc"}` {
		t.Errorf("order scanner: link=%v attribution=%s", ord.SalesLinkID, ord.Attribution)
	}
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil || string(cs.Attribution) != "{}" {
		t.Errorf("checkout session scanner: %v attribution=%s", err, cs.Attribution)
	}
	if _, err := q.ListAllCheckoutSessions(ctx, &orgID, nil, 10, 0); err != nil {
		t.Errorf("ListAllCheckoutSessions: %v", err)
	}
	if _, err := q.ListAllOrders(ctx, &orgID, nil, nil, 10, 0); err != nil {
		t.Errorf("ListAllOrders: %v", err)
	}
}
