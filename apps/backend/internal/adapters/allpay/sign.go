// sign.go — AllPay's request and webhook signature (allpay.to/docs/api-reference.htm,
// "Signature Algorithm").
//
// Every request arena sends and every webhook AllPay sends carries a `sign`
// field: SHA-256 (lowercase hex) of the parameter VALUES joined with ":", in
// key order, with ":" + the API key appended. The rules, all of which the
// documentation names as the usual causes of "Signature is incorrect":
//
//   - the `sign` field itself never takes part;
//   - "" and null are left out, exactly like a parameter that is not sent;
//     0, "0" and false ARE values and stay;
//   - keys are sorted A–Z at every level: the top level, an object parameter
//     (subscription) and every object inside an array (items); array
//     elements keep their order;
//   - a JSON number is written in its shortest exact form (100.00 → 100,
//     100.50 → 100.5); a string keeps its exact text ("100.00" stays);
//   - true → 1, false → 0;
//   - values are not trimmed, escaped or case-folded.
//
// The signature is computed over the values AS THEY ARRIVE in the JSON body,
// so a webhook is decoded with json.Number (never float64 first) and a
// request is built from json.Number values, which marshal back verbatim.
// TestSign_DocumentedWorkedExample pins the documentation's own example.
package allpay

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// signField is the name of the signature parameter.
const signField = "sign"

// Sign computes the AllPay signature of params under the API key.
func Sign(apiKey string, params map[string]any) string {
	chunks := make([]string, 0, len(params))
	for _, k := range sortedKeys(params) {
		if k == signField {
			continue
		}
		chunks = appendValue(chunks, params[k])
	}
	chunks = append(chunks, apiKey)
	sum := sha256.Sum256([]byte(strings.Join(chunks, ":")))
	return hex.EncodeToString(sum[:])
}

// appendValue adds one parameter's contribution to the signed string. An
// array or object contributes its elements (objects inside it by sorted
// key), an empty one contributes nothing.
func appendValue(chunks []string, v any) []string {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			chunks = appendNested(chunks, item)
		}
		return chunks
	case map[string]any:
		for _, k := range sortedKeys(t) {
			chunks = appendNested(chunks, t[k])
		}
		return chunks
	}
	if s, ok := scalarValue(v); ok {
		chunks = append(chunks, s)
	}
	return chunks
}

// appendNested handles one element of an array or object parameter: an
// object contributes its scalar values by sorted key, a scalar itself.
func appendNested(chunks []string, item any) []string {
	if m, ok := item.(map[string]any); ok {
		for _, k := range sortedKeys(m) {
			if s, ok := scalarValue(m[k]); ok {
				chunks = append(chunks, s)
			}
		}
		return chunks
	}
	if s, ok := scalarValue(item); ok {
		chunks = append(chunks, s)
	}
	return chunks
}

// scalarValue renders a decoded JSON scalar the way AllPay signs it. ok is
// false for a value that is left out ("" and null). A value nested deeper
// than AllPay documents (an object inside an item) is serialised compactly
// rather than dropped, so a body arena did not expect can never verify by
// accident of omission.
func scalarValue(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, t != ""
	case json.Number:
		return numberValue(t.String()), true
	case bool:
		if t {
			return "1", true
		}
		return "0", true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

// numberValue writes a JSON number literal in its shortest exact form, the
// way JavaScript's String(n) and PHP's json_encode do: an integer literal
// stays as it is, a decimal loses its trailing zeros (100.00 → 100,
// 100.50 → 100.5).
func numberValue(lit string) string {
	if !strings.ContainsAny(lit, ".eE") {
		return lit
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return lit
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─────────────────────────────────────────────────────────────────────────────
// Webhook (AllPay calls it a webhook; arena's state machine calls it a callback)
// ─────────────────────────────────────────────────────────────────────────────

// ErrInvalidSignature is returned when a webhook does not verify.
var ErrInvalidSignature = fmt.Errorf("%w: allpay signature mismatch", payments.ErrInvalidWebhookSignature)

// Webhook types. A payment webhook has no `type`; a chargeback has one.
const (
	TypeChargeback       = "chargeback"
	TypeChargebackRevert = "chargeback_revert"
)

// Payment statuses as AllPay reports them.
const (
	StatusUnpaid            = 0
	StatusPaid              = 1
	StatusRefunded          = 3
	StatusPartiallyRefunded = 4
)

// Callback is a verified webhook.
type Callback struct {
	// OrderID is the order_id arena sent: the checkout session id.
	OrderID string
	// Type is empty for a payment webhook, chargeback / chargeback_revert
	// otherwise.
	Type string
	// Status is 0 (this attempt failed), 1 (paid); for a chargeback the
	// order's state after it (1, 3, 4).
	Status int
	// AmountMinor is `amount` in minor units of Currency.
	AmountMinor int64
	// Currency is the currency of `amount`: `currency_display` (API reference,
	// webhook: "amount: in the display currency in every webhook"), falling
	// back to the v14 `currency`, then to `currency_payment`.
	Currency string
	// CurrencyPayment is `currency_payment`, the currency the card was
	// charged in, when sent.
	CurrencyPayment string
	// NotCharged is true for a webhook that reports a saved card or an
	// authorised reservation rather than money taken (subscription_create,
	// preauth=authorized). arena never asks for either; such a webhook moves
	// nothing.
	NotCharged bool
	// DeclineCode / Error describe a failed attempt (status 0).
	DeclineCode string
	Error       string
	// Raw is the decoded body, for the audit trail.
	Raw map[string]any
}

// decodeObject decodes a JSON object keeping numbers as json.Number, so a
// value is signed exactly as it arrived.
func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("allpay: empty webhook object")
	}
	return m, nil
}

// LooksLikeCallback reports whether body has the shape of an AllPay webhook:
// a JSON object with order_id, status and a string sign. Flitt's callback
// (order_status, merchant_id, signature) and a Stripe event envelope
// (type + data) never have all three.
func LooksLikeCallback(body []byte) bool {
	m, err := decodeObject(body)
	if err != nil {
		return false
	}
	_, hasOrder := m["order_id"]
	_, hasStatus := m["status"]
	sign, _ := m[signField].(string)
	return hasOrder && hasStatus && sign != ""
}

// VerifyCallback checks the signature of a webhook body under the API key and
// returns the parsed webhook. Constant-time compare.
func VerifyCallback(body []byte, apiKey string) (*Callback, error) {
	m, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("allpay: decode webhook: %w", err)
	}
	got, _ := m[signField].(string)
	if got == "" || apiKey == "" {
		return nil, ErrInvalidSignature
	}
	want := Sign(apiKey, m)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(got))), []byte(want)) != 1 {
		return nil, ErrInvalidSignature
	}
	return callbackFromMap(m), nil
}

func callbackFromMap(m map[string]any) *Callback {
	str := func(k string) string { s, _ := scalarValue(m[k]); return s }
	status, _ := strconv.Atoi(str("status"))
	cb := &Callback{
		OrderID:     str("order_id"),
		Type:        strings.ToLower(str("type")),
		Status:      status,
		DeclineCode: str("decline_code"),
		Error:       str("error"),
		Raw:         m,
	}
	cb.Currency, cb.CurrencyPayment = displayAndChargeCurrency(str)
	if cb.Error == "" {
		cb.Error = str("decline_reason")
	}
	if minor, ok := MajorToMinor(str("amount")); ok {
		cb.AmountMinor = minor
	}
	if _, saved := m["subscription_create"]; saved {
		cb.NotCharged = true
	}
	if strings.EqualFold(str("preauth"), "authorized") {
		cb.NotCharged = true
	}
	return cb
}

// ─────────────────────────────────────────────────────────────────────────────
// Money
// ─────────────────────────────────────────────────────────────────────────────

// MinorToMajor writes an amount in minor units (agorot, cents) as AllPay's
// major-unit number, in the shortest exact form: 12350 → "123.5",
// 10000 → "100", 105 → "1.05". AllPay's currencies (ILS, USD, EUR) all have
// two decimals. Integer arithmetic only, so no float ever rounds a price.
func MinorToMajor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	whole, frac := minor/100, minor%100
	switch {
	case frac == 0:
		return fmt.Sprintf("%s%d", sign, whole)
	case frac%10 == 0:
		return fmt.Sprintf("%s%d.%d", sign, whole, frac/10)
	default:
		return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
	}
}

// MajorToMinor parses an AllPay amount ("100", "100.5", "99.89999999999999")
// into minor units, rounding to the nearest agora as AllPay itself does.
func MajorToMinor(major string) (int64, bool) {
	major = strings.TrimSpace(major)
	if major == "" {
		return 0, false
	}
	if !strings.ContainsAny(major, ".eE") {
		n, err := strconv.ParseInt(major, 10, 64)
		if err != nil {
			return 0, false
		}
		return n * 100, true
	}
	f, err := strconv.ParseFloat(major, 64)
	if err != nil {
		return 0, false
	}
	if f < 0 {
		return -int64(-f*100 + 0.5), true
	}
	return int64(f*100 + 0.5), true
}
