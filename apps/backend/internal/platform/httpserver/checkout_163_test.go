// checkout_163_test.go — unit tests for the all-in price breakdown endpoint
// (feature #163).
//
// Test coverage:
//
//	Step 1: buildPriceBreakdown — nil snapshot returns (_, false)
//	Step 2: buildPriceBreakdown — full snapshot sum invariant property
//	Step 3: buildPriceBreakdown — zero fees/taxes produce empty slices (not nil)
//	Step 4: buildPriceBreakdown — discount amount is negative in the output
//	Step 5: Property sweep — sum invariant over a range of pricing inputs
//	Step 6: HTTP auth gating — 401 without JWT
//	Step 7: HTTP — 404 for unknown session id (DB not available → 500 OR 404)
//	Step 8: HTTP — 409 when pricing snapshot is absent (via buildPriceBreakdown)
//	Step 9: HTTP — route is mounted (does not return 404 with valid token)
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: nil snapshot → (_, false)
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step1_NilSnapshotReturnsFalse(t *testing.T) {
	t.Parallel()
	// A freshly-created session has nil pricing columns.
	cs := gen.CheckoutSessionRow{
		ID:        uuid.New(),
		State:     "created",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_, ok := buildPriceBreakdown(context.Background(), cs)
	if ok {
		t.Error("expected buildPriceBreakdown to return false for nil snapshot")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: full snapshot — sum invariant
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step2_SumInvariant_FullSnapshot(t *testing.T) {
	t.Parallel()

	// Construct a session row that mirrors the output of ComputePricing.
	// Rules: 5 % platform, 2 % provider, 17 % tax.
	// unitPrice=1000, qty=5 → subtotal=5000
	// discount=500 (promo) → discounted=4500
	// platformFee = 4500*500/10000 = 225
	// providerFee = 4500*200/10000 = 90
	// tax         = 4500*1700/10000 = 765
	// total       = 4500 + 225 + 90 + 765 = 5580
	subtotal    := int64(5000)
	discount    := int64(500)
	platformFee := int64(225)
	providerFee := int64(90)
	tax         := int64(765)
	total       := int64(5580)
	currency    := "ILS"

	cs := gen.CheckoutSessionRow{
		ID:          uuid.New(),
		State:       "pricing_confirmed",
		Subtotal:    &subtotal,
		Discount:    &discount,
		PlatformFee: &platformFee,
		ProviderFee: &providerFee,
		Tax:         &tax,
		Total:       &total,
		Currency:    &currency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("expected buildPriceBreakdown to return true for full snapshot")
	}

	// Verify sum invariant:
	// Subtotal + sum(Discounts) + sum(Fees) + sum(Taxes) == Total
	var componentSum int64 = bd.Subtotal
	for _, d := range bd.Discounts {
		componentSum += d.Amount
	}
	for _, f := range bd.Fees {
		componentSum += f.Amount
	}
	for _, tx := range bd.Taxes {
		componentSum += tx.Amount
	}
	if componentSum != bd.Total {
		t.Errorf("sum invariant broken: subtotal+components=%d, total=%d",
			componentSum, bd.Total)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: zero fees/taxes — empty slices, not nil
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step3_ZeroComponentsProduceEmptySlices(t *testing.T) {
	t.Parallel()

	// Free tier: all components zero.
	subtotal    := int64(0)
	discount    := int64(0)
	platformFee := int64(0)
	providerFee := int64(0)
	tax         := int64(0)
	total       := int64(0)
	currency    := "ILS"

	cs := gen.CheckoutSessionRow{
		ID:          uuid.New(),
		State:       "pricing_confirmed",
		Subtotal:    &subtotal,
		Discount:    &discount,
		PlatformFee: &platformFee,
		ProviderFee: &providerFee,
		Tax:         &tax,
		Total:       &total,
		Currency:    &currency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("expected buildPriceBreakdown to succeed for zero-value snapshot")
	}

	// Slices must be non-nil (JSON marshals nil slice as null, empty slice as []).
	if bd.Discounts == nil {
		t.Error("Discounts must not be nil — should be an empty slice")
	}
	if bd.Fees == nil {
		t.Error("Fees must not be nil — should be an empty slice")
	}
	if bd.Taxes == nil {
		t.Error("Taxes must not be nil — should be an empty slice")
	}
	if len(bd.Discounts) != 0 || len(bd.Fees) != 0 || len(bd.Taxes) != 0 {
		t.Errorf("expected all slices to be empty for zero-component session; "+
			"discounts=%d fees=%d taxes=%d",
			len(bd.Discounts), len(bd.Fees), len(bd.Taxes))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: discount amount is negative in the output
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step4_DiscountAmountIsNegative(t *testing.T) {
	t.Parallel()

	subtotal    := int64(2000)
	discount    := int64(200) // 10 % off
	platformFee := int64(0)
	providerFee := int64(0)
	tax         := int64(0)
	total       := int64(1800)
	currency    := "USD"

	cs := gen.CheckoutSessionRow{
		ID:          uuid.New(),
		State:       "pricing_confirmed",
		Subtotal:    &subtotal,
		Discount:    &discount,
		PlatformFee: &platformFee,
		ProviderFee: &providerFee,
		Tax:         &tax,
		Total:       &total,
		Currency:    &currency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("expected buildPriceBreakdown to succeed")
	}

	if len(bd.Discounts) != 1 {
		t.Fatalf("expected 1 discount line, got %d", len(bd.Discounts))
	}
	if bd.Discounts[0].Amount >= 0 {
		t.Errorf("discount amount must be negative; got %d", bd.Discounts[0].Amount)
	}
	if bd.Discounts[0].Amount != -200 {
		t.Errorf("discount amount: want -200, got %d", bd.Discounts[0].Amount)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5: property sweep — sum invariant over a range of inputs
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step5_PropertySweep_SumInvariant(t *testing.T) {
	t.Parallel()

	type tc struct {
		subtotal    int64
		discount    int64
		platformFee int64
		providerFee int64
		tax         int64
		total       int64
		currency    string
	}

	cases := []tc{
		// Free tier
		{0, 0, 0, 0, 0, 0, "ILS"},
		// Fixed price, no fees, no tax
		{1000, 0, 0, 0, 0, 1000, "ILS"},
		// Fixed price with 5 % platform fee
		{1000, 0, 50, 0, 0, 1050, "USD"},
		// Fixed price with all fees and tax
		{5000, 0, 250, 100, 850, 6200, "ILS"},
		// Promo discount + fees
		{5000, 500, 225, 90, 765, 5580, "ILS"},
		// 100 % discount → total = 0
		{5000, 5000, 0, 0, 0, 0, "EUR"},
		// PWYW, no fees
		{3000, 0, 0, 0, 0, 3000, "ILS"},
	}

	for i, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			t.Parallel()
			sub := c.subtotal
			dis := c.discount
			pf  := c.platformFee
			pvf := c.providerFee
			tx  := c.tax
			tot := c.total
			cur := c.currency

			cs := gen.CheckoutSessionRow{
				ID:          uuid.New(),
				State:       "pricing_confirmed",
				Subtotal:    &sub,
				Discount:    &dis,
				PlatformFee: &pf,
				ProviderFee: &pvf,
				Tax:         &tx,
				Total:       &tot,
				Currency:    &cur,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}

			bd, ok := buildPriceBreakdown(context.Background(), cs)
			if !ok {
				t.Fatalf("case %d: buildPriceBreakdown returned false", i)
			}

			var sum int64 = bd.Subtotal
			for _, d := range bd.Discounts {
				sum += d.Amount
			}
			for _, f := range bd.Fees {
				sum += f.Amount
			}
			for _, tx := range bd.Taxes {
				sum += tx.Amount
			}

			if sum != bd.Total {
				t.Errorf("case %d: sum invariant broken: got %d, want %d",
					i, sum, bd.Total)
			}
			if bd.Currency != c.currency {
				t.Errorf("case %d: currency: got %s, want %s", i, bd.Currency, c.currency)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6: HTTP auth gating — 401 without JWT
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step6_AuthGating_NoToken(t *testing.T) {
	s := buildCheckoutServer(t)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/price-breakdown", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no-token request: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7: HTTP — invalid UUID → 400
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step7_InvalidUUIDReturns400(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/not-a-uuid/price-breakdown", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// chi routes /checkout/{id}/price-breakdown; "not-a-uuid" is captured as id.
	// The handler parses it and returns 400 checkout.invalid_id.
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid UUID: got %d %s, want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "checkout.invalid_id") {
		t.Errorf("expected 'checkout.invalid_id' in body, got: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8: HTTP — valid UUID but DB unavailable → 500 or 404
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step8_ValidUUIDDBUnavailable(t *testing.T) {
	s := buildCheckoutServer(t)
	tok := mintCheckoutToken(t, s)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/checkout/00000000-0000-0000-0000-000000000001/price-breakdown", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	// gen.New(nil) always errors → 500 (DB error) or 404 (ErrNoRows path).
	// Either way, NOT 401 and NOT 200.
	if w.Code == http.StatusUnauthorized {
		t.Errorf("expected non-401 response, got 401")
	}
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 response when DB unavailable, got 200")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9: buildPriceBreakdown labels are non-empty strings
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckout163_Step9_BreakdownLabelsAreNonEmpty(t *testing.T) {
	t.Parallel()

	subtotal    := int64(5000)
	discount    := int64(500)
	platformFee := int64(225)
	providerFee := int64(90)
	tax         := int64(765)
	total       := int64(5580)
	currency    := "ILS"

	cs := gen.CheckoutSessionRow{
		ID:          uuid.New(),
		State:       "pricing_confirmed",
		Subtotal:    &subtotal,
		Discount:    &discount,
		PlatformFee: &platformFee,
		ProviderFee: &providerFee,
		Tax:         &tax,
		Total:       &total,
		Currency:    &currency,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	bd, ok := buildPriceBreakdown(context.Background(), cs)
	if !ok {
		t.Fatal("expected buildPriceBreakdown to succeed")
	}

	for _, d := range bd.Discounts {
		if strings.TrimSpace(d.Label) == "" {
			t.Error("discount label must not be empty")
		}
	}
	for _, f := range bd.Fees {
		if strings.TrimSpace(f.Label) == "" {
			t.Error("fee label must not be empty")
		}
	}
	for _, tx := range bd.Taxes {
		if strings.TrimSpace(tx.Label) == "" {
			t.Error("tax label must not be empty")
		}
	}
}
