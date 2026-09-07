//go:build integration

// get_order_info_493_test.go — feature #493 (W1-B1c, spec §7.8): GET_ORDER_INFO
// strict mode.
//
// Drives GET_ORDER_INFO over the REAL server against a live database: real
// chi router, real hbil24 handler, real checkout_sessions row created by a
// genuine CREATE_ORDER_EXT round trip (feature #492). Nothing below the HTTP
// boundary is stubbed.
//
// What it proves:
//
//	basic       a real pending (pending_payment, no tickets issued) order
//	            round-trips through the reduced §9.3 projection
//	            (buildGetOrderInfoBody) with the corrected GET_ORDER_INFO/
//	            basic.json golden — id/status/sum/discount/charge/totalSum/
//	            currency/ticketQuantity, no `expiration` (deferred to a later
//	            slice, see bil24_476_order_test.go)
//	cross_org   an orderId that belongs to another org/channel returns -3,
//	            never leaking the order's existence to a foreign fid/token
//	invalid_id  a malformed orderId returns -2 with a non-empty userMessage
//
// Every error path additionally checks the new `userMessage` field (spec
// §7.8: GET_ORDER_INFO error responses duplicate `description` into
// `userMessage` so the legacy WordPress plugin can render it without its own
// message table).
package compat_bil24_test

import (
	"strconv"
	"testing"
)

func TestCompatBil24_493_GetOrderInfo(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	if len(labels) < 1 {
		t.Fatalf("seed produced %d seats for the assigned-seats session, need at least 1", len(labels))
	}
	assignedTierWireID := sc6TierWireID(t, st, st.AssignedTierID)

	sess, user := createGatewayUser(t, base, st, "harness-493@example.test")
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(actionEventID, 10),
		"categoryPriceId": strconv.FormatInt(assignedTierWireID, 10),
		"sessionId":       sess,
	}

	// ── step 1: hold a seat, then create the order via CREATE_ORDER_EXT ─────
	reserveResp := postBil24(t, base, map[string]any{
		"command":       "RESERVATION",
		"fid":           st.ChannelFID,
		"token":         st.ChannelToken,
		"locale":        "ru-RU",
		"type":          "RESERVE",
		"userId":        user,
		"sessionId":     sess,
		"actionEventId": actionEventID,
		"seatList":      []any{map[string]any{"seatId": st.SeatIDs[labels[0]]}},
	})
	if code := numberField(t, reserveResp, "resultCode"); code != 0 {
		t.Fatalf("RESERVE resultCode = %v, want 0 (description %v)", code, reserveResp["description"])
	}

	createReq, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "basic")
	createReq = resolveGolden(createReq, st, runtime)
	createReq["fid"] = st.ChannelFID
	createReq["token"] = st.ChannelToken
	createReq["userId"] = user
	createResp := postBil24(t, base, createReq)
	if code := numberField(t, createResp, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT basic resultCode = %v, want 0 (description %v)",
			code, createResp["description"])
	}
	orderID, _ := createResp["orderId"].(string)
	if orderID == "" {
		t.Fatalf("CREATE_ORDER_EXT basic did not return an orderId: %v", createResp)
	}

	// ── step 2: GET_ORDER_INFO against the pending order ────────────────────
	infoReq, gld := loadWPFixture(t, "GET_ORDER_INFO", "basic")
	infoRuntime := map[string]string{"orderId": orderID}
	infoReq = resolveGolden(infoReq, st, infoRuntime)
	infoReq["fid"] = st.ChannelFID
	infoReq["token"] = st.ChannelToken
	infoReq["userId"] = user
	infoReq["sessionId"] = sess
	gld = resolveGolden(gld, st, infoRuntime)

	info := postBil24(t, base, infoReq)
	if code := numberField(t, info, "resultCode"); code != 0 {
		t.Fatalf("GET_ORDER_INFO basic resultCode = %v, want 0 (description %v)",
			code, info["description"])
	}
	assertGoldenKeySet(t, info, gld)

	order, ok := info["order"].(map[string]interface{})
	if !ok {
		t.Fatalf("GET_ORDER_INFO basic: order = %#v, want an object", info["order"])
	}
	if order["id"] != orderID {
		t.Errorf("GET_ORDER_INFO basic: order.id = %v, want %v", order["id"], orderID)
	}
	if order["status"] != "pending_payment" {
		t.Errorf("GET_ORDER_INFO basic: order.status = %v, want pending_payment", order["status"])
	}
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"sum", 500}, {"discount", 0}, {"charge", 25}, {"totalSum", 525}, {"ticketQuantity", 0},
	} {
		got, _ := order[tc.key].(float64)
		if got != tc.want {
			t.Errorf("GET_ORDER_INFO basic: order.%s = %v, want %v", tc.key, order[tc.key], tc.want)
		}
	}
	if order["currency"] != "CZK" {
		t.Errorf("GET_ORDER_INFO basic: order.currency = %v, want CZK", order["currency"])
	}
	if _, has := order["expiration"]; has {
		t.Errorf("GET_ORDER_INFO basic: order.expiration must be absent this slice, got %v", order["expiration"])
	}
	if _, has := info["userMessage"]; has {
		t.Errorf("GET_ORDER_INFO basic: a resultCode=0 response must not carry userMessage, got %v", info["userMessage"])
	}

	// ── step 3: cross-org — a foreign channel must never see this order ─────
	other := setupHarness(t)
	crossReq := map[string]any{
		"command":   "GET_ORDER_INFO",
		"fid":       other.ChannelFID,
		"token":     other.ChannelToken,
		"locale":    "en-US",
		"orderId":   orderID,
		"userId":    10001,
		"sessionId": "",
	}
	cross := postBil24(t, base, crossReq)
	if code := numberField(t, cross, "resultCode"); code != -3 {
		t.Fatalf("GET_ORDER_INFO cross-org resultCode = %v, want -3 (description %v)",
			code, cross["description"])
	}
	assertGetOrderInfoUserMessage(t, cross)

	// ── step 4: a malformed orderId is a protocol error, not a lookup miss ──
	badReq := map[string]any{
		"command":   "GET_ORDER_INFO",
		"fid":       st.ChannelFID,
		"token":     st.ChannelToken,
		"locale":    "en-US",
		"orderId":   "not-a-valid-id-or-uuid",
		"userId":    user,
		"sessionId": sess,
	}
	bad := postBil24(t, base, badReq)
	if code := numberField(t, bad, "resultCode"); code != -2 {
		t.Fatalf("GET_ORDER_INFO invalid orderId resultCode = %v, want -2 (description %v)",
			code, bad["description"])
	}
	assertGetOrderInfoUserMessage(t, bad)
}

// assertGetOrderInfoUserMessage pins spec §7.8: every GET_ORDER_INFO error
// response duplicates `description` into a `userMessage` field so the legacy
// WordPress plugin can surface it without its own message table.
func assertGetOrderInfoUserMessage(t *testing.T, resp map[string]interface{}) {
	t.Helper()
	desc, _ := resp["description"].(string)
	if desc == "" {
		t.Error("GET_ORDER_INFO error response has an empty description")
	}
	msg, ok := resp["userMessage"].(string)
	if !ok || msg == "" {
		t.Errorf("GET_ORDER_INFO error response userMessage = %#v, want a non-empty string", resp["userMessage"])
		return
	}
	if msg != desc {
		t.Errorf("GET_ORDER_INFO error response userMessage = %q, want it to duplicate description %q", msg, desc)
	}
}
