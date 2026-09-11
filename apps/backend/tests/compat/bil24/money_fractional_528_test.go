//go:build integration

// money_fractional_528_test.go — feature #528 (W1-M1), spec
// 08_architecture/20_bil24_gateway_money_units_spec_ru.md §5.
//
// Every other money assertion in this harness happens to land on a whole
// number of major units: the seeded CZK 500 seat and the EUR 900 tier both
// divide by 100 without a remainder, so a gateway that emitted MINOR units
// scaled by the wrong factor would still be caught, but a gateway that
// emitted major units with the WRONG ROUNDING would not be. Neither would a
// JSON encoder that rendered the number as "18.90" or "18.899999999999999".
//
// This scenario is the fractional case spec 20 §5 pins by name. One
// general-admission unit of a tier priced at 1890 MINOR units (EUR 18.90),
// through the harness channel's 5 % charge:
//
//	sum      1890 minor                      → 18.9  on the wire
//	charge   5 % of 1890 = 94.5 → 95 minor   →  0.95 on the wire
//	totalSum 1890 + 95 = 1985 minor          → 19.85 on the wire
//
// The charge is the whole point: 94.5 minor units do not exist, so the fee
// has to round (half away from zero, money.RoundMinor) and the identity
// sum − discount + charge == totalSum must still hold EXACTLY on the wire.
// A float pipeline that computed the charge in major units would answer
// 0.9450000000000001 and a totalSum that does not add up.
//
// The PAY_ORDER half pins spec §7.9 step 3 restated by spec 20 §2.4: the
// ±1 MINOR unit tolerance. WooCommerce's own cart rounds independently of
// ours, so 19.84 and 19.86 are the same payment as 19.85 and must record
// nothing; 19.9 is five minor units out, which is a real disagreement and
// must land in order_events.amount_mismatch — recorded, never refused,
// because the buyer's card has already been charged.
package compat_bil24_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
)

// The seeded fractional tier and everything derived from it, in the two unit
// systems spec 20 distinguishes: DB/domain minor, wire major.
const (
	fx528PriceMinor  = int64(1890) // ticket_tiers.price_amount
	fx528PriceMajor  = 18.9        // sum / price on the wire
	fx528ChargeMinor = int64(95)   // 5 % of 1890 = 94.5, rounded half away from zero
	fx528ChargeMajor = 0.95
	fx528TotalMinor  = int64(1985)
	fx528TotalMajor  = 19.85
	fx528Currency    = "EUR"
)

// TestCompatBil24_528_FractionalMoneyOnTheWire is the spec 20 §5 golden case.
func TestCompatBil24_528_FractionalMoneyOnTheWire(t *testing.T) {
	st := setupHarness(t)
	// Booted first: startHarnessServer registers the wire-row sweep with
	// t.Cleanup, and t.Cleanup is LIFO, so the orders this test creates are
	// torn down before the seed's own fixture teardown runs.
	base := startHarnessServer(t, st)

	tierID := fx528SeedTier(t, st)
	tierWireID := sc6TierWireID(t, st, tierID)
	actionEventID := mustActionEventID(t, st, st.GAsessID)

	// payment_intents.provider_payment_id ('wc:<external_ref>:<method>') is
	// covered by a GLOBAL unique index, unscoped by org, so every shop order
	// number carries a per-run tag: a literal would collide with a leftover
	// row from an interrupted run against the shared dev-stand.
	runTag := uuid.NewString()[:8]

	// ── the wire arithmetic, asserted on a live RESERVATION ─────────────────
	//
	// The first buyer's hold is also the one whose RAW response body is
	// inspected, because the decoded float64 map cannot tell 18.9 from
	// "18.90": both unmarshal to the same number.
	sess, user := createGatewayUser(t, base, st, "harness-528-money@example.test")
	reserved, rawBody := fx528Reserve(t, base, st, sess, user, actionEventID, tierWireID)
	if code := numberField(t, reserved, "resultCode"); code != 0 {
		t.Fatalf("RESERVATION resultCode = %v, want 0 (description %v)",
			code, reserved["description"])
	}
	fx528AssertMoney(t, "RESERVATION", reserved, map[string]float64{
		"sum":      fx528PriceMajor,
		"discount": 0,
		"charge":   fx528ChargeMajor,
		"totalSum": fx528TotalMajor,
	})
	if got, _ := reserved["currency"].(string); got != fx528Currency {
		t.Errorf("RESERVATION currency = %q, want %q", got, fx528Currency)
	}
	// The identity spec 20 §5 requires to hold on the WIRE, not merely in the
	// database: a shop that re-adds our own numbers must arrive at our total.
	// It is checked in MINOR units on purpose — 18.9 + 0.95 is
	// 19.849999999999998 in binary floating point, so a major-unit comparison
	// would fail on correct numbers, which is exactly why the gateway does its
	// own arithmetic in integers.
	if sum, discount, charge, total := money.Minor(numberField(t, reserved, "sum")),
		money.Minor(numberField(t, reserved, "discount")),
		money.Minor(numberField(t, reserved, "charge")),
		money.Minor(numberField(t, reserved, "totalSum")); sum-discount+charge != total {
		t.Errorf("RESERVATION: sum %d − discount %d + charge %d = %d minor units, but "+
			"totalSum = %d — the shop re-adds these figures and must reach our total",
			sum, discount, charge, sum-discount+charge, total)
	}
	// The per-seat row carries the unit price, and it is fractional too.
	fx528AssertSeatPrice(t, reserved)
	// Spec 20 §2.1: ≤2 decimals, no trailing zero, no float dust.
	fx528AssertRawJSON(t, rawBody)

	// ── PAY_ORDER: the ±1 minor unit tolerance of spec 20 §2.4 ──────────────
	//
	// Each case needs its OWN order: a paid order short-circuits to an
	// idempotent 0 before the amount check ever runs (cmd_order_pay.go step 1),
	// so replaying different amounts against one order would assert nothing.
	for _, tc := range []struct {
		// name doubles as the sub-test name and the buyer's address.
		name string
		// amount is what the shop reports it charged, in major units.
		amount float64
		// mismatch is whether the figure is further than one minor unit from
		// the order total and must therefore be recorded.
		mismatch bool
	}{
		// the figure the gateway itself emitted
		{"exact", 19.85, false},
		// one minor unit of rounding drift in the shop's own cart
		{"one_minor_low", 19.84, false},
		// one minor unit of drift the other way
		{"one_minor_high", 19.86, false},
		// five minor units out — a real disagreement
		{"five_minor_high", 19.90, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buyer := "harness-528-" + tc.name + "@example.test"
			bSess, bUser := createGatewayUser(t, base, st, buyer)
			if r, _ := fx528Reserve(t, base, st, bSess, bUser, actionEventID, tierWireID); numberField(t, r, "resultCode") != 0 {
				t.Fatalf("%s: RESERVATION resultCode = %v (description %v)",
					tc.name, r["resultCode"], r["description"])
			}

			externalRef := "wc-528-" + tc.name + "-" + runTag
			created := postBil24(t, base, map[string]any{
				"command":         "CREATE_ORDER_EXT",
				"fid":             st.ChannelFID,
				"token":           st.ChannelToken,
				"locale":          "ru-RU",
				"orderId":         externalRef,
				"userId":          bUser,
				"sessionId":       bSess,
				"currency":        fx528Currency,
				"total":           fx528TotalMajor,
				"actionEventId":   actionEventID,
				"longReservation": false,
				"lines": []any{map[string]any{
					"categoryPriceId": tierWireID,
					"quantity":        1,
					"tariffPlanId":    nil,
				}},
				"email":         "buyer@example.com",
				"phone":         "+420123456789",
				"fullName":      "Anna Novak",
				"chargePercent": 5,
				"promoCodes":    []any{},
			})
			if code := numberField(t, created, "resultCode"); code != 0 {
				t.Fatalf("%s: CREATE_ORDER_EXT resultCode = %v (description %v)",
					tc.name, code, created["description"])
			}
			// The order response is the same fractional arithmetic, so a
			// regression that only touched the cart projection is still caught.
			fx528AssertMoney(t, "CREATE_ORDER_EXT "+tc.name, created, map[string]float64{
				"sum":      fx528PriceMajor,
				"discount": 0,
				"charge":   fx528ChargeMajor,
				"totalSum": fx528TotalMajor,
			})
			orderID := sc6OrderID(t, tc.name, created)
			// The database side of the same money, in MINOR units: this is the
			// half of spec 20 §2.2 no wire assertion can see.
			fx528AssertOrderRow(t, st, orderID)

			paid := postBil24(t, base, map[string]any{
				"command":   "PAY_ORDER",
				"fid":       st.ChannelFID,
				"token":     st.ChannelToken,
				"locale":    "ru-RU",
				"orderId":   orderID.String(),
				"userId":    bUser,
				"sessionId": bSess,
				"amount":    tc.amount,
				"currency":  fx528Currency,
				"method":    "woo_bank_card",
			})
			// Spec §7.9: the money has already moved, so even a real
			// disagreement is recorded and waved through — never refused.
			if code := numberField(t, paid, "resultCode"); code != 0 {
				t.Fatalf("%s: PAY_ORDER amount %v resultCode = %v, want 0 — a reported amount "+
					"is evidence for reconciliation, never a refusal (description %v)",
					tc.name, tc.amount, code, paid["description"])
			}
			sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")

			if tc.mismatch {
				fx528AssertMismatchEvent(t, st, orderID, tc.amount)
			} else {
				sc5AssertNoOrderEvent(t, st, orderID, "amount_mismatch")
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// fixture
// ─────────────────────────────────────────────────────────────────────────────

// fx528SeedTier adds the fractional tier to the seeded GA session and returns
// its uuid. The GA session's session-level inventory_ledger row (NULL tier) is
// what a categoryList hold consumes, so no per-tier ledger row is needed — the
// Early Bird tier scenario 2 reserves against has none either. The seed's own
// teardown deletes ticket_tiers by session, so this row needs no cleanup of its
// own.
func fx528SeedTier(t *testing.T, st *harnessState) string {
	t.Helper()
	gaSessionID, err := uuid.Parse(st.GAsessID)
	if err != nil {
		t.Fatalf("parse GA session uuid %q: %v", st.GAsessID, err)
	}
	var tierID uuid.UUID
	if err := st.Pool.QueryRow(context.Background(),
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1, 'Fractional 528', 'fixed', $2, $3, 9)
		 RETURNING id`,
		gaSessionID, fx528PriceMinor, fx528Currency,
	).Scan(&tierID); err != nil {
		t.Fatalf("seed the fractional tier on GA session %s: %v", st.GAsessID, err)
	}
	return tierID.String()
}

// fx528Reserve holds ONE general-admission unit of the fractional tier and
// returns both the decoded response and the raw body bytes — the raw form is
// the only place the JSON RENDERING of a number survives.
func fx528Reserve(
	t *testing.T,
	base string,
	st *harnessState,
	sess string,
	user float64,
	actionEventID, tierWireID int64,
) (map[string]interface{}, []byte) {
	t.Helper()
	return postBil24Raw(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventID,
		"categoryList": []any{map[string]any{
			"categoryPriceId": tierWireID,
			"quantity":        1,
			"tariffPlanId":    nil,
		}},
	})
}

// postBil24Raw is postBil24 that also hands back the untouched response bytes.
// postBil24 itself cannot: json.Unmarshal into float64 erases the difference
// between 18.9, 18.90 and 18.899999999999999, which is exactly the drift spec
// 20 §2.1 forbids.
func postBil24Raw(t *testing.T, base string, body map[string]any) (map[string]interface{}, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(base+"/compat/bil24/json", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /compat/bil24/json: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /compat/bil24/json: status %d, body %s", resp.StatusCode, payload)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("parse response %s: %v", payload, err)
	}
	return out, payload
}

// ─────────────────────────────────────────────────────────────────────────────
// assertions
// ─────────────────────────────────────────────────────────────────────────────

func fx528AssertMoney(t *testing.T, label string, resp map[string]interface{}, want map[string]float64) {
	t.Helper()
	for key, wantVal := range want {
		if got := numberField(t, resp, key); got != wantVal {
			t.Errorf("%s: %s = %v, want %v (spec 20 §5)", label, key, got, wantVal)
		}
	}
}

// fx528AssertSeatPrice checks the per-row unit price. assertGoldenKeySet never
// recurses into arrays, so a row-level money regression is only ever caught
// here.
func fx528AssertSeatPrice(t *testing.T, resp map[string]interface{}) {
	t.Helper()
	rows, ok := resp["seatList"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("RESERVATION seatList = %#v, want exactly one row", resp["seatList"])
	}
	row, ok := rows[0].(map[string]interface{})
	if !ok {
		t.Fatalf("RESERVATION seatList[0] = %#v, want an object", rows[0])
	}
	if got := numberField(t, row, "price"); got != fx528PriceMajor {
		t.Errorf("RESERVATION seatList[0].price = %v, want %v — the row price is major "+
			"units too, not the %d minor units the database stores",
			got, fx528PriceMajor, fx528PriceMinor)
	}
}

// fx528AssertRawJSON enforces the spec 20 §2.1 rendering rules on the bytes the
// WordPress plugin actually parses: a JSON number, at most two decimals, no
// trailing zero and no float dust. PHP's json_decode would swallow all three
// forms, but the plugin prints the raw value into the buyer's cart.
func fx528AssertRawJSON(t *testing.T, body []byte) {
	t.Helper()
	doc := string(body)
	for _, want := range []string{
		`"sum":18.9`,
		`"charge":0.95`,
		`"totalSum":19.85`,
		`"price":18.9`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("RESERVATION body does not render %s\n---\n%s", want, doc)
		}
	}
	// The three shapes spec 20 §2.1 rules out by name. A quoted "18.9" would
	// be a string, 18.90 a trailing zero, 18.899999999999999 float dust.
	for _, bad := range []string{
		`"sum":"`, `"totalSum":"`, `"charge":"`,
		`18.90`, `18.899`, `0.9450`, `0.94500`, `19.8500`,
	} {
		if strings.Contains(doc, bad) {
			t.Errorf("RESERVATION body contains %s — spec 20 §2.1 requires a plain JSON "+
				"number with at most two decimals and no trailing zeros\n---\n%s", bad, doc)
		}
	}
}

// fx528AssertOrderRow pins the database side: orders.* stay in MINOR units no
// matter what the wire said (spec 20 §2.2). The wire-only half of the rule is
// what makes a stray conversion inside the ordering aggregate invisible to
// every wire assertion above.
func fx528AssertOrderRow(t *testing.T, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	var subtotal, discount, charge, total int64
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT subtotal, discount, charge, total FROM orders WHERE id=$1`, orderID,
	).Scan(&subtotal, &discount, &charge, &total); err != nil {
		t.Fatalf("read orders row %s: %v", orderID, err)
	}
	for _, tc := range []struct {
		col  string
		got  int64
		want int64
	}{
		{"subtotal", subtotal, fx528PriceMinor},
		{"discount", discount, 0},
		{"charge", charge, fx528ChargeMinor},
		{"total", total, fx528TotalMinor},
	} {
		if tc.got != tc.want {
			t.Errorf("orders.%s = %d, want %d minor units", tc.col, tc.got, tc.want)
		}
	}
}

// fx528AssertMismatchEvent proves the §7.9 step 3 record exists AND carries
// both sides in minor units, which is what an operator reconciles against.
func fx528AssertMismatchEvent(t *testing.T, st *harnessState, orderID uuid.UUID, reportedMajor float64) {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT payload FROM order_events WHERE order_id=$1 AND type='amount_mismatch'
		 ORDER BY created_at DESC LIMIT 1`, orderID,
	).Scan(&raw); err != nil {
		t.Fatalf("order %s has no amount_mismatch event for a reported %v: %v",
			orderID, reportedMajor, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse amount_mismatch payload %s: %v", raw, err)
	}
	wantReportedMinor := float64(money.Minor(reportedMajor))
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"reported_amount", reportedMajor},
		{"reported_amount_minor", wantReportedMinor},
		{"expected_amount_minor", float64(fx528TotalMinor)},
	} {
		got, ok := payload[tc.key].(float64)
		if !ok {
			t.Errorf("amount_mismatch payload has no numeric %s: %s", tc.key, raw)
			continue
		}
		if got != tc.want {
			t.Errorf("amount_mismatch payload %s = %v, want %v", tc.key, got, tc.want)
		}
	}
	// order_events is the reconciliation trail, so the order's own total has
	// to be there in its native units as well.
	if got, ok := payload["order_total"].(float64); !ok || got != float64(fx528TotalMinor) {
		t.Errorf("amount_mismatch payload order_total = %#v, want %d minor units",
			payload["order_total"], fx528TotalMinor)
	}
}
