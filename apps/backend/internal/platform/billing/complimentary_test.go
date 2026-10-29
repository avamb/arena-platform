package billing_test

import (
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func i32(n int32) *int32 { return &n }

// Spec 46 section 3.6: every branch of the invitation rule. The seed rates
// are 0.59 unseated / 1.09 seated, the individual rate 0.50 / 1.00.
func TestRate_ComplimentaryRule(t *testing.T) {
	type tc struct {
		name          string
		complimentary string
		override      bool
		plan          string
		seated        bool
		bulk          bool
		overLimit     bool
		want          int64
		reason        string
		rule          billing.RuleType
	}
	cases := []tc{
		// free mode
		{name: "free, GA", want: 0, reason: billing.ReasonFree},
		{name: "free, seated", seated: true, want: 0, reason: billing.ReasonFree},
		{name: "free, explicit", complimentary: "free", seated: true, want: 0, reason: billing.ReasonFree},
		{name: "free, over limit GA", overLimit: true, want: 59, rule: billing.RuleFixedPerTicket},
		{name: "free, over limit seated pays unseated", seated: true, overLimit: true, want: 59, rule: billing.RuleFixedPerTicket},
		{name: "free, over limit with override", seated: true, overLimit: true, override: true, want: 50, reason: billing.ReasonOverride, rule: billing.RuleOrgRateOverride},
		// standard mode
		{name: "standard, GA", complimentary: "standard", want: 59, rule: billing.RuleFixedPerTicket},
		{name: "standard, seated", complimentary: "standard", seated: true, want: 109, rule: billing.RuleFixedPerTicket},
		{name: "standard, override GA", complimentary: "standard", override: true, want: 50, reason: billing.ReasonOverride, rule: billing.RuleOrgRateOverride},
		{name: "standard, override seated", complimentary: "standard", override: true, seated: true, want: 100, reason: billing.ReasonOverride, rule: billing.RuleOrgRateOverride},
		{name: "standard, over limit ignored", complimentary: "standard", seated: true, overLimit: true, want: 109, rule: billing.RuleFixedPerTicket},
		{name: "standard, venue plan uses the fixed rate", complimentary: "standard", plan: billing.PlanVenue, seated: true, want: 109, rule: billing.RuleFixedPerTicket},
		// bulk (CSV) whatever the mode
		{name: "bulk on free, GA", bulk: true, want: 59, rule: billing.RuleFixedPerTicket},
		{name: "bulk on free, seated", bulk: true, seated: true, want: 109, rule: billing.RuleFixedPerTicket},
		{name: "bulk on free, override seated", bulk: true, seated: true, override: true, want: 100, reason: billing.ReasonOverride, rule: billing.RuleOrgRateOverride},
		{name: "bulk on free, override GA", bulk: true, override: true, want: 50, reason: billing.ReasonOverride, rule: billing.RuleOrgRateOverride},
		{name: "bulk on standard, seated", bulk: true, complimentary: "standard", seated: true, want: 109, rule: billing.RuleFixedPerTicket},
		{name: "bulk, over limit seated stays seated rate", bulk: true, seated: true, overLimit: true, want: 109, rule: billing.RuleFixedPerTicket},
		{name: "bulk, venue plan", bulk: true, plan: billing.PlanVenue, want: 59, rule: billing.RuleFixedPerTicket},
	}
	for _, c := range cases {
		spec := termsSpec{complimentary: c.complimentary, plan: c.plan}
		if c.override {
			spec.override = lampyrisOverride()
		}
		terms := buildTerms(t, spec)
		ev := billing.Event{Kind: billing.KindTicketComplimentary, OccurredAt: saleAt, Currency: "EUR",
			Seated: c.seated, Bulk: c.bulk, FreeTicketsOverLimit: c.overLimit}
		got := mustRate(t, terms, ev)
		if got.AmountMinor != c.want || got.Reason != c.reason || got.RuleType != c.rule {
			t.Errorf("%s: %d %q %s, want %d %q %s", c.name, got.AmountMinor, got.Reason, got.RuleType, c.want, c.reason, c.rule)
		}
	}
}

// Percentages, the low-price formula and fee_mode buyer_pays never touch an
// invitation: the price on the event is irrelevant, the rate is flat.
func TestRate_ComplimentaryStandardIgnoresPriceAndFeeMode(t *testing.T) {
	terms := buildTerms(t, termsSpec{complimentary: "standard"})
	terms.Terms.FeeMode = billing.FeeModeBuyerPays
	for _, ev := range []billing.Event{
		{Kind: billing.KindTicketComplimentary, OccurredAt: saleAt, Currency: "EUR", UnitPriceMinor: 300, PricingClass: billing.PricingClassLowPrice},
		{Kind: billing.KindTicketComplimentary, OccurredAt: saleAt, Currency: "CZK", UnitPriceMinor: 90000, ServiceChargeMinor: 500},
	} {
		if c := mustRate(t, terms, ev); c.AmountMinor != 59 || c.RuleType != billing.RuleFixedPerTicket || c.Deferred != "" {
			t.Errorf("%+v: %+v, want 59 fixed_per_ticket", ev, c)
		}
	}
}

// A price list without fixed_per_ticket cannot give a standard rate: refused,
// never read as zero.
func TestRate_ComplimentaryStandardWithoutFixedRule(t *testing.T) {
	terms := buildTerms(t, termsSpec{complimentary: "standard"})
	kept := terms.Rules[:0]
	for _, r := range terms.Rules {
		if r.Type != billing.RuleFixedPerTicket {
			kept = append(kept, r)
		}
	}
	terms.Rules = kept
	_, err := billing.Rate(terms, billing.Event{Kind: billing.KindTicketComplimentary, OccurredAt: saleAt, Currency: "EUR", Bulk: true})
	if !errors.Is(err, billing.ErrNoRule) {
		t.Fatalf("err = %v, want ErrNoRule", err)
	}
}

func TestBulkFromAttributes(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"order_source":"complimentary","issued_via":"staff_bulk"}`: true,
		`{"order_source":"complimentary","issued_via":"staff"}`:      false,
		`{"order_source":"public_feed"}`:                             false,
		``:                                                           false,
		`not json`:                                                   false,
	} {
		if got := billing.BulkFromAttributes([]byte(raw)); got != want {
			t.Errorf("%q: %v, want %v", raw, got, want)
		}
	}
}

func TestQuoteComplimentary(t *testing.T) {
	type tc struct {
		name          string
		spec          termsSpec
		in            billing.ComplimentaryQuoteInput
		mode          string
		free, charged int
		amount        int64
		limit, left   *int64
		text          string
	}
	i64 := func(n int64) *int64 { return &n }
	cases := []tc{
		{name: "free, no limit", in: billing.ComplimentaryQuoteInput{Seated: 2, Unseated: 1},
			mode: "free", free: 3, text: "free"},
		{name: "free, limit left", spec: termsSpec{freeLimit: i32(500)}, in: billing.ComplimentaryQuoteInput{Seated: 12, UsedFree: 0},
			mode: "free", free: 12, limit: i64(500), left: i64(488), text: "free, 488 of the yearly limit left"},
		{name: "free, crosses the limit", spec: termsSpec{freeLimit: i32(10)}, in: billing.ComplimentaryQuoteInput{Seated: 3, Unseated: 2, UsedFree: 8},
			mode: "free", free: 2, charged: 3, amount: 177, limit: i64(10), left: i64(0),
			text: "2 free + 3 × 0.59 EUR = 1.77 EUR, 0 of the yearly limit left"},
		{name: "free, already over", spec: termsSpec{freeLimit: i32(10)}, in: billing.ComplimentaryQuoteInput{Unseated: 2, UsedFree: 15},
			mode: "free", charged: 2, amount: 118, limit: i64(10), left: i64(0),
			text: "2 × 0.59 EUR = 1.18 EUR, 0 of the yearly limit left"},
		{name: "standard, mixed", spec: termsSpec{complimentary: "standard", freeLimit: i32(500)}, in: billing.ComplimentaryQuoteInput{Seated: 10, Unseated: 2},
			mode: "standard", charged: 12, amount: 10*109 + 2*59, text: "10 × 1.09 EUR + 2 × 0.59 EUR = 12.08 EUR"},
		{name: "bulk, override", spec: termsSpec{override: lampyrisOverride()}, in: billing.ComplimentaryQuoteInput{Unseated: 12, Bulk: true},
			mode: "bulk", charged: 12, amount: 600, text: "12 × 0.50 EUR = 6.00 EUR"},
	}
	for _, c := range cases {
		q, err := billing.QuoteComplimentary(buildTerms(t, c.spec), c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		eq := func(a, b *int64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
		if q.Mode != c.mode || q.FreeTickets != c.free || q.ChargedTickets != c.charged || q.AmountMinor != c.amount ||
			!eq(q.FreeLimit, c.limit) || !eq(q.FreeLeft, c.left) || q.Text != c.text || q.Currency != "EUR" ||
			q.Tickets != c.in.Seated+c.in.Unseated {
			t.Errorf("%s: %+v (limit %v left %v)", c.name, q, q.FreeLimit, q.FreeLeft)
		}
		var sum int64
		for _, l := range q.Lines {
			sum += l.AmountMinor
		}
		if sum != q.AmountMinor {
			t.Errorf("%s: lines sum %d, total %d", c.name, sum, q.AmountMinor)
		}
	}
	if _, err := billing.QuoteComplimentary(buildTerms(t, termsSpec{}), billing.ComplimentaryQuoteInput{Seated: -1}); err == nil {
		t.Error("negative input accepted")
	}
}
