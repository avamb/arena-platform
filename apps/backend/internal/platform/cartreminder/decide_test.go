package cartreminder

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	later := now.Add(20 * time.Minute)
	soon := now.Add(3 * time.Minute)
	ok := Order{
		Status: "pending_payment", BuyerEmail: "a@example.com", ExpiresAt: &later,
		PaymentURL: "https://pay.example/x", CheckoutToken: "tok", ReminderOn: true,
	}
	cases := []struct {
		name string
		edit func(o *Order)
		want string
	}{
		{"sends", func(*Order) {}, ""},
		{"switched off", func(o *Order) { o.ReminderOn = false }, SkipDisabled},
		{"paid meanwhile", func(o *Order) { o.Status = "paid" }, SkipPaid},
		{"refunded", func(o *Order) { o.Status = "refunded" }, SkipPaid},
		{"expired order", func(o *Order) { o.Status = "expired" }, SkipNotPending},
		{"no expiry", func(o *Order) { o.ExpiresAt = nil }, SkipExpired},
		{"hold ends too soon", func(o *Order) { o.ExpiresAt = &soon }, SkipExpired},
		{"no address", func(o *Order) { o.BuyerEmail = "" }, SkipNoRecipient},
		{"no payment page", func(o *Order) { o.PaymentURL = "" }, SkipNoPayment},
		{"newer order of the buyer", func(o *Order) { o.NewerOrder = true }, SkipNewerOrder},
		{"no checkout token", func(o *Order) { o.CheckoutToken = "" }, SkipNoReturnPage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := ok
			c.edit(&o)
			if got := Decide(o, now); got != c.want {
				t.Fatalf("Decide = %q, want %q", got, c.want)
			}
		})
	}
}

// The pick rule leaves more room than the send rule, so an order picked at
// the edge is still sendable a few minutes later.
func TestWindows_PickLeavesRoomToSend(t *testing.T) {
	if MinLeftAtPick <= MinLeftAtSend {
		t.Fatalf("MinLeftAtPick %v must exceed MinLeftAtSend %v", MinLeftAtPick, MinLeftAtSend)
	}
	if DueAfter+MinLeftAtPick > 1860*time.Second {
		t.Fatalf("a reminder could never fit inside the widget payment window")
	}
}
