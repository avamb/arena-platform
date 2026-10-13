//go:build integration

// refund_scanned_cancel_spend_integration_test.go - inside the cancellation
// transaction, a manual cancellation spends a scanned ticket's exception and
// an automatic one never takes it.
package httpserver

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func TestRefundScanned_CancellationTransactionSpendsOnlyForManual(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	_, cerr := e.srv.ticketsHandler().CancelTicketTx(context.Background(), htickets.CancelTicketParams{
		TicketID: e.f.tickets[0], Reason: "show stopped", RefundMode: "automatic", ActorType: "user", ScanGuard: true,
	})
	if cerr == nil || cerr.Code != refunds.CodeTicketScanned {
		t.Fatalf("automatic cancellation of a scanned ticket with an exception: %v; want %s", cerr, refunds.CodeTicketScanned)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the refused automatic cancellation spent the exception (%q)", by)
	}
	if _, cerr := e.srv.ticketsHandler().CancelTicketTx(context.Background(), htickets.CancelTicketParams{
		TicketID: e.f.tickets[0], Reason: "show stopped", RefundMode: "manual", ActorType: "user", ScanGuard: true,
	}); cerr != nil {
		t.Fatalf("manual cancellation on the exception: %v", cerr)
	}
	if by := e.exceptionUsedBy(t, 0); by != "spent" {
		t.Fatalf("after the manual cancellation the exception is %q; want spent with it", by)
	}
}
