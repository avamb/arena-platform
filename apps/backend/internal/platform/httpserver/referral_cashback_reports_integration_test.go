//go:build integration

package httpserver

// referral_cashback_reports_integration_test.go — the "Refer a friend"
// cashback (spec 52) in the sales reports, through the real router and the
// real queries (owner decision 2026-11-03): it is money the organizer
// returned to a referrer, so the session and event summaries show it on its
// own line and take it off the net, the refunds to buyers stay without it,
// the post-event report carries it as its own line, and the order cards say
// which friend paid off (referrer) and which code was used (friend).
//
// The stand's case: two orders paid 252,50 and 454,50; the friend's order
// gets a refund of 207,05; the referrer got 45,45 back. The organizer kept
// 454,50, not 499,95.
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestReferralCashback_InSalesReports(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	ctx := context.Background()
	f := newBotInviteFixture(t, pool)
	seed := seedPromoOrg(t, pool, f.orgID)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	jwt, _, err := auth.IssueJWT(botTestJWTSecret, f.ownerID, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	session, event := seed.sessA[0], seed.eventA
	referrer, referrerNum := seed.paidOrder(session, event, "EUR", 45450, "RC Referrer")
	friend, friendNum := seed.paidOrder(session, event, "EUR", 25250, "RC Friend")
	program, code, refundID, cashbackID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	codeText := "RC-" + strings.ToUpper(seed.suffix)

	// Runs before the seed's cleanup (cleanups run last-in first): these rows
	// hold foreign keys to its orders and checkout sessions.
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM event_reports WHERE org_id = $1`,
			`DELETE FROM referral_rewards WHERE org_id = $1`,
			`DELETE FROM refunds WHERE org_id = $1`,
			`DELETE FROM payment_intents WHERE org_id = $1`,
			`UPDATE orders SET promo_code_id = NULL WHERE org_id = $1`,
			`UPDATE promo_codes SET referrer_order_id = NULL WHERE org_id = $1`,
			`DELETE FROM promo_codes WHERE org_id = $1 AND origin = 'referral'`,
			`DELETE FROM referral_programs WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(c, sql, f.orgID); err != nil {
				t.Logf("cashback reports cleanup: %s: %v", sql, err)
			}
		}
	})

	intent := func(order uuid.UUID, amount int64) uuid.UUID {
		t.Helper()
		var cs, pi uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, order).Scan(&cs); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO payment_intents (checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state)
		      VALUES ($1, $2, 'stripe', $3, $4, 'EUR', 'succeeded') RETURNING id`, cs, f.orgID, "cs_test_rc_"+uuid.NewString(), amount).Scan(&pi); err != nil {
			t.Fatal(err)
		}
		return pi
	}
	referrerPI, friendPI := intent(referrer, 45450), intent(friend, 25250)

	seed.exec(`INSERT INTO referral_programs (id, org_id, scope, event_id, friend_discount_pct, cashback_pct, terms_accepted_at)
	           VALUES ($1, $2, 'event', $3, 10, 10, now())`, program, f.orgID, event)
	seed.exec(`INSERT INTO promo_codes (id, org_id, code, discount_type, discount_value, origin, referral_program_id, referrer_order_id)
	           VALUES ($1, $2, $3, 'percent', 10, 'referral', $4, $5)`, code, f.orgID, codeText, program, referrer)
	seed.exec(`UPDATE orders SET promo_code_id = $1, status = 'partially_refunded' WHERE id = $2`, code, friend)
	// A refund to the friend (a buyer) and the cashback to the referrer.
	seed.exec(`INSERT INTO refunds (id, payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, succeeded_at)
	           VALUES ($1, $2, $3, $4, 20705, 'EUR', 'requested_by_customer', 'succeeded', 'provider', now())`, refundID, friendPI, f.orgID, friend)
	paidAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seed.exec(`INSERT INTO refunds (id, payment_intent_id, org_id, order_id, amount, currency, reason, state, settlement, origin, succeeded_at)
	           VALUES ($1, $2, $3, $4, 4545, 'EUR', 'referral_cashback', 'succeeded', 'provider', 'referral_cashback', $5)`,
		cashbackID, referrerPI, f.orgID, referrer, paidAt)
	seed.exec(`INSERT INTO referral_rewards (org_id, program_id, promo_code_id, referrer_order_id, friend_order_id,
	                                         amount_minor, currency, cashback_pct, cap_pct, state, refund_id)
	           VALUES ($1, $2, $3, $4, $5, 4545, 'EUR', 10, 100, 'paid', $6)`, f.orgID, program, code, referrer, friend, cashbackID)

	get := func(path string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, api.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+jwt)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body := decodeJSONBody(t, res)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d: %v", path, res.StatusCode, body)
		}
		return body
	}
	eur := func(label string, body map[string]any) map[string]any {
		t.Helper()
		list, _ := body["money"].([]any)
		for _, m := range list {
			if mm, _ := m.(map[string]any); mm["currency"] == "EUR" {
				return mm
			}
		}
		t.Fatalf("%s: no EUR money in %v", label, body["money"])
		return nil
	}
	org := "/v1/organizations/" + f.orgID.String()
	for label, path := range map[string]string{
		"session summary":     org + "/sessions/" + session.String() + "/summary",
		"event summary":       org + "/events/" + event.String() + "/summary",
		"event sales summary": org + "/events/sales-summary?ids=" + event.String(),
	} {
		body := get(path)
		if label == "event sales summary" {
			items, _ := body["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("%s: items = %v", label, body)
			}
			body, _ = items[0].(map[string]any)
		}
		m := eur(label, body)
		if m["paid"] != 70700.0 || m["refunded"] != 20705.0 || m["cashback"] != 4545.0 || m["cashback_refunds"] != 1.0 || m["net"] != 45450.0 {
			t.Errorf("%s: money = %v; want paid 70700, refunded 20705, cashback 4545 in 1, net 45450", label, m)
		}
		// The refund groups are refunds to buyers only.
		var refunded float64
		for _, r := range body["refunds"].([]any) {
			refunded += r.(map[string]any)["amount"].(float64)
		}
		if refunded != 20705 {
			t.Errorf("%s: refund groups sum to %v; the cashback must not be one of them", label, refunded)
		}
	}
	// The sources of the session add up to the net.
	var sources float64
	for _, s := range get(org + "/sessions/" + session.String() + "/summary")["sources"].([]any) {
		for _, r := range s.(map[string]any)["revenue"].([]any) {
			sources += r.(map[string]any)["amount_minor"].(float64)
		}
	}
	if sources != 45450 {
		t.Errorf("sources add up to %v, want the net 45450", sources)
	}

	// The referrer's card: the friend who paid off, with the date.
	card := get(org + "/orders/" + referrer.String())
	ref, _ := card["referral"].(map[string]any)
	cb, _ := ref["cashback"].([]any)
	if len(cb) != 1 {
		t.Fatalf("referrer's card referral = %v", card["referral"])
	}
	c := cb[0].(map[string]any)
	if c["state"] != "paid" || c["amount"] != 4545.0 || c["friend_order_number"] != float64(friendNum) ||
		c["friend_order_id"] != friend.String() || c["paid_at"] != paidAt.Format(time.RFC3339) {
		t.Errorf("referrer's cashback = %v", c)
	}
	// The friend's card: bought with the referrer's code.
	card = get(org + "/orders/" + friend.String())
	ref, _ = card["referral"].(map[string]any)
	used, _ := ref["used_code"].(map[string]any)
	if used["code"] != codeText || used["referrer_order_number"] != float64(referrerNum) || used["referrer_order_id"] != referrer.String() {
		t.Errorf("friend's card referral = %v", card["referral"])
	}
	// An order with no part in the programme says nothing.
	plain, _ := seed.paidOrder(session, event, "EUR", 1000, "RC Plain")
	if card = get(org + "/orders/" + plain.String()); card["referral"] != nil {
		t.Errorf("plain order referral = %v, want null", card["referral"])
	}
	// Another organization's card says nothing about these orders.
	q := gen.New(pool)
	if rows, err := q.ListOrderReferralCashback(ctx, referrer, uuid.New()); err != nil || len(rows) != 0 {
		t.Errorf("foreign org lookup = %v, %v", rows, err)
	}

	// The post-event report: the cashback is its own line (migration 0195).
	agg, err := q.AggregateReferralCashbackForEvent(ctx, event)
	if err != nil || agg.Quantity != 1 || agg.GrossAmount != 4545 || agg.Currency != "EUR" {
		t.Fatalf("AggregateReferralCashbackForEvent = %+v, %v", agg, err)
	}
	report, err := q.InsertEventReport(ctx, event, f.orgID, nil, nil)
	if err != nil {
		t.Fatalf("InsertEventReport: %v", err)
	}
	if _, err := q.InsertEventReportLine(ctx, report.ID, "referral_cashback", agg.Quantity, agg.GrossAmount, agg.NetAmount, agg.Currency); err != nil {
		t.Fatalf("a referral_cashback report line must pass the category CHECK: %v", err)
	}
}
