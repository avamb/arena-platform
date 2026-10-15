package flitt

import (
	"context"
	"crypto/sha1" //nolint:gosec // Flitt signs with SHA-1, see adapter.go
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

const okReply = `{"response":{"response_status":"success","checkout_url":"https://x/y","payment_id":"1"}}`

func decodeReservationData(t *testing.T, v any) map[string]string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("reservation_data is %T, want string", v)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("reservation_data is not BASE64: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("reservation_data is not a JSON object: %v", err)
	}
	return out
}

// manualSignature is the signature written out by hand from the documented
// rule (docs.flitt.com/api/building-signature): the payment key, then every
// non-empty parameter value in the alphabetical order of the parameter names,
// joined with "|", SHA-1. It deliberately does not call Sign.
func manualSignature(key string, params map[string]string) string {
	names := make([]string, 0, len(params))
	for k, v := range params {
		if k != "signature" && v != "" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	parts := []string{key}
	for _, k := range names {
		parts = append(parts, params[k])
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "|"))) //nolint:gosec // see import
	return hex.EncodeToString(sum[:])
}

func TestCreateCheckoutSession_BuyerNameAndPhoneAreSentAndSigned(t *testing.T) {
	var got captured
	srv := stubFlitt(t, okReply, 200, &got)
	a := New(Config{MerchantID: "1549901", PaymentKey: "k3y", BaseURL: srv.URL})
	_, err := a.CreateCheckoutSession(context.Background(), payments.CreateHostedCheckoutRequest{
		Amount: 4250, Currency: "EUR", ProductName: "Master class",
		CustomerEmail: "buyer@example.com", CustomerName: "Ana García", CustomerPhone: "+34 600-111-222",
		ClientReferenceID: "order-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	rd := decodeReservationData(t, got.params["reservation_data"])
	if rd["customer_name"] != "Ana Garcia" || rd["phonemobile"] != "+34600111222" || len(rd) != 2 {
		t.Fatalf("reservation_data = %v", rd)
	}
	if got.params["sender_email"] != "buyer@example.com" {
		t.Fatalf("sender_email must stay: %v", got.params["sender_email"])
	}
	// Recompute the signature by hand over everything that was actually sent.
	flat := map[string]string{}
	for k, v := range got.params {
		switch x := v.(type) {
		case string:
			flat[k] = x
		case json.Number:
			flat[k] = x.String()
		default:
			t.Fatalf("unexpected param type %T for %s", v, k)
		}
	}
	if want := manualSignature("k3y", flat); flat["signature"] != want {
		t.Fatalf("signature %s, hand-computed %s", flat["signature"], want)
	}
	// reservation_data really is part of the signed string: dropping it
	// changes the signature.
	delete(flat, "reservation_data")
	if manualSignature("k3y", flat) == got.params["signature"] {
		t.Fatal("reservation_data must take part in the signature")
	}
}

func TestCreateCheckoutSession_NoNameNoPhoneSendsNoReservationData(t *testing.T) {
	for name, req := range map[string]payments.CreateHostedCheckoutRequest{
		"empty":      {},
		"blank":      {CustomerName: "  ", CustomerPhone: " "},
		"cyrillic":   {CustomerName: "Анна Иванова"},
		"short":      {CustomerPhone: "123"},
		"bad phone":  {CustomerPhone: "call me"},
		"too long":   {CustomerPhone: "+1234567890123456"},
		"slashed":    {CustomerName: "Ana/Garcia"},
		"hebrew":     {CustomerName: "דוד"},
		"stroke o":   {CustomerName: "Søren"},
		"only space": {CustomerName: "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			var got captured
			srv := stubFlitt(t, okReply, 200, &got)
			a := New(Config{MerchantID: "1", PaymentKey: "k", BaseURL: srv.URL})
			req.Amount, req.Currency, req.ClientReferenceID = 100, "EUR", "o1"
			if _, err := a.CreateCheckoutSession(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if v, present := got.params["reservation_data"]; present {
				t.Fatalf("reservation_data must be absent, got %v", v)
			}
		})
	}
}

func TestCreateCheckoutSession_OnlyTheValidHalfIsSent(t *testing.T) {
	var got captured
	srv := stubFlitt(t, okReply, 200, &got)
	a := New(Config{MerchantID: "1", PaymentKey: "k", BaseURL: srv.URL})
	if _, err := a.CreateCheckoutSession(context.Background(), payments.CreateHostedCheckoutRequest{
		Amount: 100, Currency: "EUR", ClientReferenceID: "o1",
		CustomerName: "Анна", CustomerPhone: "0034600111222",
	}); err != nil {
		t.Fatal(err)
	}
	rd := decodeReservationData(t, got.params["reservation_data"])
	if len(rd) != 1 || rd["phonemobile"] != "+34600111222" {
		t.Fatalf("a Cyrillic name is left out, the phone stays (00 -> +): %v", rd)
	}
}

func TestLatinName(t *testing.T) {
	for in, want := range map[string]string{
		"Ana García":       "Ana Garcia",
		"  Jan   Novák  ":  "Jan Novak",
		"Zdeněk Šťastný":   "Zdenek Stastny",
		"Mary-Jane O'Neil": "Mary-Jane O'Neil",
		"Анна":             "",
		"Łukasz":           "",
		"":                 "",
	} {
		if got := latinName(in); got != want {
			t.Errorf("latinName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := latinName(strings.Repeat("a", 2000)); len(got) != maxCustomerNameRunes {
		t.Errorf("a long name must be cut to %d, got %d", maxCustomerNameRunes, len(got))
	}
}

func TestPhoneMobile(t *testing.T) {
	for in, want := range map[string]string{
		"+34 600 111 222":   "+34600111222",
		"0034600111222":     "+34600111222",
		"(050) 123-4567":    "0501234567",
		"+972.50.123.4567":  "+972501234567",
		"+1234567":          "+1234567",
		"123456":            "",
		"+34 600 abc":       "",
		"+1234567890123456": "",
		"":                  "",
	} {
		if got := phoneMobile(in); got != want {
			t.Errorf("phoneMobile(%q) = %q, want %q", in, got, want)
		}
	}
}
