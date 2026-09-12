//go:build integration

// scenario02_ga_purchase_test.go — spec §15.3 scenario 2, the general-admission
// purchase flow end to end (feature #495, W1-B2b, spec §7.10 / §7.11).
//
// This is the only scenario that walks a WordPress checkout from an empty cart
// all the way to the buyer holding a PDF link, over the REAL server against a
// live database: real chi router, real hbil24 handler, real ordering aggregate,
// real pricing, real htickets issuance, real delivery queue. Nothing below the
// HTTP boundary is stubbed.
//
// The steps, in the order the shop performs them:
//
//	CREATE_USER        a gateway session for the buyer
//	RESERVATION        2 general-admission units of the EUR 900 "Early Bird"
//	                   tier — no seat map, a categoryList hold
//	GET_CART           the untouched cart: EUR 1800 + 5 % charge
//	ADD_PROMO_CODES    WAVE1 (10 %) is accepted as NEW
//	GET_CART           the same cart re-priced: the charge is taken on the NET
//	CREATE_ORDER_EXT   a pending_payment order carrying the session's promo
//	GET_TICKETS_BY_ORDER  before payment: EMPTY lists and resultCode 0, because
//	                   §7.10 says "not paid yet" is not an error
//	PAY_ORDER          WooCommerce reports the money, issuance runs
//	GET_TICKETS_BY_ORDER  after payment: 2 rows with ABSOLUTE PDF links, and
//	                   pdfUrl == downloadUrl (the spec writes downloadUrl as
//	                   "the same one")
//	SEND_TICKETS_TO_EMAIL  the site asks arena to re-mail those exact tickets
//
// Money is asserted explicitly at every step because assertGoldenKeySet compares
// KEY SETS only and never recurses into arrays: a golden can catch a renamed
// field, never a pricing regression.
package compat_bil24_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Seeded money for this scenario. The GA session prices "Early Bird" at EUR 900
// and the harness channel takes a 5 % charge; WAVE1 is a 10 % percent promo.
//
//	gross    2 × 900              = 1800
//	discount 10 % of 1800         =  180
//	net                           = 1620
//	charge   5 % of the NET       =   81
//	total    net + charge         = 1701
const (
	sc2Gross    = 1800.0
	sc2Discount = 180.0
	sc2Charge   = 81.0
	sc2Total    = 1701.0
	// Before the promo the charge is taken on the gross.
	sc2ChargeNoPromo = 90.0
	sc2TotalNoPromo  = 1890.0
	sc2Currency      = "EUR"
	sc2Quantity      = 2
	sc2Buyer         = "harness-495@example.test"
)

// runScenario02GAPurchase is the body of the 02_ga_purchase_flow sub-test.
func runScenario02GAPurchase(t *testing.T, st *harnessState) {
	t.Helper()

	// The server is booted FIRST on purpose: startHarnessServer registers the
	// wire-row sweep with t.Cleanup, and t.Cleanup is LIFO, so everything this
	// scenario creates is torn down before that sweep runs.
	base := startHarnessServer(t, st)

	// Spec §4: both ids travel as int64 compatibility ids, never as platform
	// UUIDs — resolveGolden's fallback for {{categoryPriceId}} IS the UUID, so
	// the scenario has to mint and override it.
	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))

	sess, user := createGatewayUser(t, base, st, sc2Buyer)
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(gaActionEventID, 10),
		"categoryPriceId": strconv.FormatInt(gaTierWireID, 10),
		"sessionId":       sess,
	}

	// post loads a checked-in fixture, stamps the live credentials on it, sends
	// it and returns the decoded response together with the resolved golden.
	post := func(command, name string) (map[string]interface{}, map[string]interface{}) {
		t.Helper()
		req, gld := loadWPFixture(t, command, name)
		req = resolveGolden(req, st, runtime)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		return postBil24(t, base, req), resolveGolden(gld, st, runtime)
	}

	// ── step 1: hold 2 general-admission units ─────────────────────────────
	reqReserve, gldReserve := loadWPFixture(t, "RESERVATION", "reserve_by_category")
	reqReserve = resolveGolden(reqReserve, st, runtime)
	reqReserve["fid"] = st.ChannelFID
	reqReserve["token"] = st.ChannelToken
	reqReserve["userId"] = user
	// The fixture hard-codes categoryPriceId 1 (the seated session's tier); a
	// GA hold has to name the GA session's own tier.
	reqReserve["categoryList"] = []any{map[string]any{
		"categoryPriceId": gaTierWireID,
		"quantity":        sc2Quantity,
		"tariffPlanId":    nil,
	}}
	reserved := postBil24(t, base, reqReserve)
	sc2RequireOK(t, "RESERVATION", reserved)
	assertGoldenKeySet(t, reserved, resolveGolden(gldReserve, st, runtime))
	// A GA hold materialises real session_seats rows (kind='ga_unit'), so the
	// cart projection must report them like any other seat.
	sc2AssertSeatCount(t, "RESERVATION", reserved, "seatList", sc2Quantity)
	// RESERVATION never applies a promo — writeCartResponse hard-codes discount
	// to 0 — so this step is the gross price by construction.
	sc2AssertMoney(t, "RESERVATION", reserved, map[string]float64{
		"sum": sc2Gross, "discount": 0, "charge": sc2ChargeNoPromo, "totalSum": sc2TotalNoPromo,
	}, sc2Currency)

	// ── step 2: the untouched cart ─────────────────────────────────────────
	cart, gldCart := post("GET_CART", "basic")
	sc2RequireOK(t, "GET_CART", cart)
	assertGoldenKeySet(t, cart, gldCart)
	sc2AssertMoney(t, "GET_CART pre-promo", cart, map[string]float64{
		"sum": sc2Gross, "discountAmount": 0, "chargeAmount": sc2ChargeNoPromo, "totalSum": sc2TotalNoPromo,
	}, sc2Currency)

	// ── step 3: the buyer types the promo code ─────────────────────────────
	promo, gldPromo := post("ADD_PROMO_CODES", "basic")
	sc2RequireOK(t, "ADD_PROMO_CODES", promo)
	assertGoldenKeySet(t, promo, gldPromo)
	sc2AssertStringList(t, "newPromoCodeList", promo, []string{"WAVE1"})
	sc2AssertStringList(t, "existPromoCodeList", promo, nil)
	sc2AssertStringList(t, "errorPromoCodeList", promo, nil)

	// ── step 4: the same cart, re-priced ───────────────────────────────────
	withPromo, gldWithPromo := post("GET_CART", "with_promo")
	sc2RequireOK(t, "GET_CART with_promo", withPromo)
	assertGoldenKeySet(t, withPromo, gldWithPromo)
	// The charge is taken on the NET, not the gross: 5 % of 1620, not of 1800.
	// A charge of 90 here would mean the buyer pays the fee on money they never
	// spent — the exact regression this assertion exists to catch.
	sc2AssertMoney(t, "GET_CART with promo", withPromo, map[string]float64{
		"sum": sc2Gross, "discountAmount": sc2Discount, "chargeAmount": sc2Charge, "totalSum": sc2Total,
	}, sc2Currency)

	// ── step 5: the order ──────────────────────────────────────────────────
	// payment_intents.provider_payment_id ('wc:<external_ref>:<method>') is
	// covered by a GLOBAL unique index, unscoped by org, so the shop order
	// number carries a per-run tag: a literal would collide with a leftover row
	// from an interrupted run against the shared dev-stand.
	externalRef := "wc-ga-495-" + uuid.NewString()[:8]
	reqOrder, gldOrder := loadWPFixture(t, "CREATE_ORDER_EXT", "ga")
	reqOrder = resolveGolden(reqOrder, st, runtime)
	reqOrder["fid"] = st.ChannelFID
	reqOrder["token"] = st.ChannelToken
	reqOrder["userId"] = user
	reqOrder["orderId"] = externalRef
	order := postBil24(t, base, reqOrder)
	sc2RequireOK(t, "CREATE_ORDER_EXT", order)
	assertGoldenKeySet(t, order, resolveGolden(gldOrder, st, runtime))
	// The request carries an EMPTY promoCodes list: the discount below can only
	// come from the session's own codes, which is the §7.7 merge rule.
	sc6AssertMoney(t, "ga", order, sc6Money{
		sum: sc2Gross, discount: sc2Discount, charge: sc2Charge, total: sc2Total, currency: sc2Currency,
	})
	orderID := sc6OrderID(t, "ga", order)

	// ── step 6: the site polls for tickets before the money moves ──────────
	// §7.10: an unpaid order is NOT an error. The lists must be present and
	// empty — never absent and never null, because the plugin iterates them
	// without a guard.
	before, gldBefore := sc2GetTickets(t, base, st, sess, user, orderID.String(), "before_issuance", runtime)
	sc2RequireOK(t, "GET_TICKETS_BY_ORDER before payment", before)
	assertGoldenKeySet(t, before, gldBefore)
	sc2AssertSeatCount(t, "GET_TICKETS_BY_ORDER before payment", before, "ticketList", 0)
	sc2AssertSeatCount(t, "GET_TICKETS_BY_ORDER before payment", before, "ticketIdList", 0)

	// ── step 7: WooCommerce reports the payment ────────────────────────────
	reqPay, gldPay := loadWPFixture(t, "PAY_ORDER", "basic")
	reqPay = resolveGolden(reqPay, st, map[string]string{
		"sessionId": sess,
		"orderId":   orderID.String(),
	})
	reqPay["fid"] = st.ChannelFID
	reqPay["token"] = st.ChannelToken
	reqPay["userId"] = user
	// The fixture is priced for the seated CZK order; this one is the GA EUR
	// total. A mismatch here would exercise §7.9's amount_mismatch branch
	// instead of the happy path.
	reqPay["amount"] = sc2Total
	reqPay["currency"] = sc2Currency
	paid := postBil24(t, base, reqPay)
	sc2RequireOK(t, "PAY_ORDER", paid)
	assertGoldenKeySet(t, paid, gldPay)
	// Issuance is SYNCHRONOUS on the gateway path, which is the whole reason
	// the site's very next poll can already find the PDF.
	// orders.payment_method is the site's own `method` verbatim — it is what an
	// operator reconciles the WooCommerce transaction against.
	sc5AssertOrderPaid(t, st, orderID, "woo_bank_card")
	sc5AssertTicketCount(t, st, orderID, sc2Quantity)

	// ── step 8: the site polls again and gets the links ────────────────────
	after, gldAfter := sc2GetTickets(t, base, st, sess, user, orderID.String(), "basic", runtime)
	sc2RequireOK(t, "GET_TICKETS_BY_ORDER after payment", after)
	assertGoldenKeySet(t, after, gldAfter)
	ticketIDs := sc2AssertTicketRows(t, after, sc2Quantity)

	// ── step 9: "re-send my tickets" ───────────────────────────────────────
	reqSend, gldSend := loadWPFixture(t, "SEND_TICKETS_TO_EMAIL", "basic")
	reqSend = resolveGolden(reqSend, st, runtime)
	reqSend["fid"] = st.ChannelFID
	reqSend["token"] = st.ChannelToken
	reqSend["userId"] = user
	// The fixture's ticketIdList is a placeholder literal; only the ids minted
	// by this run address real tickets.
	reqSend["ticketIdList"] = ticketIDs
	sent := postBil24(t, base, reqSend)
	sc2RequireOK(t, "SEND_TICKETS_TO_EMAIL", sent)
	assertGoldenKeySet(t, sent, gldSend)
	// The command's whole purpose is the queue row; delivery_jobs.ticket_id is
	// ON DELETE CASCADE from tickets, so the harness sweep reclaims these.
	sc2AssertDeliveryJobs(t, st, orderID, sc2Quantity)
}

// sc2GetTickets sends GET_TICKETS_BY_ORDER for a concrete order and returns the
// response with the resolved golden. The order id is minted per run, so it can
// only reach the fixture through a runtime override.
func sc2GetTickets(
	t *testing.T,
	base string,
	st *harnessState,
	sess string,
	user float64,
	orderID string,
	caseName string,
	runtime map[string]string,
) (map[string]interface{}, map[string]interface{}) {
	t.Helper()
	rt := map[string]string{}
	for k, v := range runtime {
		rt[k] = v
	}
	rt["sessionId"] = sess
	rt["orderId"] = orderID

	req, gld := loadWPFixture(t, "GET_TICKETS_BY_ORDER", caseName)
	req = resolveGolden(req, st, rt)
	req["fid"] = st.ChannelFID
	req["token"] = st.ChannelToken
	req["userId"] = user
	return postBil24(t, base, req), resolveGolden(gld, st, rt)
}

// sc2EarlyBirdTierID reads the GA session's cheapest seeded tier.
func sc2EarlyBirdTierID(t *testing.T, st *harnessState) string {
	t.Helper()
	gaSessionID, err := uuid.Parse(st.GAsessID)
	if err != nil {
		t.Fatalf("parse GA session uuid %q: %v", st.GAsessID, err)
	}
	var tierID uuid.UUID
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT id FROM ticket_tiers WHERE session_id=$1 AND name='Early Bird'`, gaSessionID,
	).Scan(&tierID); err != nil {
		t.Fatalf("look up the Early Bird tier of GA session %s: %v", st.GAsessID, err)
	}
	return tierID.String()
}

func sc2RequireOK(t *testing.T, label string, resp map[string]interface{}) {
	t.Helper()
	if code := numberField(t, resp, "resultCode"); code != 0 {
		t.Fatalf("%s resultCode = %v, want 0 (description %v)", label, code, resp["description"])
	}
}

// sc2AssertMoney checks a set of money fields plus the currency. The field
// names differ between commands (RESERVATION says discount/charge, GET_CART
// says discountAmount/chargeAmount), so they are passed in.
func sc2AssertMoney(t *testing.T, label string, resp map[string]interface{}, want map[string]float64, currency string) {
	t.Helper()
	for key, wantVal := range want {
		if got := numberField(t, resp, key); got != wantVal {
			t.Errorf("%s: %s = %v, want %v", label, key, got, wantVal)
		}
	}
	if got, _ := resp["currency"].(string); got != currency {
		t.Errorf("%s: currency = %q, want %q", label, got, currency)
	}
}

// sc2AssertSeatCount checks the length of a top-level array field.
func sc2AssertSeatCount(t *testing.T, label string, resp map[string]interface{}, key string, want int) {
	t.Helper()
	raw, ok := resp[key]
	if !ok {
		t.Fatalf("%s: response has no %s", label, key)
	}
	list, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("%s: %s = %#v, want a JSON array (null breaks the plugin's loop)", label, key, raw)
	}
	if len(list) != want {
		t.Errorf("%s: len(%s) = %d, want %d", label, key, len(list), want)
	}
}

func sc2AssertStringList(t *testing.T, key string, resp map[string]interface{}, want []string) {
	t.Helper()
	raw, ok := resp[key].([]interface{})
	if !ok {
		t.Fatalf("ADD_PROMO_CODES: %s = %#v, want a JSON array", key, resp[key])
	}
	if len(raw) != len(want) {
		t.Fatalf("ADD_PROMO_CODES: %s = %#v, want %#v", key, raw, want)
	}
	for i, v := range want {
		if got, _ := raw[i].(string); got != v {
			t.Errorf("ADD_PROMO_CODES: %s[%d] = %#v, want %q", key, i, raw[i], v)
		}
	}
}

// sc2AssertTicketRows checks the §7.10 row shape of an issued ticketList and
// returns the wire ticket ids for SEND_TICKETS_TO_EMAIL.
//
// assertGoldenKeySet never recurses into arrays, so every row-level invariant
// has to be asserted here or it is not asserted at all.
func sc2AssertTicketRows(t *testing.T, resp map[string]interface{}, want int) []interface{} {
	t.Helper()
	sc2AssertSeatCount(t, "GET_TICKETS_BY_ORDER", resp, "ticketList", want)
	sc2AssertSeatCount(t, "GET_TICKETS_BY_ORDER", resp, "ticketIdList", want)

	list, _ := resp["ticketList"].([]interface{})
	ids, _ := resp["ticketIdList"].([]interface{})

	// The link the buyer clicks must be ABSOLUTE: the WordPress site renders it
	// on its own origin, so a relative path would resolve against the shop.
	// Feature #535: the origin is API_PUBLIC_URL — the PDF is served by the
	// API, not by the admin SPA.
	wantPrefix := harnessAPIPublicBaseURL + "/v1/public/checkout/"
	seen := map[string]bool{}
	for i, raw := range list {
		row, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("ticketList[%d] = %#v, want an object", i, raw)
		}
		for _, k := range []string{"ticketId", "pdfUrl", "downloadUrl", "barcode", "seatId", "categoryPriceId"} {
			if _, ok := row[k]; !ok {
				t.Errorf("ticketList[%d] has no %s", i, k)
			}
		}
		pdf, _ := row["pdfUrl"].(string)
		dl, _ := row["downloadUrl"].(string)
		if !strings.HasPrefix(pdf, wantPrefix) {
			t.Errorf("ticketList[%d].pdfUrl = %q, want an absolute link under %q", i, pdf, wantPrefix)
		}
		if !strings.HasSuffix(pdf, "/pdf") {
			t.Errorf("ticketList[%d].pdfUrl = %q, want the /pdf route", i, pdf)
		}
		// Spec §7.10 writes downloadUrl as "the same one": two keys exist for
		// legacy clients, not because there are two endpoints.
		if dl != pdf {
			t.Errorf("ticketList[%d]: downloadUrl %q != pdfUrl %q", i, dl, pdf)
		}
		if seen[pdf] {
			t.Errorf("ticketList[%d].pdfUrl %q is a duplicate — every ticket has its own link", i, pdf)
		}
		seen[pdf] = true

		if got, _ := row["barcode"].(string); got == "" {
			t.Errorf("ticketList[%d].barcode is empty — it is what the scanner reads", i)
		}
		if got, _ := row["ticketId"].(float64); got <= 0 {
			t.Errorf("ticketList[%d].ticketId = %#v, want a positive int64 wire id", i, row["ticketId"])
		}
		if got, _ := row["categoryPriceId"].(float64); got <= 0 {
			t.Errorf("ticketList[%d].categoryPriceId = %#v, want the tier's int64 wire id",
				i, row["categoryPriceId"])
		}
		if got, _ := ids[i].(float64); got != row["ticketId"] {
			t.Errorf("ticketIdList[%d] = %#v but ticketList[%d].ticketId = %#v — the two must agree",
				i, ids[i], i, row["ticketId"])
		}
	}
	return ids
}

// sc2AssertDeliveryJobs requires SEND_TICKETS_TO_EMAIL to have queued exactly
// one job per ticket. Scenario 5 asserts the opposite for a plain PAY_ORDER
// (the shop mails its own PDF); this command is the explicit opt-in.
func sc2AssertDeliveryJobs(t *testing.T, st *harnessState, orderID uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := st.Pool.QueryRow(context.Background(),
		`SELECT count(*)
		 FROM   delivery_jobs dj
		 JOIN   tickets t ON t.id = dj.ticket_id
		 WHERE  t.order_id = $1`, orderID).Scan(&got); err != nil {
		t.Fatalf("count delivery jobs for %s: %v", orderID, err)
	}
	if got != want {
		t.Errorf("delivery_jobs for order %s = %d, want %d — SEND_TICKETS_TO_EMAIL must queue "+
			"exactly one job per requested ticket", orderID, got, want)
	}
}

// TestCompatBil24_495_TicketsByOrderGoldensExist keeps the checked-in §7.10 and
// §7.11 fixtures honest without needing a database.
//
// The invariant that matters is the one assertGoldenKeySet can never enforce,
// because it does not recurse into arrays: pdfUrl and downloadUrl are the SAME
// absolute link, and the "not issued yet" golden carries EMPTY lists rather
// than omitting them.
func TestCompatBil24_495_TicketsByOrderGoldensExist(t *testing.T) {
	for _, name := range []string{"basic", "before_issuance"} {
		gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "GET_TICKETS_BY_ORDER", name+".json"))
		for _, k := range []string{"resultCode", "description", "command", "ticketList", "ticketIdList"} {
			if _, ok := gld[k]; !ok {
				t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json has no %s", name, k)
			}
		}
		if got, _ := gld["resultCode"].(float64); got != 0 {
			t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json resultCode = %v, want 0 — §7.10 says an "+
				"unpaid order is not an error", name, got)
		}
		if got, _ := gld["command"].(string); got != "GET_TICKETS_BY_ORDER" {
			t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json command = %q", name, got)
		}

		list, ok := gld["ticketList"].([]interface{})
		if !ok {
			t.Fatalf("golden GET_TICKETS_BY_ORDER/%s.json ticketList = %#v, want an array — null "+
				"breaks the plugin's loop", name, gld["ticketList"])
		}
		ids, ok := gld["ticketIdList"].([]interface{})
		if !ok {
			t.Fatalf("golden GET_TICKETS_BY_ORDER/%s.json ticketIdList = %#v, want an array",
				name, gld["ticketIdList"])
		}
		if len(list) != len(ids) {
			t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json: len(ticketList) %d != len(ticketIdList) %d",
				name, len(list), len(ids))
		}
		if name == "before_issuance" && len(list) != 0 {
			t.Errorf("golden GET_TICKETS_BY_ORDER/before_issuance.json has %d ticket(s); the case "+
				"exists to pin the empty answer", len(list))
		}
		for i, raw := range list {
			row, ok := raw.(map[string]interface{})
			if !ok {
				t.Fatalf("golden GET_TICKETS_BY_ORDER/%s.json ticketList[%d] is not an object", name, i)
			}
			for _, k := range []string{"ticketId", "pdfUrl", "downloadUrl", "barcode", "seatId", "categoryPriceId"} {
				if _, ok := row[k]; !ok {
					t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json ticketList[%d] has no %s", name, i, k)
				}
			}
			pdf, _ := row["pdfUrl"].(string)
			dl, _ := row["downloadUrl"].(string)
			if pdf != dl {
				t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json ticketList[%d]: downloadUrl %q != pdfUrl %q "+
					"— the spec writes downloadUrl as the same link", name, i, dl, pdf)
			}
			if !strings.HasPrefix(pdf, "https://") {
				t.Errorf("golden GET_TICKETS_BY_ORDER/%s.json ticketList[%d].pdfUrl = %q, want an "+
					"absolute link — the WordPress site renders it on its own origin", name, i, pdf)
			}
		}

		req := mustReadJSON(t, filepath.Join("testdata", "wp", "requests", "GET_TICKETS_BY_ORDER", name+".json"))
		for _, k := range []string{"orderId", "sessionId", "token", "locale"} {
			if _, ok := req[k]; !ok {
				t.Errorf("request GET_TICKETS_BY_ORDER/%s.json has no %s", name, k)
			}
		}
		// The order id is minted per run, so it can only travel as a
		// placeholder — a hard-coded id would address another run's order.
		if got, _ := req["orderId"].(string); got != "{{orderId}}" {
			t.Errorf("request GET_TICKETS_BY_ORDER/%s.json orderId = %#v, want the {{orderId}} placeholder",
				name, req["orderId"])
		}
	}

	// §7.11's envelope is the bare three-key OK shape; what has to stay true is
	// that the request names both an address and the tickets to send.
	gld := mustReadJSON(t, filepath.Join("testdata", "wp", "golden", "SEND_TICKETS_TO_EMAIL", "basic.json"))
	if got, _ := gld["resultCode"].(float64); got != 0 {
		t.Errorf("golden SEND_TICKETS_TO_EMAIL/basic.json resultCode = %v, want 0", got)
	}
	if got, _ := gld["command"].(string); got != "SEND_TICKETS_TO_EMAIL" {
		t.Errorf("golden SEND_TICKETS_TO_EMAIL/basic.json command = %q", got)
	}
	if desc, _ := gld["description"].(string); desc == "" {
		t.Error("golden SEND_TICKETS_TO_EMAIL/basic.json has an empty description")
	}
	req := mustReadJSON(t, filepath.Join("testdata", "wp", "requests", "SEND_TICKETS_TO_EMAIL", "basic.json"))
	for _, k := range []string{"email", "ticketIdList", "sessionId", "token"} {
		if _, ok := req[k]; !ok {
			t.Errorf("request SEND_TICKETS_TO_EMAIL/basic.json has no %s", k)
		}
	}
	if list, ok := req["ticketIdList"].([]interface{}); !ok || len(list) == 0 {
		t.Errorf("request SEND_TICKETS_TO_EMAIL/basic.json ticketIdList = %#v, want a non-empty array "+
			"— an empty list is a -2 by §7.11", req["ticketIdList"])
	}
}
