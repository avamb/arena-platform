//go:build integration

// promo_491_test.go — feature #491 (W1-B1a) live wire contract for the spec
// §7.6 promo commands: ADD_PROMO_CODES, CHECK_KDP and the GET_CART discount
// they feed.
//
// Like the §7.5 contract next door this runs against a REAL server over the
// seeded database — no stubbed handler, no stubbed dispatcher — and walks the
// sequence a WordPress checkout actually performs: create the buyer, hold a
// seat, probe the code with CHECK_KDP, add it with ADD_PROMO_CODES, re-add it
// (the plugin resends the whole list on every render), then read the cart and
// see the money move.
//
// The seeded code is WAVE1, 10% off, unrestricted — see seed_test.go step 9.
// The seeded cart is one CZK 500 seat on a 5% channel, so the discount is 50,
// the charge is 5% of the NET 450 = 22.5 and totalSum is 472.5.

package compat_bil24_test

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestCompatBil24_491_PromoCodes_WireContract(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) == 0 {
		t.Fatal("seed produced no seats for the assigned-seats session")
	}
	seatID := st.SeatIDs[labels[0]]

	sess, user := createGatewayUser(t, base, st, "harness-491@example.test")
	runtime := map[string]string{
		"actionEventId": strconv.FormatInt(actionEventID, 10),
		"sessionId":     sess,
	}

	// post loads a checked-in fixture, stamps the live credentials on it and
	// sends it, returning the decoded response and the resolved golden.
	post := func(command, name string) (map[string]interface{}, map[string]interface{}) {
		t.Helper()
		req, gld := loadWPFixture(t, command, name)
		req = resolveGolden(req, st, runtime)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		return postBil24(t, base, req), resolveGolden(gld, st, runtime)
	}

	// ── step 1: hold one seat so the cart has lines to validate against ──────
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

	// ── step 2: CHECK_KDP probes without storing anything ────────────────────
	// The fixture spells the code lowercase; matching is case-insensitive.
	kdp, gldKDP := post("CHECK_KDP", "ok")
	if code := numberField(t, kdp, "resultCode"); code != 0 {
		t.Fatalf("CHECK_KDP resultCode = %v, want 0 (description %v)", code, kdp["description"])
	}
	assertGoldenKeySet(t, kdp, gldKDP)

	bad, gldBad := post("CHECK_KDP", "invalid")
	if code := numberField(t, bad, "resultCode"); code != 101 {
		t.Errorf("CHECK_KDP on an unknown code resultCode = %v, want 101 (description %v)",
			code, bad["description"])
	}
	assertGoldenKeySet(t, bad, gldBad)
	if desc, _ := bad["description"].(string); desc == "" {
		t.Error("CHECK_KDP refusal carries an empty description; §7.6 renders it verbatim")
	}

	// The probe must NOT have persisted anything: the cart is still undiscounted.
	preCart, _ := post("GET_CART", "basic")
	if got := numberField(t, preCart, "discountAmount"); got != 0 {
		t.Errorf("after CHECK_KDP only, GET_CART discountAmount = %v, want 0", got)
	}

	// ── step 3: ADD_PROMO_CODES accepts the union of both spellings ──────────
	add, gldAdd := post("ADD_PROMO_CODES", "basic")
	if code := numberField(t, add, "resultCode"); code != 0 {
		t.Fatalf("ADD_PROMO_CODES resultCode = %v, want 0 (description %v)",
			code, add["description"])
	}
	assertGoldenKeySet(t, add, gldAdd)
	// promoCodeList=["WAVE1"] and promoCodes=["wave1"] are the SAME code: the
	// union deduplicates case-insensitively, so exactly one entry comes back.
	assertPromoLists(t, "ADD_PROMO_CODES basic", add, 1, 0, 0)

	// ── step 4: re-adding the same code is an "exist", never a duplicate ─────
	again, gldAgain := post("ADD_PROMO_CODES", "exist")
	if code := numberField(t, again, "resultCode"); code != 0 {
		t.Fatalf("ADD_PROMO_CODES (repeat) resultCode = %v, want 0", code)
	}
	assertGoldenKeySet(t, again, gldAgain)
	assertPromoLists(t, "ADD_PROMO_CODES exist", again, 0, 1, 0)

	// ── step 5: an unknown code is an error ENTRY, not an error envelope ─────
	errResp, gldErr := post("ADD_PROMO_CODES", "error")
	if code := numberField(t, errResp, "resultCode"); code != 0 {
		t.Errorf("ADD_PROMO_CODES with a bad code resultCode = %v, want 0 — the "+
			"per-code lists carry the refusal (description %v)", code, errResp["description"])
	}
	assertGoldenKeySet(t, errResp, gldErr)
	assertPromoLists(t, "ADD_PROMO_CODES error", errResp, 0, 0, 1)
	if desc, _ := errResp["description"].(string); desc == "" {
		t.Error("ADD_PROMO_CODES refusal carries an empty description")
	}

	// ── step 6: GET_CART now prices the discount ─────────────────────────────
	cart, gldCart := post("GET_CART", "with_promo")
	if code := numberField(t, cart, "resultCode"); code != 0 {
		t.Fatalf("GET_CART resultCode = %v, want 0 (description %v)", code, cart["description"])
	}
	assertGoldenKeySet(t, cart, gldCart)
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"sum", 500},
		{"discountAmount", 50}, // WAVE1 is 10% of 500
		{"chargeAmount", 22.5}, // 5% of the NET 450, not of 500
		{"totalSum", 472.5},    // 500 - 50 + 22.5
	} {
		if got := numberField(t, cart, tc.key); got != tc.want {
			t.Errorf("GET_CART %s = %v, want %v", tc.key, got, tc.want)
		}
	}

	// The per-row discount must sum back to discountAmount: with a single row
	// the whole discount lands on it.
	events, ok := cart["actionEventList"].([]interface{})
	if !ok || len(events) != 1 {
		t.Fatalf("GET_CART actionEventList = %#v, want exactly one group", cart["actionEventList"])
	}
	group, _ := events[0].(map[string]interface{})
	seats, ok := group["seatList"].([]interface{})
	if !ok || len(seats) != 1 {
		t.Fatalf("actionEventList[0].seatList = %#v, want exactly one row", group["seatList"])
	}
	row, _ := seats[0].(map[string]interface{})
	assertObjectKeys(t, "actionEventList[0].seatList[0]", row,
		[]string{"seatId", "categoryPriceId", "tariffPlanId", "price", "discount"})
	if got := numberField(t, row, "discount"); got != 50 {
		t.Errorf("seatList[0].discount = %v, want the whole prorated 50", got)
	}
}

// assertPromoLists checks the §7.6 classification lists are present, are
// arrays, and hold the expected number of entries.
func assertPromoLists(t *testing.T, label string, resp map[string]interface{}, wantNew, wantExist, wantErr int) {
	t.Helper()
	for _, tc := range []struct {
		key  string
		want int
	}{
		{"newPromoCodeList", wantNew},
		{"existPromoCodeList", wantExist},
		{"errorPromoCodeList", wantErr},
	} {
		list, ok := resp[tc.key].([]interface{})
		if !ok {
			t.Errorf("%s: %s = %#v, want an array", label, tc.key, resp[tc.key])
			continue
		}
		if len(list) != tc.want {
			t.Errorf("%s: %s = %#v, want %d entries", label, tc.key, list, tc.want)
		}
	}
}

// TestCompatBil24_491_PromoGoldensExist keeps the checked-in fixtures honest
// for readers running the suite without a database: every §7.6 case must be
// present, and the ADD_PROMO_CODES goldens must spell all three classification
// lists even when empty (the plugin indexes into them unconditionally).
func TestCompatBil24_491_PromoGoldensExist(t *testing.T) {
	for _, name := range []string{"basic", "exist", "error"} {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "ADD_PROMO_CODES", name+".json"))
		for _, key := range []string{"newPromoCodeList", "existPromoCodeList", "errorPromoCodeList"} {
			if _, ok := gld[key]; !ok {
				t.Errorf("golden ADD_PROMO_CODES/%s.json has no %s", name, key)
			}
		}
	}
	for _, name := range []string{"ok", "invalid"} {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "CHECK_KDP", name+".json"))
		if _, ok := gld["resultCode"]; !ok {
			t.Errorf("golden CHECK_KDP/%s.json has no resultCode", name)
		}
	}
	promo := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "GET_CART", "with_promo.json"))
	if _, ok := promo["discountAmount"]; !ok {
		t.Error("golden GET_CART/with_promo.json has no discountAmount")
	}
}
