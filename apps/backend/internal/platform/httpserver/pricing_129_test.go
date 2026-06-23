// pricing_129_test.go — unit and property tests for the pricing pipeline
// (feature #129).
//
// Test coverage:
//
//	Step 1: ComputePricing — each pipeline step is correct
//	Step 2: Accounting invariant — (Subtotal-Discount)+Fees+Tax == Total
//	Step 3: computeDiscount integration within the pipeline
//	Step 4: GET /v1/checkout/quote — auth gating, missing params, server wiring
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"fmt"
	"math"
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
// Test actor ID
// ─────────────────────────────────────────────────────────────────────────────

const pricingTestActorID = "00000000-0000-0000-0000-000000000129"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for quote route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildPricingServer builds a Server with stub auth, quote route mounted,
// and a non-nil TierQueries so the route conditional passes.
// Real DB operations will not execute (gen.New(nil) for queries).
func buildPricingServer(t *testing.T) *Server {
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
		t.Fatalf("buildPricingServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		Pool:   &dbDownPool{},
		// TierQueries non-nil so the quote route conditional passes.
		TierQueries: gen.New(nil),
		// PromoQueries for promo-code lookup within the quote handler.
		PromoQueries: gen.New(nil),
		// Typical fee config: 5 % platform, 2 % provider, 0 % tax.
		PricingRules: PricingRules{
			PlatformFeeRate: 500,
			ProviderFeeRate: 200,
			TaxRate:         0,
		},
	})
}

// mintPricingToken mints a dev JWT (admin role) for pricing route tests.
func mintPricingToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + pricingTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintPricingToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintPricingToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintPricingToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: ComputePricing — each pipeline step
// ─────────────────────────────────────────────────────────────────────────────

func TestPricing129_Step1_NoFeesNoDiscount(t *testing.T) {
	t.Parallel()
	rules := PricingRules{} // zero value — no fees, no tax
	bd := ComputePricing(1000, 2, 0, "ILS", rules)

	checks := []struct {
		name string
		want any
		got  any
	}{
		{"unit_price", int64(1000), bd.UnitPrice},
		{"quantity", int32(2), bd.Quantity},
		{"subtotal", int64(2000), bd.Subtotal},
		{"discount", int64(0), bd.Discount},
		{"platform_fee", int64(0), bd.PlatformFee},
		{"provider_fee", int64(0), bd.ProviderFee},
		{"tax", int64(0), bd.Tax},
		{"total", int64(2000), bd.Total},
		{"currency", "ILS", bd.Currency},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestPricing129_Step1_PlatformFeeOnly(t *testing.T) {
	t.Parallel()
	// 5 % platform fee on 1000 → 50
	rules := PricingRules{PlatformFeeRate: 500}
	bd := ComputePricing(1000, 1, 0, "USD", rules)

	if bd.PlatformFee != 50 {
		t.Errorf("platform_fee: got %d, want 50", bd.PlatformFee)
	}
	if bd.ProviderFee != 0 {
		t.Errorf("provider_fee: got %d, want 0", bd.ProviderFee)
	}
	if bd.Tax != 0 {
		t.Errorf("tax: got %d, want 0", bd.Tax)
	}
	if bd.Total != 1050 {
		t.Errorf("total: got %d, want 1050", bd.Total)
	}
}

func TestPricing129_Step1_ProviderFeeOnly(t *testing.T) {
	t.Parallel()
	// 2 % provider fee on 1000 → 20
	rules := PricingRules{ProviderFeeRate: 200}
	bd := ComputePricing(1000, 1, 0, "USD", rules)

	if bd.ProviderFee != 20 {
		t.Errorf("provider_fee: got %d, want 20", bd.ProviderFee)
	}
	if bd.Total != 1020 {
		t.Errorf("total: got %d, want 1020", bd.Total)
	}
}

func TestPricing129_Step1_TaxOnly(t *testing.T) {
	t.Parallel()
	// 17 % VAT on 1000 → 170
	rules := PricingRules{TaxRate: 1700}
	bd := ComputePricing(1000, 1, 0, "ILS", rules)

	if bd.Tax != 170 {
		t.Errorf("tax: got %d, want 170", bd.Tax)
	}
	if bd.Total != 1170 {
		t.Errorf("total: got %d, want 1170", bd.Total)
	}
}

func TestPricing129_Step1_AllFeesWithDiscount(t *testing.T) {
	t.Parallel()
	rules := PricingRules{
		PlatformFeeRate: 500,  // 5 %
		ProviderFeeRate: 200,  // 2 %
		TaxRate:         1700, // 17 %
	}
	// subtotal=2000, discount=200 → discounted=1800
	// platformFee = 1800*500/10000 = 90
	// providerFee = 1800*200/10000 = 36
	// tax         = 1800*1700/10000 = 306
	// total       = 1800+90+36+306 = 2232
	bd := ComputePricing(1000, 2, 200, "ILS", rules)

	if bd.Subtotal != 2000 {
		t.Errorf("subtotal: got %d, want 2000", bd.Subtotal)
	}
	if bd.Discount != 200 {
		t.Errorf("discount: got %d, want 200", bd.Discount)
	}
	if bd.PlatformFee != 90 {
		t.Errorf("platform_fee: got %d, want 90", bd.PlatformFee)
	}
	if bd.ProviderFee != 36 {
		t.Errorf("provider_fee: got %d, want 36", bd.ProviderFee)
	}
	if bd.Tax != 306 {
		t.Errorf("tax: got %d, want 306", bd.Tax)
	}
	if bd.Total != 2232 {
		t.Errorf("total: got %d, want 2232", bd.Total)
	}
}

func TestPricing129_Step1_FreeTier(t *testing.T) {
	t.Parallel()
	rules := PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}
	bd := ComputePricing(0, 5, 0, "ILS", rules)

	if bd.Subtotal != 0 || bd.PlatformFee != 0 || bd.Total != 0 {
		t.Errorf("free tier should have zero everything: %+v", bd)
	}
}

func TestPricing129_Step1_DiscountCappedAtSubtotal(t *testing.T) {
	t.Parallel()
	// discount > subtotal → capped at subtotal → total = 0
	bd := ComputePricing(500, 2, 9999, "USD", PricingRules{})
	if bd.Discount != 1000 {
		t.Errorf("discount should be capped at subtotal (1000), got %d", bd.Discount)
	}
	if bd.Total != 0 {
		t.Errorf("total should be 0 after full discount, got %d", bd.Total)
	}
}

func TestPricing129_Step1_NegativeDiscountClamped(t *testing.T) {
	t.Parallel()
	bd := ComputePricing(500, 2, -100, "USD", PricingRules{})
	if bd.Discount != 0 {
		t.Errorf("negative discount should be clamped to 0, got %d", bd.Discount)
	}
}

func TestPricing129_Step1_FloorDivision(t *testing.T) {
	t.Parallel()
	// 1 % on 333 → 333*100/10000 = 3 (floor, not 3.33)
	bd := ComputePricing(333, 1, 0, "USD", PricingRules{PlatformFeeRate: 100})
	if bd.PlatformFee != 3 {
		t.Errorf("platform_fee floor: got %d, want 3", bd.PlatformFee)
	}
	if bd.Total != 336 {
		t.Errorf("total: got %d, want 336", bd.Total)
	}
}

func TestPricing129_Step1_CurrencyPropagated(t *testing.T) {
	t.Parallel()
	bd := ComputePricing(100, 1, 0, "EUR", PricingRules{})
	if bd.Currency != "EUR" {
		t.Errorf("currency: got %q, want EUR", bd.Currency)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Accounting invariant — property tests
// (Subtotal - Discount) + PlatformFee + ProviderFee + Tax == Total
// ─────────────────────────────────────────────────────────────────────────────

func TestPricing129_Step2_AccountingInvariant_Cases(t *testing.T) {
	t.Parallel()

	type tc struct {
		unitPrice int64
		quantity  int32
		discount  int64
		rules     PricingRules
	}

	cases := []tc{
		{1000, 1, 0, PricingRules{}},
		{1000, 5, 200, PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200, TaxRate: 1700}},
		{0, 10, 0, PricingRules{PlatformFeeRate: 300}},
		{1000, 3, 3000, PricingRules{PlatformFeeRate: 500}}, // 100 % discount
		{1, 1, 0, PricingRules{PlatformFeeRate: 9999, ProviderFeeRate: 9999, TaxRate: 9999}},
		{math.MaxInt32 / 100, 100, 0, PricingRules{TaxRate: 1700}},
		{500, 2, 50, PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200}},
	}

	for i, c := range cases {
		c := c
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			t.Parallel()
			bd := ComputePricing(c.unitPrice, c.quantity, c.discount, "ILS", c.rules)
			discounted := bd.Subtotal - bd.Discount
			want := discounted + bd.PlatformFee + bd.ProviderFee + bd.Tax
			if bd.Total != want {
				t.Errorf(
					"accounting invariant violated: total=%d want=%d (subtotal=%d discount=%d platformFee=%d providerFee=%d tax=%d)",
					bd.Total, want, bd.Subtotal, bd.Discount, bd.PlatformFee, bd.ProviderFee, bd.Tax,
				)
			}
		})
	}
}

func TestPricing129_Step2_AccountingInvariant_Sweep(t *testing.T) {
	t.Parallel()
	// Systematic sweep across prices × quantities × discount percentages.
	prices := []int64{0, 1, 99, 100, 1000, 9999, 50_000}
	quantities := []int32{1, 2, 5, 10, 100}
	discountPcts := []int64{0, 10, 50, 99, 100}
	rules := PricingRules{PlatformFeeRate: 300, ProviderFeeRate: 150, TaxRate: 1700}

	for _, price := range prices {
		for _, qty := range quantities {
			for _, pct := range discountPcts {
				subtotal := price * int64(qty)
				discount := computeDiscount("percent", pct, subtotal)
				bd := ComputePricing(price, qty, discount, "ILS", rules)
				discounted := bd.Subtotal - bd.Discount
				want := discounted + bd.PlatformFee + bd.ProviderFee + bd.Tax
				if bd.Total != want {
					t.Errorf(
						"price=%d qty=%d pct=%d: total=%d want=%d",
						price, qty, pct, bd.Total, want,
					)
				}
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: computeDiscount → ComputePricing integration
// ─────────────────────────────────────────────────────────────────────────────

func TestPricing129_Step3_WithPercentDiscount(t *testing.T) {
	t.Parallel()
	// 10 % promo on 2000 → discount = 200
	discount := computeDiscount("percent", 10, 2000)
	if discount != 200 {
		t.Fatalf("computeDiscount: got %d, want 200", discount)
	}

	rules := PricingRules{PlatformFeeRate: 500}
	bd := ComputePricing(1000, 2, discount, "ILS", rules)

	// discounted = 1800, platformFee = 1800*500/10000 = 90
	if bd.Discount != 200 {
		t.Errorf("discount: got %d, want 200", bd.Discount)
	}
	if bd.PlatformFee != 90 {
		t.Errorf("platform_fee: got %d, want 90", bd.PlatformFee)
	}
	if bd.Total != 1890 {
		t.Errorf("total: got %d, want 1890", bd.Total)
	}
}

func TestPricing129_Step3_WithFixedDiscount(t *testing.T) {
	t.Parallel()
	// Fixed 300 discount on 2000 → discount = 300
	discount := computeDiscount("fixed_amount", 300, 2000)
	if discount != 300 {
		t.Fatalf("computeDiscount fixed: got %d, want 300", discount)
	}

	bd := ComputePricing(1000, 2, discount, "ILS", PricingRules{})
	if bd.Total != 1700 {
		t.Errorf("total: got %d, want 1700", bd.Total)
	}
}

func TestPricing129_Step3_ZeroValuePricingRules(t *testing.T) {
	t.Parallel()
	var rules PricingRules
	bd := ComputePricing(500, 2, 0, "ILS", rules)
	if bd.Total != 1000 {
		t.Errorf("zero PricingRules: expected total=1000, got %d", bd.Total)
	}
}

func TestPricing129_Step3_MaxRatesNoOverflow(t *testing.T) {
	t.Parallel()
	// 100 % fees should not panic or overflow for small prices.
	rules := PricingRules{
		PlatformFeeRate: 10_000, // 100 %
		ProviderFeeRate: 10_000,
		TaxRate:         10_000,
	}
	bd := ComputePricing(100, 1, 0, "USD", rules)
	// discounted=100, platformFee=100, providerFee=100, tax=100 → total=400
	if bd.Total != 400 {
		t.Errorf("100%% rates: expected total=400, got %d", bd.Total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP routes — auth gating and param validation
// ─────────────────────────────────────────────────────────────────────────────

const quoteBasePath = "/v1/checkout/quote"

func TestPricing129_Step4_GetQuoteRequiresAuth(t *testing.T) {
	s := buildPricingServer(t)
	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=00000000-0000-0000-0000-000000000001&session_id=00000000-0000-0000-0000-000000000002&quantity=1&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("quote without auth: got %d, want 401", w.Code)
	}
}

func TestPricing129_Step4_MissingTierID(t *testing.T) {
	s := buildPricingServer(t)
	tok := mintPricingToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?session_id=00000000-0000-0000-0000-000000000002&quantity=1&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing tier_id: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pricing.missing_params") {
		t.Errorf("expected 'pricing.missing_params' in body, got: %s", w.Body.String())
	}
}

func TestPricing129_Step4_MissingQuantity(t *testing.T) {
	s := buildPricingServer(t)
	tok := mintPricingToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=00000000-0000-0000-0000-000000000001&session_id=00000000-0000-0000-0000-000000000002&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing quantity: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pricing.missing_params") {
		t.Errorf("expected 'pricing.missing_params' in body, got: %s", w.Body.String())
	}
}

func TestPricing129_Step4_InvalidTierIDFormat(t *testing.T) {
	s := buildPricingServer(t)
	tok := mintPricingToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=not-a-uuid&session_id=00000000-0000-0000-0000-000000000002&quantity=1&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid tier_id: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pricing.invalid_tier_id") {
		t.Errorf("expected 'pricing.invalid_tier_id' in body, got: %s", w.Body.String())
	}
}

func TestPricing129_Step4_ZeroQuantity(t *testing.T) {
	s := buildPricingServer(t)
	tok := mintPricingToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=00000000-0000-0000-0000-000000000001&session_id=00000000-0000-0000-0000-000000000002&quantity=0&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("quantity=0: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pricing.invalid_quantity") {
		t.Errorf("expected 'pricing.invalid_quantity' in body, got: %s", w.Body.String())
	}
}

func TestPricing129_Step4_NegativeQuantity(t *testing.T) {
	s := buildPricingServer(t)
	tok := mintPricingToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=00000000-0000-0000-0000-000000000001&session_id=00000000-0000-0000-0000-000000000002&quantity=-1&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("quantity=-1: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pricing.invalid_quantity") {
		t.Errorf("expected 'pricing.invalid_quantity' in body, got: %s", w.Body.String())
	}
}

func TestPricing129_Step4_DatabaseUnavailableWhenTierQueriesNil(t *testing.T) {
	// Build server WITHOUT TierQueries — route should be unmounted (→ 404 or 405)
	// or return 503 if the handler still runs but checks nil.
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
	// No TierQueries → route not mounted
	s := New(Options{Config: cfg, Auth: stub})

	// Mint token from this server.
	w0 := httptest.NewRecorder()
	b0 := `{"actor_id":"` + pricingTestActorID + `","roles":["admin"]}`
	r0 := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(b0))
	r0.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w0, r0)
	if w0.Code != http.StatusOK {
		t.Fatalf("mintToken: got %d, want 200", w0.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w0.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tok := resp["token"]

	req := httptest.NewRequest(http.MethodGet,
		quoteBasePath+"?tier_id=00000000-0000-0000-0000-000000000001&session_id=00000000-0000-0000-0000-000000000002&quantity=1&org_id=00000000-0000-0000-0000-000000000003",
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// Route is not mounted when TierQueries is nil → expect 404 or 405
	if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("no TierQueries: expected 404 or 405 (route not mounted), got %d body=%s",
			w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// File structure check
// ─────────────────────────────────────────────────────────────────────────────

func TestPricing129_PricingCalculatorFileExists(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller not available")
	}
	if !filepath.IsAbs(thisFile) {
		t.Skip("non-absolute path (trimpath build)")
	}
	dir := filepath.Dir(thisFile)
	target := filepath.Join(dir, "pricing_calculator.go")
	if _, err := os.Stat(target); err != nil {
		t.Errorf("pricing_calculator.go not found at %s: %v", target, err)
	}
}
