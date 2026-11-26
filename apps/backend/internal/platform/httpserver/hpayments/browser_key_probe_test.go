package hpayments

import (
	"encoding/json"
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
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "Bearer pk_test_bad":
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"pii_stub"}`))
		case auth == "Bearer sk_test_other":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"resource_missing"}}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	h := newProbeHandler(srv.URL + "/v1")

	do := func(patch map[string]string, stored string) (bool, int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		var raw json.RawMessage
		if stored != "" {
			raw = json.RawMessage(stored)
		}
		refused := h.rejectUnknownBrowserKey(rec, req, "stripe", "test", patch, raw)
		return refused, rec.Code, rec.Body.String()
	}

	refused, code, body := do(map[string]string{"publishable_key": "pk_test_bad"}, "")
	if !refused || code != http.StatusUnprocessableEntity {
		t.Fatalf("bad key: refused=%v code=%d", refused, code)
	}
	if !strings.Contains(body, "payment_config.publishable_key_refused") || strings.Contains(body, "pk_test_bad") {
		t.Errorf("unexpected body: %s", body)
	}

	if refused, _, _ := do(map[string]string{"publishable_key": "pk_test_good"}, ""); refused {
		t.Error("a key Stripe knows was refused")
	}
	if refused, _, _ := do(map[string]string{"publishable_key": "pk_test_good"}, `{"api_key":"sk_test_same"}`); refused {
		t.Error("same-account keys were refused")
	}

	// Mismatch: the secret key comes from the stored row on a PATCH.
	refused, code, body = do(map[string]string{"publishable_key": "pk_test_good"}, `{"api_key":"sk_test_other"}`)
	if !refused || code != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "payment_config.publishable_key_account_mismatch") ||
		strings.Contains(body, "sk_test_other") || strings.Contains(body, "pk_test_good") {
		t.Errorf("stored mismatch: refused=%v code=%d body=%s", refused, code, body)
	}
	// ... and from the patch itself on a create.
	if refused, _, body := do(map[string]string{"publishable_key": "pk_test_good", "api_key": "sk_test_other"}, ""); !refused ||
		!strings.Contains(body, "payment_config.publishable_key_account_mismatch") {
		t.Errorf("patch mismatch: refused=%v body=%s", refused, body)
	}

	before := calls
	if refused, _, _ := do(map[string]string{"api_key": "sk_test_x"}, ""); refused || calls != before {
		t.Error("a patch without a publishable key anywhere must not call Stripe")
	}
	// A new secret key of another account against a stored publishable key.
	if refused, _, body := do(map[string]string{"api_key": "sk_test_other"}, `{"publishable_key":"pk_test_good","api_key":"sk_test_same"}`); !refused ||
		!strings.Contains(body, "payment_config.publishable_key_account_mismatch") {
		t.Errorf("new sk vs stored pk: refused=%v body=%s", refused, body)
	}
	before = calls
	if refused, _, _ := do(map[string]string{"api_key": ""}, `{"publishable_key":"pk_test_bad","api_key":"sk_test_same"}`); refused || calls != before {
		t.Error("deleting the secret key must not probe the stored publishable key")
	}
	if refused, _, _ := do(map[string]string{"publishable_key": ""}, ""); refused || calls != before {
		t.Error("deleting the publishable key must not call Stripe")
	}
	rec := httptest.NewRecorder()
	if h.rejectUnknownBrowserKey(rec, httptest.NewRequest(http.MethodPost, "/x", nil), "flitt", "test", map[string]string{"publishable_key": "x"}, nil) {
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
