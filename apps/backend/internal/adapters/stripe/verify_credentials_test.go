package stripe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// The whole value of the check is that these two outcomes are told apart: a
// refusal is a verdict on the KEY and belongs on screen in red, while an
// unreachable provider is a verdict on the trip and must never be recorded
// as a bad key.
func TestVerifyCredentials_ClassifiesStripesAnswer(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantErr  error
		wantText string
	}{
		{
			name:   "accepted",
			status: http.StatusOK,
			body:   `{"object":"balance","livemode":false}`,
		},
		{
			name:     "rejected key",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"type":"invalid_request_error","message":"Invalid API Key provided: sk_test_****abcd"}}`,
			wantErr:  payments.ErrCredentialRefused,
			wantText: "Invalid API Key provided",
		},
		{
			name:    "key valid but not permitted",
			status:  http.StatusForbidden,
			body:    `{"error":{"type":"invalid_request_error","message":"The provided key does not have the required permissions."}}`,
			wantErr: payments.ErrCredentialRefused,
		},
		{
			// Stripe having a bad day says nothing about the organization's
			// key, so it must not paint a working credential red.
			name:    "provider is down",
			status:  http.StatusInternalServerError,
			body:    `{"error":{"type":"api_error","message":"internal"}}`,
			wantErr: payments.ErrProviderUnreachable,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			a := New(Config{SecretKey: "sk_test_whatever", BaseURL: srv.URL + "/v1"})
			err := a.VerifyCredentials(context.Background())

			// Read-only by construction: the check must never be able to move
			// money or create anything.
			if gotPath != "/v1/balance" {
				t.Errorf("called %q, want the read-only /v1/balance", gotPath)
			}
			if gotAuth != "Bearer sk_test_whatever" {
				t.Errorf("Authorization = %q, want the configured key", gotAuth)
			}

			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("an accepted key returned %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, c.wantErr)
			}
			if c.wantText != "" && !strings.Contains(err.Error(), c.wantText) {
				t.Errorf("the provider's own wording must survive for the operator: %v", err)
			}
		})
	}
}

// A dead endpoint is the same class of answer as a 5xx: we could not ask.
func TestVerifyCredentials_UnreachableProviderIsNotABadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	a := New(Config{SecretKey: "sk_test_whatever", BaseURL: url + "/v1"})
	err := a.VerifyCredentials(context.Background())
	if !errors.Is(err, payments.ErrProviderUnreachable) {
		t.Fatalf("err = %v, want ErrProviderUnreachable", err)
	}
}
