package stripe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func TestReadCheckoutSessionPayment(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus string
		wantPI     string
		inProgress bool
		wantErr    bool
	}{
		{"no payment yet", `{"id":"cs_1","status":"open","payment_intent":null}`, payments.EmbeddedSessionOpen, "", false, false},
		{"3-D Secure", `{"id":"cs_1","status":"open","payment_intent":{"id":"pi_1","status":"requires_action"}}`, payments.EmbeddedSessionOpen, "requires_action", true, false},
		{"processing", `{"id":"cs_1","status":"open","payment_intent":{"id":"pi_1","status":"processing"}}`, payments.EmbeddedSessionOpen, "processing", true, false},
		{"paid", `{"id":"cs_1","status":"complete","payment_intent":{"id":"pi_1","status":"succeeded"}}`, payments.EmbeddedSessionComplete, "succeeded", false, false},
		{"expired", `{"id":"cs_1","status":"expired"}`, payments.EmbeddedSessionExpired, "", false, false},
		{"not expanded", `{"id":"cs_1","status":"open","payment_intent":"pi_1"}`, "", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotQuery, gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery, gotMethod = r.URL.Path, r.URL.RawQuery, r.Method
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
			got, err := a.ReadCheckoutSessionPayment(context.Background(), "cs_1")
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error for an unexpanded payment_intent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != http.MethodGet || gotPath != "/v1/checkout/sessions/cs_1" || gotQuery != "expand%5B%5D=payment_intent" {
				t.Fatalf("call = %s %s?%s", gotMethod, gotPath, gotQuery)
			}
			if got.Status != tc.wantStatus || got.PaymentStatus != tc.wantPI || got.PaymentInProgress() != tc.inProgress {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestReadCheckoutSessionPayment_ServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	a := New(Config{SecretKey: "sk_test_abc", BaseURL: srv.URL + "/v1"})
	if _, err := a.ReadCheckoutSessionPayment(context.Background(), "cs_1"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := a.ReadCheckoutSessionPayment(context.Background(), "pi_1"); err == nil {
		t.Fatal("want an error for a non-session id")
	}
}
