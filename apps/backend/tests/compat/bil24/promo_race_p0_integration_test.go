//go:build integration

// promo_race_p0_integration_test.go — P0-1 defect D-02 on the Bil24 gateway.
//
// A promo code's max_uses and max_uses_per_customer are taken when
// CREATE_ORDER_EXT writes the order (hcheckout.ReservePromoUseTx, a lock per
// code held until that transaction commits), not when PAY_ORDER records the
// redemption. Several sites sending CREATE_ORDER_EXT at the same moment with
// the same code must therefore end with exactly one discounted order and,
// after PAY_ORDER, exactly one redemption.

package compat_bil24_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// p0PromoRaceBuyers is how many buyers race for one max_uses=1 code.
const p0PromoRaceBuyers = 5

// p0InsertPromo adds a 10 % code to the harness organization and removes it
// (with what points at it) before the harness sweeps the orders.
func p0InsertPromo(t *testing.T, st *harnessState, code, limitCol string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := st.Pool.QueryRow(ctx, `INSERT INTO promo_codes (org_id, code, discount_type, discount_value, `+limitCol+`)
		VALUES ($1, $2, 'percent', 10, 1) RETURNING id`, st.OrgID, code).Scan(&id); err != nil {
		t.Fatalf("insert promo %s: %v", code, err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM promo_code_redemptions WHERE promo_code_id = $1`,
			`UPDATE orders SET promo_code_id = NULL WHERE promo_code_id = $1`,
			`UPDATE checkout_sessions SET promo_code_id = NULL WHERE promo_code_id = $1`,
			`DELETE FROM promo_code_attempts WHERE promo_code_id = $1`,
			`DELETE FROM promo_codes WHERE id = $1`,
		} {
			if _, err := st.Pool.Exec(ctx, sql, id); err != nil {
				t.Logf("promo cleanup (%.50s): %v", sql, err)
			}
		}
	})
	return id
}

// p0Buyer is one cart that is ready for CREATE_ORDER_EXT.
type p0Buyer struct {
	sess  string
	user  float64
	order map[string]any
}

// p0GABuyer holds one GA place for a new gateway session of email and
// returns the CREATE_ORDER_EXT body that would buy it with code.
func p0GABuyer(t *testing.T, base string, st *harnessState, email, code string) p0Buyer {
	t.Helper()
	actionEventID := mustActionEventID(t, st, st.GAsessID)
	tierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))
	sess, user := createGatewayUser(t, base, st, email)
	sc2RequireOK(t, "RESERVATION "+email, postBil24(t, base, map[string]any{
		"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
		"type": "RESERVE", "userId": user, "sessionId": sess, "actionEventId": actionEventID,
		"categoryList": []any{map[string]any{"categoryPriceId": tierWireID, "quantity": 1, "tariffPlanId": nil}},
	}))
	return p0Buyer{sess: sess, user: user, order: map[string]any{
		"command": "CREATE_ORDER_EXT", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
		"orderId": "wc-p0-" + uuid.NewString()[:12], "userId": user, "sessionId": sess,
		"actionEventId": actionEventID, "longReservation": false,
		"lines": []any{map[string]any{"categoryPriceId": tierWireID, "quantity": 1, "tariffPlanId": nil}},
		"email": email, "phone": "+420123456789", "fullName": "P0 Buyer",
		"promoCodeList": []any{code},
	}}
}

// p0SeatBuyer holds one seat of the assigned-seats session the same way.
func p0SeatBuyer(t *testing.T, base string, st *harnessState, email, code, seatLabel string) p0Buyer {
	t.Helper()
	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)
	sess, user := createGatewayUser(t, base, st, email)
	sc2RequireOK(t, "RESERVATION seat "+seatLabel, postBil24(t, base, map[string]any{
		"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
		"type": "RESERVE", "userId": user, "sessionId": sess, "actionEventId": actionEventID,
		"seatList": []any{map[string]any{"seatId": st.SeatIDs[seatLabel]}},
	}))
	return p0Buyer{sess: sess, user: user, order: map[string]any{
		"command": "CREATE_ORDER_EXT", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "en-US",
		"orderId": "wc-p0-" + uuid.NewString()[:12], "userId": user, "sessionId": sess,
		"actionEventId": actionEventID, "longReservation": false,
		"lines": []any{map[string]any{"categoryPriceId": tierWireID, "quantity": 1, "tariffPlanId": nil}},
		"email": email, "phone": "+420123456789", "fullName": "P0 Buyer",
		"promoCodeList": []any{code},
	}}
}

// p0RaceOrders sends every buyer's CREATE_ORDER_EXT at the same moment, then
// a PAY_ORDER for every order that was written, also at the same moment. It
// returns the CREATE_ORDER_EXT answers in buyer order.
func p0RaceOrders(t *testing.T, base string, buyers []p0Buyer) []map[string]interface{} {
	t.Helper()
	created := make([]map[string]interface{}, len(buyers))
	errs := make([]error, len(buyers))
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i, b := range buyers {
		wg.Add(1)
		go func(i int, body map[string]any) {
			defer wg.Done()
			<-gate
			created[i], errs[i] = postBil24Concurrent(base, body)
		}(i, b.order)
	}
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("CREATE_ORDER_EXT of buyer %d: %v", i, err)
		}
	}

	paid := make([]map[string]interface{}, len(buyers))
	gate = make(chan struct{})
	for i, b := range buyers {
		if code := numberField(t, created[i], "resultCode"); code != 0 {
			continue
		}
		cur, _ := created[i]["currency"].(string)
		body := map[string]any{
			"command": "PAY_ORDER", "fid": b.order["fid"], "token": b.order["token"], "locale": "en-US",
			"orderId": strconv.FormatInt(sc6OrderSystemID(t, "p0", created[i]), 10),
			"userId":  b.user, "sessionId": b.sess,
			"amount": numberField(t, created[i], "totalSum"), "currency": cur, "method": "woo_bank_card",
		}
		wg.Add(1)
		go func(i int, body map[string]any) {
			defer wg.Done()
			<-gate
			paid[i], errs[i] = postBil24Concurrent(base, body)
		}(i, body)
	}
	close(gate)
	wg.Wait()
	for i := range buyers {
		if paid[i] == nil {
			continue
		}
		if errs[i] != nil {
			t.Fatalf("PAY_ORDER of buyer %d: %v", i, errs[i])
		}
		if code := numberField(t, paid[i], "resultCode"); code != 0 {
			t.Errorf("PAY_ORDER of buyer %d resultCode = %v, want 0 (description %v)", i, code, paid[i]["description"])
		}
	}
	return created
}

// p0AssertOneUse checks that exactly one order was written with the code,
// that every other buyer got 101 with the text of the broken rule, and that
// the code has exactly one redemption and one paid order.
func p0AssertOneUse(t *testing.T, st *harnessState, promoID uuid.UUID, created []map[string]interface{}, refusalTexts ...string) {
	t.Helper()
	written := 0
	for i, resp := range created {
		code := numberField(t, resp, "resultCode")
		if code == 0 {
			written++
			if d := numberField(t, resp, "discount"); d <= 0 {
				t.Errorf("buyer %d: written order has discount %v, want > 0", i, d)
			}
			continue
		}
		desc, _ := resp["description"].(string)
		matched := false
		for _, want := range refusalTexts {
			if strings.Contains(strings.ToLower(desc), want) {
				matched = true
			}
		}
		if code != 101 || !matched {
			t.Errorf("buyer %d refused with %v %q, want 101 naming one of %q", i, code, desc, refusalTexts)
		}
	}
	if written != 1 {
		t.Errorf("CREATE_ORDER_EXT written with the code = %d of %d, want exactly 1", written, len(created))
	}
	var redemptions, paidOrders, ordersWithCode int
	if err := st.Pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM promo_code_redemptions WHERE promo_code_id = $1),
		(SELECT count(*) FROM orders WHERE promo_code_id = $1 AND status = 'paid'),
		(SELECT count(*) FROM orders WHERE promo_code_id = $1)`, promoID).
		Scan(&redemptions, &paidOrders, &ordersWithCode); err != nil {
		t.Fatal(err)
	}
	if redemptions != 1 || paidOrders != 1 || ordersWithCode != 1 {
		t.Errorf("redemptions = %d, paid orders = %d, orders with the code = %d; want 1, 1, 1",
			redemptions, paidOrders, ordersWithCode)
	}
}

// TestCompatBil24_P0_MaxUsesOneCode_ConcurrentOrders: five buyers on five
// sites send CREATE_ORDER_EXT with a max_uses=1 code at the same moment.
func TestCompatBil24_P0_MaxUsesOneCode_ConcurrentOrders(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	code := "P0MAX" + strings.ToUpper(uuid.NewString()[:6])
	promoID := p0InsertPromo(t, st, code, "max_uses")

	tag := uuid.NewString()[:8]
	buyers := make([]p0Buyer, p0PromoRaceBuyers)
	for i := range buyers {
		buyers[i] = p0GABuyer(t, base, st, "p0-max-"+strconv.Itoa(i)+"-"+tag+"@example.test", code)
	}
	created := p0RaceOrders(t, base, buyers)
	p0AssertOneUse(t, st, promoID, created, "used up", "no uses left")
}

// TestCompatBil24_P0_PerCustomerOneCode_ConcurrentOrders: one buyer (one
// e-mail, so one customer) has two carts open at once, one per date, and
// sends both with a max_uses_per_customer=1 code at the same moment. Two
// dates, because on one date the second order is refused anyway with
// bil24.open_order_exists.
func TestCompatBil24_P0_PerCustomerOneCode_ConcurrentOrders(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	code := "P0MINE" + strings.ToUpper(uuid.NewString()[:6])
	promoID := p0InsertPromo(t, st, code, "max_uses_per_customer")

	email := "p0-mine-" + uuid.NewString()[:8] + "@example.test"
	seat := sortedSeatLabels(st)[0]
	buyers := []p0Buyer{
		p0GABuyer(t, base, st, email, code),
		p0SeatBuyer(t, base, st, email, code, seat),
	}
	created := p0RaceOrders(t, base, buyers)
	p0AssertOneUse(t, st, promoID, created, "already used this promo code")
}

// TestCompatBil24_P0_OneBuyerTwoDatesInARow_MaxUsesOneCode — finding 1 of the
// review of PR #346: with no race at all, one buyer writes an order on one
// date with a max_uses=1 code and does not pay, then writes an order on
// another date with the same code. The first order counts: the second is
// refused (bil24.open_order_exists only ever covered ONE date).
func TestCompatBil24_P0_OneBuyerTwoDatesInARow_MaxUsesOneCode(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	code := "P0SEQ" + strings.ToUpper(uuid.NewString()[:6])
	promoID := p0InsertPromo(t, st, code, "max_uses")

	email := "p0-seq-" + uuid.NewString()[:8] + "@example.test"
	ga := p0GABuyer(t, base, st, email, code)
	seat := p0SeatBuyer(t, base, st, email, code, sortedSeatLabels(st)[0])

	first := postBil24(t, base, ga.order)
	if c := numberField(t, first, "resultCode"); c != 0 {
		t.Fatalf("first CREATE_ORDER_EXT resultCode = %v (%v)", c, first["description"])
	}
	second := postBil24(t, base, seat.order)
	desc, _ := second["description"].(string)
	if c := numberField(t, second, "resultCode"); c != 101 || !strings.Contains(strings.ToLower(desc), "used up") &&
		!strings.Contains(strings.ToLower(desc), "no uses left") {
		t.Errorf("second date of the same buyer = %v %q, want 101 naming the used-up code", c, desc)
	}
	var withCode int
	if err := st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE promo_code_id = $1`, promoID).Scan(&withCode); err != nil {
		t.Fatal(err)
	}
	if withCode != 1 {
		t.Errorf("orders written with the max_uses=1 code = %d, want 1", withCode)
	}
}
