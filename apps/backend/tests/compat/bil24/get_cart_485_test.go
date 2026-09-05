//go:build integration

// get_cart_485_test.go — feature #485 (W1-A5c) live wire contract for GET_CART.
//
// The §15.3 harness proves the shape of GET_CART against the checked-in WP
// fixtures on a REAL server over the seeded database: no stubbed handler, no
// stubbed dispatcher. The walk is deliberately the one the WordPress plugin
// performs on a cart page — CREATE_USER, read the (still empty) cart, RESERVE
// one seat, read the cart again — because the empty-cart response is the shape
// the plugin sees most often and the one an implementation is most likely to
// get wrong by answering an error instead of a zeroed success.
//
// Scenario 02 of harness_test.go stays skipped: the GA purchase flow belongs to
// feature #495. This file only covers the §7.5 read.

package compat_bil24_test

import (
	"path/filepath"
	"strconv"
	"testing"
)

// getCartTopLevelKeys mirrors the golden envelopes; the assertion that matters
// is that totalSum is the ONLY total (spec §7.5).
var getCartTotalAliases = []string{"totalAmount", "estimatedTotal", "estimateTotal"}

func TestCompatBil24_485_GetCart_WireContract(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) == 0 {
		t.Fatal("seed produced no seats for the assigned-seats session")
	}
	seatID := st.SeatIDs[labels[0]]

	sess, user := createGatewayUser(t, base, st, "harness-485@example.test")
	runtime := map[string]string{
		"actionEventId": strconv.FormatInt(actionEventID, 10),
		"sessionId":     sess,
	}

	// ── step 1: the empty cart is a SUCCESS, not an error ────────────────────
	reqEmpty, gldEmpty := loadWPFixture(t, "GET_CART", "empty")
	reqEmpty = resolveGolden(reqEmpty, st, runtime)
	reqEmpty["fid"] = st.ChannelFID
	reqEmpty["token"] = st.ChannelToken
	reqEmpty["userId"] = user

	empty := postBil24(t, base, reqEmpty)
	if code := numberField(t, empty, "resultCode"); code != 0 {
		t.Fatalf("GET_CART on an empty cart resultCode = %v, want 0 (description %v)",
			code, empty["description"])
	}
	assertGoldenKeySet(t, empty, resolveGolden(gldEmpty, st, runtime))
	for _, key := range []string{"cartTimeout", "sum", "discountAmount", "chargeAmount", "totalSum"} {
		if got := numberField(t, empty, key); got != 0 {
			t.Errorf("empty cart %s = %v, want 0", key, got)
		}
	}
	if list, ok := empty["actionEventList"].([]interface{}); !ok || len(list) != 0 {
		t.Errorf("empty cart actionEventList = %#v, want an empty array", empty["actionEventList"])
	}

	// ── step 2: RESERVE one seat through the real §7.4 command ───────────────
	reserve := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventID,
		"seatList":      []any{map[string]any{"seatId": seatID}},
	})
	if code := numberField(t, reserve, "resultCode"); code != 0 {
		t.Fatalf("RESERVE resultCode = %v, want 0 (description %v)", code, reserve["description"])
	}

	// ── step 3: the cart now projects the §7.5 grouped shape ─────────────────
	reqBasic, gldBasic := loadWPFixture(t, "GET_CART", "basic")
	reqBasic = resolveGolden(reqBasic, st, runtime)
	reqBasic["fid"] = st.ChannelFID
	reqBasic["token"] = st.ChannelToken
	reqBasic["userId"] = user

	cart := postBil24(t, base, reqBasic)
	if code := numberField(t, cart, "resultCode"); code != 0 {
		t.Fatalf("GET_CART resultCode = %v, want 0 (description %v)", code, cart["description"])
	}
	assertGoldenKeySet(t, cart, resolveGolden(gldBasic, st, runtime))

	// Seeded money facts: one CZK 500 seat on a 5% channel.
	if got, want := cart["currency"], "CZK"; got != want {
		t.Errorf("GET_CART currency = %v, want %v", got, want)
	}
	if got := numberField(t, cart, "sum"); got != 500 {
		t.Errorf("GET_CART sum = %v, want 500", got)
	}
	if got := numberField(t, cart, "discountAmount"); got != 0 {
		t.Errorf("GET_CART discountAmount = %v, want 0 until promo codes land", got)
	}
	if got := numberField(t, cart, "chargeAmount"); got != 25 {
		t.Errorf("GET_CART chargeAmount = %v, want 25 (5%% of 500)", got)
	}
	if got := numberField(t, cart, "totalSum"); got != 525 {
		t.Errorf("GET_CART totalSum = %v, want 525", got)
	}
	// Spec §7.5: totalSum is the one and only total.
	for _, alias := range getCartTotalAliases {
		if _, leaked := cart[alias]; leaked {
			t.Errorf("GET_CART carries %q; totalSum is the only total in §7.5", alias)
		}
	}
	// cartTimeout is a whole number of seconds counting down from the hold TTL.
	ct := numberField(t, cart, "cartTimeout")
	if ct <= 0 || ct > harnessTTLSeconds {
		t.Errorf("GET_CART cartTimeout = %v, want 0 < n <= %d", ct, harnessTTLSeconds)
	}
	if ct != float64(int64(ct)) {
		t.Errorf("GET_CART cartTimeout = %v, want an integer", ct)
	}

	// The grouped body: exactly one action event carrying exactly one seat.
	events, ok := cart["actionEventList"].([]interface{})
	if !ok || len(events) != 1 {
		t.Fatalf("GET_CART actionEventList = %#v, want exactly one group", cart["actionEventList"])
	}
	group, _ := events[0].(map[string]interface{})
	assertObjectKeys(t, "actionEventList[0]", group,
		[]string{"actionEventId", "chargePercent", "seatList"})
	if got := numberField(t, group, "actionEventId"); got != float64(actionEventID) {
		t.Errorf("actionEventId = %v, want the compat id %d", got, actionEventID)
	}
	if got := numberField(t, group, "chargePercent"); got != 5 {
		t.Errorf("chargePercent = %v, want 5", got)
	}
	seats, ok := group["seatList"].([]interface{})
	if !ok || len(seats) != 1 {
		t.Fatalf("actionEventList[0].seatList = %#v, want exactly one row", group["seatList"])
	}
	row, _ := seats[0].(map[string]interface{})
	assertObjectKeys(t, "actionEventList[0].seatList[0]", row,
		[]string{"seatId", "categoryPriceId", "tariffPlanId", "price", "discount"})
	wantSeat, err := strconv.ParseFloat(seatID, 64)
	if err != nil {
		t.Fatalf("seed seat id %q is not an int64 literal: %v", seatID, err)
	}
	if got := numberField(t, row, "seatId"); got != wantSeat {
		t.Errorf("seatList[0].seatId = %v, want %v", got, wantSeat)
	}
	if got := numberField(t, row, "categoryPriceId"); got <= 0 {
		t.Errorf("seatList[0].categoryPriceId = %v, want the minted compat id", got)
	}
	if got := numberField(t, row, "price"); got != 500 {
		t.Errorf("seatList[0].price = %v, want 500", got)
	}
	if got := numberField(t, row, "discount"); got != 0 {
		t.Errorf("seatList[0].discount = %v, want 0", got)
	}
	if v, present := row["tariffPlanId"]; !present || v != nil {
		t.Errorf("seatList[0].tariffPlanId = %#v, want an explicit null", v)
	}

	// ── step 4: a backdated gateway session is stale, not an internal error ──
	expireGatewaySession(t, st, sess)
	stale := postBil24(t, base, reqBasic)
	if code := numberField(t, stale, "resultCode"); code != 1 {
		t.Errorf("GET_CART on an expired session resultCode = %v, want 1 (description %v)",
			code, stale["description"])
	}
}

// TestCompatBil24_485_GetCart_GoldensExist keeps the checked-in fixtures honest
// for readers who run the suite without a database: both cases must be present
// and must spell totalSum rather than any legacy alias.
func TestCompatBil24_485_GetCart_GoldensExist(t *testing.T) {
	for _, name := range []string{"basic", "empty"} {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "GET_CART", name+".json"))
		if _, ok := gld["totalSum"]; !ok {
			t.Errorf("golden GET_CART/%s.json has no totalSum", name)
		}
		for _, alias := range getCartTotalAliases {
			if _, leaked := gld[alias]; leaked {
				t.Errorf("golden GET_CART/%s.json carries %q", name, alias)
			}
		}
	}
}

// assertObjectKeys enforces the spec §15.2 STRICT key-set rule on one object
// nested inside an array — assertGoldenKeySet only recurses through objects.
func assertObjectKeys(t *testing.T, label string, got map[string]interface{}, want []string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is not an object", label)
	}
	wanted := map[string]bool{}
	for _, k := range want {
		wanted[k] = true
		if _, ok := got[k]; !ok {
			t.Errorf("%s: missing key %q", label, k)
		}
	}
	for k := range got {
		if !wanted[k] {
			t.Errorf("%s: extra key %q", label, k)
		}
	}
}
