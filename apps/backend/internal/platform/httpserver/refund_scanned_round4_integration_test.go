//go:build integration

// refund_scanned_round4_integration_test.go - review round 3 of the
// scanned-ticket refund block.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// N1: cancelling a scanned ticket without money (allowed) must not open the
// way to a refund of the whole payment, which would return that ticket's
// money too: the whole-payment check counts every scanned ticket sold with
// the payment, cancelled or not.
func TestRefundScanned_CancelledScannedTicketStillBlocksTheWholePayment(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read", "refund.create", "refund.approve")
	e.exec(t, `UPDATE tickets SET used_at = now() WHERE id = $1`, e.f.tickets[0])
	if st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"scan4","refund_mode":"none"}`); st != http.StatusOK {
		t.Fatalf("cancel without money: %d %v; want 200", st, out)
	}
	body := fmt.Sprintf(`{"payment_intent_id":%q,"amount":5000,"currency":"EUR","reason":"whole payment"}`, e.f.pay)
	st, out := e.pf.do(t, http.MethodPost, "/v1/refunds", e.key, body)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("flat refund of the whole payment: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}

	// A whole-payment refund already waiting is stopped at the claim too.
	var id uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `INSERT INTO refunds (payment_intent_id, org_id, amount, currency, reason,
		state, settlement, provider, origin, cancel_ticket)
		VALUES ($1, $2, 5000, 'EUR', 'whole payment', 'provider_pending', 'provider', 'stripe', 'arena', false) RETURNING id`,
		e.f.pay, e.org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	got, err := e.srv.refundEngine().Drive(context.Background(), id)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	if got.State != "failed" || e.calls.Load() != 0 {
		t.Fatalf("whole-payment refund with a cancelled scanned ticket = %s with %d provider call(s); want failed and none", got.State, e.calls.Load())
	}
}
