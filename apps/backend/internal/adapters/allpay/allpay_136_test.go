// Package allpay_test verifies the AllPay adapter (feature #136).
//
// Test coverage (contract + unit):
//   - Interface guard: adapter satisfies payments.PaymentProvider
//   - ProviderName returns "allpay"
//   - CreateIntent: success (hosted URL returned), idempotency key forwarded, API error
//   - CapturePayment: full capture, partial capture, API error
//   - RefundPayment: full refund, partial refund with reason, API error
//   - HandleWebhook: valid HMAC, invalid HMAC, missing header, event type parsing
//   - Webhook fixture contract: all AllPay event types round-trip correctly
//   - Helper functions: FormatAmount, InstallmentAmount, ValidatePaymentMethod,
//     IsValidInstallmentCount, StatusToPaymentIntentState, EventTypeToPaymentIntentState,
//     HostedCheckoutURL, BuildWebhookSignature
//   - Israeli payment methods enumeration
//   - Routing policy integration: adapter registers under "allpay" key
//   - SandboxBaseURL constant exists
//
// All tests are pure unit tests; no live AllPay API or PostgreSQL required.
package allpay_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	allpayadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/allpay"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// compile-time interface guard — verifies from an external test package.
var _ payments.PaymentProvider = (*allpayadapter.Adapter)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func newTestAdapter(serverURL string) *allpayadapter.Adapter {
	return allpayadapter.New(allpayadapter.Config{
		APIKey:                "test_api_key_abc123",
		WebhookSecret:         "test_webhook_secret_xyz",
		MerchantID:            "merchant_001",
		BaseURL:               serverURL,
		ReturnURL:             "https://example.com/payment/return",
		NotifyURL:             "https://example.com/v1/allpay/webhook",
		AllowedPaymentMethods: []string{"isracard", "leumi", "bit", "tashlumim"},
	})
}

func buildSignedWebhookBody(paymentID, eventType, status string) ([]byte, string) {
	body, _ := json.Marshal(map[string]interface{}{
		"event_type": eventType,
		"payment_id": paymentID,
		"status":     status,
	})
	sig := allpayadapter.BuildWebhookSignature("test_webhook_secret_xyz", body)
	return body, sig
}

func serveCreatePayment(w http.ResponseWriter, _ *http.Request, paymentID, hostedURL, status string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"payment_id":%q,"hosted_url":%q,"status":%q}`, paymentID, hostedURL, status)
}

func serveCapture(w http.ResponseWriter, _ *http.Request, paymentID, status string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"payment_id":%q,"status":%q}`, paymentID, status)
}

func serveRefund(w http.ResponseWriter, _ *http.Request, refundID, paymentID, status string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"refund_id":%q,"payment_id":%q,"status":%q}`, refundID, paymentID, status)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_ProviderName
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_ProviderName(t *testing.T) {
	a := allpayadapter.New(allpayadapter.Config{APIKey: "key"})
	if got := a.ProviderName(); got != "allpay" {
		t.Errorf("ProviderName() = %q; want %q", got, "allpay")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_CreateIntent_*
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CreateIntent_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payments" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		serveCreatePayment(w, r, "ap_pay_001", "https://checkout.allpay.co.il/pay/ap_pay_001", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         10000,
		Currency:       "ILS",
		Description:    "Concert ticket",
		IdempotencyKey: "idem_001",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if resp.ProviderIntentID != "ap_pay_001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "ap_pay_001")
	}
	if resp.ClientSecret != "https://checkout.allpay.co.il/pay/ap_pay_001" {
		t.Errorf("ClientSecret (hosted URL) = %q; want hosted URL", resp.ClientSecret)
	}
	if resp.Status != "pending" {
		t.Errorf("Status = %q; want %q", resp.Status, "pending")
	}
}

func TestAllPay136_CreateIntent_HostedURL_InMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveCreatePayment(w, r, "ap_pay_002", "https://checkout.allpay.co.il/pay/ap_pay_002", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   5000,
		Currency: "ILS",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	hostedURL := allpayadapter.HostedCheckoutURL(resp)
	if hostedURL != "https://checkout.allpay.co.il/pay/ap_pay_002" {
		t.Errorf("HostedCheckoutURL = %q; want hosted URL", hostedURL)
	}
}

func TestAllPay136_CreateIntent_IdempotencyKeyForwarded(t *testing.T) {
	const wantKey = "unique-idem-key-allpay-001"
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		serveCreatePayment(w, r, "ap_pay_003", "https://checkout.allpay.co.il/pay/ap_pay_003", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         2000,
		Currency:       "ILS",
		IdempotencyKey: wantKey,
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if gotKey != wantKey {
		t.Errorf("Idempotency-Key = %q; want %q", gotKey, wantKey)
	}
}

func TestAllPay136_CreateIntent_AuthorizationHeaderSet(t *testing.T) {
	var gotAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		serveCreatePayment(w, r, "ap_pay_004", "https://checkout.allpay.co.il/pay/ap_pay_004", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   3000,
		Currency: "ILS",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if !strings.HasPrefix(gotAuthHeader, "Bearer ") {
		t.Errorf("Authorization header = %q; want Bearer prefix", gotAuthHeader)
	}
}

func TestAllPay136_CreateIntent_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"code":"invalid_amount","message":"Amount must be positive"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   -100,
		Currency: "ILS",
	})
	if err == nil {
		t.Fatal("expected error for 422 response; got nil")
	}
}

func TestAllPay136_CreateIntent_PaymentMethodsForwarded(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		serveCreatePayment(w, r, "ap_pay_005", "https://checkout.allpay.co.il/pay/ap_pay_005", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   5000,
		Currency: "ILS",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if capturedBody == nil {
		t.Fatal("captured body is nil")
	}
	methods, ok := capturedBody["payment_methods"]
	if !ok {
		t.Errorf("payment_methods field not forwarded to AllPay API")
		return
	}
	// JSON arrays are decoded as []interface{} by default.
	arr, ok := methods.([]interface{})
	if !ok || len(arr) == 0 {
		t.Errorf("payment_methods = %v; want non-empty array", methods)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_CapturePayment_*
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CapturePayment_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/capture") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		serveCapture(w, r, "ap_pay_cap001", "captured")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_pay_cap001",
	})
	if err != nil {
		t.Fatalf("CapturePayment error: %v", err)
	}
	if resp.ProviderIntentID != "ap_pay_cap001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "ap_pay_cap001")
	}
	if resp.Status != "captured" {
		t.Errorf("Status = %q; want %q", resp.Status, "captured")
	}
}

func TestAllPay136_CapturePayment_PartialAmount(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		serveCapture(w, r, "ap_pay_cap002", "captured")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_pay_cap002",
		Amount:           5000,
	})
	if err != nil {
		t.Fatalf("CapturePayment error: %v", err)
	}
	// amount field should be present for partial capture.
	amountVal, ok := capturedBody["amount"]
	if !ok {
		t.Errorf("amount field not present in partial capture request")
		return
	}
	// JSON numbers decode as float64.
	if amountVal.(float64) != 5000 {
		t.Errorf("amount = %v; want 5000", amountVal)
	}
}

func TestAllPay136_CapturePayment_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"code":"already_captured","message":"Payment already captured"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_pay_cap_dup",
	})
	if err == nil {
		t.Fatal("expected error for 409 response; got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_RefundPayment_*
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_RefundPayment_Full(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		serveRefund(w, r, "ap_ref_001", "ap_pay_ref001", "succeeded")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	resp, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pay_ref001",
		Amount:           0, // full refund
	})
	if err != nil {
		t.Fatalf("RefundPayment error: %v", err)
	}
	if resp.RefundID != "ap_ref_001" {
		t.Errorf("RefundID = %q; want %q", resp.RefundID, "ap_ref_001")
	}
	if resp.Status != "succeeded" {
		t.Errorf("Status = %q; want %q", resp.Status, "succeeded")
	}
	// Full refund: amount should not be present in request body.
	if _, ok := capturedBody["amount"]; ok {
		t.Errorf("amount field should not be present for full refund")
	}
}

func TestAllPay136_RefundPayment_Partial(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		serveRefund(w, r, "ap_ref_002", "ap_pay_ref002", "pending")
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pay_ref002",
		Amount:           2500,
		Reason:           "customer_request",
		IdempotencyKey:   "idem_ref_001",
	})
	if err != nil {
		t.Fatalf("RefundPayment error: %v", err)
	}
	if capturedBody["amount"].(float64) != 2500 {
		t.Errorf("amount = %v; want 2500", capturedBody["amount"])
	}
	if capturedBody["reason"] != "customer_request" {
		t.Errorf("reason = %v; want customer_request", capturedBody["reason"])
	}
}

func TestAllPay136_RefundPayment_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"code":"refund_exceeds_amount","message":"Refund amount exceeds captured amount"}`)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL)
	_, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pay_ref003",
		Amount:           999999,
	})
	if err == nil {
		t.Fatal("expected error for refund exceeding amount; got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_HandleWebhook_* (signature verification + event parsing)
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_HandleWebhook_ValidSignature(t *testing.T) {
	body, sig := buildSignedWebhookBody("ap_pay_wh001", "payment.captured", "captured")

	a := allpayadapter.New(allpayadapter.Config{
		APIKey:        "key",
		WebhookSecret: "test_webhook_secret_xyz",
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("HandleWebhook unexpected error: %v", err)
	}
	if resp.EventType != "payment.captured" {
		t.Errorf("EventType = %q; want %q", resp.EventType, "payment.captured")
	}
	if resp.ProviderIntentID != "ap_pay_wh001" {
		t.Errorf("ProviderIntentID = %q; want %q", resp.ProviderIntentID, "ap_pay_wh001")
	}
	if resp.Status != "captured" {
		t.Errorf("Status = %q; want %q", resp.Status, "captured")
	}
}

func TestAllPay136_HandleWebhook_InvalidSignature(t *testing.T) {
	body, _ := buildSignedWebhookBody("ap_pay_wh002", "payment.captured", "captured")

	a := allpayadapter.New(allpayadapter.Config{
		APIKey:        "key",
		WebhookSecret: "correct_secret",
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: "deadbeefcafebabe0000000000000000deadbeefcafebabe0000000000000000",
		Body:            body,
	})
	if err == nil {
		t.Fatal("expected error for invalid signature; got nil")
	}
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("expected ErrInvalidWebhookSignature; got %v", err)
	}
}

func TestAllPay136_HandleWebhook_MissingSignatureHeader(t *testing.T) {
	body, _ := buildSignedWebhookBody("ap_pay_wh003", "payment.captured", "captured")

	a := allpayadapter.New(allpayadapter.Config{
		APIKey:        "key",
		WebhookSecret: "secret",
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: "", // missing
		Body:            body,
	})
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Errorf("expected ErrInvalidWebhookSignature for empty header; got %v", err)
	}
}

func TestAllPay136_HandleWebhook_SecretOverriddenFromRequest(t *testing.T) {
	const overrideSecret = "per_channel_secret_override"
	body, _ := json.Marshal(map[string]interface{}{
		"event_type": "payment.authorized",
		"payment_id": "ap_pay_wh_override",
		"status":     "authorized",
	})
	sig := allpayadapter.BuildWebhookSignature(overrideSecret, body)

	// Adapter configured with a different default secret.
	a := allpayadapter.New(allpayadapter.Config{
		APIKey:        "key",
		WebhookSecret: "different_default_secret",
	})
	// Per-request secret override should be used.
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            body,
		Secret:          overrideSecret,
	})
	if err != nil {
		t.Fatalf("HandleWebhook with override secret failed: %v", err)
	}
	if resp.EventType != "payment.authorized" {
		t.Errorf("EventType = %q; want payment.authorized", resp.EventType)
	}
}

func TestAllPay136_HandleWebhook_MetadataForwarded(t *testing.T) {
	const secret = "meta_secret"
	body, _ := json.Marshal(map[string]interface{}{
		"event_type": "payment.failed",
		"payment_id": "ap_pay_meta001",
		"status":     "failed",
		"metadata": map[string]string{
			"order_id": "order_abc",
		},
	})
	sig := allpayadapter.BuildWebhookSignature(secret, body)

	a := allpayadapter.New(allpayadapter.Config{APIKey: "key", WebhookSecret: secret})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            body,
	})
	if err != nil {
		t.Fatalf("HandleWebhook error: %v", err)
	}
	if resp.Metadata["order_id"] != "order_abc" {
		t.Errorf("metadata order_id = %q; want %q", resp.Metadata["order_id"], "order_abc")
	}
	if resp.Metadata["allpay_payment_id"] != "ap_pay_meta001" {
		t.Errorf("metadata allpay_payment_id = %q; want ap_pay_meta001", resp.Metadata["allpay_payment_id"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook fixture contract — all AllPay event types round-trip correctly
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_WebhookFixture_AllEventTypes(t *testing.T) {
	const secret = "fixture_secret"

	fixtures := []struct {
		name      string
		eventType string
		status    string
		wantState string
	}{
		{"authorized", "payment.authorized", "authorized", "authorized"},
		{"captured", "payment.captured", "captured", "succeeded"},
		{"failed", "payment.failed", "failed", "failed"},
		{"expired", "payment.expired", "expired", "failed"},
		{"processing", "payment.processing", "processing", "processing"},
		{"requires_action", "payment.requires_action", "requires_action", "requires_action"},
		{"manual_review", "payment.manual_review", "manual_review", "manual_review"},
	}

	a := allpayadapter.New(allpayadapter.Config{APIKey: "key", WebhookSecret: secret})

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"event_type": tc.eventType,
				"payment_id": "ap_fixture_" + tc.name,
				"status":     tc.status,
			})
			sig := allpayadapter.BuildWebhookSignature(secret, body)

			resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
				Provider:        "allpay",
				SignatureHeader: sig,
				Body:            body,
			})
			if err != nil {
				t.Fatalf("HandleWebhook(%s) error: %v", tc.eventType, err)
			}
			if resp.EventType != tc.eventType {
				t.Errorf("EventType = %q; want %q", resp.EventType, tc.eventType)
			}

			// Verify EventTypeToPaymentIntentState maps correctly.
			gotState := allpayadapter.EventTypeToPaymentIntentState(tc.eventType)
			if gotState != tc.wantState {
				t.Errorf("EventTypeToPaymentIntentState(%q) = %q; want %q",
					tc.eventType, gotState, tc.wantState)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_StatusToPaymentIntentState
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_StatusToPaymentIntentState(t *testing.T) {
	tests := []struct {
		status    string
		wantState string
	}{
		{"pending", "created"},
		{"created", "created"},
		{"requires_action", "requires_action"},
		{"3ds_required", "requires_action"},
		{"processing", "processing"},
		{"authorized", "authorized"},
		{"captured", "succeeded"},
		{"succeeded", "succeeded"},
		{"failed", "failed"},
		{"declined", "failed"},
		{"error", "failed"},
		{"manual_review", "manual_review"},
		{"unknown_status_xyz", ""},
	}
	for _, tc := range tests {
		got := allpayadapter.StatusToPaymentIntentState(tc.status)
		if got != tc.wantState {
			t.Errorf("StatusToPaymentIntentState(%q) = %q; want %q", tc.status, got, tc.wantState)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_IsraelPaymentMethods
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_IsraelPaymentMethods(t *testing.T) {
	methods := allpayadapter.IsraelPaymentMethods()
	required := []string{"isracard", "leumi", "bit", "tashlumim"}
	methodSet := make(map[string]bool, len(methods))
	for _, m := range methods {
		methodSet[m] = true
	}
	for _, r := range required {
		if !methodSet[r] {
			t.Errorf("IsraelPaymentMethods() missing required method %q", r)
		}
	}
}

func TestAllPay136_ValidatePaymentMethod(t *testing.T) {
	validMethods := []string{"isracard", "leumi", "bit", "tashlumim"}
	for _, m := range validMethods {
		if !allpayadapter.ValidatePaymentMethod(m) {
			t.Errorf("ValidatePaymentMethod(%q) = false; want true", m)
		}
	}
	invalidMethods := []string{"visa", "mastercard", "paypal", ""}
	for _, m := range invalidMethods {
		if allpayadapter.ValidatePaymentMethod(m) {
			t.Errorf("ValidatePaymentMethod(%q) = true; want false", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_FormatAmount
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_FormatAmount(t *testing.T) {
	tests := []struct {
		agorot int64
		want   string
	}{
		{10050, "100.50 ₪"},
		{100, "1.00 ₪"},
		{0, "0.00 ₪"},
		{999999, "9999.99 ₪"},
		{5, "0.05 ₪"},
	}
	for _, tc := range tests {
		got := allpayadapter.FormatAmount(tc.agorot)
		if got != tc.want {
			t.Errorf("FormatAmount(%d) = %q; want %q", tc.agorot, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_InstallmentAmount
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_InstallmentAmount(t *testing.T) {
	tests := []struct {
		total        int64
		installments int
		want         int64
	}{
		{12000, 12, 1000},
		{9999, 3, 3333},
		{5000, 1, 5000},
		{1000, 0, 0}, // zero installments → 0
	}
	for _, tc := range tests {
		got := allpayadapter.InstallmentAmount(tc.total, tc.installments)
		if got != tc.want {
			t.Errorf("InstallmentAmount(%d, %d) = %d; want %d", tc.total, tc.installments, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_IsValidInstallmentCount
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_IsValidInstallmentCount(t *testing.T) {
	valid := []int{1, 2, 3, 4, 6, 9, 12, 18, 24, 36}
	for _, n := range valid {
		if !allpayadapter.IsValidInstallmentCount(n) {
			t.Errorf("IsValidInstallmentCount(%d) = false; want true", n)
		}
	}
	invalid := []int{0, 5, 7, 10, 15, 48}
	for _, n := range invalid {
		if allpayadapter.IsValidInstallmentCount(n) {
			t.Errorf("IsValidInstallmentCount(%d) = true; want false", n)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_HostedCheckoutURL
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_HostedCheckoutURL(t *testing.T) {
	resp := &payments.CreateIntentResponse{
		ProviderIntentID: "ap_001",
		ClientSecret:     "https://checkout.allpay.co.il/pay/ap_001",
		Status:           "pending",
		Metadata:         map[string]string{"hosted_url": "https://checkout.allpay.co.il/pay/ap_001"},
	}
	got := allpayadapter.HostedCheckoutURL(resp)
	want := "https://checkout.allpay.co.il/pay/ap_001"
	if got != want {
		t.Errorf("HostedCheckoutURL = %q; want %q", got, want)
	}
}

func TestAllPay136_HostedCheckoutURL_NilResponse(t *testing.T) {
	got := allpayadapter.HostedCheckoutURL(nil)
	if got != "" {
		t.Errorf("HostedCheckoutURL(nil) = %q; want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_RoutingPolicy_Integration
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_RoutingPolicy_Integration(t *testing.T) {
	adapter := allpayadapter.New(allpayadapter.Config{APIKey: "key"})

	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(adapter)

	if policy.Len() != 1 {
		t.Errorf("policy.Len() = %d; want 1", policy.Len())
	}

	resolved, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "allpay",
		PaymentMode: "direct_merchant",
	})
	if err != nil {
		t.Fatalf("ResolveProvider(allpay, direct_merchant) error: %v", err)
	}
	if resolved.ProviderName() != "allpay" {
		t.Errorf("resolved.ProviderName() = %q; want %q", resolved.ProviderName(), "allpay")
	}
}

func TestAllPay136_RoutingPolicy_MerchantOfRecord(t *testing.T) {
	adapter := allpayadapter.New(allpayadapter.Config{APIKey: "key"})

	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(adapter)

	resolved, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "allpay",
		PaymentMode: "merchant_of_record",
	})
	if err != nil {
		t.Fatalf("ResolveProvider(allpay, merchant_of_record) error: %v", err)
	}
	if resolved.ProviderName() != "allpay" {
		t.Errorf("resolved.ProviderName() = %q; want %q", resolved.ProviderName(), "allpay")
	}
}

func TestAllPay136_RoutingPolicy_UnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	// No adapters registered.

	_, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "allpay",
		PaymentMode: "direct_merchant",
	})
	if !errors.Is(err, payments.ErrUnknownProvider) {
		t.Errorf("expected ErrUnknownProvider; got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_SandboxBaseURL
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_SandboxBaseURL(t *testing.T) {
	if allpayadapter.SandboxBaseURL == "" {
		t.Error("SandboxBaseURL is empty; want a non-empty sandbox endpoint URL")
	}
	if !strings.HasPrefix(allpayadapter.SandboxBaseURL, "https://") {
		t.Errorf("SandboxBaseURL = %q; want https:// prefix", allpayadapter.SandboxBaseURL)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_BuildWebhookSignature
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_BuildWebhookSignature_Deterministic(t *testing.T) {
	secret := "consistency_secret"
	body := []byte(`{"event_type":"payment.captured","payment_id":"ap_det001","status":"captured"}`)

	sig1 := allpayadapter.BuildWebhookSignature(secret, body)
	sig2 := allpayadapter.BuildWebhookSignature(secret, body)
	if sig1 != sig2 {
		t.Errorf("BuildWebhookSignature not deterministic: %q != %q", sig1, sig2)
	}
	if sig1 == "" {
		t.Error("BuildWebhookSignature returned empty string")
	}
}

func TestAllPay136_BuildWebhookSignature_DifferentSecretsDifferentSigs(t *testing.T) {
	body := []byte(`{"event_type":"payment.captured"}`)
	sig1 := allpayadapter.BuildWebhookSignature("secret_A", body)
	sig2 := allpayadapter.BuildWebhookSignature("secret_B", body)
	if sig1 == sig2 {
		t.Errorf("different secrets produced same signature: %q", sig1)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_New_DefaultBaseURL
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_New_DefaultBaseURL(t *testing.T) {
	// When BaseURL is not set, the adapter should still be constructable.
	// We can't easily verify the default URL without a real HTTP call, but we
	// can verify the adapter is non-nil and ProviderName works.
	a := allpayadapter.New(allpayadapter.Config{APIKey: "key"})
	if a == nil {
		t.Fatal("New() returned nil adapter")
	}
	if a.ProviderName() != "allpay" {
		t.Errorf("ProviderName() = %q; want allpay", a.ProviderName())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_EventTypeToPaymentIntentState_UnknownEvent
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_EventTypeToPaymentIntentState_UnknownEvent(t *testing.T) {
	got := allpayadapter.EventTypeToPaymentIntentState("payment.unknown_event")
	if got != "" {
		t.Errorf("EventTypeToPaymentIntentState(unknown) = %q; want empty string", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestAllPay136_CreateIntent_MerchantIDInHeader
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CreateIntent_MerchantIDInHeader(t *testing.T) {
	var gotMerchantHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMerchantHeader = r.Header.Get("X-Merchant-ID")
		serveCreatePayment(w, r, "ap_pay_mid001", "https://checkout.allpay.co.il/pay/ap_pay_mid001", "pending")
	}))
	defer srv.Close()

	a := allpayadapter.New(allpayadapter.Config{
		APIKey:     "test_key",
		MerchantID: "merch_12345",
		BaseURL:    srv.URL,
	})
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   5000,
		Currency: "ILS",
	})
	if err != nil {
		t.Fatalf("CreateIntent error: %v", err)
	}
	if gotMerchantHeader != "merch_12345" {
		t.Errorf("X-Merchant-ID = %q; want %q", gotMerchantHeader, "merch_12345")
	}
}
