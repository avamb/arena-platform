package refunds

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A refund the provider reversed after it had succeeded gets its own alert:
// the money stayed at the provider, and its tickets may already be void.
func TestAlertText_ReversedAfterSuccess(t *testing.T) {
	code, provider, status := failureReversedAfterSuccess, "stripe", "failed"
	ticket := uuid.New()
	r := Refund{ID: uuid.New(), Amount: 2500, Currency: "EUR", State: StateManualReview, Provider: &provider,
		FailureCode: &code, ProviderStatus: &status, TicketID: &ticket}
	text := alertText(r, "cancelled")
	for _, want := range []string{"reversed", "did not go back", "cancelled"} {
		if !strings.Contains(text, want) {
			t.Errorf("alert lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "partial") {
		t.Errorf("a reversal is not a partial refund:\n%s", text)
	}
}
