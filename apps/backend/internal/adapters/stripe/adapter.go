// Package stripe implements the payments.PaymentProvider interface for Stripe.
//
// The adapter supports two payment modes (ADR-011):
//
//   - direct_merchant: Payment flows through the organizer's Stripe Connect
//     Standard account. The platform takes an application fee configured via
//     ApplicationFeePercent. OAuth onboarding managed via ConnectAuthorizeURL
//     and ConnectExchangeCode.
//
//   - merchant_of_record: Payment flows through the platform account. Use
//     PlatformAccountID to route to the correct sub-account if needed.
//
// Webhook signature verification delegates to payments.VerifyStripeSignature,
// which implements the Stripe HMAC-SHA256 "t=<ts>,v1=<sig>" scheme.
//
// SCA/3DS: When CreateIntent returns a Status of "requires_action" and the
// response contains a next_action.redirect_to_url.url, callers should store
// that URL as sca_redirect_url and transition the payment intent to the
// requires_action state so the front-end can redirect the customer.
package stripe

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

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// compile-time interface guard
var _ payments.PaymentProvider = (*Adapter)(nil)

const (
	defaultBaseURL      = "https://api.stripe.com/v1"
	defaultOAuthBaseURL = "https://connect.stripe.com"
	stripeAPIVersion    = "2024-11-20.acacia"
)

// Config holds all configuration for the Stripe adapter.
type Config struct {
	// SecretKey is the Stripe API secret key (sk_live_... or sk_test_...).
	SecretKey string
	// WebhookSecret is the signing secret for webhook endpoint verification (whsec_...).
	WebhookSecret string
	// ClientID is the Stripe Connect platform client ID (ca_...) used to build
	// OAuth authorization URLs for direct-merchant onboarding.
	ClientID string
	// ApplicationFeePercent is the platform fee as a percentage of the transaction
	// amount applied when PaymentMode is "direct_merchant". E.g. 2.5 means 2.5%.
	// Zero disables application fees.
	ApplicationFeePercent float64
	// PlatformAccountID is the Stripe Connect account ID for merchant-of-record mode.
	// Optional; used when PaymentMode is "merchant_of_record".
	PlatformAccountID string
	// WebhookTolerance is the maximum age of a Stripe webhook event before it is
	// rejected as a possible replay attack. Defaults to payments.DefaultWebhookTolerance.
	WebhookTolerance time.Duration
	// BaseURL overrides the Stripe API base URL. Used in tests. Defaults to
	// "https://api.stripe.com/v1".
	BaseURL string
	// OAuthBaseURL overrides the Stripe Connect OAuth base URL. Used in tests.
	// Defaults to "https://connect.stripe.com".
	OAuthBaseURL string
}

// Adapter is the Stripe implementation of payments.PaymentProvider. It uses
// the standard library net/http client and communicates with the Stripe REST API
// using application/x-www-form-urlencoded encoding.
type Adapter struct {
	cfg    Config
	client *http.Client
}

// New creates a new Stripe Adapter from the provided Config. The HTTP client
// timeout is set to 30 seconds. If WebhookTolerance is zero it is set to
// payments.DefaultWebhookTolerance. If BaseURL or OAuthBaseURL are empty they
// default to the Stripe production endpoints.
func New(cfg Config) *Adapter {
	if cfg.WebhookTolerance == 0 {
		cfg.WebhookTolerance = payments.DefaultWebhookTolerance
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.OAuthBaseURL == "" {
		cfg.OAuthBaseURL = defaultOAuthBaseURL
	}
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// ProviderName returns "stripe", the canonical key used in PaymentRoutingPolicy.
func (a *Adapter) ProviderName() string {
	return "stripe"
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal structs for Stripe API responses
// ─────────────────────────────────────────────────────────────────────────────

type stripeNextActionRedirectToURL struct {
	URL string `json:"url"`
}

type stripeNextAction struct {
	Type          string                        `json:"type"`
	RedirectToURL stripeNextActionRedirectToURL `json:"redirect_to_url"`
}

type stripePaymentIntent struct {
	ID           string            `json:"id"`
	ClientSecret string            `json:"client_secret"`
	Status       string            `json:"status"`
	NextAction   *stripeNextAction `json:"next_action"`
}

type stripeRefund struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type stripeEventDataObject struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type stripeEventData struct {
	Object stripeEventDataObject `json:"object"`
}

type stripeEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data stripeEventData `json:"data"`
}

type stripeAPIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type stripeAPIError struct {
	Error stripeAPIErrorDetail `json:"error"`
}

type stripeOAuthTokenResponse struct {
	StripeUserID     string `json:"stripe_user_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal HTTP helper
// ─────────────────────────────────────────────────────────────────────────────

// doRequest executes a form-encoded HTTP request against the Stripe API.
// It sets the Authorization, Content-Type, Stripe-Version, and (optionally)
// Idempotency-Key headers. On HTTP status >= 400 it parses the Stripe error
// envelope and returns a descriptive error. The raw body bytes and status code
// are returned on success.
func (a *Adapter) doRequest(ctx context.Context, method, endpoint string, form url.Values, idempotencyKey string) ([]byte, int, error) {
	var bodyReader io.Reader
	if form != nil {
		bodyReader = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("stripe: build request: %w", err)
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
		return nil, 0, fmt.Errorf("stripe: http: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("stripe: read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr stripeAPIError
		if jsonErr := json.Unmarshal(rawBody, &apiErr); jsonErr == nil && apiErr.Error.Message != "" {
			return nil, resp.StatusCode, fmt.Errorf("stripe: API error (status %d, type %s, code %s): %s",
				resp.StatusCode, apiErr.Error.Type, apiErr.Error.Code, apiErr.Error.Message)
		}
		return nil, resp.StatusCode, fmt.Errorf("stripe: API error (status %d): %s", resp.StatusCode, string(rawBody))
	}

	return rawBody, resp.StatusCode, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// PaymentProvider implementation
// ─────────────────────────────────────────────────────────────────────────────

// CreateIntent creates a new Stripe PaymentIntent via POST /v1/payment_intents.
//
// The intent is created with capture_method=manual so that funds are only
// reserved on the card and captured later via CapturePayment. When
// ApplicationFeePercent > 0 and the requested Amount > 0, an application_fee_amount
// is computed and added to the request (direct-merchant Connect flows). The
// IdempotencyKey is forwarded as the Idempotency-Key header.
//
// If Stripe returns a PaymentIntent in the "requires_action" status, callers
// should inspect the returned Status and store the SCA redirect URL for the
// front-end.
func (a *Adapter) CreateIntent(ctx context.Context, req payments.CreateIntentRequest) (*payments.CreateIntentResponse, error) {
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(req.Amount, 10))
	form.Set("currency", req.Currency)
	form.Set("capture_method", "manual")
	if req.Description != "" {
		form.Set("description", req.Description)
	}
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
	}
	if a.cfg.ApplicationFeePercent > 0 && req.Amount > 0 {
		feeAmount := int64(float64(req.Amount) * a.cfg.ApplicationFeePercent / 100.0)
		form.Set("application_fee_amount", strconv.FormatInt(feeAmount, 10))
	}

	endpoint := a.cfg.BaseURL + "/payment_intents"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("stripe: CreateIntent: %w", err)
	}

	var pi stripePaymentIntent
	if err := json.Unmarshal(rawBody, &pi); err != nil {
		return nil, fmt.Errorf("stripe: CreateIntent: unmarshal response: %w", err)
	}

	resp := &payments.CreateIntentResponse{
		ProviderIntentID: pi.ID,
		ClientSecret:     pi.ClientSecret,
		Status:           pi.Status,
		Metadata:         make(map[string]string),
	}
	return resp, nil
}

// CapturePayment captures a previously authorised Stripe PaymentIntent via
// POST /v1/payment_intents/{id}/capture. When req.Amount > 0, the
// amount_to_capture field is included for partial captures; otherwise Stripe
// captures the full authorised amount.
func (a *Adapter) CapturePayment(ctx context.Context, req payments.CapturePaymentRequest) (*payments.CapturePaymentResponse, error) {
	form := url.Values{}
	if req.Amount > 0 {
		form.Set("amount_to_capture", strconv.FormatInt(req.Amount, 10))
	}

	endpoint := a.cfg.BaseURL + "/payment_intents/" + req.ProviderIntentID + "/capture"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, "")
	if err != nil {
		return nil, fmt.Errorf("stripe: CapturePayment: %w", err)
	}

	var pi stripePaymentIntent
	if err := json.Unmarshal(rawBody, &pi); err != nil {
		return nil, fmt.Errorf("stripe: CapturePayment: unmarshal response: %w", err)
	}

	return &payments.CapturePaymentResponse{
		ProviderIntentID: pi.ID,
		Status:           pi.Status,
	}, nil
}

// RefundPayment creates a Stripe Refund via POST /v1/refunds. When req.Amount
// is zero a full refund is issued; otherwise the specified partial amount is
// refunded. The optional Reason field is forwarded to Stripe. The
// IdempotencyKey is forwarded as the Idempotency-Key header.
func (a *Adapter) RefundPayment(ctx context.Context, req payments.RefundPaymentRequest) (*payments.RefundPaymentResponse, error) {
	form := url.Values{}
	form.Set("payment_intent", req.ProviderIntentID)
	if req.Amount > 0 {
		form.Set("amount", strconv.FormatInt(req.Amount, 10))
	}
	if req.Reason != "" {
		form.Set("reason", req.Reason)
	}

	endpoint := a.cfg.BaseURL + "/refunds"
	rawBody, _, err := a.doRequest(ctx, http.MethodPost, endpoint, form, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("stripe: RefundPayment: %w", err)
	}

	var refund stripeRefund
	if err := json.Unmarshal(rawBody, &refund); err != nil {
		return nil, fmt.Errorf("stripe: RefundPayment: unmarshal response: %w", err)
	}

	return &payments.RefundPaymentResponse{
		RefundID: refund.ID,
		Status:   refund.Status,
	}, nil
}

// HandleWebhook validates the Stripe-Signature header and parses the event
// body. The webhook secret is taken from req.Secret if non-empty, otherwise
// cfg.WebhookSecret is used. Returns payments.ErrInvalidWebhookSignature if
// signature verification fails.
func (a *Adapter) HandleWebhook(_ context.Context, req payments.WebhookRequest) (*payments.WebhookResponse, error) {
	secret := req.Secret
	if secret == "" {
		secret = a.cfg.WebhookSecret
	}

	if err := payments.VerifyStripeSignature(req.SignatureHeader, req.Body, secret, a.cfg.WebhookTolerance); err != nil {
		return nil, err
	}

	var event stripeEvent
	if err := json.Unmarshal(req.Body, &event); err != nil {
		return nil, fmt.Errorf("stripe: HandleWebhook: unmarshal event: %w", err)
	}

	return &payments.WebhookResponse{
		EventType:        event.Type,
		ProviderIntentID: event.Data.Object.ID,
		Status:           event.Data.Object.Status,
		Metadata: map[string]string{
			"stripe_event_id": event.ID,
		},
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Stripe Connect helpers (direct-merchant onboarding)
// ─────────────────────────────────────────────────────────────────────────────

// ConnectAuthorizeURL builds the Stripe Connect Standard OAuth authorization
// URL. The caller should redirect the organizer's browser to this URL. After
// the organizer grants access Stripe redirects to redirectURI with a "code"
// query parameter that should be exchanged via ConnectExchangeCode.
func (a *Adapter) ConnectAuthorizeURL(redirectURI, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("scope", "read_write")
	params.Set("client_id", a.cfg.ClientID)
	if redirectURI != "" {
		params.Set("redirect_uri", redirectURI)
	}
	if state != "" {
		params.Set("state", state)
	}
	return a.cfg.OAuthBaseURL + "/oauth/authorize?" + params.Encode()
}

// ConnectExchangeCode exchanges a Stripe Connect OAuth authorization code for a
// connected account ID by posting to POST /oauth/token on the Connect endpoint.
// Returns the stripe_user_id from the token response.
func (a *Adapter) ConnectExchangeCode(ctx context.Context, code string) (accountID string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)

	endpoint := a.cfg.OAuthBaseURL + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", stripeAPIVersion)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: http: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: read response: %w", err)
	}

	var tokenResp stripeOAuthTokenResponse
	if err := json.Unmarshal(rawBody, &tokenResp); err != nil {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: unmarshal response: %w", err)
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: %s: %s", tokenResp.Error, tokenResp.ErrorDescription)
	}
	if tokenResp.StripeUserID == "" {
		return "", fmt.Errorf("stripe: ConnectExchangeCode: empty stripe_user_id in response")
	}

	return tokenResp.StripeUserID, nil
}
