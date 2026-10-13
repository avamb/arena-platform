package allpay

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// TestSign_DocumentedWorkedExample pins the worked example of AllPay's API
// reference ("Signature Algorithm"): the same body and key must give the
// same SHA-256. The example's empty strings are left out, its 0 values stay,
// the item's keys are sorted and flattened inline.
func TestSign_DocumentedWorkedExample(t *testing.T) {
	body := []byte(`{
	  "items": [{"name": "Test payment", "price": 1000, "qty": 1, "discount_val": 0, "discount_type": "fixed", "vat": 1}],
	  "order_id": "1589758an",
	  "client_name": "Jason Statham",
	  "client_email": "jason@company.io",
	  "client_tehudat": "000000000",
	  "currency": "ILS",
	  "currency_display": "ILS",
	  "lang": "AUTO",
	  "preauthorize": 0,
	  "allpay_token": "",
	  "inst": "",
	  "doc_type": "",
	  "button_title": "pay",
	  "success_url": "https://example.com/success",
	  "webhook_url": "https://example.com/hook/allpay",
	  "backlink_url": "",
	  "login": "pp1008795"
	}`)
	m, err := decodeObject(body)
	if err != nil {
		t.Fatal(err)
	}
	const key = "CB545B50989469F7258C8E043462B30C"
	const want = "c3c5458724d88837bd60879b5130c860589e42248360b4ca42c2539ebf8a7803"
	if got := Sign(key, m); got != want {
		t.Fatalf("Sign(documented example) = %s; want %s", got, want)
	}
	// The sign field itself never takes part.
	m["sign"] = "anything"
	if got := Sign(key, m); got != want {
		t.Fatalf("Sign with a sign field present = %s; want %s", got, want)
	}
}

func TestSign_ValueRules(t *testing.T) {
	const key = "K"
	sig := func(raw string) string {
		t.Helper()
		m, err := decodeObject([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return Sign(key, m)
	}
	same := []struct{ name, a, b string }{
		{"100.00 is signed as 100", `{"a":100.00}`, `{"a":100}`},
		{"100.50 is signed as 100.5", `{"a":100.50}`, `{"a":"100.5"}`},
		{"true is 1", `{"a":true}`, `{"a":1}`},
		{"false is 0, not skipped", `{"a":false}`, `{"a":0}`},
		{"empty string is left out", `{"a":"","b":"x"}`, `{"b":"x"}`},
		{"null is left out", `{"a":null,"b":"x"}`, `{"b":"x"}`},
		{"empty array adds nothing", `{"a":[],"b":"x"}`, `{"b":"x"}`},
		{"keys are sorted", `{"b":"2","a":"1"}`, `{"a":"1","b":"2"}`},
		{"item keys are sorted", `{"items":[{"qty":1,"name":"n"}]}`, `{"items":[{"name":"n","qty":1}]}`},
		{"object values by sorted key", `{"s":{"z":"2","a":"1"}}`, `{"s":{"a":"1","z":"2"}}`},
	}
	for _, c := range same {
		if sig(c.a) != sig(c.b) {
			t.Errorf("%s: %s and %s sign differently", c.name, c.a, c.b)
		}
	}
	different := []struct{ name, a, b string }{
		{"a string keeps its exact text", `{"a":"100.00"}`, `{"a":100}`},
		{"zero is a value", `{"a":0,"b":"x"}`, `{"b":"x"}`},
		{"no trimming", `{"a":" x"}`, `{"a":"x"}`},
		{"array order is kept", `{"a":["1","2"]}`, `{"a":["2","1"]}`},
	}
	for _, c := range different {
		if sig(c.a) == sig(c.b) {
			t.Errorf("%s: %s and %s sign the same", c.name, c.a, c.b)
		}
	}
}

func TestMoney_MinorMajorRoundTrip(t *testing.T) {
	cases := []struct {
		minor int64
		major string
	}{
		{10000, "100"}, {12350, "123.5"}, {105, "1.05"}, {1, "0.01"}, {500, "5"}, {123456789, "1234567.89"},
	}
	for _, c := range cases {
		if got := MinorToMajor(c.minor); got != c.major {
			t.Errorf("MinorToMajor(%d) = %q; want %q", c.minor, got, c.major)
		}
		if got, ok := MajorToMinor(c.major); !ok || got != c.minor {
			t.Errorf("MajorToMinor(%q) = %d, %v; want %d", c.major, got, ok, c.minor)
		}
	}
	if got, _ := MajorToMinor("99.89999999999999"); got != 9990 {
		t.Errorf("MajorToMinor(99.89999999999999) = %d; want 9990", got)
	}
	if _, ok := MajorToMinor(""); ok {
		t.Error("MajorToMinor(\"\") reported a value")
	}
}

// signedWebhook builds a webhook body the way AllPay sends it: the given
// fields plus sign over them.
func signedWebhook(t *testing.T, key string, fields map[string]any) []byte {
	t.Helper()
	fields["sign"] = Sign(key, fields)
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func paidWebhookFields(orderID string) map[string]any {
	return map[string]any{
		"order_id":         orderID,
		"status":           json.Number("1"),
		"amount":           json.Number("123.5"),
		"currency":         "ILS",
		"currency_display": "ILS",
		"currency_payment": "ILS",
		"currency_course":  json.Number("1"),
		"name":             "Concert",
		"items":            []any{map[string]any{"name": "Concert", "qty": json.Number("1"), "price": json.Number("123.5"), "vat": json.Number("0")}},
		"inst":             json.Number("1"),
		"card_mask":        "455743******3431",
		"card_brand":       "Visa",
		"foreign_card":     json.Number("0"),
		"client_email":     "buyer@example.com",
		"receipt":          "",
	}
}

func TestVerifyCallback(t *testing.T) {
	body := signedWebhook(t, "the-key", paidWebhookFields("6f1c0f6e-0000-4000-8000-000000000001"))
	if !LooksLikeCallback(body) {
		t.Fatal("a paid webhook is not recognized")
	}
	cb, err := VerifyCallback(body, "the-key")
	if err != nil {
		t.Fatalf("VerifyCallback: %v", err)
	}
	if cb.OrderID != "6f1c0f6e-0000-4000-8000-000000000001" || cb.Status != StatusPaid || cb.AmountMinor != 12350 || cb.Currency != "ILS" {
		t.Fatalf("parsed = %+v", cb)
	}
	if _, err := VerifyCallback(body, "another-key"); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("wrong key = %v; want ErrInvalidWebhookSignature", err)
	}
	if _, err := VerifyCallback(body, ""); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("empty key = %v; want ErrInvalidWebhookSignature", err)
	}
	// A tampered amount no longer verifies.
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	m["amount"] = 1
	tampered, _ := json.Marshal(m)
	if _, err := VerifyCallback(tampered, "the-key"); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("tampered amount = %v; want ErrInvalidWebhookSignature", err)
	}
}

func TestLooksLikeCallback_OnlyItsShape(t *testing.T) {
	others := []string{
		`{"order_id":"1","order_status":"approved","merchant_id":1,"signature":"x"}`, // Flitt
		`{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1"}}}`,
		`{"provider_payment_id":"x","event_type":"mock.succeeded"}`,
		`{"order_id":"1","status":1}`, // no sign
		`not json`,
	}
	for _, b := range others {
		if LooksLikeCallback([]byte(b)) {
			t.Errorf("%s was recognized as an AllPay webhook", b)
		}
	}
}
