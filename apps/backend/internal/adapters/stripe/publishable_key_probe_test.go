package stripe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func probeStub(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/tokens/tok_arena_pk_probe" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotAuth
}

func TestProbePublishableKey_OnlyUnauthorizedRefuses(t *testing.T) {
	cases := []struct {
		status  int
		refused bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusNotFound, false},
		{http.StatusForbidden, false},
		{http.StatusBadRequest, false},
		{http.StatusInternalServerError, false},
	}
	for _, c := range cases {
		srv, auth := probeStub(t, c.status)
		err := ProbePublishableKey(context.Background(), srv.Client(), srv.URL+"/v1", "pk_test_abc")
		if got := errors.Is(err, payments.ErrCredentialRefused); got != c.refused {
			t.Errorf("status %d: refused=%v want %v (err=%v)", c.status, got, c.refused, err)
		}
		if *auth != "Bearer pk_test_abc" {
			t.Errorf("status %d: Authorization = %q", c.status, *auth)
		}
		if err != nil && containsKey(err.Error(), "pk_test_abc") {
			t.Errorf("error leaks the key: %v", err)
		}
	}
}

func containsKey(s, key string) bool {
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] == key {
			return true
		}
	}
	return false
}

func TestProbePublishableKey_TimeoutAndNetworkErrorDoNotRefuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ProbePublishableKey(ctx, srv.Client(), srv.URL+"/v1", "pk_test_abc"); err != nil {
		t.Errorf("timeout refused: %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/v1"
	dead.Close()
	if err := ProbePublishableKey(context.Background(), nil, url, "pk_test_abc"); err != nil {
		t.Errorf("network error refused: %v", err)
	}
}
