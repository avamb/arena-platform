package refunds

import (
	"testing"

	"github.com/google/uuid"
)

// Round-2 review, item 6: a refund stopped because its ticket was scanned is
// never offered for a retry (it would be refused again and notify the owners
// again), while an ordinary refusal still is.
func TestRetryable_NotForAScannedTicket(t *testing.T) {
	ticket, order := uuid.New(), uuid.New()
	provider := "stripe"
	r := Refund{State: StateFailed, Settlement: "provider", Provider: &provider, TicketID: &ticket, OrderID: &order}
	if !Retryable(r) {
		t.Fatal("an ordinary refused refund must stay retryable")
	}
	code := failureTicketScanned
	r.FailureCode = &code
	if Retryable(r) {
		t.Fatal("a refund stopped for a scanned ticket is offered for a retry")
	}
}
