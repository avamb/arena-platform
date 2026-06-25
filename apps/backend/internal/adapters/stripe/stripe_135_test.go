package stripe_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	stripeadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/payments"
)

// compile-time interface guard (mirrors adapter.go; verifies from external package too)
var _ payments.PaymentProvider = (*stripeadapter.Adapter)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func buildStripeSignatureHeader(t *testing.T, body []byte, secret string) string {
	t.Helper()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

func newTestAdapter(serverURL string) *stripeadapter.Adapter {
	return stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_abc123",
		WebhookSecret: "whsec_testsecret",
		ClientID:      "ca_testclientid",
		BaseURL:       serverURL,
		OAuthBaseURL:  serverURL,
	})
}

// mockPaymentIntentJSON returns a minimal Stripe PaymentIntent JSON body.
func mockPaymentIntentJSON(id, clientSecret, status string) string {
	return fmt.Sprintf(`{"id":%q,"client_secret":%q,"status":%q}`, id, clientSecret, status)
}

func mockPaymentIntentJSONWithNextAction(id, clientSecret, status, scaURL string) string {
	return fmt.Sprintf(`{
		"id":%q,
		"client_secret":%q,
		"status":%q,
		"next_action":{
			"type":"redirect_to_url",
			"redirect_to_url":{"url":%q}
		}
	}`, id, clientSecret, status, scaURL)
}

func mockWebhookEvent(eventType, intentID, intentStatus string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"id":   "evt_test123",
		"type": eventType,
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":     intentID,
				"status": intentStatus,
			},
		},
	})
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestStripe135_ProviderName verifies ProviderName returns "stripe".
func TestStripe135_ProviderName(t *testing.T) {
	a := stripeadapter.New(stripeadapter.Config{SecretKey: "sk_test_x"})
	if got := a.ProviderName(); got != "stripe" {
		t.Errorf("ProviderName() = %q; want %q", got, "stripe")
	}
}

// TestStripe135_HandleWebhook_ValidSignature verifies that a correctly signed
// webhook is accepted and the EventType is parsed.
func TestStripe135_HandleWebhook_ValidSignature(t *testing.T) {
	const secret = "whsec_valid"
	body := mockWebhookEvent("payment_intent.succeeded", "pi_001", "succeeded")
	sigHeader := buildStripeSignatureHeader(t, body, secret)

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "stripe",
		SignatureHeader: sigHeader,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("HandleWebhook returned unexpected error: %v", err)
	}
	if resp.EventType != "payment_intent.succeeded" {
		t.Errorf("EventType = %q; want %q", resp.EventType, "payment_intent.succeeded")
	}
}

// TestStripe135_HandleWebhook_InvalidSignature verifies that a bad signature
// returns payments.ErrInvalidWebhookSignature.
func TestStripe135_HandleWebhook_InvalidSignature(t *testing.T) {
	const secret = "whsec_valid"
	body := mockWebhookEvent("payment_intent.succeeded", "pi_001", "succeeded")

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "stripe",
		SignatureHeader: "t=1234567890,v1=invalidsig",
		Body:            body,
	})
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("expected ErrInvalidWebhookSignature; got %v", err)
	}
}

// TestStripe135_HandleWebhook_MissingSignature verifies that an empty
// SignatureHeader returns ErrInvalidWebhookSignature.
func TestStripe135_HandleWebhook_MissingSignature(t *testing.T) {
	body := mockWebhookEvent("payment_intent.succeeded", "pi_001", "succeeded")

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: "whsec_any",
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "stripe",
		SignatureHeader: "",
		Body:            body,
	})
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("expected ErrInvalidWebhookSignature for empty header; got %v", err)
	}
}

// TestStripe135_HandleWebhook_EventTypeParsed verifies "payment_intent.succeeded"
// event is parsed correctly.
func TestStripe135_HandleWebhook_EventTypeParsed(t *testing.T) {
	const secret = "whsec_parsedtest"
	body := mockWebhookEvent("payment_intent.succeeded", "pi_parse001", "succeeded")
	sigHeader := buildStripeSignatureHeader(t, body, secret)

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		SignatureHeader: sigHeader,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EventType != "payment_intent.succeeded" {
		t.Errorf("EventType = %q; want %q", resp.EventType, "payment_intent.succeeded")
	}
	if resp.ProviderIntentID != "pi_parse001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "pi_parse001")
	}
}

// TestStripe135_HandleWebhook_RequiresAction verifies "payment_intent.requires_action"
// event is parsed correctly.
func TestStripe135_HandleWebhook_RequiresAction(t *testing.T) {
	const secret = "whsec_reqaction"
	body := mockWebhookEvent("payment_intent.requires_action", "pi_sca001", "requires_action")
	sigHeader := buildStripeSignatureHeader(t, body, secret)

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		SignatureHeader: sigHeader,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EventType != "payment_intent.requires_action" {
		t.Errorf("EventType = %q; want %q", resp.EventType, "payment_intent.requires_action")
	}
}

// TestStripe135_CreateIntent_Success verifies that CreateIntent parses a
// successful PaymentIntent response correctly.
func TestStripe135_CreateIntent_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment_intents" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSON("pi_success001", "pi_secret_001_secret", "requires_payment_method"))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         5000,
		Currency:       "usd",
		Description:    "Test intent",
		IdempotencyKey: "idem_001",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if resp.ProviderIntentID != "pi_success001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "pi_success001")
	}
	if resp.ClientSecret != "pi_secret_001_secret" {
		t.Errorf("ClientSecret = %q; want %q", resp.ClientSecret, "pi_secret_001_secret")
	}
}

// TestStripe135_CreateIntent_ApplicationFee verifies that application_fee_amount
// is computed and sent when ApplicationFeePercent > 0.
func TestStripe135_CreateIntent_ApplicationFee(t *testing.T) {
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSON("pi_fee001", "secret_fee", "requires_payment_method"))
	}))
	defer srv.Close()

	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:             "sk_test_x",
		BaseURL:               srv.URL,
		OAuthBaseURL:          srv.URL,
		ApplicationFeePercent: 2.5,
	})
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   10000,
		Currency: "usd",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}

	got := capturedForm.Get("application_fee_amount")
	if got != "250" {
		t.Errorf("application_fee_amount = %q; want %q", got, "250")
	}
}

// TestStripe135_CreateIntent_SCA_RequiresAction verifies that when Stripe returns
// requires_action status, the adapter surfaces it correctly.
func TestStripe135_CreateIntent_SCA_RequiresAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSONWithNextAction(
			"pi_sca001", "pi_sca_secret", "requires_action", "https://stripe.com/3ds"))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   2000,
		Currency: "eur",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if resp.Status != "requires_action" {
		t.Errorf("Status = %q; want %q", resp.Status, "requires_action")
	}
}

// TestStripe135_CreateIntent_IdempotencyKey verifies the Idempotency-Key header
// is forwarded to Stripe.
func TestStripe135_CreateIntent_IdempotencyKey(t *testing.T) {
	const wantKey = "unique-idem-key-xyz"
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSON("pi_idem001", "secret_idem", "requires_payment_method"))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         1000,
		Currency:       "usd",
		IdempotencyKey: wantKey,
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if gotKey != wantKey {
		t.Errorf("Idempotency-Key header = %q; want %q", gotKey, wantKey)
	}
}

// TestStripe135_CapturePayment_Success verifies CapturePayment parses the
// response correctly.
func TestStripe135_CapturePayment_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/capture") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSON("pi_cap001", "", "succeeded"))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "pi_cap001",
	})
	if err != nil {
		t.Fatalf("CapturePayment error: %v", err)
	}
	if resp.ProviderIntentID != "pi_cap001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "pi_cap001")
	}
	if resp.Status != "succeeded" {
		t.Errorf("Status = %q; want %q", resp.Status, "succeeded")
	}
}

// TestStripe135_CapturePayment_PartialAmount verifies amount_to_capture is
// included in the form when Amount > 0.
func TestStripe135_CapturePayment_PartialAmount(t *testing.T) {
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, mockPaymentIntentJSON("pi_partial001", "", "succeeded"))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "pi_partial001",
		Amount:           5000,
	})
	if err != nil {
		t.Fatalf("CapturePayment error: %v", err)
	}
	if got := capturedForm.Get("amount_to_capture"); got != "5000" {
		t.Errorf("amount_to_capture = %q; want %q", got, "5000")
	}
}

// TestStripe135_RefundPayment_Full verifies that a full refund (Amount=0) does
// not include an amount field in the form, and RefundID is returned.
func TestStripe135_RefundPayment_Full(t *testing.T) {
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"re_full001","status":"succeeded"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "pi_refund001",
		Amount:           0,
	})
	if err != nil {
		t.Fatalf("RefundPayment error: %v", err)
	}
	if resp.RefundID != "re_full001" {
		t.Errorf("RefundID = %q; want %q", resp.RefundID, "re_full001")
	}
	if capturedForm.Get("amount") != "" {
		t.Errorf("expected no amount field for full refund; got %q", capturedForm.Get("amount"))
	}
}

// TestStripe135_RefundPayment_Partial verifies partial refund with amount and
// reason included in the form.
func TestStripe135_RefundPayment_Partial(t *testing.T) {
	var capturedForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"re_partial001","status":"pending"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "pi_refund002",
		Amount:           1000,
		Reason:           "customer_request",
		IdempotencyKey:   "idem_refund_001",
	})
	if err != nil {
		t.Fatalf("RefundPayment error: %v", err)
	}
	if got := capturedForm.Get("amount"); got != "1000" {
		t.Errorf("amount = %q; want %q", got, "1000")
	}
	if got := capturedForm.Get("reason"); got != "customer_request" {
		t.Errorf("reason = %q; want %q", got, "customer_request")
	}
}

// TestStripe135_ConnectAuthorizeURL verifies the Connect OAuth URL contains all
// required parameters.
func TestStripe135_ConnectAuthorizeURL(t *testing.T) {
	a := stripeadapter.New(stripeadapter.Config{
		SecretKey:    "sk_test_x",
		ClientID:     "ca_testclientid",
		OAuthBaseURL: "https://connect.stripe.com",
	})
	rawURL := a.ConnectAuthorizeURL("https://example.com/callback", "state_abc")
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse error: %v", err)
	}
	q := parsed.Query()

	tests := []struct {
		param string
		want  string
	}{
		{"response_type", "code"},
		{"scope", "read_write"},
		{"client_id", "ca_testclientid"},
		{"state", "state_abc"},
	}
	for _, tc := range tests {
		if got := q.Get(tc.param); got != tc.want {
			t.Errorf("param %q = %q; want %q", tc.param, got, tc.want)
		}
	}
}

// TestStripe135_ConnectExchangeCode_Success verifies the token exchange returns
// the stripe_user_id from the mock response.
func TestStripe135_ConnectExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"stripe_user_id":"acct_testconnect123","access_token":"sk_live_xxx","token_type":"bearer"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	accountID, err := a.ConnectExchangeCode(context.Background(), "ac_testcode")
	if err != nil {
		t.Fatalf("ConnectExchangeCode error: %v", err)
	}
	if accountID != "acct_testconnect123" {
		t.Errorf("accountID = %q; want %q", accountID, "acct_testconnect123")
	}
}

// TestStripe135_ConnectExchangeCode_Error verifies that an error response from
// the token endpoint returns a non-nil error.
func TestStripe135_ConnectExchangeCode_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Authorization code does not exist"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.ConnectExchangeCode(context.Background(), "bad_code")
	if err == nil {
		t.Fatal("expected error for bad OAuth code; got nil")
	}
}

// TestStripe135_APIError_Returns500 verifies that a non-2xx Stripe response
// (e.g. 429 Too Many Requests) returns a descriptive error from CreateIntent.
func TestStripe135_APIError_Returns500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"Too many requests","type":"rate_limit_error","code":"rate_limit"}}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   1000,
		Currency: "usd",
	})
	if err == nil {
		t.Fatal("expected error for 429 response; got nil")
	}
}
