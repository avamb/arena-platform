package horders

import (
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestFoldDeliveryCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   gen.OrderDeliveryCountsRow
		want string
	}{
		{gen.OrderDeliveryCountsRow{Tickets: 2, Sent: 2}, deliverySent},
		{gen.OrderDeliveryCountsRow{Tickets: 2, Sent: 1, Pending: 1}, deliveryPending},
		{gen.OrderDeliveryCountsRow{Tickets: 2, Sent: 1, Failed: 1}, deliveryFailed},
		{gen.OrderDeliveryCountsRow{Tickets: 2, Failed: 1, Pending: 1}, deliveryPending},
		{gen.OrderDeliveryCountsRow{Tickets: 2}, deliveryNone},
		{gen.OrderDeliveryCountsRow{}, deliveryNone},
	}
	for i, c := range cases {
		if got := foldDeliveryCounts(c.in); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestRefundLetterEntry(t *testing.T) {
	t.Parallel()
	at := time.Date(2099, 1, 1, 10, 0, 0, 0, time.UTC)
	if refundLetterEntry(gen.OrderRefundLetterRow{}) != nil {
		t.Fatal("an order without refunds has no refund letter")
	}
	cases := []struct {
		in    gen.OrderRefundLetterRow
		state string
		at    bool
	}{
		{gen.OrderRefundLetterRow{Refunds: 2}, refundLetterOff, false},
		{gen.OrderRefundLetterRow{Refunds: 2, Notify: 2}, refundLetterQueued, false},
		{gen.OrderRefundLetterRow{Refunds: 2, Notify: 2, Notified: 1, LastNotified: &at}, refundLetterPartial, true},
		{gen.OrderRefundLetterRow{Refunds: 2, Notify: 2, Notified: 2, LastNotified: &at}, refundLetterSent, true},
		{gen.OrderRefundLetterRow{Refunds: 2, Notify: 2, Notified: 1, GivenUp: 1, LastGivenUp: &at}, refundLetterFailed, true},
	}
	for i, c := range cases {
		m := refundLetterEntry(c.in)
		if m["state"] != c.state || (m["at"] != nil) != c.at {
			t.Errorf("case %d: %v, want %s at=%v", i, m, c.state, c.at)
		}
	}
	if m := refundLetterEntry(gen.OrderRefundLetterRow{Refunds: 3}); m["refunds"] != int64(3) {
		t.Errorf("an `off` letter counts every refund of the order: %v", m)
	}
}
