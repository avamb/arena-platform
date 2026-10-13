//go:build integration

// refund_scanned_cancel_spend_integration_test.go - the ticket-cancel route
// spends the exception in the cancellation transaction.
package httpserver

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// L2: the ticket-cancel route spends the exception in the cancellation's own
// transaction for refund_mode automatic too, so nothing can take it between
// the cancellation and the refund it owes.
func TestRefundScanned_AutomaticCancelSpendsTheExceptionInTheCancellation(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	if _, cerr := e.srv.ticketsHandler().CancelTicketTx(context.Background(), htickets.CancelTicketParams{
		TicketID: e.f.tickets[0], Reason: "show stopped", RefundMode: "automatic", ActorType: "user", ScanGuard: true,
	}); cerr != nil {
		t.Fatalf("cancel: %v", cerr)
	}
	if by := e.exceptionUsedBy(t, 0); by != "spent" {
		t.Fatalf("after the cancellation the exception is %q; want spent with it", by)
	}
}
