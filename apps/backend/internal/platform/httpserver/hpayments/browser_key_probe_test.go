package hpayments

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newProbeHandler(base string) *Handler {
	h := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h.WithStripeBaseURL(base)
}

func TestRejectUnknownBrowserKey(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer pk_test_bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	h := newProbeHandler(srv.URL + "/v1")

	do := func(patch map[string]string) (bool, int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		refused := h.rejectUnknownBrowserKey(rec, req, "stripe", "test", patch)
		return refused, rec.Code, rec.Body.String()
	}

	refused, code, body := do(map[string]string{"publishable_key": "pk_test_bad"})
	if !refused || code != http.StatusUnprocessableEntity {
		t.Fatalf("bad key: refused=%v code=%d", refused, code)
	}
	if !strings.Contains(body, "payment_config.publishable_key_refused") || strings.Contains(body, "pk_test_bad") {
		t.Errorf("unexpected body: %s", body)
	}

	if refused, _, _ := do(map[string]string{"publishable_key": "pk_test_good"}); refused {
		t.Error("a key Stripe knows was refused")
	}

	before := calls
	if refused, _, _ := do(map[string]string{"api_key": "sk_test_x"}); refused || calls != before {
		t.Error("a patch without a publishable key must not call Stripe")
	}
	if refused, _, _ := do(map[string]string{"publishable_key": ""}); refused || calls != before {
		t.Error("deleting the publishable key must not call Stripe")
	}
	rec := httptest.NewRecorder()
	if h.rejectUnknownBrowserKey(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "flitt", "test", map[string]string{"publishable_key": "x"}) {
		t.Error("a provider without a browser key was refused")
	}
}

func TestPatchTouchesCredential(t *testing.T) {
	if patchTouchesCredential("stripe", map[string]string{"publishable_key": "pk_test_x"}) {
		t.Fatal("a publishable-key-only patch must not re-verify the secret key")
	}
	if !patchTouchesCredential("stripe", map[string]string{"api_key": "sk_test_x"}) {
		t.Fatal("an api_key patch must re-verify")
	}
	if !patchTouchesCredential("stripe", map[string]string{"api_key": ""}) {
		t.Fatal("deleting api_key must re-verify")
	}
	if !patchTouchesCredential("no-such-provider", map[string]string{"x": "y"}) {
		t.Fatal("an unknown provider keeps the old always-verify behaviour")
	}
}
