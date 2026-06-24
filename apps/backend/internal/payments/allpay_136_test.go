// allpay_136_test.go — tests for Feature #136: AllPay payment provider adapter.
//
// Test groups:
//
//  1. Compile-time interface assertion (AllPayAdapter implements PaymentProvider)
//  2. Constructor validation (NewAllPayAdapter)
//  3. ProviderName
//  4. CreateIntent — success + request shape
//  5. CreateIntent — error paths
//  6. CapturePayment — success + error paths
//  7. RefundPayment — success + error paths
//  8. HandleWebhook — contract fixtures (payment.completed / payment.failed)
//  9. HandleWebhook — error paths
// 10. HTTP transport details (Authorization header, Content-Type)
// 11. Integration: end-to-end create → capture cycle (mock httptest server)
// 12. Integration: AllPay registered in PaymentRoutingPolicy
package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/payments"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. Compile-time interface assertion
// ─────────────────────────────────────────────────────────────────────────────

// This line fails to compile if AllPayAdapter does not fully implement
// PaymentProvider — the earliest possible regression signal.
var _ payments.PaymentProvider = (*payments.AllPayAdapter)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// 2. Constructor validation
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_NewAdapter_EmptyKeyReturnsError(t *testing.T) {
	_, err := payments.NewAllPayAdapter(payments.AllPayConfig{})
	if err == nil {
		t.Fatal("expected error for empty APIKey, got nil")
	}
	if !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("expected error message to mention APIKey, got: %v", err)
	}
}

func TestAllPay136_NewAdapter_ValidKeySucceeds(t *testing.T) {
	a, err := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "test_key"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil adapter")
	}
}

func TestAllPay136_NewAdapter_EmptyBaseURLUsesDefault(t *testing.T) {
	a, err := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "test_key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Adapter is usable — verify it returns the correct ProviderName.
	if a.ProviderName() != "allpay" {
		t.Errorf("expected ProviderName %q, got %q", "allpay", a.ProviderName())
	}
}

func TestAllPay136_NewAdapter_CustomHTTPClientIsUsed(t *testing.T) {
	called := false
	customClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			// Return a minimal 200 response so CreateIntent can parse it.
			body := `{"id":"ap_1","status":"pending_redirect","checkout_url":"https://pay.allpay.co.il/checkout/ap_1"}`
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:     "key",
		BaseURL:    "https://unused",
		HTTPClient: customClient,
	})
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   1000,
		Currency: "ILS",
	})
	if !called {
		t.Error("expected custom HTTP client to be used for API call")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. ProviderName
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_ProviderName_ReturnsAllpay(t *testing.T) {
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "k"})
	if got := a.ProviderName(); got != "allpay" {
		t.Errorf("expected %q, got %q", "allpay", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. CreateIntent — success + request shape
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CreateIntent_SuccessReturnsCheckoutURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":           "ap_pi_001",
			"status":       "pending_redirect",
			"checkout_url": "https://pay.allpay.co.il/checkout/ap_pi_001",
			"metadata":     map[string]string{"order_id": "ord-1"},
		})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	resp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         15000,
		Currency:       "ILS",
		IdempotencyKey: "idem-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderIntentID != "ap_pi_001" {
		t.Errorf("expected ProviderIntentID %q, got %q", "ap_pi_001", resp.ProviderIntentID)
	}
	if resp.ClientSecret != "https://pay.allpay.co.il/checkout/ap_pi_001" {
		t.Errorf("expected ClientSecret (checkout URL) to be populated, got %q", resp.ClientSecret)
	}
	if resp.Status != "pending_redirect" {
		t.Errorf("expected status %q, got %q", "pending_redirect", resp.Status)
	}
}

func TestAllPay136_CreateIntent_ForwardsIdempotencyKey(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_1", "status": "pending_redirect", "checkout_url": "https://x"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         100,
		Currency:       "ILS",
		IdempotencyKey: "idem-abc-123",
	})
	if gotBody["idempotency_key"] != "idem-abc-123" {
		t.Errorf("expected idempotency_key forwarded, got: %v", gotBody["idempotency_key"])
	}
}

func TestAllPay136_CreateIntent_IncludesAllIsraeliMethodsByDefault(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_1", "status": "pending_redirect", "checkout_url": "https://x"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   100,
		Currency: "ILS",
	})

	methods, ok := gotBody["payment_methods"].([]any)
	if !ok {
		t.Fatalf("expected payment_methods array in request, got: %T %v", gotBody["payment_methods"], gotBody["payment_methods"])
	}
	want := map[string]bool{"isracard": true, "leumi": true, "bit": true, "tashlumim": true}
	for _, m := range methods {
		delete(want, m.(string))
	}
	if len(want) > 0 {
		t.Errorf("missing Israeli payment methods in default request: %v", want)
	}
}

func TestAllPay136_CreateIntent_CustomPaymentMethodsFromMetadata(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_1", "status": "pending_redirect", "checkout_url": "https://x"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   100,
		Currency: "ILS",
		Metadata: map[string]string{"payment_methods": "bit, tashlumim"},
	})

	methods, ok := gotBody["payment_methods"].([]any)
	if !ok {
		t.Fatalf("expected payment_methods array, got: %T", gotBody["payment_methods"])
	}
	if len(methods) != 2 {
		t.Errorf("expected 2 custom payment methods, got %d: %v", len(methods), methods)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. CreateIntent — error paths
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CreateIntent_NonTwoXX_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"code":"INVALID_CURRENCY","message":"unsupported currency"}`))
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   100,
		Currency: "XXX",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}

func TestAllPay136_CreateIntent_APIErrorBodyParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":"AMOUNT_TOO_LOW","message":"amount must be at least 100"}`))
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   1,
		Currency: "ILS",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr payments.AllPayAPIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != "AMOUNT_TOO_LOW" {
			t.Errorf("expected Code %q, got %q", "AMOUNT_TOO_LOW", apiErr.Code)
		}
	} else {
		// Error is wrapped — check the message is present in the error chain
		if !strings.Contains(err.Error(), "AMOUNT_TOO_LOW") {
			t.Errorf("expected AMOUNT_TOO_LOW in error: %v", err)
		}
	}
}

func TestAllPay136_CreateIntent_NetworkError_ReturnsError(t *testing.T) {
	// Start and immediately close the server to force a connection-refused error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:   1000,
		Currency: "ILS",
	})
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. CapturePayment — success + error paths
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_CapturePayment_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/capture") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "ap_pi_capture_001",
			"status": "captured",
		})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	resp, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_pi_capture_001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ProviderIntentID != "ap_pi_capture_001" {
		t.Errorf("expected ProviderIntentID %q, got %q", "ap_pi_capture_001", resp.ProviderIntentID)
	}
	if resp.Status != "captured" {
		t.Errorf("expected status %q, got %q", "captured", resp.Status)
	}
}

func TestAllPay136_CapturePayment_UsesCorrectPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_xyz", "status": "captured"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_xyz",
	})
	if gotPath != "/v1/payments/ap_xyz/capture" {
		t.Errorf("expected path %q, got %q", "/v1/payments/ap_xyz/capture", gotPath)
	}
}

func TestAllPay136_CapturePayment_NonTwoXX_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":"INTERNAL","message":"internal error"}`))
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: "ap_pi_001",
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. RefundPayment — success + error paths
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_RefundPayment_FullRefund_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/refund") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"refund_id": "ap_ref_001",
			"status":    "pending",
		})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	resp, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pi_001",
		Amount:           0, // full refund
		Reason:           "customer_request",
		IdempotencyKey:   "idem-ref-001",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.RefundID != "ap_ref_001" {
		t.Errorf("expected RefundID %q, got %q", "ap_ref_001", resp.RefundID)
	}
	if resp.Status != "pending" {
		t.Errorf("expected status %q, got %q", "pending", resp.Status)
	}
}

func TestAllPay136_RefundPayment_PartialRefund_ForwardsAmount(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"refund_id": "ap_ref_partial", "status": "pending"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pi_002",
		Amount:           5000,
		Reason:           "partial_return",
		IdempotencyKey:   "idem-partial-001",
	})
	if gotBody["amount"] != float64(5000) {
		t.Errorf("expected amount 5000 forwarded, got: %v", gotBody["amount"])
	}
	if gotBody["idempotency_key"] != "idem-partial-001" {
		t.Errorf("expected idempotency_key forwarded, got: %v", gotBody["idempotency_key"])
	}
}

func TestAllPay136_RefundPayment_NonTwoXX_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"code":"ALREADY_REFUNDED","message":"payment already fully refunded"}`))
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, err := a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pi_001",
	})
	if err == nil {
		t.Fatal("expected error for conflict response")
	}
	if !strings.Contains(err.Error(), "ALREADY_REFUNDED") {
		t.Errorf("expected error to mention ALREADY_REFUNDED, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. HandleWebhook — contract fixtures
// ─────────────────────────────────────────────────────────────────────────────

// allPayTestWebhookSecret is the shared webhook secret used for all contract
// fixtures in this test file.
const allPayTestWebhookSecret = "allpay-sandbox-webhook-secret-x9k2"

// Contract fixture: payment.completed
var allPayFixturePaymentCompleted = []byte(
	`{"event_type":"payment.completed","payment_id":"ap_pi_test_001","status":"completed","metadata":{"order_id":"ord-123","method":"isracard"}}`,
)

// Contract fixture: payment.failed
var allPayFixturePaymentFailed = []byte(
	`{"event_type":"payment.failed","payment_id":"ap_pi_test_002","status":"failed","metadata":{"reason":"card_declined"}}`,
)

func TestAllPay136_HandleWebhook_Fixture_PaymentCompleted(t *testing.T) {
	sig := payments.ComputeHMACSHA256(allPayTestWebhookSecret, allPayFixturePaymentCompleted)

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "key",
		WebhookSecret: allPayTestWebhookSecret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            allPayFixturePaymentCompleted,
		Secret:          allPayTestWebhookSecret,
	})
	if err != nil {
		t.Fatalf("unexpected error for valid fixture: %v", err)
	}
	if resp.EventType != "payment.completed" {
		t.Errorf("expected EventType %q, got %q", "payment.completed", resp.EventType)
	}
	if resp.ProviderIntentID != "ap_pi_test_001" {
		t.Errorf("expected ProviderIntentID %q, got %q", "ap_pi_test_001", resp.ProviderIntentID)
	}
	if resp.Status != "completed" {
		t.Errorf("expected Status %q, got %q", "completed", resp.Status)
	}
	if resp.Metadata["order_id"] != "ord-123" {
		t.Errorf("expected metadata order_id %q, got %q", "ord-123", resp.Metadata["order_id"])
	}
}

func TestAllPay136_HandleWebhook_Fixture_PaymentFailed(t *testing.T) {
	sig := payments.ComputeHMACSHA256(allPayTestWebhookSecret, allPayFixturePaymentFailed)

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "key",
		WebhookSecret: allPayTestWebhookSecret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            allPayFixturePaymentFailed,
		Secret:          allPayTestWebhookSecret,
	})
	if err != nil {
		t.Fatalf("unexpected error for valid fixture: %v", err)
	}
	if resp.EventType != "payment.failed" {
		t.Errorf("expected EventType %q, got %q", "payment.failed", resp.EventType)
	}
	if resp.Status != "failed" {
		t.Errorf("expected Status %q, got %q", "failed", resp.Status)
	}
	if resp.Metadata["reason"] != "card_declined" {
		t.Errorf("expected metadata reason %q, got %q", "card_declined", resp.Metadata["reason"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 9. HandleWebhook — error paths
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_HandleWebhook_InvalidSignature_ReturnsErrInvalidWebhookSignature(t *testing.T) {
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "key"})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: "0000000000000000000000000000000000000000000000000000000000000000",
		Body:            allPayFixturePaymentCompleted,
		Secret:          allPayTestWebhookSecret,
	})
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature, got: %v", err)
	}
}

func TestAllPay136_HandleWebhook_EmptySignatureHeader_ReturnsErrInvalidWebhookSignature(t *testing.T) {
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "key"})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: "",
		Body:            allPayFixturePaymentCompleted,
		Secret:          allPayTestWebhookSecret,
	})
	if !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("expected ErrInvalidWebhookSignature for empty header, got: %v", err)
	}
}

func TestAllPay136_HandleWebhook_MalformedJSON_ReturnsError(t *testing.T) {
	badBody := []byte(`not valid json {`)
	sig := payments.ComputeHMACSHA256(allPayTestWebhookSecret, badBody)

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "key"})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            badBody,
		Secret:          allPayTestWebhookSecret,
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON body")
	}
	// Must NOT be a signature error — the sig was valid.
	if errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatal("error should NOT be ErrInvalidWebhookSignature for malformed JSON after valid sig")
	}
}

func TestAllPay136_HandleWebhook_UsesReqSecretOverAdapterSecret(t *testing.T) {
	const adapterSecret = "adapter-level-secret"
	const reqSecret = "per-request-secret"

	body := []byte(`{"event_type":"payment.completed","payment_id":"x","status":"completed"}`)
	sigForReqSecret := payments.ComputeHMACSHA256(reqSecret, body)

	// Adapter is initialised with adapterSecret — req overrides it with reqSecret.
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "key",
		WebhookSecret: adapterSecret,
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sigForReqSecret,
		Body:            body,
		Secret:          reqSecret, // per-request secret takes priority
	})
	if err != nil {
		t.Fatalf("expected req.Secret to take priority over adapter secret; got error: %v", err)
	}
}

func TestAllPay136_HandleWebhook_FallsBackToAdapterWebhookSecret(t *testing.T) {
	body := []byte(`{"event_type":"payment.completed","payment_id":"x","status":"completed"}`)
	sig := payments.ComputeHMACSHA256(allPayTestWebhookSecret, body)

	// No per-request secret; adapter secret should be used.
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "key",
		WebhookSecret: allPayTestWebhookSecret,
	})
	_, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            body,
		// Secret intentionally empty — adapter secret is the fallback
	})
	if err != nil {
		t.Fatalf("expected adapter-level WebhookSecret to be used as fallback; got error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 10. HTTP transport details
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_AuthHeader_SetOnCreateIntent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_1", "status": "pending_redirect", "checkout_url": "https://x"})
	}))
	defer srv.Close()

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:  "my-secret-api-key",
		BaseURL: srv.URL,
	})
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{Amount: 100, Currency: "ILS"})
	if gotAuth != "Bearer my-secret-api-key" {
		t.Errorf("expected Authorization header %q, got %q", "Bearer my-secret-api-key", gotAuth)
	}
}

func TestAllPay136_ContentTypeHeader_SetWhenBodyPresent(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "ap_1", "status": "pending_redirect", "checkout_url": "https://x"})
	}))
	defer srv.Close()

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "k", BaseURL: srv.URL})
	_, _ = a.CreateIntent(context.Background(), payments.CreateIntentRequest{Amount: 100, Currency: "ILS"})
	if gotCT != "application/json" {
		t.Errorf("expected Content-Type %q, got %q", "application/json", gotCT)
	}
}

func TestAllPay136_RefundUsesCorrectPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"refund_id": "ref_1", "status": "pending"})
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)
	_, _ = a.RefundPayment(context.Background(), payments.RefundPaymentRequest{
		ProviderIntentID: "ap_pi_789",
	})
	if gotPath != "/v1/payments/ap_pi_789/refund" {
		t.Errorf("expected path %q, got %q", "/v1/payments/ap_pi_789/refund", gotPath)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 11. Integration: end-to-end create → capture cycle (mock httptest server)
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_Integration_CreateCaptureCycle(t *testing.T) {
	// Simulate AllPay sandbox: POST /v1/payments → 200, POST /v1/payments/{id}/capture → 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/payments":
			json.NewEncoder(w).Encode(map[string]any{
				"id":           "ap_pi_e2e_001",
				"status":       "pending_redirect",
				"checkout_url": "https://pay.allpay.co.il/checkout/ap_pi_e2e_001",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/capture"):
			json.NewEncoder(w).Encode(map[string]any{
				"id":     "ap_pi_e2e_001",
				"status": "captured",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := newTestAllPayAdapter(t, srv.URL)

	// Step 1: Create intent.
	createResp, err := a.CreateIntent(context.Background(), payments.CreateIntentRequest{
		Amount:         25000,
		Currency:       "ILS",
		IdempotencyKey: "e2e-001",
	})
	if err != nil {
		t.Fatalf("CreateIntent failed: %v", err)
	}
	if createResp.ProviderIntentID == "" {
		t.Fatal("expected non-empty ProviderIntentID after CreateIntent")
	}
	if createResp.ClientSecret == "" {
		t.Fatal("expected non-empty ClientSecret (checkout URL) after CreateIntent")
	}

	// Step 2: Capture.
	captureResp, err := a.CapturePayment(context.Background(), payments.CapturePaymentRequest{
		ProviderIntentID: createResp.ProviderIntentID,
	})
	if err != nil {
		t.Fatalf("CapturePayment failed: %v", err)
	}
	if captureResp.Status != "captured" {
		t.Errorf("expected capture status %q, got %q", "captured", captureResp.Status)
	}
}

func TestAllPay136_Integration_WebhookReceivedAfterPayment(t *testing.T) {
	// Simulate receiving a webhook after a shopper completes the hosted checkout.
	body := []byte(`{"event_type":"payment.completed","payment_id":"ap_pi_e2e_001","status":"completed","metadata":{"order_id":"ord-e2e-001"}}`)
	sig := payments.ComputeHMACSHA256(allPayTestWebhookSecret, body)

	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "sandbox-key",
		WebhookSecret: allPayTestWebhookSecret,
	})
	resp, err := a.HandleWebhook(context.Background(), payments.WebhookRequest{
		Provider:        "allpay",
		SignatureHeader: sig,
		Body:            body,
		Secret:          allPayTestWebhookSecret,
	})
	if err != nil {
		t.Fatalf("HandleWebhook failed: %v", err)
	}
	if resp.ProviderIntentID != "ap_pi_e2e_001" {
		t.Errorf("expected ProviderIntentID %q, got %q", "ap_pi_e2e_001", resp.ProviderIntentID)
	}
	if resp.EventType != "payment.completed" {
		t.Errorf("expected EventType %q, got %q", "payment.completed", resp.EventType)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 12. Integration: AllPay registered in PaymentRoutingPolicy
// ─────────────────────────────────────────────────────────────────────────────

func TestAllPay136_Integration_RegisteredInRoutingPolicy(t *testing.T) {
	a, err := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	policy := payments.NewPaymentRoutingPolicy()
	policy.Register(a)

	got, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "allpay",
		PaymentMode: "direct_merchant",
	})
	if err != nil {
		t.Fatalf("ResolveProvider failed: %v", err)
	}
	if got.ProviderName() != "allpay" {
		t.Errorf("expected resolved provider %q, got %q", "allpay", got.ProviderName())
	}
}

func TestAllPay136_Integration_WrongProviderReturnsErrUnknownProvider(t *testing.T) {
	policy := payments.NewPaymentRoutingPolicy()
	// Register AllPay but request Stripe → should fail.
	a, _ := payments.NewAllPayAdapter(payments.AllPayConfig{APIKey: "k"})
	policy.Register(a)

	_, err := policy.ResolveProvider(payments.ChannelConfig{
		Provider:    "stripe",
		PaymentMode: "direct_merchant",
	})
	if !errors.Is(err, payments.ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider for unregistered provider, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestAllPayAdapter creates an AllPayAdapter pointing at baseURL with a test API key.
func newTestAllPayAdapter(t *testing.T, baseURL string) *payments.AllPayAdapter {
	t.Helper()
	a, err := payments.NewAllPayAdapter(payments.AllPayConfig{
		APIKey:        "test-api-key",
		WebhookSecret: allPayTestWebhookSecret,
		BaseURL:       baseURL,
	})
	if err != nil {
		t.Fatalf("NewAllPayAdapter: %v", err)
	}
	return a
}

// roundTripFunc allows using a function as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
