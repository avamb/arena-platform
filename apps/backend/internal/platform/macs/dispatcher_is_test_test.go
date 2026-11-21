package macs

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// TestMACSDispatcher_SkipsTestOrders proves a v1.order.paid / v1.ticket.*
// event of a test order (P-13, payload is_test) never reaches the database
// or the venue's scanning service: a nil pool would panic otherwise.
func TestMACSDispatcher_SkipsTestOrders(t *testing.T) {
	d := &Dispatcher{} // nil pool and client: any delivery attempt panics
	for _, typ := range []string{EventOrderPaid, EventTicketIssued, EventTicketRefunded, EventTicketCancelled, EventTicketRevoked} {
		ev := outbox.Event{EventType: typ, Payload: map[string]any{
			"order_id": "00000000-0000-0000-0000-000000000001", outbox.PayloadIsTest: true,
		}}
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Errorf("%s: Dispatch = %v, want nil", typ, err)
		}
	}
}
