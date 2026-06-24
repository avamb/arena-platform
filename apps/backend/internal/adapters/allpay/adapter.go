// Package allpay implements the payments.PaymentProvider interface for AllPay,
// an Israeli payment gateway (ADR-010).
//
// AllPay is a hosted-checkout payment provider used in Israel. The organizer
// registers directly with AllPay and supplies their API credentials to the
// platform (direct-merchant model, ADR-011). The platform stores the credentials
// per sales_channel (encrypted) and uses them to create hosted checkout sessions
// on behalf of the organizer.
//
// Supported Israeli payment methods:
//   - isracard  — Isracard domestic credit/debit card network
//   - leumi     — Leumi Card (Max) credit network
//   - bit       — Bit mobile payment (Bank HaPoalim wallet)
//   - tashlumim — Israeli installment payments (תשלומים)
//
// Payment flow:
//
//  1. Call CreateIntent → AllPay returns a hosted_url for the customer.
//  2. Redirect customer to hosted_url; AllPay handles card entry.
//  3. AllPay posts a webhook to our /v1/allpay/webhook endpoint.
//  4. HandleWebhook verifies the X-AllPay-Signature header (HMAC-SHA256).
//  5. On authorized/captured status, capture via CapturePayment.
//
// Webhook signature verification is provided by payments.VerifyAllPaySignature.
package allpay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/payments"
)

// compile-time interface guard
var _ payments.PaymentProvider = (*Adapter)(nil)

const (
	// defaultBaseURL is the AllPay production API endpoint.
	defaultBaseURL = "https://api.allpay.co.il/v1"
)

// Config holds all configuration for the AllPay adapter.
type Config struct {
	// APIKey is the AllPay merchant API key (per-org, provided by the organizer).
	// For sandbox testing use the AllPay sandbox key.
	APIKey string

	// WebhookSecret is the shared HMAC secret used to verify inbound webhook
	// signatures from AllPay (X-AllPay-Signature header).
	WebhookSecret string

	// MerchantID is the AllPay merchant identifier assigned during registration.
	MerchantID string

	// BaseURL overrides the AllPay API base URL. Leave empty for production.
	// Useful in tests: set to the httptest.Server URL.
	BaseURL string

	// AllowedPaymentMethods restricts the payment methods shown on the hosted
	// checkout page. Valid values: "isracard", "leumi", "bit", "tashlumim".
	// When empty, AllPay shows all methods enabled for the merchant account.
	AllowedPaymentMethods []string

	// ReturnURL is the URL AllPay redirects the customer to after checkout
	// (success or failure). Required for hosted-checkout flows.
	ReturnURL string

	// NotifyURL is the webhook endpoint URL AllPay will POST payment events to.
	NotifyURL string
}

// Adapter is the AllPay implementation of payments.PaymentProvider.
// It communicates with the AllPay REST JSON API using an internal http.Client.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New creates a new AllPay Adapter from the given Config.
// The HTTP client timeout is set to 30 seconds.
// If BaseURL is empty, the production AllPay endpoint is used.
func New(cfg Config) *Adapter {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// ProviderName returns "allpay", the canonical key used in PaymentRoutingPolicy.
func (a *Adapter) ProviderName() string {
	return "allpay"
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal API response types
// ─────────────────────────────────────────────────────────────────────────────

// allpayPaymentResponse is the JSON response from the AllPay payment creation endpoint.
type allpayPaymentResponse struct {
	PaymentID  string `json:"payment_id"`
	HostedURL  string `json:"hosted_url"`
	Status     string `json:"status"`
	MerchantID string `json:"merchant_id"`
}

// allpayCaptureResponse is the JSON response from the AllPay capture endpoint.
type allpayCaptureResponse struct {
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
}

// allpayRefundResponse is the JSON response from the AllPay refund endpoint.
type allpayRefundResponse struct {
	RefundID  string `json:"refund_id"`
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
}

// allpayWebhookEvent is the JSON body of an AllPay webhook notification.
type allpayWebhookEvent struct {
	EventType string `json:"event_type"`
	PaymentID string `json:"payment_id"`
	Status    string `json:"status"`
	// Metadata contains optional extra fields forwarded from the original request.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// allpayAPIError is the JSON error envelope returned by AllPay on HTTP 4xx/5xx.
type allpayAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal HTTP helper
// ─────────────────────────────────────────────────────────────────────────────

// doRequest executes a JSON-encoded POST request against the AllPay API.
// It sets Authorization, Content-Type, and Idempotency-Key headers.
// On HTTP status >= 400 it parses the AllPay error envelope and returns a
// descriptive error.
func (a *Adapter) doRequest(ctx context.Context, method, endpoint string, body []byte, idempotencyKey string) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("allpay: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if a.cfg.MerchantID != "" {
		req.Header.Set("X-Merchant-ID", a.cfg.MerchantID)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("allpay: http: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("allpay: read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr allpayAPIError
		if jsonErr := json.Unmarshal(rawBody, &apiErr); jsonErr == nil && apiErr.Message != "" {
			return nil, resp.StatusCode, fmt.Errorf("allpay: API error (status %d, code %s): %s",
				resp.StatusCode, apiErr.Code, apiErr.Message)
		}
		return nil, resp.StatusCode, fmt.Errorf("allpay: API error (status %d): %s",
			resp.StatusCode, string(rawBody))
	}

	return rawBody, resp.StatusCode, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// PaymentProvider implementation
// ─────────────────────────────────────────────────────────────────────────────

// CreateIntent creates a new AllPay hosted-checkout session.
//
// AllPay returns a hosted_url that the customer should be redirected to in
// order to complete payment. The returned ProviderIntentID is AllPay's
// payment_id, which is used in subsequent CapturePayment and RefundPayment calls.
// The ClientSecret field in the response contains the hosted_url for
// front-end redirect handling.
//
// Idempotency: IdempotencyKey is forwarded as the Idempotency-Key header so
// that retried requests do not create duplicate payment sessions.
func (a *Adapter) CreateIntent(ctx context.Context, req payments.CreateIntentRequest) (*payments.CreateIntentResponse, error) {
	type createPaymentRequest struct {
		Amount         int64             `json:"amount"`
		Currency       string            `json:"currency"`
		Description    string            `json:"description,omitempty"`
		MerchantID     string            `json:"merchant_id,omitempty"`
		ReturnURL      string            `json:"return_url,omitempty"`
		NotifyURL      string            `json:"notify_url,omitempty"`
		PaymentMethods []string          `json:"payment_methods,omitempty"`
		Metadata       map[string]string `json:"metadata,omitempty"`
	}

	payload := createPaymentRequest{
		Amount:         req.Amount,
		Currency:       req.Currency,
		Description:    req.Description,
		MerchantID:     a.cfg.MerchantID,
		ReturnURL:      a.cfg.ReturnURL,
		NotifyURL:      a.cfg.NotifyURL,
		PaymentMethods: a.cfg.AllowedPaymentMethods,
		Metadata:       req.Metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("allpay: CreateIntent: marshal request: %w", err)
	}

	endpoint := a.cfg.BaseURL + "/payments"
	rawResp, _, err := a.doRequest(ctx, http.MethodPost, endpoint, body, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("allpay: CreateIntent: %w", err)
	}

	var apiResp allpayPaymentResponse
	if err := json.Unmarshal(rawResp, &apiResp); err != nil {
		return nil, fmt.Errorf("allpay: CreateIntent: unmarshal response: %w", err)
	}

	return &payments.CreateIntentResponse{
		ProviderIntentID: apiResp.PaymentID,
		// ClientSecret stores the hosted URL; front-end redirects the customer there.
		ClientSecret: apiResp.HostedURL,
		Status:       apiResp.Status,
		Metadata:     map[string]string{"hosted_url": apiResp.HostedURL},
	}, nil
}

// CapturePayment captures a previously authorised AllPay payment via
// POST /payments/{id}/capture.
//
// When req.Amount > 0, a partial capture is requested; otherwise the full
// authorised amount is captured.
func (a *Adapter) CapturePayment(ctx context.Context, req payments.CapturePaymentRequest) (*payments.CapturePaymentResponse, error) {
	type captureRequest struct {
		Amount int64 `json:"amount,omitempty"`
	}

	payload := captureRequest{}
	if req.Amount > 0 {
		payload.Amount = req.Amount
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("allpay: CapturePayment: marshal request: %w", err)
	}

	endpoint := a.cfg.BaseURL + "/payments/" + req.ProviderIntentID + "/capture"
	rawResp, _, err := a.doRequest(ctx, http.MethodPost, endpoint, body, "")
	if err != nil {
		return nil, fmt.Errorf("allpay: CapturePayment: %w", err)
	}

	var apiResp allpayCaptureResponse
	if err := json.Unmarshal(rawResp, &apiResp); err != nil {
		return nil, fmt.Errorf("allpay: CapturePayment: unmarshal response: %w", err)
	}

	return &payments.CapturePaymentResponse{
		ProviderIntentID: apiResp.PaymentID,
		Status:           apiResp.Status,
	}, nil
}

// RefundPayment initiates a refund via POST /payments/{id}/refund.
//
// When req.Amount is zero, a full refund is issued. A non-zero Amount
// triggers a partial refund. The IdempotencyKey is forwarded to prevent
// duplicate refund operations.
func (a *Adapter) RefundPayment(ctx context.Context, req payments.RefundPaymentRequest) (*payments.RefundPaymentResponse, error) {
	type refundRequest struct {
		Amount         int64  `json:"amount,omitempty"`
		Reason         string `json:"reason,omitempty"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}

	payload := refundRequest{
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	}
	if req.Amount > 0 {
		payload.Amount = req.Amount
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("allpay: RefundPayment: marshal request: %w", err)
	}

	endpoint := a.cfg.BaseURL + "/payments/" + req.ProviderIntentID + "/refund"
	rawResp, _, err := a.doRequest(ctx, http.MethodPost, endpoint, body, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("allpay: RefundPayment: %w", err)
	}

	var apiResp allpayRefundResponse
	if err := json.Unmarshal(rawResp, &apiResp); err != nil {
		return nil, fmt.Errorf("allpay: RefundPayment: unmarshal response: %w", err)
	}

	return &payments.RefundPaymentResponse{
		RefundID: apiResp.RefundID,
		Status:   apiResp.Status,
	}, nil
}

// HandleWebhook validates the X-AllPay-Signature header and parses the event.
//
// AllPay signs the raw request body with HMAC-SHA256 using the shared webhook
// secret and sends the lowercase hex digest in the X-AllPay-Signature header.
// This method delegates signature verification to payments.VerifyAllPaySignature.
//
// Supported event types:
//   - payment.authorized      — customer authorised payment (capture required)
//   - payment.captured        — payment captured (terminal success)
//   - payment.failed          — payment attempt failed
//   - payment.refunded        — refund processed
//   - payment.expired         — hosted checkout session expired
//
// Returns payments.ErrInvalidWebhookSignature when the signature does not match.
func (a *Adapter) HandleWebhook(ctx context.Context, req payments.WebhookRequest) (*payments.WebhookResponse, error) {
	secret := req.Secret
	if secret == "" {
		secret = a.cfg.WebhookSecret
	}

	if err := payments.VerifyAllPaySignature(req.SignatureHeader, req.Body, secret); err != nil {
		return nil, err
	}

	var event allpayWebhookEvent
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return nil, fmt.Errorf("allpay: HandleWebhook: unmarshal event: %w", err)
	}

	meta := make(map[string]string)
	for k, v := range event.Metadata {
		meta[k] = v
	}
	meta["allpay_payment_id"] = event.PaymentID

	return &payments.WebhookResponse{
		EventType:        event.EventType,
		ProviderIntentID: event.PaymentID,
		Status:           event.Status,
		Metadata:         meta,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Hosted checkout helpers
// ─────────────────────────────────────────────────────────────────────────────

// HostedCheckoutURL extracts the hosted checkout redirect URL from a
// CreateIntentResponse. This is the URL to which the customer should be
// redirected to complete payment on the AllPay hosted page.
//
// Returns an empty string if the response does not contain a hosted URL.
func HostedCheckoutURL(resp *payments.CreateIntentResponse) string {
	if resp == nil {
		return ""
	}
	if url, ok := resp.Metadata["hosted_url"]; ok {
		return url
	}
	return ""
}

// IsraelPaymentMethods returns the list of recognised Israeli payment method
// identifiers supported by AllPay. Callers may pass a subset to Config.AllowedPaymentMethods
// to restrict the methods shown on the hosted checkout page.
func IsraelPaymentMethods() []string {
	return []string{"isracard", "leumi", "bit", "tashlumim"}
}

// ValidatePaymentMethod reports whether the given method identifier is a
// recognised AllPay payment method.
func ValidatePaymentMethod(method string) bool {
	for _, m := range IsraelPaymentMethods() {
		if m == method {
			return true
		}
	}
	return false
}

// installmentOptions returns the standard Israeli tashlumim (installment) counts.
func installmentOptions() []int {
	return []int{1, 2, 3, 4, 6, 9, 12, 18, 24, 36}
}

// FormatAmount formats an amount in the smallest currency unit (agorot) as
// NIS shekels for display purposes. AllPay uses agorot (100 agorot = 1 NIS).
// E.g. FormatAmount(10050) → "100.50 ₪".
func FormatAmount(agorot int64) string {
	shekels := agorot / 100
	cents := agorot % 100
	if cents < 0 {
		cents = -cents
	}
	return fmt.Sprintf("%d.%02d ₪", shekels, cents)
}

// InstallmentAmount computes the per-installment amount for a given total and
// installment count. Returns 0 if installments is zero. Rounded down to agorot.
func InstallmentAmount(totalAgorot int64, installments int) int64 {
	if installments <= 0 {
		return 0
	}
	return totalAgorot / int64(installments)
}

// IsValidInstallmentCount reports whether the given installment count is
// supported by AllPay's tashlumim feature.
func IsValidInstallmentCount(n int) bool {
	for _, opt := range installmentOptions() {
		if opt == n {
			return true
		}
	}
	return false
}

// StatusToPaymentIntentState maps an AllPay payment status string to the
// arena payment_intent state machine state. Returns an empty string for
// unknown statuses.
func StatusToPaymentIntentState(allpayStatus string) string {
	switch strings.ToLower(allpayStatus) {
	case "pending", "created":
		return "created"
	case "requires_action", "3ds_required":
		return "requires_action"
	case "processing":
		return "processing"
	case "authorized":
		return "authorized"
	case "captured", "succeeded":
		return "succeeded"
	case "failed", "declined", "error":
		return "failed"
	case "manual_review":
		return "manual_review"
	default:
		return ""
	}
}

// EventTypeToPaymentIntentState maps an AllPay webhook event type to the
// corresponding payment_intent state machine state.
func EventTypeToPaymentIntentState(eventType string) string {
	switch eventType {
	case "payment.authorized":
		return "authorized"
	case "payment.captured":
		return "succeeded"
	case "payment.failed", "payment.expired":
		return "failed"
	case "payment.processing":
		return "processing"
	case "payment.requires_action":
		return "requires_action"
	case "payment.manual_review":
		return "manual_review"
	default:
		return ""
	}
}

// BuildWebhookSignature computes the X-AllPay-Signature value for a given
// payload and secret. This is used in tests to generate valid webhook fixtures
// without reimplementing the HMAC primitive.
func BuildWebhookSignature(secret string, body []byte) string {
	return payments.ComputeHMACSHA256(secret, body)
}

// SandboxBaseURL is the AllPay sandbox environment base URL used for integration
// testing. Configure Config.BaseURL to this value during development and CI.
const SandboxBaseURL = "https://sandbox-api.allpay.co.il/v1"

// InstallmentsToString converts an int installment count to the AllPay API
// string representation used in the create-payment payload.
func InstallmentsToString(n int) string {
	return strconv.Itoa(n)
}
