// Package allpay implements the hosted-checkout (redirect) flow of AllPay, an
// Israeli payment gateway (allpay.co.il; API reference
// https://allpay.to/docs/api-reference.htm). AllPay takes Israeli and foreign
// cards, Bit, Apple Pay and Google Pay, and installments (tashlumim), all on
// its own payment page.
//
// Like Flitt, AllPay renders the payment page itself: arena posts the order,
// gets a payment_url back, redirects the buyer there and learns the outcome
// from a host-to-host webhook. Card data never reaches arena.
//
// What shapes this adapter (the traps are in AGENTS.md and spec
// 08_architecture/37_allpay_payment_provider_ru.md):
//
//   - There is no bearer token. An integration ("API login") is identified by
//     `login`, and every request and every webhook carries a `sign` field: a
//     SHA-256 over the parameter values signed with the integration's API
//     KEY (sign.go). The same key verifies webhooks, so an AllPay config
//     needs no separate webhook secret.
//   - The webhook carries its signature in the BODY and names no merchant, so
//     only the per-config webhook route can verify it.
//   - Every method is POST https://allpay.to/app/?show=<method>&mode=api15
//     with a JSON body. Errors come back with HTTP 200 and
//     {"error_code", "error_msg"}.
//   - order_id is chosen by the merchant. arena uses the checkout_sessions row
//     id; it is also payment_intents.provider_payment_id and, since AllPay
//     refunds and reads statuses by order_id, provider_charge_ref.
//   - Amounts are MAJOR units (100.5 shekels), not minor units like the rest
//     of arena: converted here and only here (MinorToMajor / MajorToMinor).
//   - A status 0 webhook is ONE failed attempt: the buyer stays on the page
//     and may pay with another card. It never fails the payment.
//   - There is no fail URL and no expiry webhook. The page shows the error
//     and lets the buyer retry; an unpaid page simply stops at `expire`, and
//     arena's own sweeps release the hold.
//   - Test and live use the SAME endpoint. The integration's test mode in the
//     AllPay dashboard decides, and a request's test_mode=1 forces a test
//     payment; arena sends it for every config filed as `test`.
//   - The billing currency is ILS, USD or EUR. An account without USD/EUR
//     permission would silently charge ILS at the day's rate, so the adapter
//     refuses anything else rather than let arena record one currency while
//     AllPay charges another.
package allpay

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
	// DefaultBaseURL is AllPay's API root; test and live share it.
	DefaultBaseURL = "https://allpay.to/app"
	// APIVersion is the `mode` query value of every call (API v15, the one
	// the reference documents in October 2026).
	APIVersion = "api15"

	// minLifetimeSeconds is the shortest page lifetime arena will ask for.
	minLifetimeSeconds = 60
	// maxItemNameRunes keeps the line item name short enough for receipts.
	maxItemNameRunes = 200
)

// API methods (the `show` query value).
const (
	methodGetPayment    = "getpayment"
	methodPaymentStatus = "paymentstatus"
	methodCheckKeys     = "checkkeys"
)

// AllPay error codes the adapter reacts to (API reference, "Errors").
const (
	ErrCodeInvalidParam   = 2
	ErrCodeBadSignature   = 3
	ErrCodeUnknownLogin   = 5
	ErrCodeOrderNotFound  = 6
	ErrCodeAlreadyPaid    = 15
	ErrCodeBelowMinimum   = 21
	ErrCodeUnknownMethod  = 20
	ErrCodePaymentAttempt = 26
)

// VAT codes of a line item: what the price already includes.
const (
	VATNone    = "0" // no VAT (a VAT-exempt dealer, osek patur)
	VATFull    = "1" // the current Israeli VAT rate (18% in 2026) is included
	VATZeroPct = "3" // 0% VAT (e.g. exported services)
)

// billingCurrencies are the currencies AllPay can charge in.
var billingCurrencies = map[string]bool{"ILS": true, "USD": true, "EUR": true}

// pageLangs are the payment page languages AllPay accepts. An unknown tag is
// not sent (AllPay then follows the browser), so a cosmetic preference can
// never cost a sale.
var pageLangs = map[string]bool{
	"en": true, "he": true, "ru": true, "ar": true, "es": true, "it": true, "de": true, "fr": true,
}

// Config is one integration's credentials and checkout preferences.
type Config struct {
	// Login is the integration's API login (Settings → API Integrations).
	Login string
	// APIKey is the same integration's API key. It signs requests and
	// verifies webhooks.
	APIKey string
	// VAT is the code printed on the receipt AllPay issues when the merchant
	// connected an accounting service: VATNone (default), VATFull or
	// VATZeroPct.
	VAT string
	// MaxInstallments is the most installments the buyer may choose (2–12);
	// 0 or 1 means one payment. AllPay allows installments in ILS only.
	MaxInstallments int
	// InstallmentsMinMinor is the smallest order total (ILS minor units)
	// that offers installments; 0 offers them on every ILS order.
	InstallmentsMinMinor int64
	// TestMode forces test_mode=1 on every payment page: real cards are not
	// charged whatever the integration's own setting says.
	TestMode bool
	// BaseURL overrides DefaultBaseURL. Tests only.
	BaseURL string
}

// Adapter talks to AllPay for ONE integration. It is cheap: callers build one
// per request from the organization's own config row.
type Adapter struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

// New builds an Adapter.
func New(cfg Config) *Adapter {
	cfg.Login = strings.TrimSpace(cfg.Login)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.VAT = strings.TrimSpace(cfg.VAT)
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

// ProviderName returns the canonical provider key.
func (a *Adapter) ProviderName() string { return Name }

// ─────────────────────────────────────────────────────────────────────────────
// HTTP
// ─────────────────────────────────────────────────────────────────────────────

// APIError is AllPay's own refusal of a request (error_code / error_msg).
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("allpay: API error %d: %s", e.Code, e.Message)
}

// endpoint is the URL of one API method.
func (a *Adapter) endpoint(method string) string {
	return a.cfg.BaseURL + "/?show=" + method + "&mode=" + APIVersion
}

// post signs params, posts them as JSON and decodes the answer. An answer
// carrying error_code is returned as *APIError. The answer's numbers stay
// json.Number.
func (a *Adapter) post(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	params["login"] = a.cfg.Login
	params[signField] = Sign(a.cfg.APIKey, params)
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("allpay: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(method), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("allpay: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("allpay: http: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("allpay: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("allpay: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	out, err := decodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("allpay: unreadable response (status %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if code, has := out["error_code"]; has {
		n, _ := strconv.Atoi(fmt.Sprint(code))
		msg, _ := scalarValue(out["error_msg"])
		return out, &APIError{Code: n, Message: msg}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ─────────────────────────────────────────────────────────────────────────────
// Hosted checkout
// ─────────────────────────────────────────────────────────────────────────────

// hiddenTehudat is the client_tehudat value that hides the Israeli ID field.
const hiddenTehudat = "000000000"

// ForeignPhone reports whether phone is written in international form
// (+ or 00) with a country code other than Israel's 972. A local number
// (0…) or an empty one is not foreign: the page then asks for the ID as
// usual.
func ForeignPhone(phone string) bool {
	var digits strings.Builder
	p := strings.TrimSpace(phone)
	international := strings.HasPrefix(p, "+")
	for _, r := range p {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if !international && strings.HasPrefix(d, "00") {
		international, d = true, d[2:]
	}
	if !international || len(d) < 7 {
		return false
	}
	return !strings.HasPrefix(d, "972")
}

// ErrUnsupportedCurrency is returned for a currency AllPay cannot charge in.
var ErrUnsupportedCurrency = errors.New("allpay: AllPay charges only in ILS, USD or EUR")

func pageLang(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	if !pageLangs[tag] {
		return ""
	}
	return strings.ToUpper(tag)
}

// vatCode validates the configured VAT code; empty means VATNone, which is
// what the WooCommerce plugin sends when the shop keeps no taxes.
func (a *Adapter) vatCode() (string, error) {
	switch a.cfg.VAT {
	case "":
		return VATNone, nil
	case VATNone, VATFull, VATZeroPct:
		return a.cfg.VAT, nil
	}
	return "", fmt.Errorf("allpay: vat code %q is not one of 0, 1, 3", a.cfg.VAT)
}

// CreateCheckoutSession creates the payment page and returns its URL.
//
// SessionID in the response is the order_id (arena's checkout session id):
// that is what every webhook is matched by, and what AllPay refunds and reads
// statuses by. AllPay mints no payment id of its own.
func (a *Adapter) CreateCheckoutSession(ctx context.Context, req payments.CreateHostedCheckoutRequest) (*payments.CreateHostedCheckoutResponse, error) {
	if a.cfg.Login == "" || a.cfg.APIKey == "" {
		return nil, errors.New("allpay: CreateCheckoutSession: the config has no API login or API key")
	}
	orderID := strings.TrimSpace(req.ClientReferenceID)
	if orderID == "" {
		return nil, errors.New("allpay: CreateCheckoutSession: ClientReferenceID (order_id) is required")
	}
	if req.Amount <= 0 {
		return nil, fmt.Errorf("allpay: CreateCheckoutSession: amount %d is not positive", req.Amount)
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if !billingCurrencies[currency] {
		return nil, fmt.Errorf("%w (asked for %q)", ErrUnsupportedCurrency, currency)
	}
	vat, err := a.vatCode()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.ProductName)
	if name == "" {
		name = "Tickets"
	}

	params := map[string]any{
		"order_id": orderID,
		// ONE line for the whole cart: arena has already done every per-ticket
		// sum, discount and service charge, and the price includes them.
		"items": []any{map[string]any{
			"name":  truncateRunes(name, maxItemNameRunes),
			"qty":   json.Number("1"),
			"price": json.Number(MinorToMajor(req.Amount)),
			"vat":   json.Number(vat),
		}},
		"currency": currency,
	}
	if req.CallbackURL != "" {
		params["webhook_url"] = req.CallbackURL
	}
	if req.SuccessURL != "" {
		params["success_url"] = req.SuccessURL
	}
	if req.CancelURL != "" {
		// AllPay has no fail URL: backlink_url is the "Return to site" button.
		params["backlink_url"] = req.CancelURL
	}
	if req.CustomerEmail != "" {
		params["client_email"] = req.CustomerEmail
	}
	if name := strings.TrimSpace(req.CustomerName); name != "" {
		params["client_name"] = truncateRunes(name, maxItemNameRunes)
	}
	if phone := strings.TrimSpace(req.CustomerPhone); phone != "" {
		params["client_phone"] = phone
		if ForeignPhone(phone) {
			// AllPay's documented value that hides the Israeli ID field: a
			// foreign buyer has no Teudat Zehut to type.
			params["client_tehudat"] = hiddenTehudat
		}
	}
	if lang := pageLang(req.Locale); lang != "" {
		params["lang"] = lang
	}
	if req.ExpiresAtUnix > 0 {
		exp := req.ExpiresAtUnix
		if floor := a.now().Unix() + minLifetimeSeconds; exp < floor {
			exp = floor
		}
		params["expire"] = json.Number(strconv.FormatInt(exp, 10))
	}
	if a.cfg.MaxInstallments > 1 && currency == "ILS" && req.Amount >= a.cfg.InstallmentsMinMinor {
		inst := a.cfg.MaxInstallments
		if inst > 12 {
			inst = 12
		}
		params["inst"] = json.Number(strconv.Itoa(inst))
	}
	if a.cfg.TestMode {
		params["test_mode"] = json.Number("1")
	}

	out, err := a.post(ctx, methodGetPayment, params)
	if err != nil {
		return nil, fmt.Errorf("allpay: CreateCheckoutSession: %w", err)
	}
	pageURL, _ := scalarValue(out["payment_url"])
	if pageURL == "" {
		return nil, errors.New("allpay: CreateCheckoutSession: response has no payment_url")
	}
	resp := &payments.CreateHostedCheckoutResponse{SessionID: orderID, URL: pageURL}
	if exp, ok := params["expire"].(json.Number); ok {
		resp.ExpiresAtUnix, _ = exp.Int64()
	}
	return resp, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Credential verification
// ─────────────────────────────────────────────────────────────────────────────

// VerifyCredentials proves the login + API key pair with AllPay's own
// checkkeys method, which moves no money and creates nothing.
//
//   - An answer without error_code means AllPay accepted the pair.
//   - error_code 3 (bad signature = wrong key) and 5 (unknown login) are
//     verdicts on the credential.
//   - Anything else (transport, 5xx, another code) says nothing about the key
//     and is reported as unreachable, never as refused.
func (a *Adapter) VerifyCredentials(ctx context.Context) error {
	if a.cfg.Login == "" || a.cfg.APIKey == "" {
		return fmt.Errorf("%w: the config has no API login or API key", payments.ErrCredentialRefused)
	}
	_, err := a.post(ctx, methodCheckKeys, map[string]any{})
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Code == ErrCodeBadSignature || apiErr.Code == ErrCodeUnknownLogin) {
		return fmt.Errorf("%w: %s", payments.ErrCredentialRefused, apiErr.Error())
	}
	return fmt.Errorf("%w: %s", payments.ErrProviderUnreachable, err.Error())
}

// ─────────────────────────────────────────────────────────────────────────────
// Payment status
// ─────────────────────────────────────────────────────────────────────────────

// ErrOrderNotFound: AllPay has no order with that order_id for this login.
var ErrOrderNotFound = errors.New("allpay: order not found")

// PaymentStatus is AllPay's view of one order.
type PaymentStatus struct {
	OrderID string
	// Status: 0 unpaid (an attempt may have failed, the page may still be
	// paid), 1 paid, 3 refunded in full, 4 refunded in part.
	Status int
	// AmountMinor is the order amount before discounts, DiscountMinor the
	// order's discount (none on arena's pages), RefundedMinor what was
	// refunded so far (AllPay sends it with status 4 only).
	AmountMinor   int64
	DiscountMinor int64
	RefundedMinor int64
	Currency      string
	// Preauth is set only for a J5 pre-authorization ("authorized" means
	// reserved, not charged). Arena's pages never ask for one.
	Preauth string
	// DeclineCode / Error describe the last failed attempt, if any.
	DeclineCode string
	Error       string
}

// PaymentStatus reads one order back (method paymentstatus). AllPay asks to
// call it at least 2 seconds after a payment attempt.
func (a *Adapter) PaymentStatus(ctx context.Context, orderID string) (*PaymentStatus, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, errors.New("allpay: PaymentStatus: order_id is required")
	}
	out, err := a.post(ctx, methodPaymentStatus, map[string]any{"order_id": orderID})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == ErrCodeOrderNotFound {
			return nil, fmt.Errorf("%w: %s", ErrOrderNotFound, orderID)
		}
		return nil, fmt.Errorf("allpay: PaymentStatus: %w", err)
	}
	str := func(k string) string { s, _ := scalarValue(out[k]); return s }
	status, err := strconv.Atoi(str("status"))
	if err != nil {
		return nil, fmt.Errorf("allpay: PaymentStatus: unreadable status %q", str("status"))
	}
	st := &PaymentStatus{
		OrderID:     str("order_id"),
		Status:      status,
		Currency:    strings.ToUpper(str("currency")),
		DeclineCode: str("decline_code"),
		Error:       str("error"),
	}
	st.AmountMinor, _ = MajorToMinor(str("amount"))
	st.RefundedMinor, _ = MajorToMinor(str("refunded"))
	st.DiscountMinor, _ = MajorToMinor(str("discount"))
	st.Preauth = str("preauth")
	return st, nil
}

// ChargedMinor is what the buyer was charged: the amount minus the discount.
func (s *PaymentStatus) ChargedMinor() int64 { return s.AmountMinor - s.DiscountMinor }
