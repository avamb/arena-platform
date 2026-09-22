package hcheckout

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func promoI32(n int32) *int32   { return &n }
func promoStr(s string) *string { return &s }

// A code for one session must not touch another session's lines, and a
// fixed sum in CZK must not come off a EUR cart.
func TestValidatePromoForLines_SessionAndCurrencyScope(t *testing.T) {
	now := time.Now().UTC()
	sessA, sessB := uuid.New().String(), uuid.New().String()
	linesA := []TierLine{{TierID: "t1", Amount: 45000, SessionID: sessA, Currency: "CZK"}}
	linesB := []TierLine{{TierID: "t1", Amount: 45000, SessionID: sessB, Currency: "CZK"}}

	scoped := gen.PromoCodeRow{Status: "active", DiscountType: "percent", DiscountValue: 10, AppliesToSessionIDs: []string{sessA}}
	if d, code := ValidatePromoForLines(scoped, linesA, now); code != "" || d != 4500 {
		t.Fatalf("own session: discount %d code %q, want 4500", d, code)
	}
	if _, code := ValidatePromoForLines(scoped, linesB, now); code != "promo.session_not_applicable" {
		t.Fatalf("other session: code %q, want promo.session_not_applicable", code)
	}
	unknown := []TierLine{{TierID: "t1", Amount: 45000}}
	if _, code := ValidatePromoForLines(scoped, unknown, now); code != "promo.session_not_applicable" {
		t.Fatalf("a line with no session must not satisfy a session scope: %q", code)
	}

	// Mixed cart: only the scoped session's line is discounted.
	mixed := append(append([]TierLine{}, linesA...), linesB...)
	if d, code := ValidatePromoForLines(scoped, mixed, now); code != "" || d != 4500 {
		t.Fatalf("mixed cart: discount %d code %q, want 4500 on the scoped line only", d, code)
	}

	czk := gen.PromoCodeRow{Status: "active", DiscountType: "fixed_amount", DiscountValue: 5000, Currency: promoStr("CZK")}
	if d, code := ValidatePromoForLines(czk, linesA, now); code != "" || d != 5000 {
		t.Fatalf("same currency: discount %d code %q", d, code)
	}
	eur := []TierLine{{TierID: "t1", Amount: 4500, SessionID: sessA, Currency: "EUR"}}
	if _, code := ValidatePromoForLines(czk, eur, now); code != "promo.currency_mismatch" {
		t.Fatalf("other currency: code %q, want promo.currency_mismatch", code)
	}
	// A legacy code (nil currency) and a percent code apply to any currency.
	legacy := gen.PromoCodeRow{Status: "active", DiscountType: "fixed_amount", DiscountValue: 500}
	if d, code := ValidatePromoForLines(legacy, eur, now); code != "" || d != 500 {
		t.Fatalf("legacy fixed code: discount %d code %q", d, code)
	}
	pct := gen.PromoCodeRow{Status: "active", DiscountType: "percent", DiscountValue: 10, Currency: promoStr("CZK")}
	if d, code := ValidatePromoForLines(pct, eur, now); code != "" || d != 450 {
		t.Fatalf("percent code ignores currency: discount %d code %q", d, code)
	}

	// Tier and session narrow each other.
	both := gen.PromoCodeRow{Status: "active", DiscountType: "percent", DiscountValue: 10,
		AppliesToSessionIDs: []string{sessA}, AppliesToTierIDs: []string{"t2"}}
	if _, code := ValidatePromoForLines(both, linesA, now); code != "promo.tier_not_applicable" {
		t.Fatalf("right session, wrong tier: %q", code)
	}
}

type fakeLimits struct {
	total, byCustomer, byEmail int32
	gotEmail                   string
	gotCustomer                *uuid.UUID
}

func (f *fakeLimits) CountPromoCodeRedemptions(context.Context, uuid.UUID) (int32, error) {
	return f.total, nil
}
func (f *fakeLimits) CountPromoRedemptionsByCustomer(_ context.Context, _ uuid.UUID, c uuid.UUID) (int32, error) {
	f.gotCustomer = &c
	return f.byCustomer, nil
}
func (f *fakeLimits) CountPromoRedemptionsByBuyerEmail(_ context.Context, _ uuid.UUID, e string) (int32, error) {
	f.gotEmail = e
	return f.byEmail, nil
}

func TestCheckPromoLimits(t *testing.T) {
	ctx := context.Background()
	pc := gen.PromoCodeRow{ID: uuid.New(), MaxUses: promoI32(2), MaxUsesPerCustomer: promoI32(1)}

	if code, err := CheckPromoLimits(ctx, &fakeLimits{total: 2}, pc, nil, "a@b.cz"); err != nil || code != PromoErrExhausted {
		t.Fatalf("total cap: %q %v", code, err)
	}
	cid := uuid.New()
	f := &fakeLimits{total: 1, byCustomer: 1}
	if code, _ := CheckPromoLimits(ctx, f, pc, &cid, "a@b.cz"); code != PromoErrPerCustomerLimit {
		t.Fatalf("per-customer by id: %q", code)
	}
	if f.gotCustomer == nil || *f.gotCustomer != cid || f.gotEmail != "" {
		t.Fatal("a known customer id must be counted by id, not by e-mail")
	}
	f = &fakeLimits{total: 1, byEmail: 1}
	if code, _ := CheckPromoLimits(ctx, f, pc, nil, " a@b.cz "); code != PromoErrPerCustomerLimit || f.gotEmail != "a@b.cz" {
		t.Fatalf("per-customer by e-mail: %q (counted %q)", code, f.gotEmail)
	}
	if code, _ := CheckPromoLimits(ctx, &fakeLimits{total: 1, byEmail: 5}, pc, nil, ""); code != "" {
		t.Fatalf("an anonymous probe is not over its own limit: %q", code)
	}
	if code, _ := CheckPromoLimits(ctx, &fakeLimits{total: 1}, pc, nil, "a@b.cz"); code != "" {
		t.Fatalf("one use left: %q", code)
	}
	if code, _ := CheckPromoLimits(ctx, &fakeLimits{total: 99}, gen.PromoCodeRow{}, nil, "a@b.cz"); code != "" {
		t.Fatalf("no caps: %q", code)
	}
}

func TestPromoRedemptionsCSV(t *testing.T) {
	n := int64(1000130627)
	status, cur, email, ch := "paid", "CZK", "buyer@example.test", "Lampyris site"
	rows := []gen.PromoCodeRedemptionReportRow{{
		Code: "ARENA10", RedeemedAt: time.Date(2026, 9, 22, 16, 8, 13, 0, time.UTC),
		DiscountAmount: 4500, OrderAmount: 45000, OrderNumber: &n, OrderStatus: &status,
		Currency: &cur, BuyerEmail: &email, ChannelName: &ch,
	}, {Code: "OLD5", RedeemedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), DiscountAmount: 5, OrderAmount: 105}}
	got := string(promoRedemptionsCSV(rows))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("csv lines: %q", got)
	}
	if lines[0] != "code,redeemed_at,order_number,order_status,currency,order_amount,discount_amount,buyer_email,channel,session_id" {
		t.Fatalf("header: %q", lines[0])
	}
	if lines[1] != "ARENA10,2026-09-22T16:08:13Z,1000130627,paid,CZK,450.00,45.00,buyer@example.test,Lampyris site," {
		t.Fatalf("row: %q", lines[1])
	}
	if lines[2] != "OLD5,2026-09-01T00:00:00Z,,,,1.05,0.05,,," {
		t.Fatalf("row without an order: %q", lines[2])
	}
}
