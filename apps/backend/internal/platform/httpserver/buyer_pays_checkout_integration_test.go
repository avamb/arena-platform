//go:build integration

// buyer_pays_checkout_integration_test.go — live-DB, real-router coverage of
// the buyer's platform commission at the widget checkout (fee_mode
// buyer_pays on the organizer's billing terms):
//
//   - the feed shows the commission per category (service_charge_minor);
//   - checkout/quote adds it on top of the channel's 1.25 %;
//   - checkout/start stores the same total and sends it to the payment page;
//   - the order carries it, each ticket its own commission in
//     order_items.charge;
//   - rating the order's tickets the way the billing job does charges exactly
//     the commission, never the organizer's percentage;
//   - organizer_pays terms and a free cart add nothing.
//
// Prerequisites: DATABASE_URL against a migrated database (migration 0157
// seeds the standard-2026 price list).
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ -run TestBuyerPaysCheckout
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// buyerPaysAccount gives the fixture's organization the billing account a sale
// of its session is routed to (no venue country: the default EUR route) and
// sets its terms to feeMode. The returned teardown must be deferred AFTER the
// fixture's own cleanup: billing_accounts reference organizations without a
// cascade, so they must go first.
func buyerPaysAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *hostedOrgFixture, feeMode string) (gen.BillingAccountRow, func()) {
	t.Helper()
	q := gen.New(pool)
	acc, err := billing.EnsureAccount(ctx, q, f.orgID, "")
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	if acc.Currency != "EUR" {
		t.Fatalf("account currency %s, want the default EUR route", acc.Currency)
	}
	setFeeMode(t, ctx, pool, acc.ID, feeMode)
	return acc, func() {
		for _, sql := range []string{
			`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
			`DELETE FROM billing_accounts WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), sql, f.orgID); err != nil {
				t.Logf("buyer pays cleanup (%.50s): %v", sql, err)
			}
		}
	}
}

func setFeeMode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID, feeMode string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE billing_account_terms SET fee_mode = $2 WHERE account_id = $1`, accountID, feeMode); err != nil {
		t.Fatalf("set fee_mode %s: %v", feeMode, err)
	}
}

// quoteCommission decodes the platform_commission the quote reports.
func quoteCommission(t *testing.T, f *hostedOrgFixture, srv *Server, payload map[string]any) (quoteBody, int64) {
	t.Helper()
	code, body := f.quote(t, srv, payload)
	if code != http.StatusOK {
		t.Fatalf("checkout/quote = %d, want 200; body: %s", code, body)
	}
	var q quoteBody
	var extra struct {
		Pricing struct {
			PlatformCommission int64 `json:"platform_commission"`
		} `json:"pricing"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	if err := json.Unmarshal(body, &extra); err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	return q, extra.Pricing.PlatformCommission
}

func TestBuyerPaysCheckout_CommissionFromQuoteToOrder(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "bp1")
	defer f.cleanup()
	defer f.cleanupPromos()
	acc, dropAccount := buyerPaysAccount(t, ctx, pool, f, billing.FeeModeBuyerPays)
	defer dropAccount()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	q := gen.New(pool)

	// The commission one 25.00 EUR ticket carries, from the price list the
	// migration seeded: the same code the organizer's service-fee-quote uses.
	terms, err := billing.TermsAt(ctx, q, acc.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("TermsAt: %v", err)
	}
	perTicket := billing.NewBuyerCharge(terms).PerTicket("EUR", 2500, false)
	if perTicket <= 0 {
		t.Fatalf("standard-2026 commission on a 25.00 EUR ticket = %d, want > 0", perTicket)
	}

	// ── 1. The feed shows it next to the category ───────────────────────────
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed event = %d; body: %s", rec.Code, rec.Body.String())
	}
	var feed struct {
		Event struct {
			Sessions []struct {
				Tiers []struct {
					ID                       string `json:"id"`
					ServiceChargeMinor       int64  `json:"service_charge_minor"`
					SeatedServiceChargeMinor int64  `json:"seated_service_charge_minor"`
				} `json:"tiers"`
			} `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	if len(feed.Event.Sessions) != 1 || len(feed.Event.Sessions[0].Tiers) != 1 {
		t.Fatalf("feed sessions/tiers: %s", rec.Body.String())
	}
	if got := feed.Event.Sessions[0].Tiers[0].ServiceChargeMinor; got != perTicket {
		t.Errorf("feed service_charge_minor = %d, want %d", got, perTicket)
	}
	if got := feed.Event.Sessions[0].Tiers[0].SeatedServiceChargeMinor; got < perTicket {
		t.Errorf("feed seated_service_charge_minor = %d, want at least the GA %d", got, perTicket)
	}

	// ── 2. The quote: channel 1.25 % of 50.00 floors to 62, plus 2 commissions
	quote, commission := quoteCommission(t, f, srv, f.gaCart(2, "", ""))
	wantFee := int64(62) + 2*perTicket
	if commission != 2*perTicket || quote.Pricing.PlatformFee != wantFee || quote.Pricing.Total != 5000+wantFee {
		t.Fatalf("quote = %+v commission %d, want fee %d (62 + 2×%d) and total %d",
			quote.Pricing, commission, wantFee, perTicket, 5000+wantFee)
	}

	// A promo code lowers the percentage's base, never the commission.
	f.insertPromo(t, ctx, "BPTEN", "percent", 10, "", "")
	promo, promoCommission := quoteCommission(t, f, srv, f.gaCart(2, "BPTEN", ""))
	if promoCommission != 2*perTicket || promo.Pricing.PlatformFee != 56+2*perTicket {
		t.Errorf("promo quote = %+v commission %d, want fee 56 + %d", promo.Pricing, promoCommission, 2*perTicket)
	}

	// A cart the code makes free pays no commission and stays free.
	f.insertPromo(t, ctx, "BPFREE", "percent", 100, "", "")
	free, freeCommission := quoteCommission(t, f, srv, f.gaCart(2, "BPFREE", ""))
	if free.Pricing.Total != 0 || freeCommission != 0 {
		t.Errorf("free cart quote = %+v commission %d, want total 0", free.Pricing, freeCommission)
	}

	// ── 3. checkout/start stores the quoted total and sends it to the page ──
	code, body := f.startWithPromo(t, srv, 2, "")
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d, want 201; body: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	csID := uuid.MustParse(start.CheckoutSession.ID)
	cs, err := q.GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("GetCheckoutSessionByID: %v", err)
	}
	if cs.PlatformFee == nil || *cs.PlatformFee != quote.Pricing.PlatformFee || *cs.Total != quote.Pricing.Total {
		t.Fatalf("start stored fee %v total %v, quote said %d / %d", cs.PlatformFee, cs.Total,
			quote.Pricing.PlatformFee, quote.Pricing.Total)
	}
	if got, want := stripe.lastForm(t).Get("line_items[0][price_data][unit_amount]"), strconv.FormatInt(quote.Pricing.Total, 10); got != want {
		t.Errorf("payment page amount = %s, quote said %s", got, want)
	}

	// ── 4. The order: charge = fee, each ticket carries its commission ──────
	order, err := q.GetOrderByCheckoutSession(ctx, csID)
	if err != nil {
		t.Fatalf("GetOrderByCheckoutSession: %v", err)
	}
	if order.Charge != quote.Pricing.PlatformFee || order.Total != quote.Pricing.Total {
		t.Errorf("order charge %d total %d, want %d / %d", order.Charge, order.Total,
			quote.Pricing.PlatformFee, quote.Pricing.Total)
	}
	rows, err := pool.Query(ctx, `SELECT unit_price, discount, charge FROM order_items WHERE order_id = $1 ORDER BY ordinal`, order.ID)
	if err != nil {
		t.Fatalf("order items: %v", err)
	}
	type item struct{ price, discount, charge int64 }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.price, &it.discount, &it.charge); err != nil {
			t.Fatal(err)
		}
		items = append(items, it)
	}
	rows.Close()
	if len(items) != 2 {
		t.Fatalf("order items = %d, want 2", len(items))
	}
	var itemCharges int64
	for i, it := range items {
		itemCharges += it.charge
		if it.charge < perTicket {
			t.Errorf("item %d charge %d is below its commission %d", i, it.charge, perTicket)
		}
	}
	if itemCharges != order.Charge {
		t.Errorf("item charges sum to %d, order charge %d", itemCharges, order.Charge)
	}

	// ── 5. Rating each ticket as the billing job does: the commission only ──
	for i, it := range items {
		ev := billing.Event{
			Kind: billing.KindTicketSold, OccurredAt: terms.At, Currency: "EUR",
			UnitPriceMinor: it.price, DiscountMinor: it.discount, ServiceChargeMinor: it.charge,
		}
		ev.PricingClass, _ = billing.ClassifyLowPrice(terms, "EUR", it.price)
		c, err := billing.Rate(terms, ev)
		if err != nil {
			t.Fatalf("Rate item %d: %v", i, err)
		}
		if c.AmountMinor != perTicket || c.RuleType != billing.RuleBuyerServiceCharge {
			t.Errorf("item %d rated %d %s, want the commission %d (the rest of %d is the organizer's percentage)",
				i, c.AmountMinor, c.RuleType, perTicket, it.charge)
		}
	}

	// ── 6. organizer_pays terms add nothing ─────────────────────────────────
	setFeeMode(t, ctx, pool, acc.ID, billing.FeeModeOrganizerPays)
	plain, plainCommission := quoteCommission(t, f, srv, f.gaCart(2, "", ""))
	if plainCommission != 0 || plain.Pricing.PlatformFee != 62 || plain.Pricing.Total != 5062 {
		t.Errorf("organizer_pays quote = %+v commission %d, want fee 62 total 5062", plain.Pricing, plainCommission)
	}
}
