package allpay

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// stubAllPay answers like allpay.to: it refuses a request whose sign does not
// verify under key (error 3), an unknown login (error 5), and records what it
// was asked.
type stubAllPay struct {
	server *httptest.Server
	login  string
	key    string

	mu       sync.Mutex
	requests []stubRequest
	// answer, when set, replaces the default answer of a method.
	answer map[string]string
}

type stubRequest struct {
	Show, Mode string
	Body       map[string]any
}

func newStubAllPay(t *testing.T, login, key string) *stubAllPay {
	t.Helper()
	s := &stubAllPay{login: login, key: key, answer: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body, err := decodeObject(raw)
		if err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		show, mode := r.URL.Query().Get("show"), r.URL.Query().Get("mode")
		s.mu.Lock()
		s.requests = append(s.requests, stubRequest{Show: show, Mode: mode, Body: body})
		custom := s.answer[show]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if login, _ := body["login"].(string); login != s.login {
			_, _ = w.Write([]byte(`{"error_code":5,"error_msg":"Login incorrect"}`))
			return
		}
		if sign, _ := body["sign"].(string); sign != Sign(s.key, body) {
			_, _ = w.Write([]byte(`{"error_code":3,"error_msg":"Signature is incorrect"}`))
			return
		}
		if custom != "" {
			_, _ = w.Write([]byte(custom))
			return
		}
		switch show {
		case methodGetPayment:
			_, _ = w.Write([]byte(`{"payment_url":"https://allpay.to/~` + s.login + `/pay/?payment_id=abc123&code=xyz"}`))
		case methodCheckKeys:
			_, _ = w.Write([]byte(`{"last_paid_order_id":"-1","last_paid_order_date":"-1"}`))
		case methodPaymentStatus:
			_, _ = w.Write([]byte(`{"order_id":"o-1","status":4,"amount":100.00,"refunded":30.5,"currency":"ILS"}`))
		default:
			_, _ = w.Write([]byte(`{"error_code":20,"error_msg":"Incorrect method"}`))
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubAllPay) last(t *testing.T) stubRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("stub allpay received no requests")
	}
	return s.requests[len(s.requests)-1]
}

func newTestAdapter(stub *stubAllPay, cfg Config) *Adapter {
	if cfg.Login == "" {
		cfg.Login = stub.login
	}
	if cfg.APIKey == "" {
		cfg.APIKey = stub.key
	}
	cfg.BaseURL = stub.server.URL + "/app"
	a := New(cfg)
	a.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return a
}

func TestCreateCheckoutSession_WhatAllPayIsAskedFor(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	a := newTestAdapter(stub, Config{TestMode: true, MaxInstallments: 6, VAT: VATFull})

	resp, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{
		Amount:            12350,
		Currency:          "ils",
		ProductName:       "Concert",
		CustomerEmail:     "buyer@example.com",
		ClientReferenceID: "6f1c0f6e-0000-4000-8000-000000000001",
		SuccessURL:        "https://api.example/v1/public/payment-return?r=x",
		CancelURL:         "https://api.example/v1/public/payment-return?r=x",
		ExpiresAtUnix:     1_800_001_860,
		Locale:            "he-IL",
		CallbackURL:       "https://api.example/v1/payment-intents/webhook/cfg",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if resp.SessionID != "6f1c0f6e-0000-4000-8000-000000000001" || !strings.HasPrefix(resp.URL, "https://allpay.to/") {
		t.Fatalf("response = %+v; want the order id as session and the payment_url", resp)
	}
	if resp.ExpiresAtUnix != 1_800_001_860 {
		t.Errorf("ExpiresAtUnix = %d", resp.ExpiresAtUnix)
	}
	req := stub.last(t)
	if req.Show != "getpayment" || req.Mode != APIVersion {
		t.Fatalf("called show=%s mode=%s", req.Show, req.Mode)
	}
	b := req.Body
	want := map[string]string{
		"login": "pp100", "order_id": "6f1c0f6e-0000-4000-8000-000000000001", "currency": "ILS",
		"webhook_url": "https://api.example/v1/payment-intents/webhook/cfg", "lang": "HE",
		"success_url": "https://api.example/v1/public/payment-return?r=x", "backlink_url": "https://api.example/v1/public/payment-return?r=x",
		"client_email": "buyer@example.com", "expire": "1800001860", "test_mode": "1", "inst": "6",
	}
	for k, v := range want {
		if got, _ := scalarValue(b[k]); got != v {
			t.Errorf("%s = %q; want %q", k, got, v)
		}
	}
	items, _ := b["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v; want one line for the whole cart", b["items"])
	}
	item, _ := items[0].(map[string]any)
	if p, _ := scalarValue(item["price"]); p != "123.5" {
		t.Errorf("price = %q; want 123.5 (major units)", p)
	}
	if q, _ := scalarValue(item["qty"]); q != "1" {
		t.Errorf("qty = %q; want 1", q)
	}
	if v, _ := scalarValue(item["vat"]); v != "1" {
		t.Errorf("vat = %q; want the configured 1", v)
	}
}

func TestCreateCheckoutSession_Defaults(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	a := newTestAdapter(stub, Config{MaxInstallments: 6})
	_, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{
		Amount: 5000, Currency: "USD", ClientReferenceID: "o-2", Locale: "cs",
		ExpiresAtUnix: 1_800_000_010, // too close: raised to a minute
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	b := stub.last(t).Body
	for _, absent := range []string{"test_mode", "lang", "inst", "webhook_url", "client_email"} {
		if _, has := b[absent]; has {
			t.Errorf("%s was sent; want it left out (live config / unknown language / installments only in ILS / not given)", absent)
		}
	}
	if exp, _ := scalarValue(b["expire"]); exp != "1800000060" {
		t.Errorf("expire = %s; want now + 60 s", exp)
	}
	item := b["items"].([]any)[0].(map[string]any)
	if v, _ := scalarValue(item["vat"]); v != VATNone {
		t.Errorf("default vat = %q; want %q (as the WooCommerce plugin without taxes)", v, VATNone)
	}
	if n, _ := scalarValue(item["name"]); n != "Tickets" {
		t.Errorf("default name = %q", n)
	}
}

func TestCreateCheckoutSession_RefusesBeforeAnyCall(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	cases := []struct {
		name string
		cfg  Config
		req  payments.CreateHostedCheckoutRequest
	}{
		{"currency AllPay cannot charge in", Config{}, payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "CZK", ClientReferenceID: "o"}},
		{"no order id", Config{}, payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "ILS"}},
		{"zero amount", Config{}, payments.CreateHostedCheckoutRequest{Amount: 0, Currency: "ILS", ClientReferenceID: "o"}},
		{"bad vat code", Config{VAT: "2"}, payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "ILS", ClientReferenceID: "o"}},
	}
	for _, c := range cases {
		a := newTestAdapter(stub, c.cfg)
		if _, err := a.CreateCheckoutSession(t.Context(), c.req); err == nil {
			t.Errorf("%s: no error", c.name)
		}
	}
	if n := len(stub.requests); n != 0 {
		t.Fatalf("stub received %d requests; every case must be refused before the network", n)
	}
	a := newTestAdapter(stub, Config{})
	_, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "CZK", ClientReferenceID: "o"})
	if !errors.Is(err, ErrUnsupportedCurrency) {
		t.Errorf("CZK = %v; want ErrUnsupportedCurrency", err)
	}
}

func TestCreateCheckoutSession_AllPayRefusal(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	stub.answer[methodGetPayment] = `{"error_code":21,"error_msg":"Amount must be at least 5 ILS"}`
	a := newTestAdapter(stub, Config{})
	_, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{Amount: 100, Currency: "ILS", ClientReferenceID: "o"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != ErrCodeBelowMinimum {
		t.Fatalf("err = %v; want APIError 21", err)
	}
	// A wrong key is AllPay's error 3.
	a = newTestAdapter(stub, Config{APIKey: "WRONG"})
	_, err = a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{Amount: 1000, Currency: "ILS", ClientReferenceID: "o"})
	if !errors.As(err, &apiErr) || apiErr.Code != ErrCodeBadSignature {
		t.Fatalf("wrong key = %v; want APIError 3", err)
	}
}

func TestVerifyCredentials(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	if err := newTestAdapter(stub, Config{}).VerifyCredentials(t.Context()); err != nil {
		t.Fatalf("good pair = %v; want nil", err)
	}
	if got := stub.last(t); got.Show != "checkkeys" || len(got.Body) != 2 {
		t.Errorf("checkkeys body = %v; want only login and sign", got.Body)
	}
	if err := newTestAdapter(stub, Config{APIKey: "WRONG"}).VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrCredentialRefused) {
		t.Errorf("wrong key = %v; want ErrCredentialRefused", err)
	}
	if err := newTestAdapter(stub, Config{Login: "nobody"}).VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrCredentialRefused) {
		t.Errorf("unknown login = %v; want ErrCredentialRefused", err)
	}
	stub.answer[methodCheckKeys] = `{"error_code":20,"error_msg":"Incorrect method"}`
	if err := newTestAdapter(stub, Config{}).VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("unexpected code = %v; want ErrProviderUnreachable", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	a := New(Config{Login: "pp100", APIKey: "KEY", BaseURL: down.URL})
	if err := a.VerifyCredentials(t.Context()); !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Errorf("502 = %v; want ErrProviderUnreachable", err)
	}
}

func TestPaymentStatus(t *testing.T) {
	stub := newStubAllPay(t, "pp100", "KEY")
	st, err := newTestAdapter(stub, Config{}).PaymentStatus(t.Context(), "o-1")
	if err != nil {
		t.Fatalf("PaymentStatus: %v", err)
	}
	if st.Status != StatusPartiallyRefunded || st.AmountMinor != 10000 || st.RefundedMinor != 3050 || st.Currency != "ILS" {
		t.Fatalf("status = %+v", st)
	}
	stub.answer[methodPaymentStatus] = `{"error_code":6,"error_msg":"Order not found"}`
	if _, err := newTestAdapter(stub, Config{}).PaymentStatus(t.Context(), "o-x"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("unknown order = %v; want ErrOrderNotFound", err)
	}
}

// TestPost_SignsWhatItSends: the body on the wire verifies under the key, so
// what AllPay recomputes from the JSON it received is what arena signed.
func TestPost_SignsWhatItSends(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"payment_url":"https://allpay.to/x"}`))
	}))
	defer srv.Close()
	a := New(Config{Login: "pp100", APIKey: "KEY", BaseURL: srv.URL})
	if _, err := a.CreateCheckoutSession(t.Context(), payments.CreateHostedCheckoutRequest{
		Amount: 9990, Currency: "EUR", ProductName: "Шоу: «עברית»", ClientReferenceID: "o-3",
	}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m["sign"] != Sign("KEY", m) {
		t.Fatalf("the body on the wire does not verify: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"price":99.9`)) {
		t.Errorf("price on the wire = %s; want the JSON number 99.9", raw)
	}
}
