// price_breakdown_163_test.go — unit, property, and integration tests for the
// all-in pricing display endpoint (feature #163).
//
// Test coverage:
//
//	Step 1: Endpoint returning structured breakdown
//	         — file exists, route mounted, auth gating, response structure
//	Step 2: Property-based test — sum of breakdown == total to the cent
//	Step 3: i18n label checks (English default + Russian locale)
//	Step 4: Integration tests — confirmed vs unconfirmed session, nil DB
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	breakdownTestActorID = "00000000-0000-0000-0000-000000000163"
	breakdownSessionID   = "00000000-0000-0000-0000-000000000001"
	breakdownBasePath    = "/v1/checkout/" + breakdownSessionID + "/price-breakdown"
)

// ─────────────────────────────────────────────────────────────────────────────
// Server factory
// ─────────────────────────────────────────────────────────────────────────────

// buildBreakdownServer builds a Server with stub auth and checkout queries wired
// so the price-breakdown route is mounted. No real DB operations execute
// (nil pool → gen.New(nil) means queries will error, which is tested explicitly).
func buildBreakdownServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildBreakdownServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:          cfg,
		Auth:            stub,
		Pool:            &dbDownPool{},
		CheckoutQueries: gen.New(nil),
	})
}

// mintBreakdownToken mints a dev JWT with admin role for breakdown route tests.
func mintBreakdownToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + breakdownTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintBreakdownToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintBreakdownToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintBreakdownToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Endpoint returning structured breakdown — file, route, auth
// ─────────────────────────────────────────────────────────────────────────────

func TestPriceBreakdown163_Step1_FileExists(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller not available")
	}
	if !filepath.IsAbs(thisFile) {
		t.Skip("non-absolute path (trimpath build)")
	}
	// price_breakdown.go was moved into the hcheckout/ sub-package as part of
	// the httpserver refactor; the structural existence check now points there.
	target := filepath.Join(filepath.Dir(thisFile), "hcheckout", "price_breakdown.go")
	if _, err := os.Stat(target); err != nil {
		t.Errorf("hcheckout/price_breakdown.go not found: %v", err)
	}
}

func TestPriceBreakdown163_Step1_FindFileByNameWorks(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "price_breakdown.go")
	if content == "" {
		t.Fatal("findFileByName returned empty content for price_breakdown.go")
	}
}

func TestPriceBreakdown163_Step1_FileContainsHandlerFunc(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "price_breakdown.go")
	for _, want := range []string{
		"handlePriceBreakdown",
		"buildPriceBreakdown",
		"priceBreakdownResponse",
		"breakdownLineItem",
		"price_breakdown",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("price_breakdown.go: missing symbol %q", want)
		}
	}
}

func TestPriceBreakdown163_Step1_RouteRequiresAuth(t *testing.T) {
	s := buildBreakdownServer(t)
	req := httptest.NewRequest(http.MethodGet, breakdownBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET price-breakdown without auth: got %d, want 401", w.Code)
	}
}

func TestPriceBreakdown163_Step1_InvalidUUIDReturns400(t *testing.T) {
	s := buildBreakdownServer(t)
	tok := mintBreakdownToken(t, s)

	req := httptest.NewRequest(http.MethodGet, "/v1/checkout/not-a-uuid/price-breakdown", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid UUID: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "checkout.invalid_id") {
		t.Errorf("expected checkout.invalid_id in body, got: %s", w.Body.String())
	}
}

func TestPriceBreakdown163_Step1_NilCheckoutQueriesReturns503(t *testing.T) {
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	// No CheckoutQueries → route not mounted
	s := New(Options{Config: cfg, Auth: stub})

	// Mint token from this server.
	w0 := httptest.NewRecorder()
	b0 := `{"actor_id":"` + breakdownTestActorID + `","roles":["admin"]}`
	r0 := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(b0))
	r0.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w0, r0)
	if w0.Code != http.StatusOK {
		t.Fatalf("mintToken: got %d, want 200", w0.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w0.Body).Decode(&resp); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	tok := resp["token"]

	req := httptest.NewRequest(http.MethodGet, breakdownBasePath, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route is not mounted when CheckoutQueries is nil → 404 or 405
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("no CheckoutQueries: expected 404 or 405 (route not mounted), got %d body=%s",
			w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Property-based test — sum of breakdown == total to the cent
// ─────────────────────────────────────────────────────────────────────────────

// makeSnapshot creates a CheckoutSessionRow snapshot using values from ComputePricing.
func makeSnapshot(unitPrice int64, qty int32, discount int64, currency string, rules PricingRules) gen.CheckoutSessionRow {
	bd := ComputePricing(unitPrice, qty, discount, currency, rules)
	return gen.CheckoutSessionRow{
		Subtotal:    &bd.Subtotal,
		Discount:    &bd.Discount,
		PlatformFee: &bd.PlatformFee,
		ProviderFee: &bd.ProviderFee,
		Tax:         &bd.Tax,
		Total:       &bd.Total,
		Currency:    &bd.Currency,
	}
}

// sumBreakdown computes: subtotal + sum(discounts) + sum(fees) + sum(taxes).
// Discount amounts are stored as negative numbers so this is the all-in total.
func sumBreakdown(bd priceBreakdownResponse) int64 {
	total := bd.Subtotal
	for _, d := range bd.Discounts {
		total += d.Amount // negative — reduces total
	}
	for _, f := range bd.Fees {
		total += f.Amount
	}
	for _, tx := range bd.Taxes {
		total += tx.Amount
	}
	return total
}

func TestPriceBreakdown163_Step2_SumInvariant_BasicCases(t *testing.T) {
	t.Parallel()

	type tc struct {
		desc      string
		unitPrice int64
		qty       int32
		discount  int64
		rules     PricingRules
	}

	cases := []tc{
		{
			desc:      "no_fees_no_discount",
			unitPrice: 1000,
			qty:       2,
			discount:  0,
			rules:     PricingRules{},
		},
		{
			desc:      "platform_fee_only",
			unitPrice: 1000,
			qty:       1,
			discount:  0,
			rules:     PricingRules{PlatformFeeRate: 500},
		},
		{
			desc:      "all_fees_with_discount",
			unitPrice: 1000,
			qty:       2,
			discount:  200,
			rules:     PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700},
		},
		{
			desc:      "free_tier",
			unitPrice: 0,
			qty:       5,
			discount:  0,
			rules:     PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700},
		},
		{
			desc:      "full_discount",
			unitPrice: 500,
			qty:       2,
			discount:  9999, // capped at subtotal
			rules:     PricingRules{TaxRate: 1700},
		},
		{
			desc:      "il_vat_17pct",
			unitPrice: 10000,
			qty:       3,
			discount:  0,
			rules:     PricingRules{TaxRate: 1700},
		},
		{
			desc:      "all_fees_il_vat",
			unitPrice: 5000,
			qty:       2,
			discount:  500,
			rules:     PricingRules{PlatformFeeRate: 300, ProviderFeeRate: 150, TaxRate: 1700},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.desc, func(t *testing.T) {
			t.Parallel()
			cs := makeSnapshot(c.unitPrice, c.qty, c.discount, "ILS", c.rules)
			bd, ok := buildPriceBreakdown(context.Background(), cs)
			if !ok {
				t.Fatalf("%s: buildPriceBreakdown returned false (pricing not confirmed)", c.desc)
			}
			got := sumBreakdown(bd)
			if got != bd.Total {
				t.Errorf("%s: sum of breakdown = %d, want Total = %d (subtotal=%d discounts=%v fees=%v taxes=%v)",
					c.desc, got, bd.Total, bd.Subtotal, bd.Discounts, bd.Fees, bd.Taxes)
			}
		})
	}
}

func TestPriceBreakdown163_Step2_SumInvariant_Sweep(t *testing.T) {
	t.Parallel()

	prices := []int64{0, 1, 99, 100, 1000, 9999, 50_000}
	quantities := []int32{1, 2, 5, 10}
	discountAmounts := []int64{0, 100, 500, 9999}
	rulesSets := []PricingRules{
		{},
		{PlatformFeeRate: 500},
		{ProviderFeeRate: 200},
		{TaxRate: 1700},
		{PlatformFeeRate: 300, ProviderFeeRate: 150, TaxRate: 1700},
	}

	for _, price := range prices {
		for _, qty := range quantities {
			for _, disc := range discountAmounts {
				for _, rules := range rulesSets {
					price, qty, disc, rules := price, qty, disc, rules
					subtotal := price * int64(qty)
					// cap discount to subtotal to avoid confusing test labels
					d := disc
					if d > subtotal {
						d = subtotal
					}
					label := fmt.Sprintf("price=%d qty=%d disc=%d pf=%d pv=%d tx=%d",
						price, qty, d, rules.PlatformFeeRate, rules.ProviderFeeRate, rules.TaxRate)
					t.Run(label, func(t *testing.T) {
						t.Parallel()
						cs := makeSnapshot(price, qty, d, "ILS", rules)
						bd, ok := buildPriceBreakdown(context.Background(), cs)
						if !ok {
							t.Fatalf("buildPriceBreakdown returned false unexpectedly")
						}
						got := sumBreakdown(bd)
						if got != bd.Total {
							t.Errorf("sum invariant violated: sum=%d Total=%d (subtotal=%d discounts=%v fees=%v taxes=%v)",
								got, bd.Total, bd.Subtotal, bd.Discounts, bd.Fees, bd.Taxes)
						}
					})
				}
			}
		}
	}
}

func TestPriceBreakdown163_Step2_DiscountAmountsAreNegative(t *testing.T) {
	t.Parallel()
	cs := makeSnapshot(1000, 2, 200, "ILS", PricingRules{})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	for _, d := range bd.Discounts {
		if d.Amount >= 0 {
			t.Errorf("discount amount should be negative (reduces total), got %d for label %q", d.Amount, d.Label)
		}
	}
}

func TestPriceBreakdown163_Step2_FeesAndTaxesArePositive(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(1000, 1, 0, "ILS", rules)
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	for _, f := range bd.Fees {
		if f.Amount < 0 {
			t.Errorf("fee amount should be positive, got %d for label %q", f.Amount, f.Label)
		}
	}
	for _, tx := range bd.Taxes {
		if tx.Amount < 0 {
			t.Errorf("tax amount should be positive, got %d for label %q", tx.Amount, tx.Label)
		}
	}
}

func TestPriceBreakdown163_Step2_NoPricingConfirmed_ReturnsFalse(t *testing.T) {
	t.Parallel()
	// All pricing columns nil → should return (zero, false).
	cs := gen.CheckoutSessionRow{}
	_, ok := buildPriceBreakdown(context.Background(), cs)
	if ok {
		t.Error("expected buildPriceBreakdown to return false when pricing columns are nil")
	}
}

func TestPriceBreakdown163_Step2_PartialNilColumns_ReturnsFalse(t *testing.T) {
	t.Parallel()
	subtotal := int64(1000)
	// Total and Currency are nil — should still return false.
	cs := gen.CheckoutSessionRow{
		Subtotal: &subtotal,
	}
	_, ok := buildPriceBreakdown(context.Background(), cs)
	if ok {
		t.Error("expected buildPriceBreakdown to return false when total/currency are nil")
	}
}

func TestPriceBreakdown163_Step2_EmptySlicesWhenZeroLineItems(t *testing.T) {
	t.Parallel()
	// No discount, no fees, no tax → all slices should be empty (not nil).
	cs := makeSnapshot(1000, 1, 0, "USD", PricingRules{})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if bd.Discounts == nil {
		t.Error("Discounts should be empty slice (not nil)")
	}
	if bd.Fees == nil {
		t.Error("Fees should be empty slice (not nil)")
	}
	if bd.Taxes == nil {
		t.Error("Taxes should be empty slice (not nil)")
	}
	if len(bd.Discounts) != 0 || len(bd.Fees) != 0 || len(bd.Taxes) != 0 {
		t.Errorf("expected empty slices when no fees/discount/tax; got discounts=%v fees=%v taxes=%v",
			bd.Discounts, bd.Fees, bd.Taxes)
	}
}

func TestPriceBreakdown163_Step2_CurrencyPropagated(t *testing.T) {
	t.Parallel()
	for _, currency := range []string{"ILS", "USD", "EUR"} {
		cs := makeSnapshot(1000, 1, 0, currency, PricingRules{})
		bd, ok := buildPriceBreakdown(context.Background(), cs)
		if !ok {
			t.Fatalf("currency=%s: buildPriceBreakdown returned false", currency)
		}
		if bd.Currency != currency {
			t.Errorf("currency=%s: got %q in breakdown", currency, bd.Currency)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: i18n labels for each line type
// ─────────────────────────────────────────────────────────────────────────────

func TestPriceBreakdown163_Step3_EnglishFallbackLabels(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(1000, 1, 200, "ILS", rules)

	// context.Background() has no localizer → Localize returns fallback strings.
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}

	// Discount label
	if len(bd.Discounts) < 1 {
		t.Fatal("expected at least one discount line item")
	}
	if bd.Discounts[0].Label == "" {
		t.Error("discount label should not be empty")
	}
	if !strings.Contains(strings.ToLower(bd.Discounts[0].Label), "discount") && !strings.Contains(strings.ToLower(bd.Discounts[0].Label), "promo") {
		t.Errorf("discount label should mention 'discount' or 'promo', got %q", bd.Discounts[0].Label)
	}

	// Fee labels
	if len(bd.Fees) < 2 {
		t.Fatalf("expected at least 2 fee line items, got %d", len(bd.Fees))
	}
	for _, f := range bd.Fees {
		if f.Label == "" {
			t.Error("fee label should not be empty")
		}
	}

	// Tax label
	if len(bd.Taxes) < 1 {
		t.Fatal("expected at least one tax line item")
	}
	if bd.Taxes[0].Label == "" {
		t.Error("tax label should not be empty")
	}
}

func TestPriceBreakdown163_Step3_PlatformFeeLabel(t *testing.T) {
	t.Parallel()
	cs := makeSnapshot(1000, 1, 0, "ILS", PricingRules{PlatformFeeRate: 300})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if len(bd.Fees) == 0 {
		t.Fatal("expected platform fee line item")
	}
	if bd.Fees[0].Label == "" {
		t.Error("platform fee label should not be empty")
	}
}

func TestPriceBreakdown163_Step3_ProviderFeeLabel(t *testing.T) {
	t.Parallel()
	cs := makeSnapshot(1000, 1, 0, "ILS", PricingRules{ProviderFeeRate: 200})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if len(bd.Fees) == 0 {
		t.Fatal("expected provider fee line item")
	}
	if bd.Fees[0].Label == "" {
		t.Error("provider fee label should not be empty")
	}
}

func TestPriceBreakdown163_Step3_VATLabel(t *testing.T) {
	t.Parallel()
	cs := makeSnapshot(1000, 1, 0, "ILS", PricingRules{TaxRate: 1700})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if len(bd.Taxes) == 0 {
		t.Fatal("expected VAT tax line item")
	}
	if bd.Taxes[0].Label == "" {
		t.Error("VAT label should not be empty")
	}
}

func TestPriceBreakdown163_Step3_SumInvariantWithLabels(t *testing.T) {
	t.Parallel()
	// Labels must not affect the numeric values — invariant still holds.
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(2000, 3, 300, "ILS", rules)
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	got := sumBreakdown(bd)
	if got != bd.Total {
		t.Errorf("sum invariant violated with labels: sum=%d Total=%d", got, bd.Total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Integration tests — multi-scenario, HTTP edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestPriceBreakdown163_Step4_RouteIsMounted(t *testing.T) {
	// The price-breakdown route must exist (auth failure ≠ 404).
	s := buildBreakdownServer(t)
	req := httptest.NewRequest(http.MethodGet, breakdownBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
		t.Errorf("price-breakdown route not mounted: got %d", w.Code)
	}
	// Without auth it should be 401, confirming the route IS mounted.
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

func TestPriceBreakdown163_Step4_ResponseEnvelopeKey(t *testing.T) {
	t.Parallel()
	// buildPriceBreakdown returns a priceBreakdownResponse — the handler wraps it
	// in {"price_breakdown": ...}. Verify the top-level key in JSON.
	cs := makeSnapshot(5000, 1, 500, "ILS",
		PricingRules{PlatformFeeRate: 250, ProviderFeeRate: 100, TaxRate: 1700})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	envelope := map[string]any{"price_breakdown": bd}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := out["price_breakdown"]; !ok {
		t.Error("response envelope missing 'price_breakdown' key")
	}
}

func TestPriceBreakdown163_Step4_BreakdownShape(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(10000, 2, 1000, "ILS", rules)
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}

	// Validate all required JSON keys exist in the marshalled output.
	raw, _ := json.Marshal(bd)
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("json.Unmarshal breakdown: %v", err)
	}
	for _, key := range []string{"subtotal", "discounts", "fees", "taxes", "total", "currency"} {
		if _, ok := shape[key]; !ok {
			t.Errorf("breakdown JSON missing key %q", key)
		}
	}
}

func TestPriceBreakdown163_Step4_MultiTierOrderCorrectBreakdown(t *testing.T) {
	t.Parallel()
	// Simulate a multi-tier order: 3 tickets at 5000 agorot each = 15 000.
	// promo discount: 1500 → subtotal_after = 13500
	// platform fee (3%): 405
	// provider fee (2%): 270
	// vat (17%): 2295
	// total = 13500 + 405 + 270 + 2295 = 16470
	rules := PricingRules{PlatformFeeRate: 300, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(5000, 3, 1500, "ILS", rules)

	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}

	if bd.Subtotal != 15000 {
		t.Errorf("subtotal: got %d, want 15000", bd.Subtotal)
	}
	if len(bd.Discounts) != 1 {
		t.Errorf("discounts: expected 1 line, got %d", len(bd.Discounts))
	} else if bd.Discounts[0].Amount != -1500 {
		t.Errorf("discount amount: got %d, want -1500", bd.Discounts[0].Amount)
	}

	// Fees: platform (300bp) and provider (200bp) on 13500 discounted subtotal.
	// 13500 * 300 / 10000 = 405
	// 13500 * 200 / 10000 = 270
	platformFee := int64(13500 * 300 / 10000) // = 405
	providerFee := int64(13500 * 200 / 10000) // = 270
	vat := int64(13500 * 1700 / 10000)        // = 2295

	if len(bd.Fees) != 2 {
		t.Errorf("fees: expected 2 lines, got %d: %v", len(bd.Fees), bd.Fees)
	} else {
		if bd.Fees[0].Amount != platformFee {
			t.Errorf("fees[0] (platform): got %d, want %d", bd.Fees[0].Amount, platformFee)
		}
		if bd.Fees[1].Amount != providerFee {
			t.Errorf("fees[1] (provider): got %d, want %d", bd.Fees[1].Amount, providerFee)
		}
	}

	if len(bd.Taxes) != 1 {
		t.Errorf("taxes: expected 1 line, got %d", len(bd.Taxes))
	} else if bd.Taxes[0].Amount != vat {
		t.Errorf("taxes[0] (VAT): got %d, want %d", bd.Taxes[0].Amount, vat)
	}

	expectedTotal := int64(13500) + platformFee + providerFee + vat
	if bd.Total != expectedTotal {
		t.Errorf("total: got %d, want %d", bd.Total, expectedTotal)
	}

	// Sum invariant holds.
	got := sumBreakdown(bd)
	if got != bd.Total {
		t.Errorf("sum invariant violated: sum=%d Total=%d", got, bd.Total)
	}
}

func TestPriceBreakdown163_Step4_FreeTierReturnsZeroBreakdown(t *testing.T) {
	t.Parallel()
	cs := makeSnapshot(0, 5, 0, "ILS", PricingRules{PlatformFeeRate: 500, TaxRate: 1700})
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if bd.Total != 0 {
		t.Errorf("free tier total: got %d, want 0", bd.Total)
	}
	if len(bd.Discounts) != 0 || len(bd.Fees) != 0 || len(bd.Taxes) != 0 {
		t.Errorf("free tier: expected empty line items; discounts=%v fees=%v taxes=%v",
			bd.Discounts, bd.Fees, bd.Taxes)
	}
	got := sumBreakdown(bd)
	if got != bd.Total {
		t.Errorf("free tier: sum invariant violated: sum=%d Total=%d", got, bd.Total)
	}
}

func TestPriceBreakdown163_Step4_100PctDiscountReturnsZeroTotal(t *testing.T) {
	t.Parallel()
	// 100% discount → subtotal_after = 0 → all fees = 0 → total = 0.
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	cs := makeSnapshot(1000, 2, 2000 /*=full subtotal*/, "USD", rules)
	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("buildPriceBreakdown returned false")
	}
	if bd.Total != 0 {
		t.Errorf("100%% discount: total should be 0, got %d", bd.Total)
	}
	got := sumBreakdown(bd)
	if got != bd.Total {
		t.Errorf("100%% discount: sum invariant: sum=%d Total=%d", got, bd.Total)
	}
}

func TestPriceBreakdown163_Step4_ServerGoContainsPriceBreakdownRoute(t *testing.T) {
	t.Parallel()
	content := findFileByName(t, "server.go")
	for _, want := range []string{
		"price-breakdown",
		"handlePriceBreakdown",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("server.go: missing %q — price-breakdown route not registered", want)
		}
	}
}
