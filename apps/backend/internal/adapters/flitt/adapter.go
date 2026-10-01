// Package flitt implements the hosted-checkout (redirect) flow of the Flitt
// payment gateway (https://docs.flitt.com).
//
// Flitt is a card gateway used for the Georgian / EU market. Like Stripe
// Checkout it renders the payment page itself: arena posts the order, gets a
// checkout_url back, redirects the buyer there and later learns the outcome
// from a host-to-host callback. Card data never reaches arena.
//
// Differences from Stripe that shape this adapter:
//
//   - There is no API key. A merchant is identified by merchant_id and every
//     request and every callback is signed with the merchant's PAYMENT KEY:
//     SHA1 over "<key>|<non-empty params sorted by name, joined with |>". The
//     same key verifies callbacks, so a Flitt config needs no separate
//     "webhook secret".
//   - The callback carries its signature in the body, not in a header.
//   - Test and live use the SAME endpoint; the credentials decide. The
//     shared sandbox merchant (1549901, key "test") works without any
//     onboarding.
//   - order_id is chosen by the merchant and must be unique per merchant
//     (error 1013 otherwise). arena uses the checkout_sessions row id, which
//     also becomes payment_intents.provider_payment_id, so a callback finds
//     its intent through the ordinary provider-id lookup.
//   - Amounts are integer minor units, exactly like orders.total; any ISO
//     currency Flitt has enabled for the merchant is passed through — the
//     adapter keeps no allow-list of its own, because Flitt answers 1012 for
//     a currency the merchant may not use and that verdict is the authority.
//
// Signing details: parameters with an empty value are skipped, the signature
// field itself never takes part, and a zero is NOT empty.
package flitt

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is the algorithm Flitt's protocol mandates; it is not our choice
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// compile-time interface guards
var (
	_ payments.HostedCheckoutProvider = (*Adapter)(nil)
	_ payments.CredentialVerifier     = (*Adapter)(nil)
)

const (
	// DefaultBaseURL is Flitt's production API root (test credentials use it too).
	DefaultBaseURL = "https://pay.flitt.com/api"

	// minLifetimeSeconds is the shortest order lifetime arena will ask for. A
	// hosted page that dies within a minute would be worse than none.
	minLifetimeSeconds = 60

	// statusProbeOrderID is the order id used by VerifyCredentials. It is
	// never created, so the answer is always "order not found" for a good key.
	statusProbeOrderID = "arena-credential-check"
)

// Flitt error codes the adapter reacts to (docs.flitt.com/api/response-codes).
const (
	errCodeInvalidSignature = "1014"
	errCodeMerchantNotFound = "1016"
	errCodeOrderNotFound    = "1018"
)

// Callback order_status values.
const (
	StatusApproved   = "approved"
	StatusDeclined   = "declined"
	StatusExpired    = "expired"
	StatusProcessing = "processing"
	StatusCreated    = "created"
	StatusReversed   = "reversed"
)

// Config is one merchant's credentials.
type Config struct {
	// MerchantID is the integer id Flitt assigned at registration.
	MerchantID string
	// PaymentKey is the "payment key" from the portal's technical settings. It
	// signs requests and callbacks. (The portal also holds a "credit key" for
	// payouts; arena never uses it.)
	PaymentKey string
	// BaseURL overrides DefaultBaseURL. Tests only.
	BaseURL string
}

// Adapter talks to Flitt for ONE merchant. It is cheap: callers build one per
// request from the organization's own config row.
type Adapter struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

// New builds an Adapter.
func New(cfg Config) *Adapter {
	cfg.MerchantID = strings.TrimSpace(cfg.MerchantID)
	cfg.PaymentKey = strings.TrimSpace(cfg.PaymentKey)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

// ProviderName returns the canonical provider key.
func (a *Adapter) ProviderName() string { return "flitt" }

// ─────────────────────────────────────────────────────────────────────────────
// Signature
// ─────────────────────────────────────────────────────────────────────────────

// excludedFromSignature are fields that never take part in a signature.
var excludedFromSignature = map[string]bool{
	"signature":                 true,
	"response_signature_string": true,
}

// stringifyParam renders a decoded JSON value the way Flitt signs it. ok is
// false for a value that is absent from the signed string (null / empty).
func stringifyParam(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, t != ""
	case json.Number:
		return t.String(), t.String() != ""
	case bool:
		return strconv.FormatBool(t), true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		// A nested object or array: Flitt documents the signed fields as flat
		// scalars, so serialise compactly rather than drop it silently.
		b, err := json.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

// Sign computes the Flitt signature of params under the payment key.
func Sign(paymentKey string, params map[string]any) string {
	names := make([]string, 0, len(params))
	for k := range params {
		if excludedFromSignature[k] {
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names)+1)
	parts = append(parts, paymentKey)
	for _, k := range names {
		if s, ok := stringifyParam(params[k]); ok {
			parts = append(parts, s)
		}
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "|"))) //nolint:gosec // see import
	return hex.EncodeToString(sum[:])
}

// ErrInvalidSignature is returned when a callback does not verify.
var ErrInvalidSignature = fmt.Errorf("%w: flitt signature mismatch", payments.ErrInvalidWebhookSignature)

// decodeFlat decodes a flat JSON object keeping numbers as json.Number, so a
// payment_id of 805243692 is signed as "805243692" and never as 8.05243692e+08.
func decodeFlat(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("flitt: empty callback object")
	}
	return m, nil
}

// Callback is a verified payment callback.
type Callback struct {
	OrderID             string
	MerchantID          string
	OrderStatus         string
	PaymentID           string
	Amount              int64
	Currency            string
	ReversalAmount      int64
	ResponseCode        string
	ResponseDescription string
	// Raw is the decoded body, for the audit trail.
	Raw map[string]any
}

// VerifyCallback checks the signature of a callback body against the payment
// key and returns the parsed callback. Constant-time compare.
func VerifyCallback(body []byte, paymentKey string) (*Callback, error) {
	m, err := decodeFlat(body)
	if err != nil {
		return nil, fmt.Errorf("flitt: decode callback: %w", err)
	}
	got, _ := m["signature"].(string)
	if got == "" || paymentKey == "" {
		return nil, ErrInvalidSignature
	}
	want := Sign(paymentKey, m)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(got)), []byte(want)) != 1 {
		return nil, ErrInvalidSignature
	}
	return callbackFromMap(m), nil
}

// ParseCallbackUnverified extracts the identifying fields WITHOUT checking the
// signature. It exists so the webhook route can compare merchant_id with the
// config's before spending a hash; nothing may be acted on from its result.
func ParseCallbackUnverified(body []byte) (*Callback, error) {
	m, err := decodeFlat(body)
	if err != nil {
		return nil, err
	}
	return callbackFromMap(m), nil
}

// LooksLikeCallback reports whether a decoded body has the shape of a Flitt
// callback: a flat object with order_id, order_status and merchant_id.
func LooksLikeCallback(body []byte) bool {
	m, err := decodeFlat(body)
	if err != nil {
		return false
	}
	_, hasOrder := m["order_id"]
	_, hasStatus := m["order_status"]
	_, hasMerchant := m["merchant_id"]
	return hasOrder && hasStatus && hasMerchant
}

func callbackFromMap(m map[string]any) *Callback {
	str := func(k string) string { s, _ := stringifyParam(m[k]); return s }
	num := func(k string) int64 {
		n, _ := strconv.ParseInt(strings.TrimSpace(str(k)), 10, 64)
		return n
	}
	return &Callback{
		OrderID:             str("order_id"),
		MerchantID:          str("merchant_id"),
		OrderStatus:         strings.ToLower(str("order_status")),
		PaymentID:           str("payment_id"),
		Amount:              num("amount"),
		Currency:            strings.ToUpper(str("currency")),
		ReversalAmount:      num("reversal_amount"),
		ResponseCode:        str("response_code"),
		ResponseDescription: str("response_description"),
		Raw:                 m,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP
// ─────────────────────────────────────────────────────────────────────────────

// flexString decodes a JSON string or number into a string (Flitt sends error
// codes as either).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = flexString(v)
		return nil
	}
	*f = flexString(s)
	return nil
}

type apiResponse struct {
	Response struct {
		ResponseStatus string     `json:"response_status"`
		CheckoutURL    string     `json:"checkout_url"`
		PaymentID      flexString `json:"payment_id"`
		OrderStatus    string     `json:"order_status"`
		ErrorCode      flexString `json:"error_code"`
		ErrorMessage   string     `json:"error_message"`
	} `json:"response"`
}

// APIError is Flitt's own refusal of a request (response_status=failure).
type APIError struct {
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("flitt: API error %s: %s", e.Code, e.Message)
}

// post signs params, posts {"request": {...}} and decodes the {"response": {...}}
// envelope. A failure answer is returned as *APIError.
func (a *Adapter) post(ctx context.Context, path string, params map[string]any) (*apiResponse, int, error) {
	params["signature"] = Sign(a.cfg.PaymentKey, params)
	payload, err := json.Marshal(map[string]any{"request": params})
	if err != nil {
		return nil, 0, fmt.Errorf("flitt: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("flitt: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("flitt: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("flitt: read response: %w", err)
	}

	var out apiResponse
	if jsonErr := json.Unmarshal(raw, &out); jsonErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("flitt: unreadable response (status %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if strings.EqualFold(out.Response.ResponseStatus, "failure") {
		return &out, resp.StatusCode, &APIError{Code: string(out.Response.ErrorCode), Message: out.Response.ErrorMessage}
	}
	if resp.StatusCode >= 400 {
		return &out, resp.StatusCode, fmt.Errorf("flitt: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return &out, resp.StatusCode, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─────────────────────────────────────────────────────────────────────────────
// Hosted checkout
// ─────────────────────────────────────────────────────────────────────────────

// checkoutLangs are the page languages Flitt accepts (docs: `lang`). Flitt
// answers 1007 for anything else, which would turn a cosmetic preference into
// a lost sale, so an unknown tag is simply omitted.
var checkoutLangs = map[string]struct{}{
	"az": {}, "da": {}, "nl": {}, "fi": {}, "ka": {}, "ko": {}, "ru": {}, "zh": {},
	"uk": {}, "en": {}, "lv": {}, "fr": {}, "cs": {}, "ro": {}, "it": {}, "sk": {},
	"pl": {}, "es": {}, "hu": {}, "de": {},
}

func checkoutLang(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	if _, ok := checkoutLangs[tag]; !ok {
		return ""
	}
	return tag
}

// CreateCheckoutSession creates the order and returns the hosted page URL.
//
// SessionID in the response is the order_id (arena's checkout session id):
// that, not Flitt's numeric payment_id, is what every callback is matched by.
// PaymentID is Flitt's own id, returned for the audit trail.
func (a *Adapter) CreateCheckoutSession(ctx context.Context, req payments.CreateHostedCheckoutRequest) (*payments.CreateHostedCheckoutResponse, error) {
	merchantID, err := strconv.ParseInt(a.cfg.MerchantID, 10, 64)
	if err != nil || merchantID <= 0 {
		return nil, fmt.Errorf("flitt: CreateCheckoutSession: merchant_id %q is not a number", a.cfg.MerchantID)
	}
	orderID := strings.TrimSpace(req.ClientReferenceID)
	if orderID == "" {
		return nil, errors.New("flitt: CreateCheckoutSession: ClientReferenceID (order_id) is required")
	}
	desc := strings.TrimSpace(req.ProductName)
	if desc == "" {
		desc = "Tickets"
	}

	params := map[string]any{
		"merchant_id": merchantID,
		"order_id":    orderID,
		"order_desc":  truncateRunes(desc, 1024),
		"amount":      req.Amount,
		"currency":    strings.ToUpper(strings.TrimSpace(req.Currency)),
	}
	if req.SuccessURL != "" {
		params["response_url"] = req.SuccessURL
	}
	if req.CancelURL != "" {
		params["cancel_url"] = req.CancelURL
	}
	if req.CallbackURL != "" {
		params["server_callback_url"] = req.CallbackURL
	}
	if req.CustomerEmail != "" {
		params["sender_email"] = req.CustomerEmail
	}
	if lang := checkoutLang(req.Locale); lang != "" {
		params["lang"] = lang
	}
	if req.ExpiresAtUnix > 0 {
		secs := req.ExpiresAtUnix - a.now().Unix()
		if secs < minLifetimeSeconds {
			secs = minLifetimeSeconds
		}
		params["lifetime"] = secs
	}
	if len(req.Metadata) > 0 {
		// json.Marshal sorts map keys, so the value is stable.
		if b, mErr := json.Marshal(req.Metadata); mErr == nil {
			params["merchant_data"] = string(b)
		}
	}

	out, _, err := a.post(ctx, "/checkout/url", params)
	if err != nil {
		return nil, fmt.Errorf("flitt: CreateCheckoutSession: %w", err)
	}
	if out.Response.CheckoutURL == "" {
		return nil, errors.New("flitt: CreateCheckoutSession: response has no checkout_url")
	}
	resp := &payments.CreateHostedCheckoutResponse{
		SessionID: orderID,
		URL:       out.Response.CheckoutURL,
		PaymentID: string(out.Response.PaymentID),
	}
	if req.ExpiresAtUnix > 0 {
		resp.ExpiresAtUnix = req.ExpiresAtUnix
	}
	return resp, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ─────────────────────────────────────────────────────────────────────────────
// Credential verification
// ─────────────────────────────────────────────────────────────────────────────

// VerifyCredentials proves merchant_id + payment key without moving money or
// creating anything: it asks for the status of an order that does not exist.
//
//   - 1018 "order not found" means Flitt accepted the merchant AND the
//     signature — the credential works.
//   - 1014 (bad signature) and 1016 (unknown merchant) are verdicts on the
//     credential.
//   - Anything else (transport, 5xx, an error we do not recognise) says
//     nothing about the key and is reported as unreachable, never as refused.
func (a *Adapter) VerifyCredentials(ctx context.Context) error {
	merchantID, err := strconv.ParseInt(a.cfg.MerchantID, 10, 64)
	if err != nil || merchantID <= 0 {
		return fmt.Errorf("%w: merchant_id %q is not a number", payments.ErrCredentialRefused, a.cfg.MerchantID)
	}
	_, status, err := a.post(ctx, "/status/order_id", map[string]any{
		"merchant_id": merchantID,
		"order_id":    statusProbeOrderID,
	})
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case errCodeOrderNotFound:
			return nil
		case errCodeInvalidSignature, errCodeMerchantNotFound:
			return fmt.Errorf("%w: %s", payments.ErrCredentialRefused, apiErr.Error())
		}
	}
	_ = status
	return fmt.Errorf("%w: %s", payments.ErrProviderUnreachable, err.Error())
}
