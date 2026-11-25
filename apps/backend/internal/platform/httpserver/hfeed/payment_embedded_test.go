package hfeed

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestWantsEmbeddedCheckout(t *testing.T) {
	for _, tc := range []struct {
		caps []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"other"}, false},
		{[]string{"stripe_elements"}, true},
		{[]string{" Stripe_Elements "}, true},
		{[]string{"x", "stripe_elements"}, true},
	} {
		if got := wantsEmbeddedCheckout(tc.caps); got != tc.want {
			t.Errorf("wantsEmbeddedCheckout(%v) = %v, want %v", tc.caps, got, tc.want)
		}
	}
}

func TestChannelForcesHostedCheckout(t *testing.T) {
	for _, tc := range []struct {
		settings string
		want     bool
	}{
		{"", false},
		{"{}", false},
		{`{"checkout":{}}`, false},
		{`{"checkout":{"stripe_ui":"auto"}}`, false},
		{`{"checkout":{"stripe_ui":"elements"}}`, false},
		{`{"checkout":{"stripe_ui":"hosted"}}`, true},
		{`{"checkout":{"stripe_ui":" HOSTED "}}`, true},
		{`not json`, false},
	} {
		if got := channelForcesHostedCheckout([]byte(tc.settings)); got != tc.want {
			t.Errorf("channelForcesHostedCheckout(%s) = %v, want %v", tc.settings, got, tc.want)
		}
	}
}

func TestOpenIntentSkipsSupersededAndTerminal(t *testing.T) {
	sup := gen.PaymentIntentFailureSuperseded
	intents := []gen.PaymentIntentRow{
		{State: "failed"},
		{State: "created", FailureCode: &sup},
		{State: "manual_review"},
		{State: "created"},
	}
	open := newestOpenIntent(intents)
	if open == nil || open != &intents[3] {
		t.Fatalf("newestOpenIntent = %v, want the last created, non-superseded row", open)
	}
	if newestOpenIntent(intents[:3]) != nil {
		t.Fatal("terminal, parked and superseded rows are not open")
	}
}

func TestExpiredEmbeddedWithoutSuccessor(t *testing.T) {
	expired, declined := "session_expired", "card_declined"
	cs, hosted := "cs_test_e", "https://checkout.stripe.test/x"
	embeddedExpired := gen.PaymentIntentRow{State: "failed", Provider: "stripe", ProviderPaymentID: &cs, FailureCode: &expired}
	if got := expiredEmbeddedWithoutSuccessor([]gen.PaymentIntentRow{embeddedExpired}); got == nil {
		t.Fatal("an expired embedded session with no successor must be recoverable")
	}
	for name, rows := range map[string][]gen.PaymentIntentRow{
		"none":     nil,
		"declined": {{State: "failed", Provider: "stripe", ProviderPaymentID: &cs, FailureCode: &declined}},
		"hosted":   {{State: "failed", Provider: "stripe", ProviderPaymentID: &cs, FailureCode: &expired, HostedCheckoutURL: &hosted}},
		"open":     {{State: "created", Provider: "stripe", ProviderPaymentID: &cs}},
		// a newer hosted row (even a failed one) is a successor
		"successor": {{State: "failed", Provider: "stripe", ProviderPaymentID: &cs, HostedCheckoutURL: &hosted}, embeddedExpired},
	} {
		if got := expiredEmbeddedWithoutSuccessor(rows); got != nil {
			t.Errorf("%s: recovered %+v, want nil", name, got)
		}
	}
}

func TestWantsPaymentBlock(t *testing.T) {
	for q, want := range map[string]bool{
		"":                            false,
		"?include=tickets":            false,
		"?include=payment":            true,
		"?include=tickets,%20Payment": true,
		"?include=a&include=payment":  true,
		"?payment=1":                  false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/t"+q, nil)
		if got := wantsPaymentBlock(r); got != want {
			t.Errorf("%q: wantsPaymentBlock = %v, want %v", q, got, want)
		}
	}
}
