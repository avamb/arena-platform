package stripe

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func TestCreateCheckoutSession_ReceiptEmailFollowsTheRequest(t *testing.T) {
	stub := newStripeStub(t, http.StatusOK, hostedOKBody)
	req := sampleHostedRequest()
	if _, err := stub.adapter().CreateCheckoutSession(t.Context(), req); err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if _, ok := stub.lastForm["payment_intent_data[receipt_email]"]; ok {
		t.Fatalf("receipt_email sent without ReceiptEmail: %v", stub.lastForm)
	}
	req.ReceiptEmail = "buyer@example.com"
	if _, err := stub.adapter().CreateCheckoutSession(t.Context(), req); err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if got := stub.lastForm.Get("payment_intent_data[receipt_email]"); got != "buyer@example.com" {
		t.Fatalf("payment_intent_data[receipt_email] = %q", got)
	}
}

// receiptStub answers the payment intent read and serves the receipt pages.
func receiptStub(t *testing.T, pdf bool, charge string) (*httptest.Server, *string) {
	t.Helper()
	var auth string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/payment_intents/pi_1"):
			if r.URL.Query().Get("expand[]") != "latest_charge" {
				t.Errorf("latest_charge not expanded: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"pi_1","latest_charge":` + strings.ReplaceAll(charge, "{BASE}", srv.URL) + `}`))
		case r.URL.Path == "/receipts/payment/abc/pdf":
			auth = r.Header.Get("Authorization")
			if !pdf {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 receipt"))
		case r.URL.Path == "/receipts/payment/abc":
			auth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html>receipt</html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestFetchReceipt_KeepsThePDF(t *testing.T) {
	srv, auth := receiptStub(t, true, `{"id":"ch_1","receipt_url":"{BASE}/receipts/payment/abc?s=ap","receipt_number":"1234-5678"}`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	rec, err := a.FetchReceipt(t.Context(), "pi_1")
	if err != nil {
		t.Fatalf("FetchReceipt: %v", err)
	}
	if rec.Number != "1234-5678" || !strings.HasSuffix(rec.URL, "/receipts/payment/abc?s=ap") {
		t.Fatalf("receipt = %+v", rec)
	}
	if rec.ContentType != "application/pdf" || string(rec.File) != "%PDF-1.4 receipt" {
		t.Fatalf("file = %q (%s)", rec.File, rec.ContentType)
	}
	if *auth != "" {
		t.Fatalf("the API key was sent to the receipt page: %q", *auth)
	}
}

func TestFetchReceipt_FallsBackToTheHTMLPage(t *testing.T) {
	srv, _ := receiptStub(t, false, `{"id":"ch_1","receipt_url":"{BASE}/receipts/payment/abc?s=ap"}`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	rec, err := a.FetchReceipt(t.Context(), "pi_1")
	if err != nil {
		t.Fatalf("FetchReceipt: %v", err)
	}
	if !strings.HasPrefix(rec.ContentType, "text/html") || string(rec.File) != "<html>receipt</html>" {
		t.Fatalf("file = %q (%s)", rec.File, rec.ContentType)
	}
}

func TestFetchReceipt_NoChargeYetIsNotReady(t *testing.T) {
	srv, _ := receiptStub(t, true, `null`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	if _, err := a.FetchReceipt(t.Context(), "pi_1"); !errors.Is(err, payments.ErrReceiptNotReady) {
		t.Fatalf("err = %v; want ErrReceiptNotReady", err)
	}
}

func TestFetchReceipt_ForeignHostIsNotDownloaded(t *testing.T) {
	srv, _ := receiptStub(t, true, `{"id":"ch_1","receipt_url":"https://evil.example.com/receipts/payment/abc"}`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	rec, err := a.FetchReceipt(t.Context(), "pi_1")
	if err != nil {
		t.Fatalf("FetchReceipt: %v", err)
	}
	if rec.File != nil || rec.URL == "" {
		t.Fatalf("receipt = %+v; want the link only", rec)
	}
}

func TestReceiptURLAllowed_ProductionHostOnly(t *testing.T) {
	a := New(Config{SecretKey: "sk_live_x"})
	for raw, want := range map[string]bool{
		"https://pay.stripe.com/receipts/payment/abc?s=ap": true,
		"http://pay.stripe.com/receipts/payment/abc":       false,
		"https://pay.stripe.com.evil.example/receipts":     false,
		"https://user@pay.stripe.com/receipts":             false,
		"https://api.stripe.com/v1/charges":                false,
	} {
		if got := a.receiptURLAllowed(raw); got != want {
			t.Errorf("receiptURLAllowed(%q) = %v; want %v", raw, got, want)
		}
	}
}

func TestFetchReceipt_FailedDownloadKeepsTheLinkAndAsksForARetry(t *testing.T) {
	srv, _ := receiptStub(t, true, `{"id":"ch_1","receipt_url":"{BASE}/receipts/payment/gone","receipt_number":"77"}`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	rec, err := a.FetchReceipt(t.Context(), "pi_1")
	if !errors.Is(err, payments.ErrReceiptDownload) {
		t.Fatalf("err = %v; want ErrReceiptDownload", err)
	}
	if rec.File != nil || rec.Number != "77" || !strings.HasSuffix(rec.URL, "/receipts/payment/gone") {
		t.Fatalf("receipt = %+v; want the link and number without a file", rec)
	}
}

func TestFetchReceipt_UnknownPaymentIsNotFound(t *testing.T) {
	srv, _ := receiptStub(t, true, `null`)
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	if _, err := a.FetchReceipt(t.Context(), "pi_unknown"); !errors.Is(err, payments.ErrReceiptNotFound) {
		t.Fatalf("err = %v; want ErrReceiptNotFound", err)
	}
}

func TestReceiptRedirectAllowed_StripeHostsOverHTTPSOnly(t *testing.T) {
	a := New(Config{SecretKey: "sk_live_x"})
	for raw, want := range map[string]bool{
		"https://pay.stripe.com/receipts/x":         true,
		"https://files.stripe.com/x":                true,
		"http://files.stripe.com/x":                 false,
		"https://stripe.com.evil.example/x":         false,
		"https://evilstripe.com/x":                  false,
		"https://user@files.stripe.com/x":           false,
		"https://169.254.169.254/latest/meta-data/": false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.receiptRedirectAllowed(u); got != want {
			t.Errorf("receiptRedirectAllowed(%q) = %v; want %v", raw, got, want)
		}
	}
}
