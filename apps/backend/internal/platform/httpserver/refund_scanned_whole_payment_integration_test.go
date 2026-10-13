//go:build integration

// refund_scanned_whole_payment_integration_test.go - a refund of the whole
// payment (the flat create route) never takes a scanned ticket's exception:
// the exception stays for the organization's per-ticket route.
package httpserver

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// M2: the flat create refuses a payment with a scanned ticket whatever its
// amount and whatever exceptions exist, spends none of them, and the
// per-ticket route then refunds the scanned ticket on its exception.
func TestRefundScanned_FlatRefundLeavesTheExceptionToTheTicketRoute(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = ANY($1)`, []uuid.UUID{e.f.tickets[0], e.f.tickets[1]})
	e.grantException(t, 0)
	e.grantException(t, 1)
	for _, amount := range []int64{100, 5000} {
		st, out := e.flatCreate(t, amount)
		if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
			t.Fatalf("flat refund of %d on two exceptions: %d %v; want 409 %s", amount, st, out, refunds.CodeTicketScanned)
		}
	}
	if a, b := e.exceptionUsedBy(t, 0), e.exceptionUsedBy(t, 1); a != "" || b != "" {
		t.Fatalf("the refused flat refund spent exceptions (%q, %q)", a, b)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, e.f.pay); n != 0 {
		t.Fatalf("the refused refund was written (%d rows)", n)
	}
	st, out := e.createRefund(t, 0)
	if st != http.StatusAccepted {
		t.Fatalf("per-ticket refund on the exception: %d %v; want 202", st, out)
	}
	if got := out["refunds"].([]any)[0].(map[string]any)["state"]; got != "succeeded" {
		t.Fatalf("per-ticket refund = %v; want succeeded", got)
	}
}

// M2: a whole-payment refund written before the scan is not let through at
// approval by an exception granted later.
func TestRefundScanned_EarlierFlatRefundDoesNotTakeALaterException(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	st, out := e.flatCreate(t, 100)
	if st != http.StatusCreated {
		t.Fatalf("flat refund before any scan: %d %v", st, out)
	}
	id := out["refund"].(map[string]any)["id"].(string)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = $1`, e.f.tickets[0])
	e.grantException(t, 0)
	st, out = e.pf.do(t, http.MethodPost, "/v1/refunds/"+id+"/approve", e.key, "")
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeTicketScanned {
		t.Fatalf("approve of an earlier small flat refund: %d %v; want 409 %s", st, out, refunds.CodeTicketScanned)
	}
	if by := e.exceptionUsedBy(t, 0); by != "" {
		t.Fatalf("the earlier refund took the exception (%q)", by)
	}
	if e.calls.Load() != 0 {
		t.Fatalf("the provider was called %d time(s)", e.calls.Load())
	}
}
