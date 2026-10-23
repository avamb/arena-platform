package billing_test

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

// The buyer's commission at checkout is exactly what billing rates the sold
// ticket at (StandardTicketCharge), and nothing for organizer_pays terms.
func TestBuyerCharge_PerTicketIsTheStandardTicketCharge(t *testing.T) {
	terms := buildTerms(t, termsSpec{})
	if bc := billing.NewBuyerCharge(terms); bc != nil {
		t.Fatal("organizer_pays terms must give no buyer charge")
	}
	var none *billing.BuyerCharge
	if none.PerTicket("EUR", 1500, false) != 0 || none.Func() != nil || none.Currency() != "" {
		t.Fatal("a nil BuyerCharge must add nothing")
	}

	terms.Terms.FeeMode = billing.FeeModeBuyerPays
	bc := billing.NewBuyerCharge(terms)
	if bc == nil || bc.Currency() != "EUR" {
		t.Fatalf("buyer_pays terms: %v", bc)
	}
	for _, tc := range []struct {
		name     string
		currency string
		price    int64
		seated   bool
	}{
		{"unseated", "EUR", 1500, false},
		{"seated", "EUR", 1500, true},
		{"low price", "EUR", 300, false},
	} {
		ev := sold(t, terms, tc.currency, tc.price, 0, tc.seated)
		std, err := billing.StandardTicketCharge(terms, ev)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := bc.PerTicket(tc.currency, tc.price, tc.seated); got != std.AmountMinor || got <= 0 {
			t.Errorf("%s: PerTicket %d, StandardTicketCharge %d", tc.name, got, std.AmountMinor)
		}
	}
	if got := bc.PerTicket("EUR", 1500, false); got != 59 {
		t.Errorf("unseated 15.00: %d, want 59", got)
	}
	if got := bc.PerTicket("EUR", 1500, true); got != 109 {
		t.Errorf("seated 15.00: %d, want 109", got)
	}
	// Never in another currency than the account's, never on a free ticket.
	if got := bc.PerTicket("USD", 1500, false); got != 0 {
		t.Errorf("USD sale on a EUR account: %d, want 0", got)
	}
	if got := bc.PerTicket("EUR", 0, false); got != 0 {
		t.Errorf("free ticket: %d, want 0", got)
	}
	if bc.Func() == nil {
		t.Error("Func of a buyer charge must be set")
	}
}
