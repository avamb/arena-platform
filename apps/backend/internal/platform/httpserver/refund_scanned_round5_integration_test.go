//go:build integration

// refund_scanned_round5_integration_test.go - review round 4 of the
// scanned-ticket refund block: an exception spent by a refund that returned
// no money (refused, rejected, closed as not refunded) comes back.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

func (e *scanEnv) dropExceptionsOnCleanup(t *testing.T) {
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM refund_scan_exceptions WHERE org_id = $1`, e.org)
	})
}

func (e *scanEnv) flatCreate(t *testing.T, amount int64) (int, map[string]any) {
	t.Helper()
	return e.pf.do(t, http.MethodPost, "/v1/refunds", e.key,
		fmt.Sprintf(`{"payment_intent_id":%q,"amount":%d,"currency":"EUR","reason":"show stopped"}`, e.f.pay, amount))
}

func (e *scanEnv) declineRefunds() {
	e.stub.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		e.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"insufficient_funds","message":"balance too low"}}`))
	}
}

func (e *scanEnv) acceptRefunds() {
	e.stub.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		e.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_%s","status":"succeeded"}`, uuid.NewString()[:8])
	}
}

// M1: the provider refuses a flat refund that relied on an exception. Nothing
// went back, so the exception comes back with the failure, and the refund
// can be sent again once the organizer's balance is topped up.
func TestRefundScanned_DeclinedFlatRefundGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	e.declineRefunds()
	st, out := e.flatCreate(t, 5000)
	if st != http.StatusCreated {
		t.Fatalf("flat refund on the exception: %d %v", st, out)
	}
	id := out["refund"].(map[string]any)["id"].(string)
	st, out = e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/approve", e.key, "")
	if st != http.StatusOK || out["refund"].(map[string]any)["state"] != "failed" {
		t.Fatalf("approve with a declining provider: %d %v; want 200 failed", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on a refund the provider refused", by)
	}
	e.acceptRefunds()
	st, out = e.flatCreate(t, 5000)
	if st != http.StatusCreated {
		t.Fatalf("second flat refund after the refusal: %d %v; want 201", st, out)
	}
	id = out["refund"].(map[string]any)["id"].(string)
	st, out = e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/approve", e.key, "")
	if st != http.StatusOK || out["refund"].(map[string]any)["state"] != "succeeded" {
		t.Fatalf("approve of the second flat refund: %d %v; want 200 succeeded", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != id {
		t.Fatalf("exception used by %q; want the refund that paid %s", by, id)
	}
}

// M1: a rejected flat refund moved no money; its exception comes back.
func TestRefundScanned_RejectedFlatRefundGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.flatCreate(t, 5000)
	if st != http.StatusCreated {
		t.Fatalf("flat refund on the exception: %d %v", st, out)
	}
	id := out["refund"].(map[string]any)["id"].(string)
	if st, out := e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/reject", e.key, ""); st != http.StatusOK {
		t.Fatalf("reject: %d %v", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on a rejected refund", by)
	}
	if st, out := e.flatCreate(t, 5000); st != http.StatusCreated {
		t.Fatalf("flat refund after the rejection: %d %v; want 201", st, out)
	}
}

// M1: the automatic refund of the ticket-cancel route, rejected, gives its
// exception back; the money still owed can then be refunded on it.
func TestRefundScanned_RejectedCancelRefundGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t, "ticket.cancel", "refund.read", "refund.create", "refund.approve")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.pf.do(t, http.MethodPost, "/v1/tickets/"+e.f.tickets[0].String()+"/cancel", e.key,
		`{"reason":"show stopped","refund_mode":"automatic","refund_amount":2500}`)
	if st != http.StatusOK {
		t.Fatalf("automatic cancel on the exception: %d %v", st, out)
	}
	var refundID string
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT id::text FROM refunds WHERE cancelled_ticket_id = $1`, e.f.tickets[0]).Scan(&refundID); err != nil {
		t.Fatalf("no refund written: %v", err)
	}
	if st, out := e.pf.do(t, http.MethodPost, "/v1/refunds/"+refundID+"/reject", e.key, ""); st != http.StatusOK {
		t.Fatalf("reject: %d %v", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on the rejected cancel refund", by)
	}
	if st, out := e.flatCreate(t, 5000); st != http.StatusCreated {
		t.Fatalf("flat refund of the money still owed: %d %v; want 201", st, out)
	}
}

// M1: an operator who found a parked flat refund not refunded at the provider
// gives its exception back with the closure.
func TestRefundScanned_NotRefundedResolveGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.flatCreate(t, 5000)
	if st != http.StatusCreated {
		t.Fatalf("flat refund on the exception: %d %v", st, out)
	}
	id := uuid.MustParse(out["refund"].(map[string]any)["id"].(string))
	e.exec(t, `UPDATE refunds SET state = 'manual_review', settlement = 'provider', provider = 'stripe', provider_attempts = 1,
		failure_code = 'provider_unavailable' WHERE id = $1`, id)
	if _, err := e.srv.refundEngine().Resolve(context.Background(), refunds.ResolveInput{OrgID: e.org, RefundID: id,
		Outcome: refunds.ResolveNotRefunded, Note: "dashboard shows nothing", Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on a refund closed as not refunded", by)
	}
}
