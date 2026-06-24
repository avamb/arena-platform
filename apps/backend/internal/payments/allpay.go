// allpay.go — AllPay payment provider adapter (Feature #136).
//
// AllPay is a hosted-checkout payment provider used in Israel (ADR-010).
// Supported Israeli payment methods: Isracard, Leumi Card, Bit, tashlumim
// (instalment plans).
//
// Integration flow:
//  1. CreateIntent  → POST /v1/payments
//     AllPay returns {id, checkout_url}.  Caller redirects the shopper to
//     checkout_url (hosted checkout page).  The checkout URL is surfaced as
//     CreateIntentResponse.ClientSecret so the front-end can redirect.
//  2. AllPay POSTs a signed webhook to the platform's /webhook endpoint.
//     HandleWebhook verifies HMAC-SHA256 and parses the event.
//  3. CapturePayment → POST /v1/payments/{id}/capture  (if not auto-captured)
//  4. RefundPayment  → POST /v1/payments/{id}/refund
//
// Per-org API credentials (APIKey, WebhookSecret) are stored on the
// sales_channel row (encrypted at rest) and injected at adapter construction
// time via AllPayConfig.  A single running process can hold many instances —
// one per org/sales-channel combination.
package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AllPayDefaultBaseURL is the production AllPay REST API base URL.
const AllPayDefaultBaseURL = "https://api.allpay.co.il"

// allPayIsraeliMethods is the full set of Israeli payment methods AllPay supports.
// Used as the default when the caller does not specify payment_methods in Metadata.
var allPayIsraeliMethods = []string{"isracard", "leumi", "bit", "tashlumim"}

// ─────────────────────────────────────────────────────────────────────────────
// Configuration + constructor
// ─────────────────────────────────────────────────────────────────────────────

// AllPayConfig holds the per-adapter (per-org) configuration for the AllPay
// adapter.  APIKey and WebhookSecret are read from the sales_channel row.
type AllPayConfig struct {
	// BaseURL overrides the default AllPay API base URL.
	// Useful for sandbox/test environments.  Empty → AllPayDefaultBaseURL.
	BaseURL string

	// APIKey is the per-org Bearer token for AllPay REST API calls.
	// Required — NewAllPayAdapter returns an error if this is empty.
	APIKey string

	// WebhookSecret is the per-org HMAC-SHA256 shared secret for webhook
	// signature verification.  Used as a fallback when WebhookRequest.Secret
	// is empty; callers should always set WebhookRequest.Secret explicitly.
	WebhookSecret string

	// HTTPClient is an optional HTTP client used for API calls.
	// If nil, a client with a 30-second timeout is created automatically.
	HTTPClient *http.Client
}

// AllPayAdapter implements PaymentProvider for the AllPay hosted-checkout API.
// Construct via NewAllPayAdapter; the zero value is not usable.
type AllPayAdapter struct {
	baseURL       string
	apiKey        string
	webhookSecret string
	httpClient    *http.Client
}

// NewAllPayAdapter creates a new AllPayAdapter from the provided configuration.
// Returns an error if cfg.APIKey is empty.
func NewAllPayAdapter(cfg AllPayConfig) (*AllPayAdapter, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("allpay: APIKey is required")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = AllPayDefaultBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &AllPayAdapter{
		baseURL:       baseURL,
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		httpClient:    client,
	}, nil
}

// ProviderName implements PaymentProvider. Returns "allpay".
func (a *AllPayAdapter) ProviderName() string { return "allpay" }

// ─────────────────────────────────────────────────────────────────────────────
// Wire types — AllPay REST API request / response bodies
// ─────────────────────────────────────────────────────────────────────────────

// allPayCreateRequest is the JSON body for POST /v1/payments.
type allPayCreateRequest struct {
	Amount         int64             `json:"amount"`
	Currency       string            `json:"currency"`
	Description    string            `json:"description,omitempty"`
	IdempotencyKey string            `json:"idempotency_key"`
	PaymentMethods []string          `json:"payment_methods,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// allPayCreateResponse is the JSON body returned by POST /v1/payments on success.
type allPayCreateResponse struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	CheckoutURL string            `json:"checkout_url"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// allPayCaptureResponse is the JSON body returned by POST /v1/payments/{id}/capture.
type allPayCaptureResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// allPayRefundRequest is the JSON body for POST /v1/payments/{id}/refund.
type allPayRefundRequest struct {
	Amount         int64  `json:"amount,omitempty"`
	Reason         string `json:"reason,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// allPayRefundResponse is the JSON body returned by POST /v1/payments/{id}/refund.
type allPayRefundResponse struct {
	RefundID string `json:"refund_id"`
	Status   string `json:"status"`
}

// allPayWebhookEvent is the JSON body that AllPay POSTs to the platform webhook endpoint.
type allPayWebhookEvent struct {
	EventType string            `json:"event_type"`
	PaymentID string            `json:"payment_id"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AllPayAPIError is returned by the AllPay adapter when the API responds with a
// non-2xx status code and a structured JSON error body.  Callers can use
// errors.As to inspect the code and message.
type AllPayAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e AllPayAPIError) Error() string {
	return fmt.Sprintf("allpay API error %s: %s", e.Code, e.Message)
}

// ─────────────────────────────────────────────────────────────────────────────
// PaymentProvider implementation
// ─────────────────────────────────────────────────────────────────────────────

// CreateIntent creates a new hosted-checkout payment session with AllPay.
//
// The CreateIntentResponse fields are populated as follows:
//   - ProviderIntentID — AllPay's internal payment ID (use for Capture/Refund)
//   - ClientSecret     — AllPay checkout URL; redirect the shopper here
//   - Status           — initial status from AllPay, typically "pending_redirect"
//
// If req.Metadata["payment_methods"] is set (comma-separated), only those
// methods are offered on the checkout page.  Otherwise all four Israeli methods
// (isracard, leumi, bit, tashlumim) are enabled by default.
func (a *AllPayAdapter) CreateIntent(ctx context.Context, req CreateIntentRequest) (*CreateIntentResponse, error) {
	body := allPayCreateRequest{
		Amount:         req.Amount,
		Currency:       req.Currency,
		Description:    req.Description,
		IdempotencyKey: req.IdempotencyKey,
		Metadata:       req.Metadata,
	}

	// Determine which payment methods to offer on the AllPay hosted page.
	if pm, ok := req.Metadata["payment_methods"]; ok && strings.TrimSpace(pm) != "" {
		body.PaymentMethods = splitAllPayCSV(pm)
	} else {
		body.PaymentMethods = allPayIsraeliMethods
	}

	var resp allPayCreateResponse
	if err := a.do(ctx, http.MethodPost, "/v1/payments", body, &resp); err != nil {
		return nil, fmt.Errorf("allpay CreateIntent: %w", err)
	}
	return &CreateIntentResponse{
		ProviderIntentID: resp.ID,
		ClientSecret:     resp.CheckoutURL, // front-end redirects here
		Status:           resp.Status,
		Metadata:         resp.Metadata,
	}, nil
}

// CapturePayment captures (finalises) a previously authorised AllPay payment.
// AllPay may auto-capture depending on the payment method; callers should
// invoke CapturePayment only when AllPay signals an "authorized" state.
func (a *AllPayAdapter) CapturePayment(ctx context.Context, req CapturePaymentRequest) (*CapturePaymentResponse, error) {
	var resp allPayCaptureResponse
	path := fmt.Sprintf("/v1/payments/%s/capture", req.ProviderIntentID)
	if err := a.do(ctx, http.MethodPost, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("allpay CapturePayment: %w", err)
	}
	return &CapturePaymentResponse{
		ProviderIntentID: resp.ID,
		Status:           resp.Status,
	}, nil
}

// RefundPayment initiates a refund for a captured AllPay payment.
// A zero req.Amount triggers a full refund; a positive value triggers a
// partial refund for exactly that amount in the smallest currency unit.
func (a *AllPayAdapter) RefundPayment(ctx context.Context, req RefundPaymentRequest) (*RefundPaymentResponse, error) {
	body := allPayRefundRequest{
		Amount:         req.Amount,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	}
	var resp allPayRefundResponse
	path := fmt.Sprintf("/v1/payments/%s/refund", req.ProviderIntentID)
	if err := a.do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, fmt.Errorf("allpay RefundPayment: %w", err)
	}
	return &RefundPaymentResponse{
		RefundID: resp.RefundID,
		Status:   resp.Status,
	}, nil
}

// HandleWebhook validates an inbound AllPay webhook and parses the event body.
//
// The caller must supply:
//   - req.SignatureHeader — value of the "X-AllPay-Signature" HTTP header
//   - req.Body           — raw (unmodified) request body bytes
//   - req.Secret         — webhook endpoint secret for this sales channel;
//     if empty, the adapter-level WebhookSecret from AllPayConfig is used
//
// Returns ErrInvalidWebhookSignature (wrapped) when HMAC verification fails.
func (a *AllPayAdapter) HandleWebhook(ctx context.Context, req WebhookRequest) (*WebhookResponse, error) {
	secret := req.Secret
	if secret == "" {
		secret = a.webhookSecret
	}
	if err := VerifyAllPaySignature(req.SignatureHeader, req.Body, secret); err != nil {
		return nil, err
	}
	var event allPayWebhookEvent
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return nil, fmt.Errorf("allpay HandleWebhook: failed to parse event body: %w", err)
	}
	return &WebhookResponse{
		EventType:        event.EventType,
		ProviderIntentID: event.PaymentID,
		Status:           event.Status,
		Metadata:         event.Metadata,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal HTTP helper
// ─────────────────────────────────────────────────────────────────────────────

// do performs an authenticated JSON API call to the AllPay REST API.
// If reqBody is non-nil it is JSON-encoded as the request body.
// The response body is JSON-decoded into respBody when non-nil.
// Non-2xx responses are decoded as AllPayAPIError when possible; otherwise a
// plain status-code error is returned.
func (a *AllPayAdapter) do(ctx context.Context, method, path string, reqBody, respBody any) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build HTTP request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	httpReq.Header.Set("Accept", "application/json")
	if reqBody != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	httpResp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("execute HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		var apiErr AllPayAPIError
		if json.Unmarshal(respBytes, &apiErr) == nil && apiErr.Message != "" {
			return apiErr
		}
		return fmt.Errorf("allpay API responded with HTTP %d", httpResp.StatusCode)
	}

	if respBody != nil {
		if err := json.Unmarshal(respBytes, respBody); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}
	return nil
}

// splitAllPayCSV splits a comma-separated list of payment method names into
// individual, whitespace-trimmed tokens.  Empty tokens are silently dropped.
func splitAllPayCSV(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			result = append(result, t)
		}
	}
	return result
}
