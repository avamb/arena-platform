// Package payplus implements the hosted-checkout (redirect) flow of PayPlus
// (Israel, https://docs.payplus.co.il, REST API v1.0).
//
// Like Stripe Checkout, Flitt and AllPay, PayPlus renders the payment page
// itself: arena asks for a payment link (PaymentPages/generateLink), redirects
// the buyer there and learns the outcome from a host-to-host callback posted
// to refURL_callback. Card data never reaches arena.
//
// What shapes this adapter:
//
//   - Every request carries the merchant's two keys as the HTTP headers
//     `api-key` and `secret-key`. A payment link also names a payment page
//     the merchant configured in the PayPlus dashboard (payment_page_uid).
//   - Test and live are DIFFERENT hosts (restapidev / restapi) with
//     different keys. The host follows the configuration's mode
//     (payments.Options.Mode); PAYPLUS_API_BASE_URL overrides both for tests.
//   - The callback is signed in the `hash` HEADER: base64(HMAC-SHA256(
//     secret_key, body)). See signature.go for which bytes are signed.
//   - Amounts are DECIMAL MAJOR units ("12.5" shekels). arena keeps minor
//     units everywhere; this package converts exactly once, in both
//     directions (money.go).
//   - provider_payment_id is PayPlus's page_request_uid (the link's id),
//     which the charge callback repeats as transaction.payment_request_uid.
//     arena's checkout session id travels as more_info. A refund is driven
//     through the charge's transaction.uid (provider_charge_ref).
package payplus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	// ProductionBaseURL is the live REST root.
	ProductionBaseURL = "https://restapi.payplus.co.il/api/v1.0"
	// StagingBaseURL is the test REST root; test keys only work here.
	StagingBaseURL = "https://restapidev.payplus.co.il/api/v1.0"

	// StatusCodeApproved is transaction.status_code of an approved charge.
	StatusCodeApproved = "000"

	// chargeMethodCharge is J4, an immediate charge. Sent explicitly so a
	// page the merchant configured for J5 (authorization only) or J2 (card
	// check) can never be reported to arena as a settled payment.
	chargeMethodCharge = 1

	// probePaymentRequestUID is the payment request VerifyCredentials asks
	// about. It never exists; the keys are judged by whether PayPlus let the
	// request through to that lookup at all.
	probePaymentRequestUID = "00000000-0000-4000-8000-00000000c0de"
)

// Config is one merchant's credentials.
type Config struct {
	// APIKey and SecretKey are the "API key" and "secret key" from the
	// PayPlus dashboard. SecretKey also verifies callbacks.
	APIKey    string
	SecretKey string
	// PaymentPageUID names the payment page the links are generated from.
	PaymentPageUID string
	// BaseURL is the REST root. Empty means ProductionBaseURL; BaseURLFor
	// picks it from the mode and the test seam.
	BaseURL string
}

// BaseURLFor returns the REST root for a configuration mode: the override
// (PAYPLUS_API_BASE_URL, tests and the stand only) when set, the staging host
// for a "test" configuration, production otherwise.
func BaseURLFor(mode, override string) string {
	if o := strings.TrimRight(strings.TrimSpace(override), "/"); o != "" {
		return o
	}
	if strings.EqualFold(strings.TrimSpace(mode), "test") {
		return StagingBaseURL
	}
	return ProductionBaseURL
}

// Adapter talks to PayPlus for ONE merchant. It is cheap: callers build one
// per request from the organization's own config row.
type Adapter struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

// New builds an Adapter.
func New(cfg Config) *Adapter {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
	cfg.PaymentPageUID = strings.TrimSpace(cfg.PaymentPageUID)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = ProductionBaseURL
	}
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

// ProviderName returns the canonical provider key.
func (a *Adapter) ProviderName() string { return Name }

// BaseURL is the REST root this adapter talks to (for diagnostics and tests).
func (a *Adapter) BaseURL() string { return a.cfg.BaseURL }

// ─────────────────────────────────────────────────────────────────────────────
// HTTP
// ─────────────────────────────────────────────────────────────────────────────

// APIError is PayPlus's own refusal of a request: an answer with
// results.status other than "success", or a 4xx carrying a message.
type APIError struct {
	HTTPStatus  int
	Code        string
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("payplus: API error (HTTP %d, code %s): %s", e.HTTPStatus, e.Code, e.Description)
}

// authRefusal reports whether the refusal is about the keys themselves.
// PayPlus answers unknown keys with HTTP 422 {"message":"MISSING API_KEY"}
// and missing headers with {"message":"AUTHORIZATION HEADER IS NOT VALID"}
// (observed on restapidev 2026-10-28); a 401/403 means the same.
func (e *APIError) authRefusal() bool {
	if e.HTTPStatus == http.StatusUnauthorized || e.HTTPStatus == http.StatusForbidden {
		return true
	}
	d := strings.ToLower(e.Description)
	for _, w := range []string{"api_key", "api-key", "api key", "secret", "authoriz", "unauthor", "permission", "not allowed"} {
		if strings.Contains(d, w) {
			return true
		}
	}
	return false
}

// notFound reports whether the refusal says the thing asked about does not
// exist (an empty search, an unknown payment request).
func (e *APIError) notFound() bool {
	d := strings.ToLower(e.Description)
	for _, w := range []string{"not found", "not-found", "no transaction", "not exist", "can-not-find", "cannot find", "can not find", "לא נמצא"} {
		if strings.Contains(d, w) {
			return true
		}
	}
	return false
}

// envelope is PayPlus's common answer: {"results":{...},"data":...}.
type envelope struct {
	Results struct {
		Status      string          `json:"status"`
		Code        json.RawMessage `json:"code"`
		Description string          `json:"description"`
	} `json:"results"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// call sends one request with the merchant's keys and decodes the envelope.
// A transport failure, a 5xx or an unreadable answer is returned as a plain
// error (the outcome is unknown); PayPlus's refusal is an *APIError.
func (a *Adapter) call(ctx context.Context, method, path string, body any) (*envelope, error) {
	var rd io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("payplus: marshal request: %w", err)
		}
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+"/"+strings.TrimLeft(path, "/"), rd)
	if err != nil {
		return nil, fmt.Errorf("payplus: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("api-key", a.cfg.APIKey)
	req.Header.Set("secret-key", a.cfg.SecretKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("payplus: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("payplus: read response: %w", err)
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("payplus: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var env envelope
	if jsonErr := json.Unmarshal(raw, &env); jsonErr != nil {
		if resp.StatusCode >= 400 {
			// A plain-text refusal ("can-not-find-payment-page" is documented
			// as a bare string for generateLink).
			return nil, &APIError{HTTPStatus: resp.StatusCode, Description: truncate(strings.Trim(string(raw), "\" \n"), 200)}
		}
		return nil, fmt.Errorf("payplus: unreadable response (status %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode >= 400 || (env.Results.Status != "" && !strings.EqualFold(env.Results.Status, "success")) {
		desc := env.Results.Description
		if desc == "" {
			desc = env.Message
		}
		return &env, &APIError{HTTPStatus: resp.StatusCode, Code: strings.Trim(string(env.Results.Code), `"`), Description: desc}
	}
	return &env, nil
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

// pageLanguage maps arena's buyer locale to the payment page's language_code.
// PayPlus documents only the default ("he"); its own plugins offer he, en and
// ru. Anything else gets English, which a buyer of a non-Hebrew page reads far
// better than the Hebrew default. An empty locale leaves the page setting.
func pageLanguage(locale string) string {
	tag := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "":
		return ""
	case "he", "iw":
		return "he"
	case "ru":
		return "ru"
	default:
		return "en"
	}
}

// expiryMinutes is the page lifetime in whole minutes, rounded DOWN so the
// page never outlives the hold behind it, and never below one minute.
func expiryMinutes(expiresAtUnix, nowUnix int64) int64 {
	m := (expiresAtUnix - nowUnix) / 60
	if m < 1 {
		m = 1
	}
	return m
}

type generateLinkData struct {
	PageRequestUID  string `json:"page_request_uid"`
	PaymentPageLink string `json:"payment_page_link"`
}

// CreateCheckoutSession generates a payment link and returns its URL.
//
// SessionID in the response is PayPlus's page_request_uid: arena stores it as
// payment_intents.provider_payment_id, and the charge callback names the
// payment by it (transaction.payment_request_uid).
func (a *Adapter) CreateCheckoutSession(ctx context.Context, req payments.CreateHostedCheckoutRequest) (*payments.CreateHostedCheckoutResponse, error) {
	switch {
	case a.cfg.PaymentPageUID == "":
		return nil, errors.New("payplus: CreateCheckoutSession: the config has no payment_page_uid")
	case req.Amount <= 0:
		return nil, errors.New("payplus: CreateCheckoutSession: amount must be positive")
	case strings.TrimSpace(req.ClientReferenceID) == "":
		return nil, errors.New("payplus: CreateCheckoutSession: ClientReferenceID (more_info) is required")
	}
	amount := json.Number(MinorToMajor(req.Amount))
	name := strings.TrimSpace(req.ProductName)
	if name == "" {
		name = "Tickets"
	}
	params := map[string]any{
		"payment_page_uid": a.cfg.PaymentPageUID,
		"charge_method":    chargeMethodCharge,
		"amount":           amount,
		"currency_code":    strings.ToUpper(strings.TrimSpace(req.Currency)),
		// arena writes to the buyer itself (ticket letter, order letter);
		// PayPlus's own e-mails would be a second, unbranded receipt.
		"sendEmailApproval":     false,
		"sendEmailFailure":      false,
		"send_failure_callback": true,
		"more_info":             strings.TrimSpace(req.ClientReferenceID),
		// One line, the whole amount: arena has done the per-item arithmetic.
		"items": []map[string]any{{
			"name":     truncateRunes(name, 200),
			"quantity": 1,
			"price":    amount,
		}},
	}
	if req.SuccessURL != "" {
		params["refURL_success"] = req.SuccessURL
	}
	if req.CancelURL != "" {
		// A failed attempt and a cancel both bring the buyer back to the
		// same page; the outcome itself comes from the callback.
		params["refURL_failure"] = req.CancelURL
		params["refURL_cancel"] = req.CancelURL
	}
	if req.CallbackURL != "" {
		params["refURL_callback"] = req.CallbackURL
	}
	if lang := pageLanguage(req.Locale); lang != "" {
		params["language_code"] = lang
	}
	if req.ExpiresAtUnix > 0 {
		params["expiry_datetime"] = strconv.FormatInt(expiryMinutes(req.ExpiresAtUnix, a.now().Unix()), 10)
	}
	customer := map[string]any{}
	buyerName := strings.TrimSpace(req.CustomerName)
	if buyerName == "" {
		buyerName = strings.TrimSpace(req.CustomerEmail)
	}
	if buyerName != "" {
		customer["customer_name"] = buyerName
	}
	if e := strings.TrimSpace(req.CustomerEmail); e != "" {
		customer["email"] = e
	}
	if p := strings.TrimSpace(req.CustomerPhone); p != "" {
		customer["phone"] = p
	}
	if len(customer) > 0 {
		params["customer"] = customer
	}

	env, err := a.call(ctx, http.MethodPost, "PaymentPages/generateLink", params)
	if err != nil {
		return nil, fmt.Errorf("payplus: CreateCheckoutSession: %w", err)
	}
	var data generateLinkData
	if err := json.Unmarshal(env.Data, &data); err != nil || data.PageRequestUID == "" || data.PaymentPageLink == "" {
		return nil, errors.New("payplus: CreateCheckoutSession: response has no page_request_uid or payment_page_link")
	}
	resp := &payments.CreateHostedCheckoutResponse{
		SessionID: data.PageRequestUID,
		URL:       data.PaymentPageLink,
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
// Transactions
// ─────────────────────────────────────────────────────────────────────────────

// Transaction is one transaction as PayPlus reports it (Transactions/View,
// PaymentPages/ipn, a refund answer). Amount is in arena minor units.
type Transaction struct {
	UID               string
	PaymentRequestUID string
	Type              string
	StatusCode        string
	AmountMinor       int64
	Currency          string
	MoreInfo          string
	Cancelled         bool
}

// Approved reports whether PayPlus approved the transaction.
func (t Transaction) Approved() bool { return t.StatusCode == StatusCodeApproved && !t.Cancelled }

// IsRefund reports whether the transaction returns money.
func (t Transaction) IsRefund() bool { return strings.Contains(strings.ToLower(t.Type), "refund") }

// transactionFromMap reads a transaction object, tolerating the two spellings
// PayPlus uses (uid / transaction_uid, payment_request_uid / page_request_uid).
func transactionFromMap(m map[string]any, outerType string) Transaction {
	str := func(keys ...string) string {
		for _, k := range keys {
			if s := scalar(m[k]); s != "" {
				return s
			}
		}
		return ""
	}
	t := Transaction{
		UID:               str("uid", "transaction_uid"),
		PaymentRequestUID: str("payment_request_uid", "page_request_uid"),
		// The callback carries the kind ("Charge", "Refund") at the TOP level
		// (transaction_type); View carries it inside. transaction.type is the
		// channel ("internal_page"), never the kind.
		Type:       outerType,
		StatusCode: str("status_code"),
		Currency:   strings.ToUpper(str("currency", "currency_code")),
		MoreInfo:   str("more_info"),
	}
	if t.Type == "" {
		t.Type = str("transaction_type")
	}
	if v, ok := MajorToMinor(scalar(m["amount"])); ok {
		t.AmountMinor = v
	}
	if b, ok := m["transaction_is_cancelled"].(bool); ok {
		t.Cancelled = b
	}
	return t
}

// scalar renders a decoded JSON scalar as a string ("" for null/objects).
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func decodeNumbers(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}

// transactionsFromData collects every transaction object found in a data
// payload: an array of {transaction:{…}}, one {transaction:{…}}, or a bare
// transaction object.
func transactionsFromData(raw json.RawMessage) []Transaction {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	if err := decodeNumbers(raw, &v); err != nil {
		return nil
	}
	var out []Transaction
	var visit func(any)
	visit = func(x any) {
		switch t := x.(type) {
		case []any:
			for _, e := range t {
				visit(e)
			}
		case map[string]any:
			if inner, ok := t["transaction"].(map[string]any); ok {
				out = append(out, transactionFromMap(inner, scalar(t["transaction_type"])))
				return
			}
			if _, ok := t["status_code"]; ok {
				out = append(out, transactionFromMap(t, ""))
			}
		}
	}
	visit(v)
	return out
}

// ViewByMoreInfo lists the transactions PayPlus holds under a more_info
// value (Transactions/View). An empty search is an empty list, not an error.
func (a *Adapter) ViewByMoreInfo(ctx context.Context, moreInfo string) ([]Transaction, error) {
	return a.view(ctx, map[string]any{"more_info": moreInfo})
}

// ViewTransaction reads one transaction by its uid (Transactions/View).
func (a *Adapter) ViewTransaction(ctx context.Context, transactionUID string) ([]Transaction, error) {
	return a.view(ctx, map[string]any{"transaction_uid": transactionUID})
}

func (a *Adapter) view(ctx context.Context, q map[string]any) ([]Transaction, error) {
	env, err := a.call(ctx, http.MethodPost, "Transactions/View", q)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.notFound() {
			return nil, nil
		}
		return nil, err
	}
	return transactionsFromData(env.Data), nil
}

// PaymentRequestTransactions reads the transactions made on one payment link
// (PaymentPages/ipn by payment_request_uid).
func (a *Adapter) PaymentRequestTransactions(ctx context.Context, pageRequestUID string) ([]Transaction, error) {
	env, err := a.call(ctx, http.MethodPost, "PaymentPages/ipn", map[string]any{"payment_request_uid": pageRequestUID})
	if err != nil {
		return nil, err
	}
	return transactionsFromData(env.Data), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Credential verification
// ─────────────────────────────────────────────────────────────────────────────

// VerifyCredentials proves the api-key / secret-key pair without moving money
// or creating anything: it asks PayPlus about a payment request that does not
// exist (PaymentPages/ipn).
//
//   - PayPlus refusing the KEYS (401/403, or its "MISSING API_KEY" /
//     "AUTHORIZATION HEADER IS NOT VALID" answers) is a refusal.
//   - Any other answer — a found-nothing, a validation complaint about the
//     probe id — means the keys got through: accepted.
//   - Transport errors and 5xx say nothing about the keys: unreachable.
//
// The payment_page_uid is not checked here (no read-only call takes it);
// a wrong one fails the first generateLink with "can-not-find-payment-page".
func (a *Adapter) VerifyCredentials(ctx context.Context) error {
	if a.cfg.APIKey == "" || a.cfg.SecretKey == "" {
		return fmt.Errorf("%w: api_key and secret_key are required", payments.ErrCredentialRefused)
	}
	_, err := a.call(ctx, http.MethodPost, "PaymentPages/ipn", map[string]any{"payment_request_uid": probePaymentRequestUID})
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.authRefusal() {
			return fmt.Errorf("%w: %s", payments.ErrCredentialRefused, apiErr.Error())
		}
		return nil
	}
	return fmt.Errorf("%w: %s", payments.ErrProviderUnreachable, err.Error())
}
