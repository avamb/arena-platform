//go:build integration

// rate_integration_test.go — TermsAt and Rate against the rows migration
// 0157 really seeds (task B1-2). Connects to DATABASE_URL and skips when it
// is unset; run it on a FRESH migrated database (AGENTS.md).
package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func TestRate_Integration_SeededPriceListOnARealAccount(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Billing B1-2 "+suffix, "billing-b12-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM org_rate_overrides WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM billing_accounts WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	acc, err := billing.EnsureAccount(ctx, q, orgID, "ES")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.October, 20, 18, 30, 0, 0, time.UTC)
	terms, err := billing.TermsAt(ctx, q, acc.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	if terms.PriceList.Code != "standard-2026" || terms.PriceList.Version != 1 || terms.Currency != "EUR" ||
		terms.Override != nil || terms.PlanKind() != billing.PlanStandard {
		t.Fatalf("terms = %+v", terms)
	}

	rate := func(terms billing.Terms, unit, discount int64, seated bool) int64 {
		t.Helper()
		class, _ := billing.ClassifyLowPrice(terms, "EUR", unit)
		c, err := billing.Rate(terms, billing.Event{Kind: billing.KindTicketSold, OccurredAt: terms.At,
			Seated: seated, Currency: "EUR", UnitPriceMinor: unit, DiscountMinor: discount, PricingClass: class})
		if err != nil {
			t.Fatal(err)
		}
		return c.AmountMinor
	}
	for _, tc := range []struct {
		unit, discount int64
		seated         bool
		want           int64
	}{
		{2500, 0, false, 59}, {2500, 0, true, 109}, // policy section 2
		{500, 0, false, 20}, {500, 0, true, 40}, // policy section 5
		{900, 0, false, 35}, {1000, 0, false, 59},
		{1200, 500, false, 59}, // the class follows the price before the promo code
	} {
		if got := rate(terms, tc.unit, tc.discount, tc.seated); got != tc.want {
			t.Errorf("unit=%d discount=%d seated=%v: %d, want %d", tc.unit, tc.discount, tc.seated, got, tc.want)
		}
	}
	if len(terms.Packs) != 7 || billing.PackTicketRate(terms.Packs[6], true) != 73 ||
		billing.PackTicketRate(terms.Packs[0], false) != 55 {
		t.Errorf("packs = %+v", terms.Packs)
	}

	// The same read inside a transaction, as the rating job will make it.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inTx, err := billing.TermsAt(ctx, q.WithTx(tx), acc.ID, at)
	_ = tx.Rollback(ctx)
	if err != nil || inTx.PriceList.ID != terms.PriceList.ID {
		t.Fatalf("TermsAt in a transaction: %v", err)
	}

	// A venue plan from November: October keeps the standard rate.
	nov := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	reason := "test venue"
	if _, err := q.InsertBillingAccountTerms(ctx, gen.InsertBillingAccountTermsParams{
		AccountID: acc.ID, EffectiveFrom: nov, PriceListCode: "standard-2026", PlanKind: "venue",
		BillingCycle: "monthly", ShortfallPolicy: "invoice_postpaid", IssuerID: acc.IssuerID, Reason: &reason,
	}); err != nil {
		t.Fatal(err)
	}
	venue, err := billing.TermsAt(ctx, q, acc.ID, nov.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := rate(venue, 20000, 0, true); got != 300 { // policy section 6: 2 %, cap 3.00
		t.Errorf("venue 200.00: %d, want 300", got)
	}
	if got := rate(venue, 2500, 0, false); got != 50 {
		t.Errorf("venue 25.00: %d, want 50", got)
	}
	again, err := billing.TermsAt(ctx, q, acc.ID, at)
	if err != nil || again.PlanKind() != billing.PlanStandard {
		t.Errorf("October after the November terms: plan %s err %v", again.PlanKind(), err)
	}

	// An individual rate replaces the price list, the low-price formula too.
	if _, err := pool.Exec(ctx, `
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, created_by)
VALUES ($1, 'EUR', 50, 100, '2026-10-01T00:00:00Z', 'test')`, orgID); err != nil {
		t.Fatal(err)
	}
	withOv, err := billing.TermsAt(ctx, q, acc.ID, at)
	if err != nil || withOv.Override == nil {
		t.Fatalf("override not loaded: %v", err)
	}
	if got := rate(withOv, 500, 0, false); got != 50 {
		t.Errorf("override at 5.00: %d, want 50", got)
	}
	if got := rate(withOv, 2500, 0, true); got != 100 {
		t.Errorf("override seated: %d, want 100", got)
	}
	before, err := billing.TermsAt(ctx, q, acc.ID, time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC))
	if err != nil || before.Override != nil {
		t.Errorf("override before its date: %+v err %v", before.Override, err)
	}
}
