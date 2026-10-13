// module_test.go — Stripe against the payment-module contract (PAY-01,
// domain/payments/contracttest), plus the Stripe-specific normalization
// rules the core relies on.
package stripe_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments/contracttest"
)

const moduleWebhookSecret = "whsec_contract"

func stripeSigned(secret string, body []byte) http.Header {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	h := http.Header{}
	h.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil))))
	return h
}

// refundStub is a minimal POST /v1/refunds that honours Idempotency-Key the
// way Stripe does: a repeated key answers the refund the first call created.
type refundStub struct {
	mu       sync.Mutex
	requests int
	byKey    map[string][]byte
	amounts  map[string]int64
}

func (s *refundStub) amountFor(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.amounts[key]; ok {
		return v
	}
	return -1
}

func (s *refundStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/refunds" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("payment_intent") == "" || r.PostForm.Get("amount") == "" {
		http.Error(w, `{"error":{"message":"bad refund request"}}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	key := r.Header.Get("Idempotency-Key")
	if body, ok := s.byKey[key]; ok && key != "" {
		_, _ = w.Write(body)
		return
	}
	body, _ := json.Marshal(map[string]any{
		"id":     fmt.Sprintf("re_%d", len(s.byKey)+1),
		"object": "refund",
		"status": "succeeded",
		"amount": r.PostForm.Get("amount"),
	})
	s.byKey[key] = body
	if s.amounts == nil {
		s.amounts = map[string]int64{}
	}
	s.amounts[key], _ = strconv.ParseInt(r.PostForm.Get("amount"), 10, 64)
	_, _ = w.Write(body)
}

func (s *refundStub) counts() (requests, created int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, len(s.byKey)
}

func TestStripeModule_Contract(t *testing.T) {
	stub := &refundStub{byKey: map[string][]byte{}}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	module, err := stripe.Entry().Build(map[string]string{
		stripe.SecretAPIKey:        "sk_test_contract",
		stripe.SecretWebhookSecret: moduleWebhookSecret,
	}, payments.Options{StripeAPIBaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	body := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_1","object":"checkout.session","payment_status":"paid","payment_intent":"pi_test_1","amount_total":1890,"currency":"eur"}}}`)
	contracttest.Run(t, contracttest.Case{
		Module:              module,
		GoodWebhook:         payments.WebhookInput{Body: body, Header: stripeSigned(moduleWebhookSecret, body)},
		BadSignatureWebhook: payments.WebhookInput{Body: body, Header: stripeSigned("whsec_someone_else", body)},
		WantEvent: payments.NormalizedEvent{
			Kind:              payments.EventPaymentSucceeded,
			Type:              "checkout.session.completed",
			ProviderPaymentID: "cs_test_1",
			ProviderChargeRef: "pi_test_1",
			AmountMinor:       1890,
			Currency:          "EUR",
		},
		Refund: &contracttest.RefundCase{
			ChargeRef:          "pi_test_1",
			PaymentAmountMinor: 1890,
			Currency:           "EUR",
			StubRequests:       func() int { n, _ := stub.counts(); return n },
			StubCreated:        func() int { _, n := stub.counts(); return n },
			StubAmountFor:      stub.amountFor,
		},
	})
}

// A checkout.session.completed whose money has not settled is NOT a payment:
// the core acknowledges it without consuming the idempotency key.
func TestStripeModule_UnpaidCompletedSessionIsPending(t *testing.T) {
	module, _ := stripe.Entry().Build(map[string]string{stripe.SecretWebhookSecret: moduleWebhookSecret}, payments.Options{})
	parser := module.(payments.WebhookParser)
	body := []byte(`{"id":"evt_2","type":"checkout.session.completed","data":{"object":{"id":"cs_test_2","payment_status":"unpaid"}}}`)
	ev, err := parser.VerifyAndParse(t.Context(), payments.WebhookInput{Body: body, Header: stripeSigned(moduleWebhookSecret, body)})
	if err != nil {
		t.Fatalf("VerifyAndParse: %v", err)
	}
	if ev.Kind != payments.EventPaymentPending {
		t.Fatalf("Kind = %q; want %q", ev.Kind, payments.EventPaymentPending)
	}
}

// An empty signing secret never verifies, whatever the header says.
func TestStripeModule_EmptySecretNeverVerifies(t *testing.T) {
	module, _ := stripe.Entry().Build(nil, payments.Options{})
	parser := module.(payments.WebhookParser)
	body := []byte(`{"id":"evt_3","type":"payment_intent.succeeded","data":{"object":{"id":"pi_3"}}}`)
	if _, err := parser.VerifyAndParse(t.Context(), payments.WebhookInput{Body: body, Header: stripeSigned("", body)}); err == nil {
		t.Fatal("a delivery verified against an empty secret")
	}
}

// A refund event carries the refund's outcome in arena's vocabulary, so the
// core never reads Stripe's status strings.
func TestStripeModule_RefundEventCarriesTheOutcome(t *testing.T) {
	module, _ := stripe.Entry().Build(map[string]string{stripe.SecretWebhookSecret: moduleWebhookSecret}, payments.Options{})
	parser := module.(payments.WebhookParser)
	cases := map[string]payments.RefundStatus{
		"succeeded": payments.RefundSucceeded,
		"pending":   payments.RefundPending,
		"failed":    payments.RefundFailed,
		"canceled":  payments.RefundFailed,
	}
	for status, want := range cases {
		body := []byte(`{"id":"evt_r","type":"refund.updated","data":{"object":{"id":"re_1","object":"refund","status":"` + status + `","failure_reason":"expired_or_canceled_card"}}}`)
		ev, err := parser.VerifyAndParse(t.Context(), payments.WebhookInput{Body: body, Header: stripeSigned(moduleWebhookSecret, body)})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if ev.Kind != payments.EventRefundUpdated || ev.ProviderRefundID != "re_1" || ev.ProviderPaymentID != "" || ev.RefundStatus != want {
			t.Errorf("%s: kind=%q refund=%q payment=%q status=%q; want refund_updated re_1 \"\" %q",
				status, ev.Kind, ev.ProviderRefundID, ev.ProviderPaymentID, ev.RefundStatus, want)
		}
	}
}
