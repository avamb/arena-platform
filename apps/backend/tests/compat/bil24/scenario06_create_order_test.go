//go:build integration

// scenario06_create_order_test.go — spec §15.3 scenario 6, "one open order
// rule" (feature #492, W1-B1b, spec §7.7).
//
// The scenario drives CREATE_ORDER_EXT over the REAL server against a live
// database: real chi router, real hbil24 handler, real
// internal/platform/ordering aggregate writer, real pricing. Nothing below the
// HTTP boundary is stubbed.
//
// What it proves, in the order a WordPress checkout actually performs it:
//
//	basic             one held CZK 500 seat → a pending_payment order,
//	                  external_ref = the site's numeric orderId, 5% channel
//	                  charge, source bil24_gateway
//	string_orderid    a repeat with a STRING orderId returns THE SAME order id
//	                  with a refreshed external_ref — never a second order
//	repeat_same_order the same again, which is the §7.7 step-5 invariant in
//	                  its plainest form
//	seated            a second held seat re-prices the SAME order to 2 units
//	promo             the request's promoCodes apply, and the charge is taken
//	                  on the NET, not the gross
//	ga                a different (general-admission, EUR) session gets its own
//	                  order, and its cart is created by the preflight from
//	                  `lines` alone — no prior RESERVATION
//	errors            empty lines → -2, empty orderId → -2, and a categoryPrice
//	                  belonging to another session → 101 bil24.line_wrong_session
//
// Money is asserted explicitly because assertGoldenKeySet compares KEY SETS
// only: a golden can never catch a pricing regression on its own.
package compat_bil24_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// sc6Money is one expected CREATE_ORDER_EXT money snapshot. The invariant
// totalSum = sum - discount + charge is checked separately, because the orders
// table enforces exactly that CHECK and a response that violates it would mean
// the wire and the aggregate disagree.
type sc6Money struct {
	sum      float64
	discount float64
	charge   float64
	total    float64
	currency string
}

// runScenario06CreateOrder is the body of the 06_one_open_order_rule sub-test.
func runScenario06CreateOrder(t *testing.T, st *harnessState) {
	t.Helper()
	ctx := context.Background()

	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 2 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 2", len(labels))
	}

	// Spec §4: categoryPriceId travels as the int64 compatibility id of the
	// ticket tier, never as the platform UUID — resolveCategoryPriceID rejects
	// a UUID with -2 once compatDB is wired. resolveGolden's fallback for the
	// placeholder IS the UUID, so the scenario has to mint and override it.
	assignedTierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user := createGatewayUser(t, base, st, "harness-492@example.test")
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(actionEventID, 10),
		"categoryPriceId": strconv.FormatInt(assignedTierWireID, 10),
		"sessionId":       sess,
	}

	// post loads a checked-in fixture, stamps the live credentials on it and
	// sends it, returning the decoded response and the resolved golden. `over`
	// lets the GA case retarget actionEventId / categoryPriceId at the
	// general-admission session without a second fixture dialect.
	post := func(name string, over map[string]string) (map[string]interface{}, map[string]interface{}) {
		t.Helper()
		rt := runtime
		if over != nil {
			rt = map[string]string{}
			for k, v := range runtime {
				rt[k] = v
			}
			for k, v := range over {
				rt[k] = v
			}
		}
		req, gld := loadWPFixture(t, "CREATE_ORDER_EXT", name)
		req = resolveGolden(req, st, rt)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		return postBil24(t, base, req), resolveGolden(gld, st, rt)
	}

	reserveSeat := func(label string) {
		t.Helper()
		resp := postBil24(t, base, map[string]any{
			"command":       "RESERVATION",
			"fid":           st.ChannelFID,
			"token":         st.ChannelToken,
			"locale":        "ru-RU",
			"type":          "RESERVE",
			"userId":        user,
			"sessionId":     sess,
			"actionEventId": actionEventID,
			"seatList":      []any{map[string]any{"seatId": st.SeatIDs[label]}},
		})
		if code := numberField(t, resp, "resultCode"); code != 0 {
			t.Fatalf("RESERVE %s resultCode = %v, want 0 (description %v)",
				label, code, resp["description"])
		}
	}

	// ── step 1: one held seat → the first order ─────────────────────────────
	reserveSeat(labels[0])

	basic, gldBasic := post("basic", nil)
	if code := numberField(t, basic, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT basic resultCode = %v, want 0 (description %v)",
			code, basic["description"])
	}
	assertGoldenKeySet(t, basic, gldBasic)
	// Seeded money: one CZK 500 seat on a 5% channel, no promo.
	sc6AssertMoney(t, "basic", basic, sc6Money{sum: 500, discount: 0, charge: 25, total: 525, currency: "CZK"})

	orderID := sc6OrderID(t, "basic", basic)
	if got, _ := basic["externalOrderId"].(string); got != "1001" {
		t.Errorf("basic externalOrderId = %q, want \"1001\" — the site's numeric "+
			"orderId travels back as a string", got)
	}
	sc6AssertExpiration(t, "basic", basic)

	// The aggregate side of §7.7 step 7.
	sc6AssertOrderRow(t, st, orderID, "1001", sc6Money{
		sum: 500, discount: 0, charge: 25, total: 525, currency: "CZK",
	})
	// §7.7 step 8: the client's own arithmetic is advisory and lives ONLY in
	// the created event's payload, never in an orders column.
	sc6AssertClientReported(t, st, orderID, 500)

	// ── step 2: a repeat with a STRING orderId updates the SAME order ───────
	str, gldStr := post("string_orderid", nil)
	if code := numberField(t, str, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT string_orderid resultCode = %v, want 0 (description %v)",
			code, str["description"])
	}
	assertGoldenKeySet(t, str, gldStr)
	if got := sc6OrderID(t, "string_orderid", str); got != orderID {
		t.Fatalf("string_orderid orderId = %s, want the SAME order %s — §7.7 step 5 "+
			"forbids a second open order for one buyer and session", got, orderID)
	}
	if got, _ := str["externalOrderId"].(string); got != "wc_string_2002" {
		t.Errorf("string_orderid externalOrderId = %q, want \"wc_string_2002\"", got)
	}
	sc6AssertMoney(t, "string_orderid", str, sc6Money{sum: 500, discount: 0, charge: 25, total: 525, currency: "CZK"})
	// external_ref must have been re-pointed at the latest WooCommerce order.
	sc6AssertOrderRow(t, st, orderID, "wc_string_2002", sc6Money{
		sum: 500, discount: 0, charge: 25, total: 525, currency: "CZK",
	})

	// ── step 3: the invariant in its plainest form ──────────────────────────
	repeat, gldRepeat := post("repeat_same_order", nil)
	if code := numberField(t, repeat, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT repeat_same_order resultCode = %v, want 0 (description %v)",
			code, repeat["description"])
	}
	assertGoldenKeySet(t, repeat, gldRepeat)
	if got := sc6OrderID(t, "repeat_same_order", repeat); got != orderID {
		t.Fatalf("repeat_same_order orderId = %s, want %s", got, orderID)
	}
	sc6AssertOpenOrderCount(t, st, 1)

	// ── step 4: a second held seat re-prices the same order ─────────────────
	reserveSeat(labels[1])

	seated, gldSeated := post("seated", nil)
	if code := numberField(t, seated, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT seated resultCode = %v, want 0 (description %v)",
			code, seated["description"])
	}
	assertGoldenKeySet(t, seated, gldSeated)
	if got := sc6OrderID(t, "seated", seated); got != orderID {
		t.Fatalf("seated orderId = %s, want the same %s", got, orderID)
	}
	sc6AssertMoney(t, "seated", seated, sc6Money{sum: 1000, discount: 0, charge: 50, total: 1050, currency: "CZK"})
	// A rewrite replaces the per-unit lines wholesale, so the item count must
	// track the cart and never accumulate.
	sc6AssertItemCount(t, st, orderID, 2)

	// ── step 5: the request's promoCodes discount, and the charge is NET ────
	promo, gldPromo := post("promo", nil)
	if code := numberField(t, promo, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT promo resultCode = %v, want 0 (description %v)",
			code, promo["description"])
	}
	assertGoldenKeySet(t, promo, gldPromo)
	if got := sc6OrderID(t, "promo", promo); got != orderID {
		t.Fatalf("promo orderId = %s, want the same %s", got, orderID)
	}
	// WAVE1 is 10% of 1000 = 100; the 5% channel charge is taken on the NET
	// 900 = 45, so totalSum is 1000 - 100 + 45.
	sc6AssertMoney(t, "promo", promo, sc6Money{sum: 1000, discount: 100, charge: 45, total: 945, currency: "CZK"})
	sc6AssertItemCount(t, st, orderID, 2)
	sc6AssertOpenOrderCount(t, st, 1)

	// ── step 6: a general-admission session gets its OWN order ──────────────
	gaSessionID, err := uuid.Parse(st.GAsessID)
	if err != nil {
		t.Fatalf("parse st.GAsessID: %v", err)
	}
	var gaTierID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`SELECT id FROM ticket_tiers WHERE session_id=$1 AND name='Early Bird'`, gaSessionID,
	).Scan(&gaTierID); err != nil {
		t.Fatalf("resolve GA 'Early Bird' tier: %v", err)
	}
	gaTierWireID := sc6TierWireID(t, st, gaTierID.String())
	gaOver := map[string]string{
		"actionEventId":   strconv.FormatInt(mustActionEventID(t, st, st.GAsessID), 10),
		"categoryPriceId": strconv.FormatInt(gaTierWireID, 10),
	}

	ga, gldGA := post("ga", gaOver)
	if code := numberField(t, ga, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT ga resultCode = %v, want 0 (description %v)",
			code, ga["description"])
	}
	assertGoldenKeySet(t, ga, gldGA)
	gaOrderID := sc6OrderID(t, "ga", ga)
	if gaOrderID == orderID {
		t.Fatal("the GA order reused the seated session's order; one order is one SESSION")
	}
	// 2 × EUR 900 Early Bird on the same 5% channel.
	sc6AssertMoney(t, "ga", ga, sc6Money{sum: 1800, discount: 0, charge: 90, total: 1890, currency: "EUR"})
	// §7.7 step 2: no RESERVATION preceded this — the preflight created the
	// cart from `lines` alone.
	sc6AssertItemCount(t, st, gaOrderID, 2)
	sc6AssertOrderRow(t, st, gaOrderID, "wc_ga_2005", sc6Money{
		sum: 1800, discount: 0, charge: 90, total: 1890, currency: "EUR",
	})
	sc6AssertOpenOrderCount(t, st, 2)

	// ── step 7: the refusals ───────────────────────────────────────────────
	//
	// An empty composition is -2, not a zero-total order: the gateway must not
	// mint an order the site cannot pay for.
	reqEmptyLines, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	reqEmptyLines = resolveGolden(reqEmptyLines, st, runtime)
	reqEmptyLines["fid"] = st.ChannelFID
	reqEmptyLines["token"] = st.ChannelToken
	reqEmptyLines["userId"] = user
	reqEmptyLines["lines"] = []any{}
	if code := numberField(t, postBil24(t, base, reqEmptyLines), "resultCode"); code != -2 {
		t.Errorf("CREATE_ORDER_EXT with empty lines resultCode = %v, want -2", code)
	}

	reqNoOrderID, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	reqNoOrderID = resolveGolden(reqNoOrderID, st, runtime)
	reqNoOrderID["fid"] = st.ChannelFID
	reqNoOrderID["token"] = st.ChannelToken
	reqNoOrderID["userId"] = user
	reqNoOrderID["orderId"] = ""
	if code := numberField(t, postBil24(t, base, reqNoOrderID), "resultCode"); code != -2 {
		t.Errorf("CREATE_ORDER_EXT with empty orderId resultCode = %v, want -2", code)
	}

	// A categoryPrice of ANOTHER session is a user-visible business error, not
	// a protocol error: the site renders the description verbatim.
	reqWrongSession, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	reqWrongSession = resolveGolden(reqWrongSession, st, runtime)
	reqWrongSession["fid"] = st.ChannelFID
	reqWrongSession["token"] = st.ChannelToken
	reqWrongSession["userId"] = user
	reqWrongSession["lines"] = []any{map[string]any{
		"categoryPriceId": strconv.FormatInt(gaTierWireID, 10),
		"quantity":        1,
		"tariffPlanId":    nil,
	}}
	wrong := postBil24(t, base, reqWrongSession)
	if code := numberField(t, wrong, "resultCode"); code != 101 {
		t.Fatalf("CREATE_ORDER_EXT with a foreign categoryPriceId resultCode = %v, "+
			"want 101 bil24.line_wrong_session (description %v)", code, wrong["description"])
	}
	if desc, _ := wrong["description"].(string); desc == "" {
		t.Error("line_wrong_session carries an empty description; §7.7 renders it verbatim")
	}
	// The refusal must not have mutated anything.
	sc6AssertOpenOrderCount(t, st, 2)
}

// ─────────────────────────────────────────────────────────────────────────────
// assertions
// ─────────────────────────────────────────────────────────────────────────────

// sc6TierWireID mints (or reads) the spec §4 int64 wire id for a ticket-tier
// UUID, which is what `lines[].categoryPriceId` must carry.
func sc6TierWireID(t *testing.T, st *harnessState, tierUUID string) int64 {
	t.Helper()
	id, err := uuid.Parse(tierUUID)
	if err != nil {
		t.Fatalf("parse tier uuid %q: %v", tierUUID, err)
	}
	wireID, err := compatids.Ensure(context.Background(), st.Pool, compatids.KindCategoryPrice, id)
	if err != nil {
		t.Fatalf("compatids.Ensure(category_price, %s): %v", tierUUID, err)
	}
	return wireID
}

func sc6AssertMoney(t *testing.T, label string, resp map[string]interface{}, want sc6Money) {
	t.Helper()
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"sum", want.sum},
		{"discount", want.discount},
		{"charge", want.charge},
		{"totalSum", want.total},
	} {
		if got := numberField(t, resp, tc.key); got != tc.want {
			t.Errorf("CREATE_ORDER_EXT %s: %s = %v, want %v", label, tc.key, got, tc.want)
		}
	}
	if got, _ := resp["currency"].(string); got != want.currency {
		t.Errorf("CREATE_ORDER_EXT %s: currency = %q, want %q", label, got, want.currency)
	}
	// orders enforces total = subtotal - discount + charge; the wire must agree.
	sum := numberField(t, resp, "sum")
	disc := numberField(t, resp, "discount")
	chg := numberField(t, resp, "charge")
	if got := numberField(t, resp, "totalSum"); got != sum-disc+chg {
		t.Errorf("CREATE_ORDER_EXT %s: totalSum %v != sum %v - discount %v + charge %v",
			label, got, sum, disc, chg)
	}
}

// sc6OrderID reads the response orderId as the platform UUID it must be.
func sc6OrderID(t *testing.T, label string, resp map[string]interface{}) uuid.UUID {
	t.Helper()
	raw, ok := resp["orderId"].(string)
	if !ok || raw == "" {
		t.Fatalf("CREATE_ORDER_EXT %s: orderId = %#v, want a non-empty string", label, resp["orderId"])
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("CREATE_ORDER_EXT %s: orderId %q is not a platform uuid: %v", label, raw, err)
	}
	return id
}

// sc6AssertExpiration checks the §7.7 expiration is an RFC3339 timestamp in the
// future — it is what the WooCommerce plugin renders as the hold countdown, so
// a bare duration or a past instant would silently break the checkout timer.
func sc6AssertExpiration(t *testing.T, label string, resp map[string]interface{}) {
	t.Helper()
	raw, ok := resp["expiration"].(string)
	if !ok || raw == "" {
		t.Errorf("CREATE_ORDER_EXT %s: expiration = %#v, want an RFC3339 string", label, resp["expiration"])
		return
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Errorf("CREATE_ORDER_EXT %s: expiration %q is not RFC3339: %v", label, raw, err)
		return
	}
	if !ts.After(time.Now()) {
		t.Errorf("CREATE_ORDER_EXT %s: expiration %q is not in the future", label, raw)
	}
}

// sc6AssertOrderRow pins the persisted aggregate: spec §7.7 step 7 requires
// status pending_payment, source bil24_gateway and external_ref = the site's
// orderId, plus the same money the wire reported.
func sc6AssertOrderRow(t *testing.T, st *harnessState, orderID uuid.UUID, wantExternalRef string, want sc6Money) {
	t.Helper()
	var (
		status, source, currency string
		externalRef              *string
		subtotal, discount       int64
		charge, total            int64
		chargePercentBP          int32
	)
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT status, source, currency, external_ref, subtotal, discount, charge, total, charge_percent_bp
		 FROM orders WHERE id=$1`, orderID,
	).Scan(&status, &source, &currency, &externalRef, &subtotal, &discount, &charge, &total, &chargePercentBP); err != nil {
		t.Fatalf("read order %s: %v", orderID, err)
	}
	if status != "pending_payment" {
		t.Errorf("orders.status = %q, want pending_payment", status)
	}
	if source != "bil24_gateway" {
		t.Errorf("orders.source = %q, want bil24_gateway", source)
	}
	if externalRef == nil || *externalRef != wantExternalRef {
		t.Errorf("orders.external_ref = %v, want %q", externalRef, wantExternalRef)
	}
	if currency != want.currency {
		t.Errorf("orders.currency = %q, want %q", currency, want.currency)
	}
	for _, tc := range []struct {
		name string
		got  int64
		want float64
	}{
		{"subtotal", subtotal, want.sum},
		{"discount", discount, want.discount},
		{"charge", charge, want.charge},
		{"total", total, want.total},
	} {
		if float64(tc.got) != tc.want {
			t.Errorf("orders.%s = %d, want %v", tc.name, tc.got, tc.want)
		}
	}
	// The channel's own 5% is authoritative; the client's chargePercent is not.
	if chargePercentBP != 500 {
		t.Errorf("orders.charge_percent_bp = %d, want 500 (the channel's 5%%, not the client's claim)",
			chargePercentBP)
	}
}

// sc6AssertClientReported proves §7.7 step 8: the site's numbers are recorded
// under order_events.created.payload.client_reported and nowhere else.
func sc6AssertClientReported(t *testing.T, st *harnessState, orderID uuid.UUID, wantTotal float64) {
	t.Helper()
	var raw []byte
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT payload FROM order_events
		 WHERE order_id=$1 AND type='created'
		 ORDER BY created_at DESC LIMIT 1`, orderID,
	).Scan(&raw); err != nil {
		t.Fatalf("read order_events.created for %s: %v", orderID, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse order_events payload %s: %v", raw, err)
	}
	reported, ok := payload["client_reported"].(map[string]interface{})
	if !ok {
		t.Fatalf("order_events.created payload has no client_reported object: %s", raw)
	}
	total, ok := reported["total"].(float64)
	if !ok || total != wantTotal {
		t.Errorf("client_reported.total = %#v, want %v", reported["total"], wantTotal)
	}
	if _, ok := reported["charge_percent"]; !ok {
		t.Errorf("client_reported has no charge_percent: %s", raw)
	}
}

// sc6AssertItemCount pins the per-unit rewrite: an update replaces the lines
// wholesale, so a shrunk or grown cart must be reflected exactly.
func sc6AssertItemCount(t *testing.T, st *harnessState, orderID uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_items WHERE order_id=$1`, orderID).Scan(&got); err != nil {
		t.Fatalf("count order_items for %s: %v", orderID, err)
	}
	if got != want {
		t.Errorf("order_items for %s = %d, want %d", orderID, got, want)
	}
}

// sc6AssertOpenOrderCount counts the org's pending_payment orders. The whole
// point of §7.7 step 5 is that repeat checkouts do not accumulate them.
func sc6AssertOpenOrderCount(t *testing.T, st *harnessState, want int) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE org_id=$1::uuid AND status='pending_payment'`,
		st.OrgID).Scan(&got); err != nil {
		t.Fatalf("count open orders: %v", err)
	}
	if got != want {
		t.Errorf("pending_payment orders = %d, want %d — §7.7 step 5 forbids duplicates", got, want)
	}
}

// TestCompatBil24_492_CreateOrderGoldensExist keeps the checked-in §7.7
// fixtures honest: every case named by the spec must be present, must carry the
// full response key set, and must satisfy the orders CHECK arithmetic. A golden
// that violates totalSum = sum - discount + charge could never be produced by
// the aggregate, so it would be a permanently red target.
func TestCompatBil24_492_CreateOrderGoldensExist(t *testing.T) {
	want := []string{"basic", "string_orderid", "ga", "seated", "promo", "repeat_same_order"}
	keys := []string{
		"resultCode", "description", "command",
		"orderId", "externalOrderId", "sum", "discount", "charge", "totalSum",
		"currency", "expiration",
	}
	for _, name := range want {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "CREATE_ORDER_EXT", name+".json"))
		for _, k := range keys {
			if _, ok := gld[k]; !ok {
				t.Errorf("golden CREATE_ORDER_EXT/%s.json has no %s", name, k)
			}
		}
		sum, _ := gld["sum"].(float64)
		disc, _ := gld["discount"].(float64)
		chg, _ := gld["charge"].(float64)
		tot, _ := gld["totalSum"].(float64)
		if tot != sum-disc+chg {
			t.Errorf("golden CREATE_ORDER_EXT/%s.json: totalSum %v != sum %v - discount %v + charge %v",
				name, tot, sum, disc, chg)
		}

		req := mustReadJSON(t, filepath.Join("testdata", "wp", "requests", "CREATE_ORDER_EXT", name+".json"))
		lines, ok := req["lines"].([]interface{})
		if !ok || len(lines) == 0 {
			t.Errorf("request CREATE_ORDER_EXT/%s.json has no lines; §7.7 makes an empty "+
				"composition a -2", name)
			continue
		}
		// Every line must address the tier through the placeholder, never a
		// hard-coded legacy id: the seeded compat ids are minted per run.
		for i, l := range lines {
			row, _ := l.(map[string]interface{})
			if cp, _ := row["categoryPriceId"].(string); cp != "{{categoryPriceId}}" {
				t.Errorf("request CREATE_ORDER_EXT/%s.json lines[%d].categoryPriceId = %#v, "+
					"want the {{categoryPriceId}} placeholder", name, i, row["categoryPriceId"])
			}
		}
	}
}
