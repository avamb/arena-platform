package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// versionStub records the method, path, Stripe-Version and form of every call.
type versionStub struct {
	server  *httptest.Server
	method  string
	path    string
	version string
	form    url.Values
	status  int
	body    string
}

func newVersionStub(t *testing.T, status int, body string) *versionStub {
	t.Helper()
	s := &versionStub{status: status, body: body}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.form, _ = url.ParseQuery(string(raw))
		s.method, s.path, s.version = r.Method, r.URL.Path, r.Header.Get("Stripe-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *versionStub) adapter() *Adapter {
	return New(Config{SecretKey: "sk_test_abc", BaseURL: s.server.URL + "/v1"})
}

func TestCreateEmbeddedCheckoutSession_WireShape(t *testing.T) {
	stub := newVersionStub(t, http.StatusOK,
		`{"id":"cs_test_el1","client_secret":"cs_test_el1_secret_x","ui_mode":"elements","url":null,"status":"open","expires_at":1900000000}`)
	req := sampleHostedRequest()
	req.ReturnURL = "https://api.example.test/v1/public/payment-return?r=x&checkout_token=t"
	req.SuccessURL, req.CancelURL = "https://ignored.test/s", "https://ignored.test/c"
	req.ExpiresAtUnix = 1900000000
	req.Locale = "cs"
	req.IdempotencyKey = "ref:elements"

	resp, err := stub.adapter().CreateEmbeddedCheckoutSession(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateEmbeddedCheckoutSession: %v", err)
	}
	if resp.SessionID != "cs_test_el1" || resp.ClientSecret != "cs_test_el1_secret_x" {
		t.Fatalf("response = %+v", resp)
	}
	f := stub.form
	for key, want := range map[string]string{
		"mode":                                   "payment",
		"ui_mode":                                "elements",
		"allowed_payment_method_types[0]":        "card",
		"wallet_options[link][display]":          "never",
		"return_url":                             req.ReturnURL,
		"customer_email":                         req.CustomerEmail,
		"client_reference_id":                    req.ClientReferenceID,
		"expires_at":                             "1900000000",
		"locale":                                 "cs",
		"line_items[0][price_data][unit_amount]": "1890",
		"metadata[arena_checkout_session_id]":    req.ClientReferenceID,
	} {
		if got := f.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for _, forbidden := range []string{"success_url", "cancel_url", "payment_method_types[0]", "allowed_payment_method_types[1]"} {
		if f.Has(forbidden) {
			t.Errorf("%s was sent; Stripe refuses it for ui_mode=elements", forbidden)
		}
	}
	if stub.version != stripeElementsAPIVersion {
		t.Errorf("Stripe-Version = %q, want the pinned %q", stub.version, stripeElementsAPIVersion)
	}
}

func TestCreateEmbeddedCheckoutSession_RequiresReturnURLAndSecret(t *testing.T) {
	stub := newVersionStub(t, http.StatusOK, `{"id":"cs_test_el2","ui_mode":"elements"}`)
	req := sampleHostedRequest()
	if _, err := stub.adapter().CreateEmbeddedCheckoutSession(context.Background(), req); err == nil {
		t.Fatal("no return_url: want an error before any call")
	}
	req.ReturnURL = "https://api.example.test/ret"
	if _, err := stub.adapter().CreateEmbeddedCheckoutSession(context.Background(), req); err == nil {
		t.Fatal("an answer without client_secret must be an error")
	}
}

func TestCreateEmbeddedCheckoutSession_RefusalIsTyped(t *testing.T) {
	stub := newVersionStub(t, http.StatusBadRequest,
		`{"error":{"type":"invalid_request_error","code":"parameter_unknown","message":"Received unknown parameter: ui_mode"}}`)
	req := sampleHostedRequest()
	req.ReturnURL = "https://api.example.test/ret"
	_, err := stub.adapter().CreateEmbeddedCheckoutSession(context.Background(), req)
	var refused *payments.HostedCheckoutRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a HostedCheckoutRefusedError", err)
	}
}

func TestHostedCheckoutSession_KeepsItsOwnAPIVersion(t *testing.T) {
	stub := newVersionStub(t, http.StatusOK, `{"id":"cs_test_h","url":"https://checkout.stripe.test/x"}`)
	if _, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest()); err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if stub.version != stripeAPIVersion {
		t.Errorf("hosted Stripe-Version = %q, want the unchanged %q", stub.version, stripeAPIVersion)
	}
	if stub.form.Has("ui_mode") || stub.form.Has("return_url") || stub.form.Has("allowed_payment_method_types[0]") {
		t.Errorf("hosted form carries embedded parameters: %v", stub.form)
	}
}

func TestRetrieveEmbeddedCheckoutSession_SecretOnlyWhileOpen(t *testing.T) {
	stub := newVersionStub(t, http.StatusOK,
		`{"id":"cs_test_r","client_secret":"cs_test_r_secret","ui_mode":"elements","status":"open","payment_status":"unpaid"}`)
	got, err := stub.adapter().RetrieveEmbeddedCheckoutSession(context.Background(), "cs_test_r")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.ClientSecret != "cs_test_r_secret" || got.Status != payments.EmbeddedSessionOpen {
		t.Fatalf("open session = %+v", got)
	}
	if stub.method != http.MethodGet || !strings.HasSuffix(stub.path, "/checkout/sessions/cs_test_r") {
		t.Errorf("call = %s %s", stub.method, stub.path)
	}
	stub.body = `{"id":"cs_test_r","client_secret":"cs_test_r_secret","ui_mode":"elements","status":"complete","payment_status":"paid"}`
	got, err = stub.adapter().RetrieveEmbeddedCheckoutSession(context.Background(), "cs_test_r")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.ClientSecret != "" {
		t.Error("a complete session must not hand out its client secret")
	}
}

func TestExpireEmbeddedCheckoutSession_RefusalIsNotExpirable(t *testing.T) {
	stub := newVersionStub(t, http.StatusBadRequest,
		`{"error":{"type":"invalid_request_error","message":"Only Checkout Sessions with a status in [\"open\"] can be expired."}}`)
	err := stub.adapter().ExpireEmbeddedCheckoutSession(context.Background(), "cs_test_paid")
	if !errors.Is(err, payments.ErrCheckoutSessionNotExpirable) {
		t.Fatalf("err = %v, want ErrCheckoutSessionNotExpirable", err)
	}
	stub.status, stub.body = http.StatusInternalServerError, `oops`
	err = stub.adapter().ExpireEmbeddedCheckoutSession(context.Background(), "cs_test_paid")
	if err == nil || errors.Is(err, payments.ErrCheckoutSessionNotExpirable) {
		t.Fatalf("a 5xx is an unknown outcome, not a refusal: %v", err)
	}
	stub.status, stub.body = http.StatusOK, `{"id":"cs_test_paid","status":"expired"}`
	if err := stub.adapter().ExpireEmbeddedCheckoutSession(context.Background(), "cs_test_paid"); err != nil {
		t.Fatalf("expire ok: %v", err)
	}
}

// TestEmbeddedCheckout_EveryCallPinnedToDahlia guards the live probe of
// 2026-11-22: 2025-09-30.clover refuses ui_mode=elements, 2026-03-25.dahlia
// accepts it. Every embedded call (create, retrieve, expire) must send the
// dahlia header; the hosted flow and refunds stay on acacia. The bodies are
// dahlia-shaped: payment_intent is null on create (created lazily on
// confirm) and may come back expanded on retrieve.
func TestEmbeddedCheckout_EveryCallPinnedToDahlia(t *testing.T) {
	const dahlia = "2026-03-25.dahlia"
	if stripeElementsAPIVersion != dahlia {
		t.Fatalf("stripeElementsAPIVersion = %q, want %q", stripeElementsAPIVersion, dahlia)
	}
	if stripeAPIVersion == dahlia {
		t.Fatal("the hosted/refund API version must stay on acacia")
	}

	stub := newVersionStub(t, http.StatusOK,
		`{"id":"cs_test_d1","object":"checkout.session","client_secret":"cs_test_d1_secret_y","ui_mode":"elements","url":null,"payment_intent":null,"status":"open","payment_status":"unpaid","expires_at":1900000000}`)
	req := sampleHostedRequest()
	req.ReturnURL = "https://api.example.test/ret"
	resp, err := stub.adapter().CreateEmbeddedCheckoutSession(context.Background(), req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if stub.version != dahlia || stub.form.Get("ui_mode") != "elements" {
		t.Errorf("create: Stripe-Version=%q ui_mode=%q", stub.version, stub.form.Get("ui_mode"))
	}
	if resp.ClientSecret != "cs_test_d1_secret_y" || resp.PaymentID != "" || resp.ExpiresAtUnix != 1900000000 {
		t.Errorf("create response = %+v", resp)
	}

	stub.body = `{"id":"cs_test_d1","client_secret":"cs_test_d1_secret_y","ui_mode":"elements","payment_intent":{"id":"pi_test_d1","object":"payment_intent"},"status":"open","payment_status":"unpaid"}`
	got, err := stub.adapter().RetrieveEmbeddedCheckoutSession(context.Background(), "cs_test_d1")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if stub.version != dahlia || got.ClientSecret != "cs_test_d1_secret_y" {
		t.Errorf("retrieve: Stripe-Version=%q session=%+v", stub.version, got)
	}

	stub.body = `{"id":"cs_test_d1","status":"expired","payment_intent":"pi_test_d1"}`
	if err := stub.adapter().ExpireEmbeddedCheckoutSession(context.Background(), "cs_test_d1"); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if stub.version != dahlia || !strings.HasSuffix(stub.path, "/cs_test_d1/expire") {
		t.Errorf("expire: Stripe-Version=%q path=%q", stub.version, stub.path)
	}

	stub.body = `{"id":"re_1","status":"succeeded","amount":100,"currency":"eur"}`
	_, _ = stub.adapter().RefundPayment(context.Background(), payments.RefundPaymentRequest{ProviderIntentID: "pi_test_d1", Amount: 100})
	if stub.version != stripeAPIVersion {
		t.Errorf("refund Stripe-Version = %q, want acacia %q", stub.version, stripeAPIVersion)
	}
}
