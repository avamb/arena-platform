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

// An arena refund the provider took back says what arena will and will not do
// next: no second refund of the ticket, money still owed goes back through
// the provider dashboard.
func TestAlertText_ArenaReversalNamesTheNextStep(t *testing.T) {
	provider, status := "stripe", "failed"
	ticket := uuid.New()
	for _, code := range []string{failureReversedAfterSuccess, failureFailedAfterAcceptance} {
		c := code
		r := Refund{ID: uuid.New(), Amount: 2500, Currency: "EUR", State: StateManualReview, Provider: &provider,
			FailureCode: &c, ProviderStatus: &status, TicketID: &ticket}
		text := alertText(r, "cancelled")
		for _, want := range []string{"did not go back", "will not send this ticket's money again", "refund it there"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s alert lacks %q:\n%s", code, want, text)
			}
		}
		if strings.Contains(text, "refund again or restore") {
			t.Errorf("%s alert still offers a second arena refund:\n%s", code, text)
		}
	}
}

// Every "could not be read back" alert says how to replay the event.
func TestProviderEventAlertText_SaysHowToReplay(t *testing.T) {
	for _, kind := range []string{UnresolvedUnconfirmed, UnresolvedKnownUnconfirmed, UnresolvedNoPayment} {
		text := providerEventAlertText(kind, "stripe", "re_1", uuid.NewString(), "pi_1", uuid.NewString(), 8)
		if !strings.Contains(text, "fix the organization's payment key") || !strings.Contains(text, "Resend") {
			t.Errorf("%s alert does not say how to replay the event:\n%s", kind, text)
		}
	}
}
