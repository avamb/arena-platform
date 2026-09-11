//go:build integration

// Package macs — W1-M2 money units (feature #529, spec
// 08_architecture/20_bil24_gateway_money_units_spec_ru.md §§2, 4).
//
// The MACS payload and the Bil24 `bil24_wp` payload describe the SAME sale to
// two different partner systems, and both are legacy contracts that expect
// MAJOR currency units. Before this wave MACS shipped the database's minor
// units verbatim, so an order the WordPress site knew as 19.85 arrived at MACS
// as 1985 — a 100× discrepancy that no whole-number fixture could reveal,
// because 1500 and 15 both look like "a price".
//
// This test seeds ONE order with the fractional figures the spec pins by name
// (1890 minor → 18.9, charge 95 → 0.95, total 1985 → 19.85) and asserts that:
//
//  1. every money field of the MACS document is in major units,
//  2. rendered as JSON it has at most two decimals and no trailing zeros
//     (`18.9`, never `18.90` or `18.899999999999999`),
//  3. it equals the bil24wire encoding of the same orderexport projection,
//  4. the accounting identity still holds EXACTLY — checked in minor units,
//     because adding major-unit floats does not reproduce it.
//
// Prerequisites:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable
//
// Run with:
//
//	go test -tags integration -run TestMACS_W1M2 ./apps/backend/internal/platform/macs/
package macs_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// The spec 20 §5 figures, in the two representations that must never be
// confused again.
const (
	m529SubtotalMinor = int64(1890)
	m529SubtotalMajor = 18.9
	m529ChargeMinor   = int64(95)
	m529TotalMinor    = int64(1985)
	m529TotalMajor    = 19.85
)

func TestMACS_W1M2_FractionalMoneyIsMajorUnits(t *testing.T) {
	pool := roundtripPool(t) // skips when DATABASE_URL not set
	ctx := context.Background()

	orgID := uuid.New()
	venueID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	channelID := uuid.New()
	cityID := uuid.New()
	orderID := uuid.New()
	checkoutID := uuid.New()
	ticketID := uuid.New()
	suffix := orgID.String()[:8]
	citySlug := "macs-w1m2-city-" + suffix

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	var countryID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2='IL' LIMIT 1`).Scan(&countryID); err != nil {
		t.Skipf("IL country not found (migration 0006 not applied?): %v", err)
	}
	mustExec(`INSERT INTO cities (id, country_id, slug) VALUES ($1, $2, $3)`, cityID, countryID, citySlug)
	mustExec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('geo.cities', $1, 'en', 'W1-M2 Test City')`,
		citySlug)
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "W1M2 Org "+suffix, "w1m2-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name, city_id) VALUES ($1, $2, $3, $4)`,
		venueID, orgID, "W1M2 Venue", cityID)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
		eventID, orgID, "W1M2 Event")
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		VALUES ($1, $2, $3, NOW()+INTERVAL '90 days', NOW()+INTERVAL '90 days 3 hours', 100, 'draft', 'general_admission', 'EUR', 'override')`,
		sessionID, eventID, venueID)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		channelID, orgID, "W1M2 Channel")
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, NULL, 100, 1)`,
		sessionID)

	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 1, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("InsertReservation: %v", err)
	}
	// orderexport projects an order's money from the CHECKOUT SESSION
	// (query.go: COALESCE(cs.total, 0) …), not from orders — the checkout
	// flow writes both. A fixture that leaves cs money NULL therefore
	// exports zeros, which is exactly the trap this test must not fall into.
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, platform_fee, total, currency, payment_provider, completed_at)
		VALUES ($1, $2, $3, $4, 'completed', $5, 0, $6, $7, 'EUR', 'stripe', NOW())`,
		checkoutID, orgID, channelID, res.ID, m529SubtotalMinor, m529ChargeMinor, m529TotalMinor)
	// subtotal 1890, no discount, a 5 % service charge of 95 → total 1985.
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total, buyer_email, paid_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid','EUR',$8,0,$9,$10,'w1m2@example.com',NOW())`,
		orderID, orgID, channelID, eventID, sessionID, checkoutID, res.ID,
		m529SubtotalMinor, m529ChargeMinor, m529TotalMinor)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, status, issued_at, ordinal, holder_email)
		VALUES ($1,$2,$3,$4,'active',NOW(),1,'w1m2@example.com')`,
		ticketID, sessionID, checkoutID, orderID)

	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM tickets WHERE id=$1`, ticketID)
		pool.Exec(c, `DELETE FROM orders WHERE id=$1`, orderID)
		pool.Exec(c, `DELETE FROM checkout_sessions WHERE id=$1`, checkoutID)
		pool.Exec(c, `DELETE FROM reservations WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM sales_channels WHERE id=$1`, channelID)
		pool.Exec(c, `DELETE FROM sessions WHERE id=$1`, sessionID)
		pool.Exec(c, `DELETE FROM events WHERE id=$1`, eventID)
		pool.Exec(c, `DELETE FROM venues WHERE id=$1`, venueID)
		pool.Exec(c, `DELETE FROM organizations WHERE id=$1`, orgID)
		pool.Exec(c, `DELETE FROM i18n_text WHERE namespace='geo.cities' AND key=$1`, citySlug)
		pool.Exec(c, `DELETE FROM cities WHERE id=$1`, cityID)
	})

	// ── The MACS document ────────────────────────────────────────────────
	order, err := macs.QueryAndBuildOrder(ctx, pool, orderID)
	if err != nil {
		t.Fatalf("QueryAndBuildOrder: %v", err)
	}
	if order == nil {
		t.Fatal("QueryAndBuildOrder returned nil for a paid order with one issued ticket")
	}
	if len(order.TicketList) != 1 {
		t.Fatalf("ticketList has %d entries; want 1", len(order.TicketList))
	}
	tk := order.TicketList[0]

	for _, c := range []struct {
		field string
		got   float64
		want  float64
	}{
		{"order.sum", order.Sum, m529SubtotalMajor},
		{"order.discount", order.Discount, 0},
		{"order.charge", order.Charge, m529TotalMajor},
		{"order.totalSum", order.TotalSum, m529TotalMajor},
		{"ticket.price", tk.Price, m529SubtotalMajor},
		{"ticket.discount", tk.Discount, 0},
		{"ticket.charge", tk.Charge, m529SubtotalMajor},
		{"ticket.totalPrice", tk.TotalPrice, m529SubtotalMajor},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v major units", c.field, c.got, c.want)
		}
		if !m529AtMostTwoDecimals(c.got) {
			t.Errorf("%s = %v has more than two decimal places", c.field, c.got)
		}
	}

	// The identity in MINOR units — the only representation in which it is
	// exact. sum - discount + serviceCharge = totalSum.
	sumMinor, discountMinor, totalMinor := money.Minor(order.Sum), money.Minor(order.Discount), money.Minor(order.TotalSum)
	if sumMinor-discountMinor+m529ChargeMinor != totalMinor {
		t.Errorf("accounting identity broken: %d - %d + %d != %d (minor units)",
			sumMinor, discountMinor, m529ChargeMinor, totalMinor)
	}

	// ── Rendered JSON: at most two decimals, no trailing zeros ───────────
	raw, err := json.Marshal(order)
	if err != nil {
		t.Fatalf("marshal MACS order: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"sum":18.9`, `"totalSum":19.85`, `"price":18.9`} {
		if !strings.Contains(body, want) {
			t.Errorf("MACS JSON is missing %s\nbody: %s", want, body)
		}
	}
	// A float artefact or a re-introduced minor-unit value would show up as
	// one of these.
	for _, forbidden := range []string{`18.90`, `18.899`, `19.8500`, `"sum":1890`, `"totalSum":1985`, `"sum":"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("MACS JSON contains %s — money must be major units with <= 2 decimals\nbody: %s", forbidden, body)
		}
	}

	// ── Same order, the other partner wire: the figures must agree ───────
	projected, err := orderexport.QueryOrder(ctx, pool, orderID)
	if err != nil {
		t.Fatalf("orderexport.QueryOrder: %v", err)
	}
	if projected == nil {
		t.Fatal("orderexport.QueryOrder returned nil for the seeded order")
	}
	wire := bil24wire.EncodeOrder(*projected, bil24wire.EncodeContext{})
	if wire.TotalSum != order.TotalSum {
		t.Errorf("bil24wire totalSum = %v, MACS totalSum = %v — the two partner payloads describe ONE sale and must carry the same figure",
			wire.TotalSum, order.TotalSum)
	}
	if wire.Sum != order.Sum {
		t.Errorf("bil24wire sum = %v, MACS sum = %v", wire.Sum, order.Sum)
	}
	if wire.Discount != order.Discount {
		t.Errorf("bil24wire discount = %v, MACS discount = %v", wire.Discount, order.Discount)
	}
	if len(wire.TicketList) == 1 && wire.TicketList[0].Price != tk.Price {
		t.Errorf("bil24wire ticket price = %v, MACS ticket price = %v", wire.TicketList[0].Price, tk.Price)
	}

	t.Logf("W1-M2 money OK: order=%s sum=%v charge(order)=%v totalSum=%v; bil24wire agrees",
		orderID, order.Sum, order.Charge, order.TotalSum)
}

// m529AtMostTwoDecimals reports whether v survives a round trip through whole
// minor units — i.e. it carries no third decimal place. The round trip goes
// through the money package rather than an open-coded ×100 both because that
// is the #528 guardrail's rule and because it is the same code path the
// encoder uses: if Major∘Minor is not the identity on a value, the wire cannot
// represent it faithfully either.
func m529AtMostTwoDecimals(v float64) bool {
	return math.Abs(money.Major(money.Minor(v))-v) < 1e-9
}
