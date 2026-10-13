//go:build integration

// refund_scanned_whole_payment_integration_test.go - a refund of the whole
// payment spends the exceptions of scanned tickets only when it returns all
// that is left of the payment.
package httpserver

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// M2: a small refund of the whole payment cannot be tied to the scanned
// tickets, so it does not spend their one-time exceptions; a refund of all
// that is left of the payment does.
func TestRefundScanned_SmallFlatRefundDoesNotSpendTheExceptions(t *testing.T) {
	e := newScanEnv(t)
	e.dropExceptionsOnCleanup(t)
	e.exec(t, `UPDATE tickets SET used_at = now() - interval '1 hour' WHERE id = ANY($1)`, []uuid.UUID{e.f.tickets[0], e.f.tickets[1]})
	e.grantException(t, 0)
	e.grantException(t, 1)
	st, out := e.flatCreate(t, 100)
	if st != http.StatusConflict || prom0113Code(out) != refunds.CodeScanExceptionWholePayment {
		t.Fatalf("1 EUR flat refund on two exceptions: %d %v; want 409 %s", st, out, refunds.CodeScanExceptionWholePayment)
	}
	if a, b := e.exceptionUsedBy(t, 0), e.exceptionUsedBy(t, 1); a != "" || b != "" {
		t.Fatalf("the refused refund spent exceptions (%q, %q)", a, b)
	}
	if n := e.count(t, `SELECT count(*) FROM refunds WHERE payment_intent_id = $1`, e.f.pay); n != 0 {
		t.Fatalf("the refused refund was written (%d rows)", n)
	}
	st, out = e.flatCreate(t, 5000)
	if st != http.StatusCreated {
		t.Fatalf("flat refund of the whole payment: %d %v; want 201", st, out)
	}
	id := out["refund"].(map[string]any)["id"].(string)
	if a, b := e.exceptionUsedBy(t, 0), e.exceptionUsedBy(t, 1); a != id || b != id {
		t.Fatalf("exceptions used by (%q, %q); want both by %s", a, b, id)
	}
}

// M2: a whole-payment refund written before the exception was granted is not
// let through by it at approval: the exception is not its to take.
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
