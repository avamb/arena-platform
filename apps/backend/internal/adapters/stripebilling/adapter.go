// Package stripebilling implements the Stripe Billing adapter for pushing
// platform SaaS invoices to the platform's Estonia Stripe account (feature #162).
//
// Unlike the stripe adapter (which implements payments.PaymentProvider for
// ticket purchases via PaymentIntents), this package uses the Stripe Invoicing
// API to collect platform service fees from organizers.
//
// Flow:
//  1. Ensure a Stripe Customer exists for the org (CreateOrUpdateCustomer).
//  2. Create Stripe InvoiceItems for each line on the platform invoice.
//  3. Create and auto-finalize a Stripe Invoice for the customer.
//  4. Store the returned Stripe invoice ID on the local invoice row.
//  5. When Stripe confirms payment, the webhook handler receives invoice.paid
//     and transitions the local invoice to "paid".
//
// Webhook events handled:
//   - invoice.paid             → transition local invoice to "paid"
//   - invoice.payment_failed   → log; local invoice stays "issued"
//
// Signature verification reuses payments.VerifyStripeSignature (HMAC-SHA256
// "t=<ts>,v1=<sig>" scheme identical to the PaymentIntent webhook).
package stripebilling

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/payments"
)

const (
	defaultBaseURL   = "https://api.stripe.com/v1"
	stripeAPIVersion = "2024-11-20.acacia"

	// EventInvoicePaid is the Stripe event type fired when a Stripe invoice is paid.
	EventInvoicePaid = "invoice.paid"
	// EventInvoicePaymentFailed is the Stripe event type fired when payment fails.
	EventInvoicePaymentFailed = "invoice.payment_failed"
)

// Config holds all configuration for the Stripe Billing adapter.
type Config struct {
	// SecretKey is the Stripe API secret key for the platform's Estonia account
	// (sk_live_... or sk_test_...).
	SecretKey string
	// WebhookSecret is the signing secret for the Stripe Billing webhook endpoint
	// (whsec_...). Used to verify Stripe-Signature headers on incoming events.
	WebhookSecret string
	// WebhookTolerance is the maximum age of a Stripe webhook event before it
	// is rejected as a possible replay attack. Defaults to
	// payments.DefaultWebhookTolerance (5 minutes).
	WebhookTolerance time.Duration
	// BaseURL overrides the Stripe API base URL. Used in tests.
	// Defaults to "https://api.stripe.com/v1".
	BaseURL string
}

// Adapter is the Stripe Billing implementation for pushing SaaS invoices.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New creates a new Adapter from the provided Config. The HTTP client timeout
// is set to 30 seconds. If WebhookTolerance is zero it is set to
// payments.DefaultWebhookTolerance. If BaseURL is empty it defaults to the
// Stripe production endpoint.
func New(cfg Config) *Adapter {
	if cfg.WebhookTolerance == 0 {
		cfg.WebhookTolerance = payments.DefaultWebhookTolerance
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal Stripe API types
// ─────────────────────────────────────────────────────────────────────────────

type stripeCustomer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type stripeInvoiceItem struct {
	ID string `json:"id"`
}

type stripeInvoice struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type stripeAPIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type stripeAPIError struct {
	Error stripeAPIErrorDetail `json:"error"`
}

// BillingWebhookEvent represents a parsed Stripe Billing webhook event.
type BillingWebhookEvent struct {
	// EventType is the Stripe event type (e.g. "invoice.paid").
	EventType string
	// StripeInvoiceID is the Stripe invoice ID from the event data object.
	StripeInvoiceID string
	// Status is the invoice status from the event data object.
	Status string
	// StripeEventID is the Stripe event ID for idempotency / logging.
	StripeEventID string
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal HTTP helper
// ─────────────────────────────────────────────────────────────────────────────

// doRequest executes a form-encoded HTTP request against the Stripe API.
// Sets Authorization, Content-Type, and Stripe-Version headers.
// On HTTP status >= 400 it parses the Stripe error envelope and returns a
// descriptive error. Returns raw body bytes and status code on success.
func (a *Adapter) doRequest(ctx context.Context, method, endpoint string, form url.Values, idempotencyKey string) ([]byte, int, error) {
	var bodyReader io.Reader
	if form != nil {
		bodyReader = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("stripebilling: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.cfg.SecretKey)
	req.Header.Set("Stripe-Version", stripeAPIVersion)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("stripebilling: http: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("stripebilling: read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr stripeAPIError
		if jsonErr := json.Unmarshal(rawBody, &apiErr); jsonErr == nil && apiErr.Error.Message != "" {
			return nil, resp.StatusCode, fmt.Errorf("stripebilling: API error (status %d, type %s, code %s): %s",
				resp.StatusCode, apiErr.Error.Type, apiErr.Error.Code, apiErr.Error.Message)
		}
		return nil, resp.StatusCode, fmt.Errorf("stripebilling: API error (status %d): %s", resp.StatusCode, string(rawBody))
	}

	return rawBody, resp.StatusCode, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Customer management
// ─────────────────────────────────────────────────────────────────────────────

// CreateOrUpdateCustomer creates a new Stripe Customer for the given email and
// name. This is an idempotency-key-protected POST; callers should pass the
// orgID as idempotencyKey to ensure at-most-once creation per org.
//
// Note: Stripe does not support native upsert on customers. The platform stores
// the mapping in stripe_customers and calls this only when no mapping exists.
// Use idempotencyKey = "cust-" + orgID.String() to prevent duplicate creates
// across retries.
func (a *Adapter) CreateOrUpdateCustomer(ctx context.Context, email, name, idempotencyKey string) (string, error) {
	form := url.Values{}
	if email != "" {
		form.Set("email", email)
	}
	if name != "" {
		form.Set("name", name)
	}
	// Tag with platform metadata for easy identification in Stripe dashboard.
	form.Set("metadata[platform]", "arena")

	endpoint := a.cfg.BaseURL + "/customers"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("stripebilling: CreateOrUpdateCustomer: %w", err)
	}

	var cust stripeCustomer
	if err := json.Unmarshal(rawBody, &cust); err != nil {
		return "", fmt.Errorf("stripebilling: CreateOrUpdateCustomer: unmarshal response: %w", err)
	}
	if cust.ID == "" {
		return "", fmt.Errorf("stripebilling: CreateOrUpdateCustomer: empty customer ID in response")
	}
	return cust.ID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Invoice creation
// ─────────────────────────────────────────────────────────────────────────────

// CreateInvoiceItem creates a Stripe InvoiceItem for the given customer.
// The item is attached to the customer's next pending invoice automatically.
// Pass idempotencyKey = "item-" + invoiceLineID to prevent duplicate line items
// across retries.
func (a *Adapter) CreateInvoiceItem(
	ctx context.Context,
	stripeCustomerID string,
	description string,
	amountMinor int64,
	currency string,
	idempotencyKey string,
) (string, error) {
	form := url.Values{}
	form.Set("customer", stripeCustomerID)
	form.Set("amount", strconv.FormatInt(amountMinor, 10))
	form.Set("currency", strings.ToLower(currency))
	if description != "" {
		form.Set("description", description)
	}

	endpoint := a.cfg.BaseURL + "/invoiceitems"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("stripebilling: CreateInvoiceItem: %w", err)
	}

	var item stripeInvoiceItem
	if err := json.Unmarshal(rawBody, &item); err != nil {
		return "", fmt.Errorf("stripebilling: CreateInvoiceItem: unmarshal response: %w", err)
	}
	if item.ID == "" {
		return "", fmt.Errorf("stripebilling: CreateInvoiceItem: empty item ID in response")
	}
	return item.ID, nil
}

// CreateAndFinalizeInvoice creates a Stripe Invoice for the given customer and
// immediately finalizes (sends) it via POST /v1/invoices/{id}/finalize. The
// invoice collects all pending invoice items attached to the customer. Returns
// the Stripe invoice ID.
//
// Pass idempotencyKey = "inv-" + localInvoiceID to prevent duplicate Stripe
// invoices across retries.
func (a *Adapter) CreateAndFinalizeInvoice(
	ctx context.Context,
	stripeCustomerID string,
	description string,
	metadata map[string]string,
	idempotencyKey string,
) (string, error) {
	form := url.Values{}
	form.Set("customer", stripeCustomerID)
	// collection_method=send_invoice means Stripe sends a PDF invoice to the
	// customer's email and waits for payment (rather than auto-charging).
	form.Set("collection_method", "send_invoice")
	// due_date 30 days from now (Unix timestamp).
	dueDate := time.Now().UTC().Add(30 * 24 * time.Hour).Unix()
	form.Set("days_until_due", "30")
	_ = dueDate // days_until_due is the right param; keep for documentation.
	if description != "" {
		form.Set("description", description)
	}
	for k, v := range metadata {
		form.Set("metadata["+k+"]", v)
	}

	// Create the invoice.
	endpoint := a.cfg.BaseURL + "/invoices"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, idempotencyKey)
	if err != nil {
		return "", fmt.Errorf("stripebilling: CreateAndFinalizeInvoice create: %w", err)
	}

	var inv stripeInvoice
	if err := json.Unmarshal(rawBody, &inv); err != nil {
		return "", fmt.Errorf("stripebilling: CreateAndFinalizeInvoice: unmarshal create response: %w", err)
	}
	if inv.ID == "" {
		return "", fmt.Errorf("stripebilling: CreateAndFinalizeInvoice: empty invoice ID in create response")
	}

	// Finalize (send) the invoice.
	finalizeEndpoint := a.cfg.BaseURL + "/invoices/" + inv.ID + "/finalize"
	finalizeRawBody, _, err := a.doRequest(ctx, http.MethodPost, finalizeEndpoint, url.Values{}, "")
	if err != nil {
		return inv.ID, fmt.Errorf("stripebilling: CreateAndFinalizeInvoice finalize: %w", err)
	}

	var finalizedInv stripeInvoice
	if err := json.Unmarshal(finalizeRawBody, &finalizedInv); err != nil {
		return inv.ID, fmt.Errorf("stripebilling: CreateAndFinalizeInvoice: unmarshal finalize response: %w", err)
	}

	return finalizedInv.ID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook handling
// ─────────────────────────────────────────────────────────────────────────────

// HandleBillingWebhook verifies the Stripe-Signature header and parses a
// Stripe Billing webhook event. The webhook secret is taken from secret if
// non-empty, otherwise cfg.WebhookSecret is used. Returns
// payments.ErrInvalidWebhookSignature if verification fails.
//
// Supported events:
//   - invoice.paid
//   - invoice.payment_failed
func (a *Adapter) HandleBillingWebhook(body []byte, sigHeader, secret string) (*BillingWebhookEvent, error) {
	if secret == "" {
		secret = a.cfg.WebhookSecret
	}

	if err := payments.VerifyStripeSignature(sigHeader, body, secret, a.cfg.WebhookTolerance); err != nil {
		return nil, err
	}

	// Parse the event envelope.
	var raw struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("stripebilling: HandleBillingWebhook: unmarshal event: %w", err)
	}

	return &BillingWebhookEvent{
		EventType:       raw.Type,
		StripeInvoiceID: raw.Data.Object.ID,
		Status:          raw.Data.Object.Status,
		StripeEventID:   raw.ID,
	}, nil
}
