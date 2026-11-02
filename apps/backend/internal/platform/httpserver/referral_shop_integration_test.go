//go:build integration

package httpserver

// referral_shop_integration_test.go — "Refer a friend" for the organizer's
// merchandise shop (spec 52 sections 0 R-RF6, 3.5 and 4) through the real
// router, a live database and the Stripe stub: the shop programme's routes and
// rights, the buyer's personal code after a paid shop order, a friend's
// purchase with the code (the discount is a percent of the goods, never of the
// delivery), the cashback paid as a partial refund of the referrer's own shop
// payment through the refund engine, and the refusals (own code, unknown code,
// paused programme, another organization).
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe):
//
//	go test -tags integration -run TestReferralShop ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// referralShopCleanup removes what the programme wrote before the shop
// fixture's own cleanup deletes the orders (rewards, codes and orders point at
// each other).
func referralShopCleanup(f *shopFixture) {
	f.t.Cleanup(func() {
		c := context.Background()
		orgs := []uuid.UUID{f.orgA, f.orgB}
		for _, st := range []string{
			`DELETE FROM worker_jobs WHERE job_type LIKE 'referral.%' AND payload->>'reward_id' IN (
			   SELECT id::text FROM referral_rewards WHERE org_id = ANY($1))`,
			`UPDATE shop_orders SET promo_code_id = NULL WHERE org_id = ANY($1)`,
			`DELETE FROM referral_rewards WHERE org_id = ANY($1)`,
			`DELETE FROM promo_codes WHERE org_id = ANY($1)`,
			`DELETE FROM referral_programs WHERE org_id = ANY($1)`,
		} {
			if _, err := f.pool.Exec(c, st, orgs); err != nil {
				fmt.Printf("referral shop cleanup %q: %v\n", st, err)
			}
		}
	})
}

// buy places a shop order with explicit buyer details and an optional personal
// code; it returns the HTTP status and the body.
func (f *shopFixture) buy(variantID string, qty int, email, phone, promo string) (int, map[string]any) {
	f.t.Helper()
	promoField := ""
	if promo != "" {
		promoField = fmt.Sprintf(`,"promo_code":%q`, promo)
	}
	code, body, _ := f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
		"items":[{"variant_id":%q,"qty":%d}],
		"buyer":{"name":"Buyer","email":%q,"phone":%q},
		"ship":{"address1":"Herzl 1","city":"Haifa","postal":"3100000","country":"IL"},
		"locale":"ru","return_url":"%s/checkout"%s}`,
		variantID, qty, email, phone, shopTestBase, promoField))
	return code, body
}

func randomShopContact() (string, string) {
	return "rf-" + uuid.NewString()[:8] + "@example.test", fmt.Sprintf("+97254%07d", rand.IntN(10_000_000))
}

func TestReferralShop_ProgrammeCodeDiscountAndCashback(t *testing.T) {
	f := newShopFixture(t)
	referralShopCleanup(f)
	ctx := context.Background()
	_, variant := f.openShop(4500, 20)
	domain := "rf-" + strings.ToLower(uuid.NewString()[:8]) + ".example.test"
	f.exec(`UPDATE organizations SET shop_domain = $2 WHERE id = $1`, f.orgA, domain)

	programPath := f.orgPath(f.orgA, "/referral-program")

	// --- the programme: absent, refused without the terms, started --------
	if code, body, _ := f.do(http.MethodGet, programPath, f.ownerA, ""); code != http.StatusNotFound || shopErrCode(body) != "referral.program_not_found" {
		t.Fatalf("GET programme before any = %d %v; want 404 referral.program_not_found", code, body)
	}
	catalogActive := func() bool {
		code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", "")
		if code != http.StatusOK {
			t.Fatalf("public catalog = %d %v", code, body)
		}
		return body["settings"].(map[string]any)["referral_active"] == true
	}
	if catalogActive() {
		t.Fatal("referral_active is true before the programme exists")
	}
	if code, body, _ := f.do(http.MethodPut, programPath, f.ownerA, `{"friend_discount_pct":10,"cashback_pct":5}`); code != http.StatusUnprocessableEntity || shopErrCode(body) != "referral.terms_not_accepted" {
		t.Fatalf("PUT without the terms = %d %v; want 422 referral.terms_not_accepted", code, body)
	}
	code, body, _ := f.do(http.MethodPut, programPath, f.ownerA, `{"friend_discount_pct":10,"cashback_pct":5,"accept_terms":true}`)
	if code != http.StatusCreated {
		t.Fatalf("PUT programme = %d %v; want 201", code, body)
	}
	prog := body["program"].(map[string]any)
	if prog["scope"] != "shop" || prog["event_id"] != nil || prog["status"] != "active" {
		t.Fatalf("programme = %v; want an active shop programme without an event", prog)
	}
	if !catalogActive() {
		t.Fatal("referral_active is false while the programme is active")
	}
	// Another organization's owner has no access to this shop's programme.
	if code, body, _ := f.do(http.MethodGet, programPath, f.ownerB, ""); code != http.StatusForbidden {
		t.Fatalf("foreign owner GET programme = %d %v; want 403", code, body)
	}

	// --- the referrer buys and gets a code only once the order is paid ----
	refEmail, refPhone := randomShopContact()
	code, body = f.buy(variant, 2, refEmail, refPhone, "")
	if code != http.StatusCreated {
		t.Fatalf("referrer checkout = %d %v", code, body)
	}
	refToken := body["order_token"].(string)
	referralURL := "/v1/public/shop/checkout/" + refToken + "/referral"
	if code, body, _ := f.do(http.MethodGet, referralURL, "", ""); code != http.StatusNotFound || shopErrCode(body) != "referral.not_available" {
		t.Fatalf("code of an unpaid order = %d %v; want 404 referral.not_available", code, body)
	}
	if code, body, _ := f.do(http.MethodGet, "/v1/public/shop/checkout/"+strings.Repeat("0", 64)+"/referral", "", ""); code != http.StatusNotFound || shopErrCode(body) != "referral.not_available" {
		t.Fatalf("code of an unknown token = %d %v; want the same 404", code, body)
	}
	f.pay(refToken)
	code, body, hdr := f.do(http.MethodGet, referralURL, "", "")
	if code != http.StatusOK {
		t.Fatalf("code of a paid order = %d %v", code, body)
	}
	if cc := hdr.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q; the buyer's code must not be cached", cc)
	}
	personal := body["code"].(string)
	if personal == "" || body["friend_discount_pct"].(float64) != 10 || body["cashback_pct"].(float64) != 5 {
		t.Fatalf("code answer = %v", body)
	}
	share, _ := body["share_url"].(string)
	for _, want := range []string{"https://" + domain, "promo=" + personal, "utm_source=referral", "utm_medium=friend"} {
		if !strings.Contains(share, want) {
			t.Fatalf("share_url %q lacks %q", share, want)
		}
	}
	if _, again, _ := f.do(http.MethodGet, referralURL, "", ""); again["code"] != personal {
		t.Fatalf("second call returned %v; want the same code %q", again["code"], personal)
	}

	// --- refusals at the friend's checkout --------------------------------
	if code, body := f.buy(variant, 1, refEmail, refPhone, personal); code != http.StatusUnprocessableEntity || shopErrCode(body) != "promo.own_referral_code" {
		t.Fatalf("own code = %d %v; want 422 promo.own_referral_code", code, body)
	}
	friendEmail, friendPhone := randomShopContact()
	if code, body := f.buy(variant, 1, friendEmail, friendPhone, "NO-SUCH-CODE"); code != http.StatusUnprocessableEntity || shopErrCode(body) != "promo.not_applicable" {
		t.Fatalf("unknown code = %d %v; want 422 promo.not_applicable", code, body)
	}

	// --- the friend buys with the code (any letter case) ------------------
	code, body = f.buy(variant, 1, friendEmail, friendPhone, strings.ToLower(personal))
	if code != http.StatusCreated {
		t.Fatalf("friend checkout = %d %v", code, body)
	}
	// 10 percent of the goods (4500) is 450; the delivery fee (5000) is never discounted.
	if body["subtotal"].(float64) != 4500 || body["discount"].(float64) != 450 || body["total"].(float64) != 9050 {
		t.Fatalf("friend order subtotal/discount/total = %v/%v/%v; want 4500/450/9050", body["subtotal"], body["discount"], body["total"])
	}
	friendToken := body["order_token"].(string)
	friendOrder, err := f.q.GetShopOrderByToken(ctx, friendToken)
	if err != nil || friendOrder.PromoCodeID == nil || friendOrder.Discount != 450 || friendOrder.Total != 9050 {
		t.Fatalf("stored friend order = %+v (%v); want the code and the discount stored", friendOrder, err)
	}
	if code, body, _ := f.do(http.MethodGet, "/v1/public/shop/checkout/"+friendToken, "", ""); code != http.StatusOK || body["discount"].(float64) != 450 {
		t.Fatalf("friend order status = %d %v; want the discount shown", code, body)
	}
	f.pay(friendToken)

	var rewardID uuid.UUID
	var amount int64
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT id, amount_minor, state FROM referral_rewards WHERE org_id = $1 AND friend_shop_order_id = $2`,
		f.orgA, friendOrder.ID).Scan(&rewardID, &amount, &state); err != nil {
		t.Fatalf("no reward for the friend order: %v", err)
	}
	// 5 percent of what the friend paid for the goods (4500 - 450 = 4050), rounded down.
	if amount != 202 || state != referral.StateDue {
		t.Fatalf("reward = %d in state %q; want 202 due", amount, state)
	}

	// --- the cashback: a partial refund of the referrer's own shop payment -
	svc := referral.NewService(referral.Options{Pool: f.pool, Engine: f.srv.refundEngine()})
	if err := svc.Payout(ctx, rewardID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT state FROM referral_rewards WHERE id = $1`, rewardID).Scan(&state); err != nil || state != referral.StatePaid {
		t.Fatalf("reward state after payout = %q (%v); want paid", state, err)
	}
	refOrder, err := f.q.GetShopOrderByToken(ctx, refToken)
	if err != nil {
		t.Fatalf("referrer order: %v", err)
	}
	var origin string
	var refunded int64
	if err := f.pool.QueryRow(ctx, `SELECT origin, amount FROM refunds WHERE shop_order_id = $1`, refOrder.ID).Scan(&origin, &refunded); err != nil {
		t.Fatalf("cashback refund row: %v", err)
	}
	if origin != "referral_cashback" || refunded != 202 {
		t.Fatalf("refund = %s %d; want referral_cashback 202", origin, refunded)
	}
	// Cashback is not the return of goods: the referrer's order stays as it was.
	if refOrder.Status != shop.StatusPaid || refOrder.RefundedMinor != 0 {
		t.Fatalf("referrer order after cashback: status %q refunded %d; want paid and 0", refOrder.Status, refOrder.RefundedMinor)
	}
	// A repeat of the payout job is a no-op (the Idempotency-Key is the reward's).
	if err := svc.Payout(ctx, rewardID); err != nil {
		t.Fatalf("second Payout: %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM refunds WHERE shop_order_id = $1`, refOrder.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("refunds of the referrer order after a repeated payout = %d (%v); want 1", n, err)
	}

	// --- what the organizer sees ------------------------------------------
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/referral-rewards"), f.ownerA, "")
	items, _ := body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("rewards = %d %v; want one item", code, body)
	}
	item := items[0].(map[string]any)
	if item["cashback_minor"].(float64) != 202 || item["state"] != "paid" || item["code"] != personal {
		t.Fatalf("reward item = %v", item)
	}
	if _, leaks := item["buyer_email"]; leaks {
		t.Fatalf("reward item leaks a buyer e-mail: %v", item)
	}
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/referral-program/summary"), f.ownerA, "")
	if code != http.StatusOK || body["event_id"] != nil || body["codes_issued"].(float64) != 1 || body["friends"].(float64) != 1 {
		t.Fatalf("summary = %d %v; want one code, one friend, no event", code, body)
	}
	if code, _, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/referral-rewards"), f.ownerB, ""); code != http.StatusForbidden {
		t.Fatalf("foreign owner GET rewards = %d; want 403", code)
	}

	// --- pause: a new friend is told the code does not work now -----------
	if code, body, _ := f.do(http.MethodPost, programPath+"/pause", f.ownerA, ""); code != http.StatusOK || body["program"].(map[string]any)["status"] != "paused" {
		t.Fatalf("pause = %d %v", code, body)
	}
	if catalogActive() {
		t.Fatal("referral_active is true while the programme is paused")
	}
	email2, phone2 := randomShopContact()
	if code, body := f.buy(variant, 1, email2, phone2, personal); code != http.StatusUnprocessableEntity || shopErrCode(body) != "promo.not_active" {
		t.Fatalf("code on a paused programme = %d %v; want 422 promo.not_active", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, programPath+"/resume", f.ownerA, ""); code != http.StatusOK || body["program"].(map[string]any)["status"] != "active" {
		t.Fatalf("resume = %d %v", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, programPath+"/end", f.ownerA, ""); code != http.StatusOK || body["program"].(map[string]any)["status"] != "ended" {
		t.Fatalf("end = %d %v", code, body)
	}
}
