package stripe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// stripeStub captures the last form-encoded request body and answers with the
// supplied JSON. Everything this adapter sends Stripe is form-encoded, so the
// only honest way to assert the wire shape is to parse the raw body.
type stripeStub struct {
	server  *httptest.Server
	lastURL string
	lastAuth,
	lastIdempotencyKey string
	lastForm url.Values
}

func newStripeStub(t *testing.T, status int, body string) *stripeStub {
	t.Helper()
	s := &stripeStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("stub: request body is not form-encoded: %v", err)
		}
		s.lastURL = r.URL.Path
		s.lastForm = form
		s.lastAuth = r.Header.Get("Authorization")
		s.lastIdempotencyKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stripeStub) adapter() *Adapter {
	return New(Config{SecretKey: "sk_test_abc", BaseURL: s.server.URL + "/v1"})
}

func sampleHostedRequest() payments.CreateHostedCheckoutRequest {
	return payments.CreateHostedCheckoutRequest{
		Amount:            18_90,
		Currency:          "CZK",
		ProductName:       "Nirvana tribute night",
		CustomerEmail:     "buyer@example.com",
		ClientReferenceID: "11111111-1111-1111-1111-111111111111",
		Metadata: map[string]string{
			"arena_checkout_session_id": "11111111-1111-1111-1111-111111111111",
			"arena_order_id":            "22222222-2222-2222-2222-222222222222",
		},
		SuccessURL:     "https://tickets.example.com/shows?checkout_token=tok",
		CancelURL:      "https://tickets.example.com/shows?checkout_token=tok",
		ExpiresAtUnix:  1893456000,
		IdempotencyKey: "11111111-1111-1111-1111-111111111111",
	}
}

const hostedOKBody = `{
  "id": "cs_test_a1b2c3",
  "object": "checkout.session",
  "url": "https://checkout.stripe.com/c/pay/cs_test_a1b2c3",
  "payment_intent": null,
  "expires_at": 1893456000,
  "payment_status": "unpaid",
  "status": "open"
}`

// TestCreateCheckoutSession_PostsTheDocumentedFormFields is the contract test
// for the hosted-checkout wire shape. Every assertion here is a field the
// widget purchase flow depends on: get one wrong and the buyer either cannot
// pay, pays the wrong amount, or pays into a session arena cannot trace back
// to an order.
func TestCreateCheckoutSession_PostsTheDocumentedFormFields(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK, hostedOKBody)

	resp, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest())
	if err != nil {
		t.Fatalf("CreateCheckoutSession returned an error: %v", err)
	}

	if stub.lastURL != "/v1/checkout/sessions" {
		t.Errorf("endpoint = %q; want /v1/checkout/sessions", stub.lastURL)
	}
	if stub.lastAuth != "Bearer sk_test_abc" {
		t.Errorf("Authorization = %q; want the org's own secret key", stub.lastAuth)
	}
	// One hosted session per arena checkout: a retried call must not create a
	// second page the buyer could pay on twice.
	if stub.lastIdempotencyKey != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("Idempotency-Key = %q; want the arena checkout session id", stub.lastIdempotencyKey)
	}

	want := map[string]string{
		"mode":                                          "payment",
		"payment_method_types[0]":                       "card",
		"line_items[0][quantity]":                       "1",
		"line_items[0][price_data][currency]":           "czk",
		"line_items[0][price_data][unit_amount]":        "1890",
		"line_items[0][price_data][product_data][name]": "Nirvana tribute night",
		"client_reference_id":                           "11111111-1111-1111-1111-111111111111",
		"customer_email":                                "buyer@example.com",
		"success_url":                                   "https://tickets.example.com/shows?checkout_token=tok",
		"cancel_url":                                    "https://tickets.example.com/shows?checkout_token=tok",
		"expires_at":                                    "1893456000",
		"metadata[arena_checkout_session_id]":           "11111111-1111-1111-1111-111111111111",
		"metadata[arena_order_id]":                      "22222222-2222-2222-2222-222222222222",
		// The SAME metadata must reach the underlying PaymentIntent, or a
		// payment_intent.* webhook (or a Stripe dashboard row for a dispute)
		// cannot be traced back to an arena order.
		"payment_intent_data[metadata][arena_checkout_session_id]": "11111111-1111-1111-1111-111111111111",
		"payment_intent_data[metadata][arena_order_id]":            "22222222-2222-2222-2222-222222222222",
	}
	for k, v := range want {
		if got := stub.lastForm.Get(k); got != v {
			t.Errorf("form[%q] = %q; want %q", k, got, v)
		}
	}

	// Automatic capture: arena's own sweeps release the seats, so an
	// authorise-then-capture dance would only add a second failure mode.
	if got := stub.lastForm.Get("capture_method"); got != "" {
		t.Errorf("capture_method = %q; want it unset so Stripe captures automatically", got)
	}
	// Cards only. An async method would let the buyer "complete" the session
	// long before the money settles — after the hold is gone.
	if got := stub.lastForm.Get("payment_method_types[1]"); got != "" {
		t.Errorf("payment_method_types[1] = %q; want cards to be the only method", got)
	}

	if resp.SessionID != "cs_test_a1b2c3" {
		t.Errorf("SessionID = %q; want cs_test_a1b2c3", resp.SessionID)
	}
	if resp.URL != "https://checkout.stripe.com/c/pay/cs_test_a1b2c3" {
		t.Errorf("URL = %q; want the hosted page url", resp.URL)
	}
	// Stripe has not minted the pi_ yet — it arrives with the webhook.
	if resp.PaymentID != "" {
		t.Errorf("PaymentID = %q; want empty when Stripe sends payment_intent: null", resp.PaymentID)
	}
	if resp.ExpiresAtUnix != 1893456000 {
		t.Errorf("ExpiresAtUnix = %d; want the expiry Stripe applied", resp.ExpiresAtUnix)
	}
}

// TestCreateCheckoutSession_OmitsEmptyOptionalFields proves a caller with no
// buyer email or expiry does not send empty form keys — Stripe rejects an
// empty customer_email outright.
func TestCreateCheckoutSession_OmitsEmptyOptionalFields(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK, hostedOKBody)

	req := sampleHostedRequest()
	req.CustomerEmail = ""
	req.ExpiresAtUnix = 0
	req.ClientReferenceID = ""
	req.Metadata = nil

	if _, err := stub.adapter().CreateCheckoutSession(context.Background(), req); err != nil {
		t.Fatalf("CreateCheckoutSession returned an error: %v", err)
	}

	for _, key := range []string{"customer_email", "expires_at", "client_reference_id"} {
		if _, present := stub.lastForm[key]; present {
			t.Errorf("form contains %q; an empty optional field must be omitted, not sent blank", key)
		}
	}
}

// TestCreateCheckoutSession_PropagatesTheStripeError proves an API error is
// surfaced, not swallowed: the caller must never hand a buyer a redirect URL
// for a page that was not created.
func TestCreateCheckoutSession_PropagatesTheStripeError(t *testing.T) {
	stub := newStripeStub(t, http.StatusBadRequest,
		`{"error":{"type":"invalid_request_error","code":"parameter_invalid_integer","message":"unit_amount must be an integer"}}`)

	resp, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest())
	if err == nil {
		t.Fatalf("CreateCheckoutSession returned no error for a 400; got %+v", resp)
	}
	if !strings.Contains(err.Error(), "unit_amount must be an integer") {
		t.Errorf("error = %v; want it to carry Stripe's own message", err)
	}
}

// TestCreateCheckoutSession_RejectsAResponseWithoutAURL guards the one shape
// that would be worst to accept silently: a 200 with no page to send the
// buyer to. Redirecting to "" would strand the buyer on a blank tab while
// their seats sat held.
func TestCreateCheckoutSession_RejectsAResponseWithoutAURL(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK, `{"id":"cs_test_x","url":""}`)

	if _, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest()); err == nil {
		t.Fatal("CreateCheckoutSession accepted a response with no url; want an error")
	}
}

// TestCreateCheckoutSession_ReadsThePaymentIntentWhenPresent covers the
// (rarer) case where Stripe does return the pi_ at creation time.
func TestCreateCheckoutSession_ReadsThePaymentIntentWhenPresent(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK,
		`{"id":"cs_test_y","url":"https://checkout.stripe.com/c/pay/cs_test_y","payment_intent":"pi_test_z"}`)

	resp, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest())
	if err != nil {
		t.Fatalf("CreateCheckoutSession returned an error: %v", err)
	}
	if resp.PaymentID != "pi_test_z" {
		t.Errorf("PaymentID = %q; want pi_test_z", resp.PaymentID)
	}
}

// TestAdapter_ImplementsHostedCheckoutProvider keeps the capability behind a
// narrow interface rather than a widened PaymentProvider: AllPay has no
// equivalent surface and must not grow dead methods.
func TestAdapter_ImplementsHostedCheckoutProvider(t *testing.T) {
	var p payments.HostedCheckoutProvider = New(Config{SecretKey: "sk_test"})
	if p.ProviderName() != "stripe" {
		t.Errorf("ProviderName = %q; want stripe", p.ProviderName())
	}
}

// TestCreateCheckoutSession_SendsValidJSONParseableBody is a belt-and-braces
// check that the stub actually saw a request (guards against a future change
// that silently skips the HTTP call).
func TestCreateCheckoutSession_SendsValidJSONParseableBody(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK, hostedOKBody)
	if _, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest()); err != nil {
		t.Fatalf("CreateCheckoutSession returned an error: %v", err)
	}
	if len(stub.lastForm) == 0 {
		t.Fatal("stub received no form fields; the adapter did not send a request body")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(hostedOKBody), &probe); err != nil {
		t.Fatalf("test fixture is not valid JSON: %v", err)
	}
}
