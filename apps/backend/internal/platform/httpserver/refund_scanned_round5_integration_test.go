//go:build integration

// refund_scanned_round5_integration_test.go - review round 4 of the
// scanned-ticket refund block: an exception is spent only by the
// organization's per-ticket route, and one spent by a refund that returned no
// money (rejected, closed as not refunded) comes back.
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

// ticketRefundAs asks the per-ticket route for a refund of ticket i with key;
// a key without refund.approve leaves it requested.
func (e *scanEnv) ticketRefundAs(t *testing.T, key string, i int) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"items":[{"ticket_id":%q}],"reason":"show stopped"}`, e.f.tickets[i])
	return refundOrgPost(t, e.pf, "/v1/organizations/"+e.org.String()+"/orders/"+e.f.order.String()+"/refunds", key, "scan5-"+uuid.NewString(), body)
}

// M1: a rejected refund of the per-ticket route moved no money; the
// exception it spent comes back in the rejecting transaction, and the ticket
// can be refunded on it again.
func TestRefundScanned_RejectedTicketRefundGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t)
	creator := e.pf.key(t, e.org, "refund.read", "refund.create")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.ticketRefundAs(t, creator, 0)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund on the exception: %d %v", st, out)
	}
	id := out["refunds"].([]any)[0].(map[string]any)["id"].(string)
	if by := e.exceptionUsedBy(t, 0); by != id {
		t.Fatalf("exception used by %q; want the new refund %s", by, id)
	}
	if st, out := e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/reject", e.key, ""); st != http.StatusOK {
		t.Fatalf("reject: %d %v", st, out)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on a rejected refund", by)
	}
	st, out = e.ticketRefundAs(t, creator, 0)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund after the rejection: %d %v; want 202", st, out)
	}
	if by, again := e.exceptionUsedBy(t, 0), out["refunds"].([]any)[0].(map[string]any)["id"].(string); by != again {
		t.Fatalf("exception used by %q; want the second refund %s", by, again)
	}
}

// M1: an operator who found at the provider that a parked refund did not go
// through gives its exception back with the closure, and the ticket can be
// refunded on it again.
func TestRefundScanned_NotRefundedResolveGivesTheExceptionBack(t *testing.T) {
	e := newScanEnv(t)
	creator := e.pf.key(t, e.org, "refund.read", "refund.create")
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out := e.ticketRefundAs(t, creator, 0)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund on the exception: %d %v", st, out)
	}
	id := uuid.MustParse(out["refunds"].([]any)[0].(map[string]any)["id"].(string))
	e.exec(t, `UPDATE refunds SET state = 'manual_review', provider = 'stripe', provider_attempts = 1,
		provider_refund_id = 're_scan5_'||gen_random_uuid(), failure_code = 'provider_unavailable' WHERE id = $1`, id)
	if _, err := e.srv.refundEngine().Resolve(context.Background(), refunds.ResolveInput{OrgID: e.org, RefundID: id,
		Outcome: refunds.ResolveNotRefunded, Note: "dashboard shows nothing", Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the exception stayed spent (%q) on a refund closed as not refunded", by)
	}
	if st, out := e.ticketRefundAs(t, creator, 0); st != http.StatusAccepted {
		t.Fatalf("ticket refund after the closure: %d %v; want 202", st, out)
	}
}

// M1: a refund the provider refused outright keeps its exception, because its
// retry takes it over (TestRefundScanned_RetryInheritsTheExceptionOfARefusedRefund).
func TestRefundScanned_DeclinedTicketRefundKeepsTheExceptionForItsRetry(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	e.stub.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		e.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"insufficient_funds","message":"balance too low"}}`))
	}
	st, out := e.createRefund(t, 0)
	if st != http.StatusAccepted {
		t.Fatalf("ticket refund on the exception: %d %v", st, out)
	}
	first := out["refunds"].([]any)[0].(map[string]any)
	if first["state"] != "failed" {
		t.Fatalf("refund = %v; want failed by the provider", first["state"])
	}
	if by := e.exceptionUsedBy(t, 0); by != first["id"].(string) {
		t.Fatalf("exception used by %q; want it kept by the refused refund %s for its retry", by, first["id"])
	}
}
