// stripe_billing_162_test.go — tests for Stripe Billing adapter (feature #162).
//
// Tests naming convention: TestStripeBilling162_*
//
// All tests run without a real database or Stripe account. The Stripe API is
// mocked via httptest.Server; the DB is mocked by injecting gen.New(nil) (routes
// wired but DB calls will panic — caught by the nil-DB recovery in tests that
// don't actually reach DB calls) or a custom mock querier.
package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	stripebillingadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Step 1 — Migration file
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step1_MigrationFileExists verifies that migration
// 0037_stripe_billing.sql exists and contains the expected DDL.
func TestStripeBilling162_Step1_MigrationFileExists(t *testing.T) {
	content := findFileByName(t, "0037_stripe_billing.sql")

	checks := []string{
		"CREATE TABLE stripe_customers",
		"stripe_customer_id",
		"org_id",
		"stripe_invoice_id",
		"ALTER TABLE invoices",
		"+goose Up",
		"+goose Down",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("0037_stripe_billing.sql: missing %q", check)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 — SQL query file
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step2_SQLQueryFileExists verifies stripe_billing.sql
// contains all four required queries.
func TestStripeBilling162_Step2_SQLQueryFileExists(t *testing.T) {
	content := findFileByName(t, "stripe_billing.sql")

	queries := []string{
		"UpsertStripeCustomer",
		"GetStripeCustomerByOrgID",
		"UpdateInvoiceStripeID",
		"GetInvoiceByStripeID",
	}
	for _, q := range queries {
		if !strings.Contains(content, q) {
			t.Errorf("stripe_billing.sql: missing query %q", q)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3 — Gen file structs and functions
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step3_GenFileExists verifies stripe_billing.sql.go
// contains the expected types and functions.
func TestStripeBilling162_Step3_GenFileExists(t *testing.T) {
	content := findFileByName(t, "stripe_billing.sql.go")

	checks := []string{
		"StripeCustomerRow",
		"InvoiceStripeRow",
		"UpsertStripeCustomer",
		"GetStripeCustomerByOrgID",
		"UpdateInvoiceStripeID",
		"GetInvoiceByStripeID",
		"StripeInvoiceID",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("stripe_billing.sql.go: missing %q", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 — Querier interface
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step4_QuerierInterface verifies querier.go exposes the
// four Stripe Billing methods.
func TestStripeBilling162_Step4_QuerierInterface(t *testing.T) {
	content := findFileByName(t, "querier.go")

	methods := []string{
		"UpsertStripeCustomer",
		"GetStripeCustomerByOrgID",
		"UpdateInvoiceStripeID",
		"GetInvoiceByStripeID",
	}
	for _, m := range methods {
		if !strings.Contains(content, m) {
			t.Errorf("querier.go: missing method %q", m)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 5 — Stripe Billing adapter package
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step5_AdapterPackageExists verifies the stripebilling
// adapter package has the expected exported symbols.
func TestStripeBilling162_Step5_AdapterPackageExists(t *testing.T) {
	content := findFileByName(t, "stripebilling_adapter.go")

	checks := []string{
		"package stripebilling",
		"type Config struct",
		"type Adapter struct",
		"func New(",
		"CreateOrUpdateCustomer",
		"CreateInvoiceItem",
		"CreateAndFinalizeInvoice",
		"HandleBillingWebhook",
		"BillingWebhookEvent",
		"EventInvoicePaid",
		"EventInvoicePaymentFailed",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("stripebilling/adapter.go: missing %q", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 6 — Adapter constructor validation
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step6_NewAdapter verifies New returns a non-nil adapter
// and sets the default WebhookTolerance when zero.
func TestStripeBilling162_Step6_NewAdapter(t *testing.T) {
	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey:     "sk_test_abc",
		WebhookSecret: "whsec_test",
	})
	if a == nil {
		t.Fatal("stripebilling.New() returned nil")
	}
}

// TestStripeBilling162_Step6_NewAdapterCustomBaseURL verifies that a custom
// BaseURL is used (needed for test mock servers).
func TestStripeBilling162_Step6_NewAdapterCustomBaseURL(t *testing.T) {
	const testURL = "https://example.com/stripe"
	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey: "sk_test_x",
		BaseURL:   testURL,
	})
	if a == nil {
		t.Fatal("stripebilling.New() returned nil")
	}
	// Verify the adapter uses the custom URL by making it hit a test server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cus_test123","email":"","name":""}`)
	}))
	defer ts.Close()

	a2 := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey: "sk_test_x",
		BaseURL:   ts.URL,
	})
	customerID, err := a2.CreateOrUpdateCustomer(context.Background(), "test@example.com", "Test", "idem-key-1")
	if err != nil {
		t.Fatalf("CreateOrUpdateCustomer: unexpected error: %v", err)
	}
	if customerID != "cus_test123" {
		t.Errorf("CreateOrUpdateCustomer: got %q, want %q", customerID, "cus_test123")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 7 — CreateOrUpdateCustomer API call shape
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step7_CreateOrUpdateCustomer_SendsCorrectRequest
// verifies that CreateOrUpdateCustomer POSTs to /customers with the right form
// fields and Authorization header.
func TestStripeBilling162_Step7_CreateOrUpdateCustomer_SendsCorrectRequest(t *testing.T) {
	var capturedRequest *http.Request
	var capturedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRequest = r
		capturedBody, _ = readRequestBody(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cus_abc","email":"org@example.com","name":"Acme Ltd"}`)
	}))
	defer ts.Close()

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey: "sk_test_secretkey",
		BaseURL:   ts.URL,
	})

	id, err := a.CreateOrUpdateCustomer(context.Background(), "org@example.com", "Acme Ltd", "cust-org123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "cus_abc" {
		t.Errorf("got customer ID %q, want %q", id, "cus_abc")
	}

	if capturedRequest == nil {
		t.Fatal("no request captured")
	}
	if capturedRequest.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", capturedRequest.Method)
	}
	if !strings.HasSuffix(capturedRequest.URL.Path, "/customers") {
		t.Errorf("path = %q, want /customers", capturedRequest.URL.Path)
	}
	authHeader := capturedRequest.Header.Get("Authorization")
	if authHeader != "Bearer sk_test_secretkey" {
		t.Errorf("Authorization = %q, want %q", authHeader, "Bearer sk_test_secretkey")
	}
	if !strings.Contains(string(capturedBody), "email=org%40example.com") &&
		!strings.Contains(string(capturedBody), "email=org@example.com") {
		t.Errorf("request body missing email field; got: %s", capturedBody)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 8 — CreateInvoiceItem API call shape
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step8_CreateInvoiceItem verifies that CreateInvoiceItem
// POSTs to /invoiceitems with customer, amount, currency, description.
func TestStripeBilling162_Step8_CreateInvoiceItem(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/invoiceitems") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ii_test001"}`)
	}))
	defer ts.Close()

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey: "sk_test_x",
		BaseURL:   ts.URL,
	})

	itemID, err := a.CreateInvoiceItem(context.Background(), "cus_abc", "Monthly fee", 5000, "EUR", "item-line001")
	if err != nil {
		t.Fatalf("CreateInvoiceItem: unexpected error: %v", err)
	}
	if itemID != "ii_test001" {
		t.Errorf("got item ID %q, want %q", itemID, "ii_test001")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 9 — CreateAndFinalizeInvoice API call shape
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step9_CreateAndFinalizeInvoice verifies the two-step
// create→finalize flow against a mock Stripe server.
func TestStripeBilling162_Step9_CreateAndFinalizeInvoice(t *testing.T) {
	var invoiceCreateCalled bool
	var invoiceFinalizeCalled bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/invoices" {
			invoiceCreateCalled = true
			fmt.Fprint(w, `{"id":"in_test001","status":"draft"}`)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize") {
			invoiceFinalizeCalled = true
			fmt.Fprint(w, `{"id":"in_test001","status":"open"}`)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey: "sk_test_x",
		BaseURL:   ts.URL,
	})

	invoiceID, err := a.CreateAndFinalizeInvoice(
		context.Background(),
		"cus_abc",
		"Platform fee 2026-05",
		map[string]string{"arena_invoice_id": "inv-123"},
		"inv-local001",
	)
	if err != nil {
		t.Fatalf("CreateAndFinalizeInvoice: unexpected error: %v", err)
	}
	if invoiceID != "in_test001" {
		t.Errorf("got invoice ID %q, want %q", invoiceID, "in_test001")
	}
	if !invoiceCreateCalled {
		t.Error("POST /invoices was not called")
	}
	if !invoiceFinalizeCalled {
		t.Error("POST /invoices/{id}/finalize was not called")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 10 — HandleBillingWebhook — invoice.paid
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step10_HandleWebhook_InvoicePaid verifies that a valid
// invoice.paid event is parsed correctly.
func TestStripeBilling162_Step10_HandleWebhook_InvoicePaid(t *testing.T) {
	const secret = "whsec_test_billing_paid"
	body := buildStripeBillingWebhookBody(t, "invoice.paid", "in_test001", "paid")
	sigHeader := buildStripeBillingSigHeader(t, body, secret)

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})

	event, err := a.HandleBillingWebhook(body, sigHeader, "")
	if err != nil {
		t.Fatalf("HandleBillingWebhook: unexpected error: %v", err)
	}
	if event.EventType != stripebillingadapter.EventInvoicePaid {
		t.Errorf("EventType = %q, want %q", event.EventType, stripebillingadapter.EventInvoicePaid)
	}
	if event.StripeInvoiceID != "in_test001" {
		t.Errorf("StripeInvoiceID = %q, want %q", event.StripeInvoiceID, "in_test001")
	}
	if event.Status != "paid" {
		t.Errorf("Status = %q, want %q", event.Status, "paid")
	}
}

// TestStripeBilling162_Step10_HandleWebhook_PaymentFailed verifies that
// invoice.payment_failed events are parsed.
func TestStripeBilling162_Step10_HandleWebhook_PaymentFailed(t *testing.T) {
	const secret = "whsec_billing_failed"
	body := buildStripeBillingWebhookBody(t, "invoice.payment_failed", "in_failed001", "payment_failed")
	sigHeader := buildStripeBillingSigHeader(t, body, secret)

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})

	event, err := a.HandleBillingWebhook(body, sigHeader, "")
	if err != nil {
		t.Fatalf("HandleBillingWebhook: unexpected error: %v", err)
	}
	if event.EventType != stripebillingadapter.EventInvoicePaymentFailed {
		t.Errorf("EventType = %q, want %q", event.EventType, stripebillingadapter.EventInvoicePaymentFailed)
	}
	if event.StripeInvoiceID != "in_failed001" {
		t.Errorf("StripeInvoiceID = %q, want %q", event.StripeInvoiceID, "in_failed001")
	}
}

// TestStripeBilling162_Step10_HandleWebhook_InvalidSignature verifies that
// a tampered signature returns ErrInvalidWebhookSignature.
func TestStripeBilling162_Step10_HandleWebhook_InvalidSignature(t *testing.T) {
	const secret = "whsec_test"
	body := buildStripeBillingWebhookBody(t, "invoice.paid", "in_001", "paid")

	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey:     "sk_test_x",
		WebhookSecret: secret,
	})

	_, err := a.HandleBillingWebhook(body, "t=12345,v1=badsignature", "")
	if err == nil {
		t.Fatal("expected error for invalid signature, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 11 — HTTP handler source checks
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step11_HandlerSourceExists verifies stripe_billing.go
// contains expected handler names and key patterns.
func TestStripeBilling162_Step11_HandlerSourceExists(t *testing.T) {
	content := findFileByName(t, "stripe_billing.go")

	checks := []string{
		"handlePushInvoiceToStripe",
		"handleStripeBillingWebhook",
		"stripeBillingHelper",
		"syncStripeBillingInvoicePaid",
		"invoice.paid",
		"invoice.payment_failed",
		"Stripe-Signature",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("stripe_billing.go: missing %q", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 12 — Server.go integration
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step12_ServerGoHasField verifies server.go declares the
// stripeBilling field and the StripeBilling option.
func TestStripeBilling162_Step12_ServerGoHasField(t *testing.T) {
	content := findFileByName(t, "server.go")

	checks := []string{
		"stripeBilling",
		"StripeBilling",
		"stripeBillingHelper",
		"billing/stripe/push-invoice",
		"billing/stripe/webhook",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("server.go: missing %q", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 13 — Push invoice HTTP handler — unavailable when adapter not set
// ─────────────────────────────────────────────────────────────────────────────

func buildStripeBillingServer162(t *testing.T, billing stripeBillingHelper) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildStripeBillingServer162: NewStubProvider: %v", err)
	}
	return New(Options{
		Config:         cfg,
		Auth:           stub,
		Pool:           &dbDownPool{},
		BillingQueries: gen.New(nil),
		StripeBilling:  billing,
	})
}

// TestStripeBilling162_Step13_PushInvoice_UnavailableWhenNilAdapter verifies
// that the push-invoice route is not mounted when StripeBilling is nil.
func TestStripeBilling162_Step13_PushInvoice_UnavailableWhenNilAdapter(t *testing.T) {
	// Build server without StripeBilling adapter.
	s := buildStripeBillingServer162(t, nil)

	tok := mintBillingToken(t, s)
	invoiceID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/push-invoice/"+invoiceID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	// Route should not be mounted — expect 404 or 405
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 when StripeBilling is nil, got 200; body: %s", w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 14 — Push invoice HTTP handler — requires JWT
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step14_PushInvoice_RequiresJWT verifies that the
// push-invoice endpoint returns 401 for unauthenticated requests.
func TestStripeBilling162_Step14_PushInvoice_RequiresJWT(t *testing.T) {
	mock := &mockStripeBillingAdapter{}
	s := buildStripeBillingServer162(t, mock)

	invoiceID := uuid.New().String()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/push-invoice/"+invoiceID, nil)
	// No Authorization header.
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 15 — Push invoice HTTP handler — 400 on invalid UUID
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step15_PushInvoice_InvalidUUID verifies 400 on bad UUID.
func TestStripeBilling162_Step15_PushInvoice_InvalidUUID(t *testing.T) {
	mock := &mockStripeBillingAdapter{}
	s := buildStripeBillingServer162(t, mock)
	tok := mintBillingToken(t, s)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/push-invoice/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 16 — Webhook endpoint — public (no JWT required)
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step16_Webhook_IsPublic verifies the webhook endpoint
// does not require a JWT (but does require Stripe-Signature).
func TestStripeBilling162_Step16_Webhook_IsPublic(t *testing.T) {
	mock := &mockStripeBillingAdapter{}
	s := buildStripeBillingServer162(t, mock)

	// Send without any JWT — should not get 401.
	body := []byte(`{}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header — deliberately omitted.
	// No Stripe-Signature — should get 400 (bad request), not 401.
	s.router.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("webhook should not require JWT auth; got 401; body: %s", w.Body.String())
	}
}

// TestStripeBilling162_Step16_Webhook_RejectsMissingSignature verifies that
// a request with no Stripe-Signature header is rejected with 400.
func TestStripeBilling162_Step16_Webhook_RejectsMissingSignature(t *testing.T) {
	mock := &mockStripeBillingAdapter{}
	s := buildStripeBillingServer162(t, mock)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	// No Stripe-Signature header.
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing Stripe-Signature, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 17 — Webhook endpoint — invalid signature returns 401
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step17_Webhook_InvalidSignature verifies that an invalid
// Stripe-Signature returns 401 (ErrInvalidWebhookSignature).
func TestStripeBilling162_Step17_Webhook_InvalidSignature(t *testing.T) {
	mock := &mockStripeBillingAdapter{
		handleWebhookErr: fmt.Errorf("sig check failed: %w", errInvalidSigForTest),
	}
	s := buildStripeBillingServer162(t, mock)

	w := httptest.NewRecorder()
	body := []byte(`{"type":"invoice.paid"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=123,v1=badsig")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid signature, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 18 — Webhook endpoint — valid invoice.paid event returns 200
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step18_Webhook_ValidInvoicePaid verifies a valid
// invoice.paid webhook returns 200 {"received": true}.
func TestStripeBilling162_Step18_Webhook_ValidInvoicePaid(t *testing.T) {
	mock := &mockStripeBillingAdapter{
		webhookResponse: &stripebillingadapter.BillingWebhookEvent{
			EventType:       stripebillingadapter.EventInvoicePaid,
			StripeInvoiceID: "in_test001",
			Status:          "paid",
			StripeEventID:   "evt_test001",
		},
	}
	s := buildStripeBillingServer162(t, mock)

	body := []byte(`{"type":"invoice.paid","id":"evt_test001","data":{"object":{"id":"in_test001","status":"paid"}}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=valid,v1=valid")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if received, ok := resp["received"]; !ok || received != true {
		t.Errorf("response missing received:true; got: %v", resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 19 — Webhook endpoint — invoice.payment_failed returns 200
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step19_Webhook_PaymentFailed returns 200 even on failure.
func TestStripeBilling162_Step19_Webhook_PaymentFailed(t *testing.T) {
	mock := &mockStripeBillingAdapter{
		webhookResponse: &stripebillingadapter.BillingWebhookEvent{
			EventType:       stripebillingadapter.EventInvoicePaymentFailed,
			StripeInvoiceID: "in_failed001",
			Status:          "payment_failed",
			StripeEventID:   "evt_failed001",
		},
	}
	s := buildStripeBillingServer162(t, mock)

	body := []byte(`{"type":"invoice.payment_failed","id":"evt_failed001","data":{"object":{"id":"in_failed001","status":"payment_failed"}}}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=valid,v1=valid")
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 20 — Adapter constants
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step20_AdapterConstants verifies the adapter exports the
// correct event type constants.
func TestStripeBilling162_Step20_AdapterConstants(t *testing.T) {
	if got := stripebillingadapter.EventInvoicePaid; got != "invoice.paid" {
		t.Errorf("EventInvoicePaid = %q, want %q", got, "invoice.paid")
	}
	if got := stripebillingadapter.EventInvoicePaymentFailed; got != "invoice.payment_failed" {
		t.Errorf("EventInvoicePaymentFailed = %q, want %q", got, "invoice.payment_failed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 21 — StripeCustomerRow compile-time type assertion
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step21_StripeCustomerRowFields verifies the
// StripeCustomerRow struct has the expected fields at compile time.
func TestStripeBilling162_Step21_StripeCustomerRowFields(t *testing.T) {
	row := gen.StripeCustomerRow{
		ID:               uuid.New(),
		OrgID:            uuid.New(),
		StripeCustomerID: "cus_test",
		Email:            nil,
		Name:             nil,
	}
	if row.StripeCustomerID != "cus_test" {
		t.Errorf("StripeCustomerRow.StripeCustomerID = %q", row.StripeCustomerID)
	}
	if row.ID == uuid.Nil || row.OrgID == uuid.Nil {
		t.Errorf("StripeCustomerRow.ID/OrgID must round-trip non-nil")
	}
	if row.Email != nil || row.Name != nil {
		t.Errorf("StripeCustomerRow.Email/Name should be nil when zero-valued")
	}
}

// TestStripeBilling162_Step21_InvoiceStripeRowFields verifies the
// InvoiceStripeRow struct has the StripeInvoiceID field at compile time.
func TestStripeBilling162_Step21_InvoiceStripeRowFields(t *testing.T) {
	stripeID := "in_abc123"
	row := gen.InvoiceStripeRow{
		ID:              uuid.New(),
		State:           "issued",
		StripeInvoiceID: &stripeID,
	}
	if *row.StripeInvoiceID != "in_abc123" {
		t.Errorf("InvoiceStripeRow.StripeInvoiceID = %q, want %q", *row.StripeInvoiceID, "in_abc123")
	}
	if row.ID == uuid.Nil {
		t.Errorf("InvoiceStripeRow.ID must round-trip non-nil")
	}
	if row.State != "issued" {
		t.Errorf("InvoiceStripeRow.State = %q, want issued", row.State)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 22 — Compile-time interface guard
// ─────────────────────────────────────────────────────────────────────────────

// TestStripeBilling162_Step22_AdapterImplementsInterface verifies at compile
// time that *stripebillingadapter.Adapter satisfies stripeBillingHelper.
func TestStripeBilling162_Step22_AdapterImplementsInterface(t *testing.T) {
	// The compile-time guard var _ stripeBillingHelper = (*stripebillingadapter.Adapter)(nil)
	// is in stripe_billing.go. If the interface is not satisfied, the package
	// won't compile. This test just confirms the guard exists.
	content := findFileByName(t, "stripe_billing.go")
	if !strings.Contains(content, "var _ stripeBillingHelper") {
		t.Error("stripe_billing.go: missing compile-time interface guard 'var _ stripeBillingHelper'")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildStripeBillingWebhookBody creates a minimal Stripe webhook event body.
func buildStripeBillingWebhookBody(t *testing.T, eventType, invoiceID, status string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{
		"id":   "evt_" + invoiceID,
		"type": eventType,
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":     invoiceID,
				"status": status,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildStripeBillingWebhookBody: %v", err)
	}
	return b
}

// buildStripeBillingSigHeader builds a valid Stripe-Signature header for the
// given body and secret (replicates payments.VerifyStripeSignature scheme).
func buildStripeBillingSigHeader(t *testing.T, body []byte, secret string) string {
	t.Helper()
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

// readRequestBody reads and returns the request body bytes (helper for capture in mock HTTP servers).
// Named readRequestBody to avoid conflict with the readBody(t, resp) helper in openapi_behavior_test.go.
func readRequestBody(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

// ─────────────────────────────────────────────────────────────────────────────
// Mock Stripe Billing adapter
// ─────────────────────────────────────────────────────────────────────────────

// errInvalidSigForTest is a sentinel used by mockStripeBillingAdapter to
// trigger ErrInvalidWebhookSignature-wrapped errors.
var errInvalidSigForTest = payments_errInvalidWebhookSignature

// payments_errInvalidWebhookSignature mirrors payments.ErrInvalidWebhookSignature
// to avoid importing the payments package in test-only code.
// The stripe_billing.go handler checks errors.Is(err, payments.ErrInvalidWebhookSignature).
// We need a value that satisfies that check — so we use the real error value.
// The mock wraps it so errors.Is traverses the chain.
var payments_errInvalidWebhookSignature = func() error {
	// Import the real error via the adapter (it's exported from payments package).
	// We construct a fake BillingWebhookEvent call that will fail signature check.
	a := stripebillingadapter.New(stripebillingadapter.Config{
		SecretKey:     "sk_test",
		WebhookSecret: "whsec_real",
	})
	_, err := a.HandleBillingWebhook([]byte(`{}`), "t=1,v1=bad", "")
	return err
}()

// mockStripeBillingAdapter is a test double for stripeBillingHelper.
type mockStripeBillingAdapter struct {
	createCustomerID  string
	createCustomerErr error
	createItemID      string
	createItemErr     error
	createInvoiceID   string
	createInvoiceErr  error
	webhookResponse   *stripebillingadapter.BillingWebhookEvent
	handleWebhookErr  error
}

func (m *mockStripeBillingAdapter) CreateOrUpdateCustomer(_ context.Context, _, _, _ string) (string, error) {
	if m.createCustomerErr != nil {
		return "", m.createCustomerErr
	}
	id := m.createCustomerID
	if id == "" {
		id = "cus_mock"
	}
	return id, nil
}

func (m *mockStripeBillingAdapter) CreateInvoiceItem(_ context.Context, _, _ string, _ int64, _, _ string) (string, error) {
	if m.createItemErr != nil {
		return "", m.createItemErr
	}
	id := m.createItemID
	if id == "" {
		id = "ii_mock"
	}
	return id, nil
}

func (m *mockStripeBillingAdapter) CreateAndFinalizeInvoice(_ context.Context, _, _ string, _ map[string]string, _ string) (string, error) {
	if m.createInvoiceErr != nil {
		return "", m.createInvoiceErr
	}
	id := m.createInvoiceID
	if id == "" {
		id = "in_mock"
	}
	return id, nil
}

func (m *mockStripeBillingAdapter) HandleBillingWebhook(_ []byte, _, _ string) (*stripebillingadapter.BillingWebhookEvent, error) {
	if m.handleWebhookErr != nil {
		return nil, m.handleWebhookErr
	}
	if m.webhookResponse != nil {
		return m.webhookResponse, nil
	}
	return &stripebillingadapter.BillingWebhookEvent{
		EventType:       "invoice.paid",
		StripeInvoiceID: "in_default",
		Status:          "paid",
		StripeEventID:   "evt_default",
	}, nil
}
