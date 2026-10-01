package flitt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// Vector computed with the reference algorithm of docs.flitt.com/api/building-signature
// (the Python snippet there) over the parameters of the create-order example.
// The signature printed IN the documentation belongs to other data and does not
// reproduce; the algorithm itself was proven against the live sandbox
// (merchant 1549901, key "test") on 2026-10-01, which accepted our signatures
// and answered 1014 to a wrong key.
func TestSign_MatchesReferenceAlgorithm(t *testing.T) {
	params := map[string]any{
		"merchant_id":         int64(1549901),
		"order_id":            "TestOrder2",
		"currency":            "GEL",
		"amount":              int64(1000),
		"order_desc":          "Test payment",
		"server_callback_url": "http://myshop/callback/",
	}
	const want = "cd0edb710cbbdb6c2a4d965cdb91fdfabc343215"
	if got := Sign("test", params); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

func TestSign_SkipsEmptyAndSignatureButKeepsZero(t *testing.T) {
	a := Sign("k", map[string]any{"a": "1", "b": "", "c": nil, "signature": "x", "response_signature_string": "y"})
	b := Sign("k", map[string]any{"a": "1"})
	if a != b {
		t.Fatalf("empty/nil/signature fields must not affect the signature: %s vs %s", a, b)
	}
	zero := Sign("k", map[string]any{"a": "1", "n": json.Number("0")})
	if zero == b {
		t.Fatal("a zero must NOT be treated as empty")
	}
}

// The callback example of docs.flitt.com/api/callbacks, re-signed under a key of
// our own (the documented signature belongs to a body we cannot reproduce byte
// for byte). It exercises numbers (payment_id) and empty strings.
const callbackBody = `{"rrn":"111111111111","masked_card":"444455XXXXXX1111","sender_cell_phone":"","currency":"EUR","fee":"",` +
	`"reversal_amount":"0","actual_amount":"200","response_description":"","sender_email":"test@test.com","order_status":"approved",` +
	`"response_status":"success","order_id":"6f1c0f6e-0000-4000-8000-000000000001","tran_type":"purchase","eci":"5",` +
	`"payment_system":"card","approval_code":"123456","merchant_id":1549901,"payment_id":805243692,"card_bin":444455,` +
	`"response_code":"","amount":"200","merchant_data":"{}","additional_info":"{\"a\":1}","response_signature_string":"masked"}`

func signedCallback(t *testing.T, key string) []byte {
	t.Helper()
	m, err := decodeFlat([]byte(callbackBody))
	if err != nil {
		t.Fatal(err)
	}
	m["signature"] = Sign(key, m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyCallback_AcceptsAndParses(t *testing.T) {
	cb, err := VerifyCallback(signedCallback(t, "secret"), "secret")
	if err != nil {
		t.Fatalf("VerifyCallback: %v", err)
	}
	if cb.OrderStatus != StatusApproved || cb.OrderID != "6f1c0f6e-0000-4000-8000-000000000001" {
		t.Fatalf("unexpected callback: %+v", cb)
	}
	if cb.PaymentID != "805243692" || cb.MerchantID != "1549901" || cb.Amount != 200 || cb.Currency != "EUR" {
		t.Fatalf("unexpected fields: %+v", cb)
	}
}

func TestVerifyCallback_RejectsWrongKeyTamperingAndMissingSignature(t *testing.T) {
	good := signedCallback(t, "secret")
	if _, err := VerifyCallback(good, "other"); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("wrong key: got %v", err)
	}
	tampered := strings.Replace(string(good), `"amount":"200"`, `"amount":"1"`, 1)
	if tampered == string(good) {
		t.Fatal("test bug: nothing tampered")
	}
	if _, err := VerifyCallback([]byte(tampered), "secret"); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("tampered amount: got %v", err)
	}
	if _, err := VerifyCallback([]byte(callbackBody), "secret"); !errors.Is(err, payments.ErrInvalidWebhookSignature) {
		t.Fatalf("no signature: got %v", err)
	}
	if _, err := VerifyCallback(good, ""); err == nil {
		t.Fatal("an empty key must never verify")
	}
}

func TestLooksLikeCallback(t *testing.T) {
	if !LooksLikeCallback([]byte(callbackBody)) {
		t.Fatal("a Flitt callback must be recognised")
	}
	for _, body := range []string{
		`{"type":"checkout.session.completed","data":{"object":{"id":"cs_1"}}}`,
		`{"provider_payment_id":"x","event_type":"mock.succeeded"}`,
		`not json`,
	} {
		if LooksLikeCallback([]byte(body)) {
			t.Fatalf("must not be recognised as Flitt: %s", body)
		}
	}
}

type captured struct {
	path   string
	params map[string]any
}

func stubFlitt(t *testing.T, reply string, status int, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var env struct {
			Request map[string]any `json:"request"`
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&env)
		if got != nil {
			got.path, got.params = r.URL.Path, env.Request
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCreateCheckoutSession_BuildsSignedRequest(t *testing.T) {
	var got captured
	srv := stubFlitt(t, `{"response":{"response_status":"success","checkout_url":"https://pay.flitt.com/merchants/x/index.html?token=t","payment_id":"805230052"}}`, 200, &got)

	a := New(Config{MerchantID: "1549901", PaymentKey: "test", BaseURL: srv.URL})
	a.now = func() time.Time { return time.Unix(1_000_000, 0) }
	resp, err := a.CreateCheckoutSession(context.Background(), payments.CreateHostedCheckoutRequest{
		Amount:            4250,
		Currency:          "eur",
		ProductName:       "Master class",
		CustomerEmail:     "buyer@example.com",
		ClientReferenceID: "11111111-1111-4111-8111-111111111111",
		SuccessURL:        "https://site.example/tickets?checkout_token=abc",
		CancelURL:         "https://site.example/tickets?checkout_token=abc",
		CallbackURL:       "https://api.example/v1/payment-intents/webhook/cfg",
		ExpiresAtUnix:     1_000_000 + 1860,
		Locale:            "fr-FR",
		Metadata:          map[string]string{"arena_org_id": "o"},
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if resp.SessionID != "11111111-1111-4111-8111-111111111111" || !strings.Contains(resp.URL, "token=t") || resp.PaymentID != "805230052" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got.path != "/checkout/url" {
		t.Fatalf("path = %s", got.path)
	}
	p := got.params
	if p["currency"] != "EUR" || p["lang"] != "fr" || p["order_id"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("params: %v", p)
	}
	if p["amount"].(json.Number).String() != "4250" || p["lifetime"].(json.Number).String() != "1860" {
		t.Fatalf("amount/lifetime: %v / %v", p["amount"], p["lifetime"])
	}
	if p["server_callback_url"] != "https://api.example/v1/payment-intents/webhook/cfg" {
		t.Fatalf("callback url: %v", p["server_callback_url"])
	}
	// The signature the stub received must verify under the key — i.e. we
	// signed exactly what we sent.
	sig, _ := p["signature"].(string)
	if want := Sign("test", p); sig != want {
		t.Fatalf("request signature %s does not match recomputed %s", sig, want)
	}
}

func TestCreateCheckoutSession_UnknownLocaleIsOmitted(t *testing.T) {
	var got captured
	srv := stubFlitt(t, `{"response":{"response_status":"success","checkout_url":"https://x/y"}}`, 200, &got)
	a := New(Config{MerchantID: "1", PaymentKey: "k", BaseURL: srv.URL})
	if _, err := a.CreateCheckoutSession(context.Background(), payments.CreateHostedCheckoutRequest{
		Amount: 100, Currency: "GEL", ClientReferenceID: "o1", Locale: "he",
	}); err != nil {
		t.Fatal(err)
	}
	if _, present := got.params["lang"]; present {
		t.Fatal("Flitt answers 1007 for an unknown lang; it must be omitted")
	}
}

func TestCreateCheckoutSession_FailureIsAnAPIError(t *testing.T) {
	srv := stubFlitt(t, `{"response":{"response_status":"failure","error_code":1012,"error_message":"Currency is not allowed"}}`, 200, nil)
	a := New(Config{MerchantID: "1", PaymentKey: "k", BaseURL: srv.URL})
	_, err := a.CreateCheckoutSession(context.Background(), payments.CreateHostedCheckoutRequest{
		Amount: 100, Currency: "XYZ", ClientReferenceID: "o1",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "1012" {
		t.Fatalf("want APIError 1012, got %v", err)
	}
}

func TestVerifyCredentials(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		status  int
		wantErr error // nil = accepted
	}{
		{"order not found proves the key", `{"response":{"response_status":"failure","error_code":"1018","error_message":"Order not found"}}`, 200, nil},
		{"bad signature is a refusal", `{"response":{"response_status":"failure","error_code":"1014","error_message":"Invalid signature"}}`, 200, payments.ErrCredentialRefused},
		{"unknown merchant is a refusal", `{"response":{"response_status":"failure","error_code":1016,"error_message":"Merchant not found"}}`, 200, payments.ErrCredentialRefused},
		{"5xx is unreachable, not a verdict", `oops`, 502, payments.ErrProviderUnreachable},
		{"an unrelated error is unreachable", `{"response":{"response_status":"failure","error_code":"1007","error_message":"bad"}}`, 200, payments.ErrProviderUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := stubFlitt(t, tc.reply, tc.status, nil)
			err := New(Config{MerchantID: "1549901", PaymentKey: "test", BaseURL: srv.URL}).VerifyCredentials(context.Background())
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestVerifyCredentials_NonNumericMerchantIsRefused(t *testing.T) {
	err := New(Config{MerchantID: "abc", PaymentKey: "k"}).VerifyCredentials(context.Background())
	if !errors.Is(err, payments.ErrCredentialRefused) {
		t.Fatalf("got %v", err)
	}
}
