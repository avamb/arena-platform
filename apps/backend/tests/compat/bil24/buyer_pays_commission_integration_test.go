//go:build integration

// buyer_pays_commission_integration_test.go — a buyer_pays organizer's
// platform commission on the Bil24 gateway's carts.
//
// The organizer's billing terms say fee_mode buyer_pays, so the buyer pays the
// platform's per-ticket commission inside the service charge, ON TOP of the
// channel's 5 %. The 5 % keeps the gateway's rounding (spec 20 §5: 0.945 is
// 0.95); the commission is whole minor units. RESERVATION, GET_CART and
// CREATE_ORDER_EXT must all answer the same charge, the order must be written
// with it (each ticket carrying its own commission), and PAY_ORDER of that
// total must be an exact payment.
package compat_bil24_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func TestCompatBil24_BuyerPaysCommissionJoinsTheCartCharge(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	tierID := fx528SeedTier(t, st)
	tierWireID := sc6TierWireID(t, st, tierID)
	actionEventID := mustActionEventID(t, st, st.GAsessID)

	// The billing account a sale of this session is routed to, switched to
	// buyer_pays. Torn down before the harness deletes the organization
	// (t.Cleanup is LIFO): billing_accounts reference it without a cascade.
	orgID := uuid.MustParse(st.OrgID)
	q := gen.New(st.Pool)
	venueCountry, orgCountry, err := q.GetSessionBillingCountry(ctx, uuid.MustParse(st.GAsessID))
	if err != nil {
		t.Fatalf("GetSessionBillingCountry: %v", err)
	}
	country := venueCountry
	if billing.NormalizeCountry(country) == "" {
		country = orgCountry
	}
	acc, err := billing.EnsureAccount(ctx, q, orgID, country)
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
			`DELETE FROM billing_accounts WHERE org_id = $1`,
		} {
			if _, err := st.Pool.Exec(context.Background(), sql, orgID); err != nil {
				t.Logf("buyer pays cleanup: %v", err)
			}
		}
	})
	if acc.Currency != fx528Currency {
		t.Fatalf("billing account currency %s, want %s (the sale currency)", acc.Currency, fx528Currency)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE billing_account_terms SET fee_mode = 'buyer_pays' WHERE account_id = $1`, acc.ID); err != nil {
		t.Fatalf("set buyer_pays: %v", err)
	}
	terms, err := billing.TermsAt(ctx, q, acc.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("TermsAt: %v", err)
	}
	commission := billing.NewBuyerCharge(terms).PerTicket(fx528Currency, fx528PriceMinor, false)
	if commission <= 0 {
		t.Fatalf("commission on an 18.90 EUR ticket = %d, want > 0", commission)
	}
	wantCharge := fx528ChargeMinor + commission // 0.95 (rounded 5 %) + commission
	wantTotal := fx528PriceMinor + wantCharge

	// ── RESERVATION ─────────────────────────────────────────────────────────
	sess, user := createGatewayUser(t, base, st, "harness-buyer-pays@example.test")
	reserved, _ := fx528Reserve(t, base, st, sess, user, actionEventID, tierWireID)
	if code := numberField(t, reserved, "resultCode"); code != 0 {
		t.Fatalf("RESERVATION resultCode = %v (description %v)", code, reserved["description"])
	}
	if got := money.Minor(numberField(t, reserved, "charge")); got != wantCharge {
		t.Errorf("RESERVATION charge = %d minor, want %d (95 + commission %d)", got, wantCharge, commission)
	}
	if got := money.Minor(numberField(t, reserved, "totalSum")); got != wantTotal {
		t.Errorf("RESERVATION totalSum = %d minor, want %d", got, wantTotal)
	}

	// ── GET_CART: the same charge ───────────────────────────────────────────
	cart := postBil24(t, base, map[string]any{
		"command": "GET_CART", "fid": st.ChannelFID, "token": st.ChannelToken,
		"locale": "ru-RU", "userId": user, "sessionId": sess,
	})
	if code := numberField(t, cart, "resultCode"); code != 0 {
		t.Fatalf("GET_CART resultCode = %v (description %v)", code, cart["description"])
	}
	if got := money.Minor(numberField(t, cart, "chargeAmount")); got != wantCharge {
		t.Errorf("GET_CART chargeAmount = %d minor, want %d", got, wantCharge)
	}
	if got := money.Minor(numberField(t, cart, "totalSum")); got != wantTotal {
		t.Errorf("GET_CART totalSum = %d minor, want %d", got, wantTotal)
	}

	// ── CREATE_ORDER_EXT: the order is written with it ──────────────────────
	externalRef := "wc-buyer-pays-" + uuid.NewString()[:8]
	created := postBil24(t, base, map[string]any{
		"command": "CREATE_ORDER_EXT", "fid": st.ChannelFID, "token": st.ChannelToken,
		"locale": "ru-RU", "orderId": externalRef, "userId": user, "sessionId": sess,
		"currency": fx528Currency, "total": money.Major(wantTotal), "actionEventId": actionEventID,
		"longReservation": false,
		"lines": []any{map[string]any{
			"categoryPriceId": tierWireID, "quantity": 1, "tariffPlanId": nil,
		}},
		"email": "buyer-pays@example.com", "phone": "+420123456780", "fullName": "Petra Novak",
		"chargePercent": 5, "promoCodes": []any{},
	})
	if code := numberField(t, created, "resultCode"); code != 0 {
		t.Fatalf("CREATE_ORDER_EXT resultCode = %v (description %v)", code, created["description"])
	}
	if got := money.Minor(numberField(t, created, "charge")); got != wantCharge {
		t.Errorf("CREATE_ORDER_EXT charge = %d minor, want %d", got, wantCharge)
	}
	orderID := sc6OrderID(t, st, "buyer_pays", created)
	var charge, total, itemCharge int64
	if err := st.Pool.QueryRow(ctx, `SELECT charge, total FROM orders WHERE id = $1`, orderID).Scan(&charge, &total); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if charge != wantCharge || total != wantTotal {
		t.Errorf("orders charge %d total %d, want %d / %d", charge, total, wantCharge, wantTotal)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT charge FROM order_items WHERE order_id = $1`, orderID).Scan(&itemCharge); err != nil {
		t.Fatalf("read order item: %v", err)
	}
	if itemCharge != wantCharge {
		t.Errorf("order_items.charge = %d, want %d", itemCharge, wantCharge)
	}

	// ── PAY_ORDER of the shown total is an exact payment ────────────────────
	paid := postBil24(t, base, map[string]any{
		"command": "PAY_ORDER", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "ru-RU",
		"orderId": orderID.String(), "userId": user, "sessionId": sess,
		"amount": money.Major(wantTotal), "currency": fx528Currency, "method": "woo_bank_card",
	})
	if code := numberField(t, paid, "resultCode"); code != 0 {
		t.Fatalf("PAY_ORDER resultCode = %v (description %v)", code, paid["description"])
	}
	var mismatches int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1 AND type = 'amount_mismatch'`, orderID).Scan(&mismatches); err != nil {
		t.Fatal(err)
	}
	if mismatches != 0 {
		t.Errorf("PAY_ORDER of the shown total recorded %d amount_mismatch event(s)", mismatches)
	}

	// The platform bills the commission, not the channel's 5 %.
	ev := billing.Event{
		Kind: billing.KindTicketSold, OccurredAt: terms.At, Currency: fx528Currency,
		UnitPriceMinor: fx528PriceMinor, ServiceChargeMinor: itemCharge,
	}
	ev.PricingClass, _ = billing.ClassifyLowPrice(terms, fx528Currency, fx528PriceMinor)
	c, err := billing.Rate(terms, ev)
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if c.AmountMinor != commission {
		t.Errorf("billing rates the ticket at %d, want the commission %d", c.AmountMinor, commission)
	}
}
