//go:build integration

// event_center_summaries_integration_test.go — EC-02, EC-03 and EC-06
// (spec 35 §5.3, §5.4, §5.7) through the REAL router:
//
//   - `sales_state` on GET /v1/organizations/{org_id}/events for the four
//     states (on_sale / upcoming / sold_out / archived), with
//     next_session_at and session_count;
//   - the extended session summary (promo redemptions, the door count, the
//     invitations);
//   - the event summary: totals over every session plus the per-session
//     list, built by the same assembly;
//   - a manager (membership organizer, empty roles claim — what the bot
//     mints) answers 200, an agent 403, another organization 404.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ecSumFixture is one organization with four events in the four sales
// states. The on-sale event has a past session (ended, nothing sold) and a
// future one carrying every kind of row the summaries aggregate.
type ecSumFixture struct {
	t       *testing.T
	f       *prom0113Fixture
	q       *gen.Queries
	org     uuid.UUID
	foreign uuid.UUID
	manager string
	agent   string
	outside string
	users   []uuid.UUID

	evOnSale, evUpcoming, evSoldOut, evArchived, evEmpty uuid.UUID
	sessFuture, sessPast                                 uuid.UUID
	tierFuture, tierPast                                 uuid.UUID
	promoID                                              uuid.UUID
	paidOrder, complOrder                                uuid.UUID
}

func newECSumFixture(t *testing.T) *ecSumFixture {
	t.Helper()
	_, secret := productionIntegrationServer(t)
	f := newProm0113Fixture(t)
	ctx := context.Background()
	x := &ecSumFixture{t: t, f: f, q: f.q, org: f.org(t, "EC"), foreign: f.org(t, "ECForeign")}

	member := func(org uuid.UUID, role string) string {
		t.Helper()
		user, err := f.q.InsertUser(ctx, "ecsum-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := f.q.InsertMembership(ctx, user.ID, org, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		x.users = append(x.users, user.ID)
		return tok
	}
	x.manager = member(x.org, "organizer")
	x.agent = member(x.org, "agent")
	x.outside = member(x.foreign, "organizer")

	suffix := uuid.NewString()[:8]
	venue := uuid.New()
	channel := uuid.New()
	x.evOnSale, x.evUpcoming, x.evSoldOut, x.evArchived, x.evEmpty = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	x.sessFuture, x.sessPast = uuid.New(), uuid.New()
	sessUpcoming, sessSoldOut, sessArchived := uuid.New(), uuid.New(), uuid.New()
	x.tierFuture, x.tierPast = uuid.New(), uuid.New()
	tierUpcoming, tierSoldOut, tierArchived := uuid.New(), uuid.New(), uuid.New()
	resPaid, csPaid, resCompl, csCompl := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	x.paidOrder, x.complOrder = uuid.New(), uuid.New()
	tkt1, tkt2, tkt3 := uuid.New(), uuid.New(), uuid.New()
	item1, item2, item3 := uuid.New(), uuid.New(), uuid.New()
	email := "ecsum-" + suffix + "@example.test"

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.q.DB().Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM promo_code_redemptions WHERE order_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.paidOrder, x.complOrder}}},
			{`DELETE FROM order_items WHERE order_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.paidOrder, x.complOrder}}},
			{`DELETE FROM tickets WHERE order_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.paidOrder, x.complOrder}}},
			{`DELETE FROM orders WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM session_seats WHERE session_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.sessFuture, x.sessPast, sessUpcoming, sessSoldOut, sessArchived}}},
			{`DELETE FROM checkout_sessions WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM reservations WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM ticket_tiers WHERE session_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.sessFuture, x.sessPast, sessUpcoming, sessSoldOut, sessArchived}}},
			{`DELETE FROM promo_codes WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM sales_channels WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM sessions WHERE event_id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.evOnSale, x.evUpcoming, x.evSoldOut, x.evArchived}}},
			{`DELETE FROM events WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM venues WHERE org_id = $1`, []any{x.org}},
			{`DELETE FROM memberships WHERE user_id = ANY($1::uuid[])`, []any{x.users}},
			{`DELETE FROM organizations WHERE id = ANY($1::uuid[])`, []any{[]uuid.UUID{x.org, x.foreign}}},
			{`DELETE FROM users WHERE id = ANY($1::uuid[])`, []any{x.users}},
		} {
			if _, err := f.q.DB().Exec(c, stmt.sql, stmt.args...); err != nil {
				t.Logf("ecsum cleanup: %s: %v", stmt.sql, err)
			}
		}
	})

	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venue, x.org, "EC Hall "+suffix)
	exec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, channel, x.org, "EC Channel "+suffix)
	for _, ev := range []struct {
		id   uuid.UUID
		name string
	}{
		{x.evOnSale, "On sale"}, {x.evUpcoming, "Upcoming"}, {x.evSoldOut, "Sold out"}, {x.evArchived, "Archived"}, {x.evEmpty, "Empty"},
	} {
		exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, ev.id, x.org, "EC "+ev.name+" "+suffix)
	}
	session := func(id, event uuid.UUID, start time.Time) {
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`, id, event, venue, start)
	}
	future := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)
	past := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Minute)
	session(x.sessFuture, x.evOnSale, future)
	session(x.sessPast, x.evOnSale, past)
	session(sessUpcoming, x.evUpcoming, future)
	session(sessSoldOut, x.evSoldOut, future)
	session(sessArchived, x.evArchived, past)

	tier := func(id, sess uuid.UUID, windowStart *time.Time) {
		exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open, sale_window_start)
		      VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true, $3)`, id, sess, windowStart)
	}
	opensLater := future.Add(-24 * time.Hour)
	tier(x.tierFuture, x.sessFuture, nil)
	tier(x.tierPast, x.sessPast, nil)
	tier(tierUpcoming, sessUpcoming, &opensLater)
	tier(tierSoldOut, sessSoldOut, nil)
	tier(tierArchived, sessArchived, nil)

	// GA places of the post-0101 shape: owned by the category, keyed ga|t<unit_seq>|<n>.
	place := func(sess, tier uuid.UUID, n int, status string, reservation *uuid.UUID) {
		key := "ga|t1|00000" + string(rune('0'+n))
		exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind, reservation_id)
		      VALUES ($1, $2, '', '', '', $3, $4, 'ga_unit', $5)`, sess, key, tier, status, reservation)
	}
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, resPaid, x.org, channel, x.sessFuture)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resCompl, x.org, channel, x.sessFuture)
	for n := 1; n <= 5; n++ {
		switch {
		case n <= 2:
			place(x.sessFuture, x.tierFuture, n, "sold", &resPaid)
		case n == 3:
			place(x.sessFuture, x.tierFuture, n, "sold", &resCompl)
		default:
			place(x.sessFuture, x.tierFuture, n, "available", nil)
		}
		place(x.sessPast, x.tierPast, n, "available", nil)
		place(sessUpcoming, tierUpcoming, n, "available", nil)
		place(sessSoldOut, tierSoldOut, n, "sold", nil) // sold in the system it was imported from
		place(sessArchived, tierArchived, n, "available", nil)
	}

	promo, err := f.q.InsertPromoCode(ctx, x.org, "EC"+suffix, "fixed_amount", 500, []string{}, []string{}, "EUR", nil, nil, nil, nil, 0, "active")
	if err != nil {
		t.Fatalf("InsertPromoCode: %v", err)
	}
	x.promoID = promo.ID

	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, csPaid, x.org, channel, resPaid)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, csCompl, x.org, channel, resCompl)
	// A paid order of two tickets with the promo code: 5000 - 500 discount + 200 charge.
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, promo_code_id, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 500, 200, 4700, $8, 'x', $9)`,
		x.paidOrder, x.org, channel, x.evOnSale, x.sessFuture, csPaid, resPaid, promo.ID, email)
	// An invitation: source complimentary, total 0.
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'complimentary', 'paid', 'EUR', 2500, 2500, 0, 0, 'x', $8)`,
		x.complOrder, x.org, channel, x.evOnSale, x.sessFuture, csCompl, resCompl, email)
	ticket := func(id, cs, order uuid.UUID, ordinal int, used bool) {
		var usedAt *time.Time
		if used {
			n := time.Now().UTC()
			usedAt = &n
		}
		exec(`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal, used_at)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, cs, x.sessFuture, x.tierFuture, email, order, ordinal, usedAt)
	}
	ticket(tkt1, csPaid, x.paidOrder, 0, true)
	ticket(tkt2, csPaid, x.paidOrder, 1, false)
	ticket(tkt3, csCompl, x.complOrder, 0, false)
	item := func(id, order, tkt uuid.UUID, ordinal int, unit, discount, charge, total int64) {
		exec(`INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, unit_price, discount, charge, total)
		      VALUES ($1, $2, $3, 'ticket', $4, $5, $6, $7, $8, $9)`, id, order, ordinal, x.tierFuture, tkt, unit, discount, charge, total)
	}
	item(item1, x.paidOrder, tkt1, 1, 2500, 250, 100, 2350)
	item(item2, x.paidOrder, tkt2, 2, 2500, 250, 100, 2350)
	item(item3, x.complOrder, tkt3, 1, 2500, 2500, 0, 0)
	if err := f.q.InsertPromoCodeRedemption(ctx, promo.ID, nil, &resPaid, 500, 5000, &x.paidOrder, nil, &channel); err != nil {
		t.Fatalf("InsertPromoCodeRedemption: %v", err)
	}
	return x
}

func (x *ecSumFixture) get(path, token string) (int, map[string]any) {
	x.t.Helper()
	return x.f.do(x.t, http.MethodGet, path, token, "")
}

func ecNum(m map[string]any, keys ...string) float64 {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return -1
		}
		cur = obj[k]
	}
	n, ok := cur.(float64)
	if !ok {
		return -1
	}
	return n
}

func TestEventCenter_SalesStateOnEventsList(t *testing.T) {
	x := newECSumFixture(t)
	st, body := x.get("/v1/organizations/"+x.org.String()+"/events", x.manager)
	if st != http.StatusOK {
		t.Fatalf("events list: %d %v", st, body)
	}
	events, _ := body["events"].([]any)
	byID := map[string]map[string]any{}
	for _, e := range events {
		ev, _ := e.(map[string]any)
		byID[ev["id"].(string)] = ev
	}
	want := map[uuid.UUID]string{
		x.evOnSale: "on_sale", x.evUpcoming: "upcoming", x.evSoldOut: "sold_out",
		x.evArchived: "archived", x.evEmpty: "archived",
	}
	for id, state := range want {
		ev, ok := byID[id.String()]
		if !ok {
			t.Fatalf("event %s missing from the list", id)
		}
		if ev["sales_state"] != state {
			t.Errorf("event %s: sales_state = %v, want %s (%v)", id, ev["sales_state"], state, ev)
		}
	}
	onSale := byID[x.evOnSale.String()]
	if onSale["session_count"] != float64(2) || onSale["next_session_at"] == nil {
		t.Errorf("on-sale event must count both sessions and name the future one: %v", onSale)
	}
	if next, _ := onSale["next_session_at"].(string); next == "" {
		t.Errorf("next_session_at must be an RFC3339 string: %v", onSale["next_session_at"])
	} else if parsed, err := time.Parse(time.RFC3339, next); err != nil || !parsed.After(time.Now()) {
		t.Errorf("next_session_at = %q, want a future RFC3339 instant", next)
	}
	if byID[x.evArchived.String()]["next_session_at"] != nil || byID[x.evEmpty.String()]["session_count"] != float64(0) {
		t.Errorf("archived events: %v / %v", byID[x.evArchived.String()], byID[x.evEmpty.String()])
	}
}

func TestEventCenter_SessionSummaryExtensions(t *testing.T) {
	x := newECSumFixture(t)
	path := "/v1/organizations/" + x.org.String() + "/sessions/" + x.sessFuture.String() + "/summary"
	st, body := x.get(path, x.manager)
	if st != http.StatusOK {
		t.Fatalf("session summary as manager: %d %v", st, body)
	}
	if ecNum(body, "entered", "used") != 1 || ecNum(body, "entered", "total") != 3 {
		t.Errorf("entered: %v", body["entered"])
	}
	if ecNum(body, "complimentary", "orders") != 1 || ecNum(body, "complimentary", "tickets") != 1 {
		t.Errorf("complimentary: %v", body["complimentary"])
	}
	promos, _ := body["promos"].([]any)
	if len(promos) != 1 {
		t.Fatalf("promos: %v", body["promos"])
	}
	p := promos[0].(map[string]any)
	if p["id"] != x.promoID.String() || p["orders"] != float64(1) || p["redemptions"] != float64(1) || p["discount"] != float64(500) || p["currency"] != "EUR" {
		t.Errorf("promo row: %v", p)
	}
	money, _ := body["money"].([]any)
	if len(money) != 1 {
		t.Fatalf("money: %v", body["money"])
	}
	m := money[0].(map[string]any)
	if m["paid_orders"] != float64(2) || m["paid"] != float64(4700) || m["net"] != float64(4700) || m["discount"] != float64(3000) {
		t.Errorf("money: %v", m)
	}
	if ecNum(body, "places", "ga", "sold") != 3 || ecNum(body, "places", "ga", "available") != 2 || ecNum(body, "tickets", "active") != 3 {
		t.Errorf("places/tickets: %v / %v", body["places"], body["tickets"])
	}

	// An agent holds no order.read; another organization cannot see the session.
	if st, _ := x.get(path, x.agent); st != http.StatusForbidden {
		t.Errorf("agent: %d, want 403", st)
	}
	if st, _ := x.get("/v1/organizations/"+x.foreign.String()+"/sessions/"+x.sessFuture.String()+"/summary", x.outside); st != http.StatusNotFound {
		t.Errorf("foreign organization: %d, want 404", st)
	}
}

func TestEventCenter_EventSummaryTotalsAndSessions(t *testing.T) {
	x := newECSumFixture(t)
	path := "/v1/organizations/" + x.org.String() + "/events/" + x.evOnSale.String() + "/summary"
	st, body := x.get(path, x.manager)
	if st != http.StatusOK {
		t.Fatalf("event summary as manager: %d %v", st, body)
	}
	if ecNum(body, "event", "session_count") != 2 || body["event"].(map[string]any)["id"] != x.evOnSale.String() {
		t.Errorf("event header: %v", body["event"])
	}
	// Totals equal the future session's figures: the past session sold nothing.
	if ecNum(body, "entered", "used") != 1 || ecNum(body, "entered", "total") != 3 ||
		ecNum(body, "complimentary", "tickets") != 1 || ecNum(body, "tickets", "active") != 3 {
		t.Errorf("event totals: entered %v complimentary %v tickets %v", body["entered"], body["complimentary"], body["tickets"])
	}
	money, _ := body["money"].([]any)
	if len(money) != 1 || money[0].(map[string]any)["net"] != float64(4700) || money[0].(map[string]any)["paid_orders"] != float64(2) {
		t.Errorf("event money: %v", body["money"])
	}
	// Both sessions sell "Standing" at 2500 EUR: one merged row of the event table, 10 places.
	tiers, _ := body["tiers"].([]any)
	if len(tiers) != 1 {
		t.Fatalf("event tiers: %v", body["tiers"])
	}
	tier := tiers[0].(map[string]any)
	if tier["name"] != "Standing" || tier["sessions"] != float64(2) || ecNum(tier, "places", "total") != 10 ||
		ecNum(tier, "places", "sold") != 3 || tier["paid_items"] != float64(3) || tier["paid_revenue"] != float64(4700) {
		t.Errorf("merged tier: %v", tier)
	}
	if ecNum(body, "places", "ga", "total") != 10 || ecNum(body, "places", "ga", "available") != 7 {
		t.Errorf("event places: %v", body["places"])
	}
	promos, _ := body["promos"].([]any)
	if len(promos) != 1 || promos[0].(map[string]any)["redemptions"] != float64(1) {
		t.Errorf("event promos: %v", body["promos"])
	}

	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("sessions: %v", body["sessions"])
	}
	pastEntry, futureEntry := sessions[0].(map[string]any), sessions[1].(map[string]any)
	if pastEntry["id"] != x.sessPast.String() || futureEntry["id"] != x.sessFuture.String() {
		t.Fatalf("sessions must be in start order: %v", sessions)
	}
	if ecNum(pastEntry, "tickets", "active") != 0 || len(pastEntry["money"].([]any)) != 0 || ecNum(pastEntry, "places", "ga", "available") != 5 {
		t.Errorf("past session entry: %v", pastEntry)
	}
	if ecNum(futureEntry, "entered", "used") != 1 || ecNum(futureEntry, "complimentary", "orders") != 1 {
		t.Errorf("future session entry: %v", futureEntry)
	}
	futureTiers, _ := futureEntry["tiers"].([]any)
	if len(futureTiers) != 1 || futureTiers[0].(map[string]any)["id"] != x.tierFuture.String() {
		t.Errorf("per-session tiers keep their ids: %v", futureEntry["tiers"])
	}
	if _, ok := futureEntry["venue_name"].(string); !ok || futureEntry["venue_timezone"] != "Europe/Prague" {
		t.Errorf("session entry header: %v", futureEntry)
	}

	// Gates: agent 403, another organization 404, a bogus event 404.
	if st, _ := x.get(path, x.agent); st != http.StatusForbidden {
		t.Errorf("agent: %d, want 403", st)
	}
	if st, _ := x.get("/v1/organizations/"+x.foreign.String()+"/events/"+x.evOnSale.String()+"/summary", x.outside); st != http.StatusNotFound {
		t.Errorf("foreign organization: %d, want 404", st)
	}
	if st, _ := x.get("/v1/organizations/"+x.org.String()+"/events/"+uuid.NewString()+"/summary", x.manager); st != http.StatusNotFound {
		t.Errorf("unknown event: %d, want 404", st)
	}
	// An event with no sessions still answers, with empty figures.
	st, body = x.get("/v1/organizations/"+x.org.String()+"/events/"+x.evEmpty.String()+"/summary", x.manager)
	if st != http.StatusOK || ecNum(body, "event", "session_count") != 0 || len(body["sessions"].([]any)) != 0 || len(body["tiers"].([]any)) != 0 {
		t.Errorf("empty event: %d %v", st, body)
	}
}
