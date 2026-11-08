// receipt_email_test.go — spec 63, decision R-17 (П-2): after a reissue the
// charge behind a hosted Checkout Session gets the buyer's real address as
// receipt_email, with the operation's idempotency key, and nothing else.
package stripe_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestUpdateReceiptEmail_FromCheckoutSessionToCharge(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
		form  string
		key   string
	)
	a := newRefundAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/checkout/sessions/cs_test_1":
			_, _ = w.Write([]byte(`{"id":"cs_test_1","payment_intent":"pi_test_1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/payment_intents/pi_test_1":
			_, _ = w.Write([]byte(`{"id":"pi_test_1","latest_charge":{"id":"ch_test_1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/charges/ch_test_1":
			_ = r.ParseForm()
			form = r.PostForm.Encode()
			key = r.Header.Get("Idempotency-Key")
			_, _ = w.Write([]byte(`{"id":"ch_test_1"}`))
		default:
			http.Error(w, `{"error":{"type":"invalid_request_error"}}`, http.StatusNotFound)
		}
	})
	if err := a.UpdateReceiptEmail(context.Background(), "cs_test_1", " buyer@example.org ", "arena-reissue-1"); err != nil {
		t.Fatal(err)
	}
	if form != "receipt_email=buyer%40example.org" {
		t.Fatalf("charge update form %q, want only receipt_email", form)
	}
	if key != "arena-reissue-1" {
		t.Fatalf("idempotency key %q", key)
	}
	if got := strings.Join(calls, ","); got != "GET /v1/checkout/sessions/cs_test_1,GET /v1/payment_intents/pi_test_1,POST /v1/charges/ch_test_1" {
		t.Fatalf("calls: %s", got)
	}
}

func TestUpdateReceiptEmail_NoChargeIsAnError(t *testing.T) {
	posted := false
	a := newRefundAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		_, _ = w.Write([]byte(`{"id":"pi_test_2","latest_charge":null}`))
	})
	if err := a.UpdateReceiptEmail(context.Background(), "pi_test_2", "buyer@example.org", "k"); err == nil {
		t.Fatal("a payment without a charge must be an error")
	}
	if posted {
		t.Fatal("nothing may be posted without a charge")
	}
	if err := a.UpdateReceiptEmail(context.Background(), "pi_test_2", "  ", "k"); err == nil {
		t.Fatal("an empty address must be an error")
	}
}
