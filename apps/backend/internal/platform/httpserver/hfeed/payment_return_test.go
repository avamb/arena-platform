package hfeed

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func returnHandler(allowed []string, fallback string) *Handler {
	return &Handler{returnURLPolicy: ReturnURLPolicy{AllowedOrigins: allowed, Fallback: fallback}}
}

const testToken = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func doReturn(h *Handler, method, rawQuery string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, PaymentReturnPath+"?"+rawQuery, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.HandlePaymentReturn(rec, req)
	return rec
}

func TestPaymentReturnURL_RoundTripsThroughTheHandler(t *testing.T) {
	built := PaymentReturnURL("https://api.example.test/", "https://site.example/tickets", testToken)
	if !strings.HasPrefix(built, "https://api.example.test"+PaymentReturnPath+"?") {
		t.Fatalf("built = %s", built)
	}
	u, _ := url.Parse(built)
	h := returnHandler([]string{"https://site.example"}, "")
	rec := doReturn(h, http.MethodGet, u.RawQuery, "")
	want := "https://site.example/tickets?checkout_token=" + testToken
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Fatalf("got %d %q, want 303 %q", rec.Code, rec.Header().Get("Location"), want)
	}
}

func TestPaymentReturnURL_NoAPIBaseGivesTheDirectBuyerURL(t *testing.T) {
	got := PaymentReturnURL("", "https://site.example/tickets", testToken)
	if want := "https://site.example/tickets?checkout_token=" + testToken; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// The whole point: a provider that POSTs the buyer back must still land them
// on a page, because the browser follows a 303 with GET.
func TestHandlePaymentReturn_PostIsAnsweredWithSeeOtherAndTheBodyIsIgnored(t *testing.T) {
	h := returnHandler([]string{"https://site.example"}, "")
	q := url.Values{"r": {"https://site.example/tickets"}, "checkout_token": {testToken}}.Encode()
	rec := doReturn(h, http.MethodPost, q, "order_status=approved&order_id=whatever&signature=x")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), "https://site.example/tickets?checkout_token="+testToken; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("token-bearing redirect must be no-store / no-referrer, got %v", rec.Header())
	}
}

func TestHandlePaymentReturn_ForeignOriginFallsBackNeverRedirectsThere(t *testing.T) {
	h := returnHandler([]string{"https://site.example"}, "https://tickets.example")
	q := url.Values{"r": {"https://evil.example/phish"}, "checkout_token": {testToken}}.Encode()
	rec := doReturn(h, http.MethodGet, q, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "evil.example") || loc != "https://tickets.example?checkout_token="+testToken {
		t.Fatalf("Location = %q; an origin outside the allow-list must be replaced by the fallback", loc)
	}
}

func TestHandlePaymentReturn_ForeignOriginWithNoFallbackIs400(t *testing.T) {
	h := returnHandler([]string{"https://site.example"}, "")
	q := url.Values{"r": {"https://evil.example/"}, "checkout_token": {testToken}}.Encode()
	if rec := doReturn(h, http.MethodGet, q, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestHandlePaymentReturn_OnlyOurTokenIsEchoedAndExtraQueryIsDropped(t *testing.T) {
	h := returnHandler([]string{"https://site.example"}, "")
	// The candidate carries its own query and a fragment; neither may survive.
	q := url.Values{"r": {"https://site.example/t?inject=1#frag"}, "checkout_token": {testToken}}.Encode()
	rec := doReturn(h, http.MethodGet, q, "")
	if got, want := rec.Header().Get("Location"), "https://site.example/t?checkout_token="+testToken; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestHandlePaymentReturn_RejectsMissingOrMalformedToken(t *testing.T) {
	h := returnHandler([]string{"https://site.example"}, "")
	for _, tok := range []string{"", "has space", "a/../b", "x&y=1", strings.Repeat("a", 129), "ключ"} {
		q := url.Values{"r": {"https://site.example/"}, "checkout_token": {tok}}.Encode()
		if rec := doReturn(h, http.MethodGet, q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("token %q = %d, want 400", tok, rec.Code)
		}
	}
}
