package stripe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// probeStub answers POST /v1/tokens with postStatus (a pii_ id on 200) and
// GET /v1/tokens/pii_stub with getStatus (resource_missing body on 404).
func probeStub(t *testing.T, postStatus, getStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Method+" "+r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tokens":
			if err := r.ParseForm(); err != nil || r.PostForm.Get("pii[id_number]") != "000000000" {
				t.Errorf("unexpected form %v", r.PostForm)
			}
			if postStatus == http.StatusOK {
				_, _ = w.Write([]byte(`{"id":"pii_stub","object":"token","type":"pii"}`))
				return
			}
			w.WriteHeader(postStatus)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tokens/pii_stub":
			if getStatus == http.StatusNotFound {
				w.WriteHeader(getStatus)
				_, _ = w.Write([]byte(`{"error":{"code":"resource_missing","type":"invalid_request_error"}}`))
				return
			}
			w.WriteHeader(getStatus)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &auths
}

func TestProbePublishableKey_Verdicts(t *testing.T) {
	cases := []struct {
		name            string
		post, get       int
		sk              string
		refused, mismat bool
	}{
		{"same account", 200, 200, "sk_test_a", false, false},
		{"mismatch", 200, 404, "sk_test_a", false, true},
		{"401 on post", 401, 200, "sk_test_a", true, false},
		{"restricted key 403", 200, 403, "rk_test_a", false, false},
		{"5xx on get", 200, 500, "sk_test_a", false, false},
		{"403 on post", 403, 404, "sk_test_a", false, false},
		{"400 on post", 400, 404, "sk_test_a", false, false},
		{"no secret key", 200, 404, "", false, false},
	}
	for _, c := range cases {
		srv, auths := probeStub(t, c.post, c.get)
		err := ProbePublishableKey(context.Background(), srv.Client(), srv.URL+"/v1", "pk_test_abc", c.sk)
		if got := errors.Is(err, payments.ErrCredentialRefused); got != c.refused {
			t.Errorf("%s: refused=%v want %v (%v)", c.name, got, c.refused, err)
		}
		if got := errors.Is(err, payments.ErrBrowserKeyAccountMismatch); got != c.mismat {
			t.Errorf("%s: mismatch=%v want %v (%v)", c.name, got, c.mismat, err)
		}
		if err != nil && (strings.Contains(err.Error(), "pk_test_abc") || strings.Contains(err.Error(), "sk_test_a")) {
			t.Errorf("%s: error leaks a key: %v", c.name, err)
		}
		if len(*auths) == 0 || (*auths)[0] != "POST Bearer pk_test_abc" {
			t.Errorf("%s: first call = %v", c.name, *auths)
		}
		if c.name == "same account" && (len(*auths) != 2 || (*auths)[1] != "GET Bearer sk_test_a") {
			t.Errorf("same account: calls = %v", *auths)
		}
		if c.name == "no secret key" && len(*auths) != 1 {
			t.Errorf("no secret key: second call made: %v", *auths)
		}
	}
}

func TestProbePublishableKey_TimeoutAndNetworkErrorDoNotRefuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ProbePublishableKey(ctx, srv.Client(), srv.URL+"/v1", "pk_test_abc", "sk_test_a"); err != nil {
		t.Errorf("timeout refused: %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/v1"
	dead.Close()
	if err := ProbePublishableKey(context.Background(), nil, url, "pk_test_abc", "sk_test_a"); err != nil {
		t.Errorf("network error refused: %v", err)
	}
}

func TestProbePublishableKey_SecondStepTimeoutDoesNotBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"pii_stub"}`))
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := ProbePublishableKey(ctx, srv.Client(), srv.URL+"/v1", "pk_test_abc", "sk_test_a"); err != nil {
		t.Errorf("step 2 timeout blocked: %v", err)
	}
}
