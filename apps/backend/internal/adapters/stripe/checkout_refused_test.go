package stripe

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// TestCreateCheckoutSession_RefusalIsTyped proves a definitive Stripe refusal
// of a Checkout Session surfaces as payments.HostedCheckoutRefusedError, with
// email_invalid named as the invalid-e-mail reason (F-BR-03), and that an
// unknown outcome (5xx, 429, a 4xx without Stripe's body) does not.
func TestCreateCheckoutSession_RefusalIsTyped(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantTyped  bool
		wantReason string
		wantCode   string
	}{
		{"email_invalid", http.StatusBadRequest,
			`{"error":{"type":"invalid_request_error","code":"email_invalid","message":"Invalid email address: x@y.comRUN1109-FC FC-0","param":"customer_email"}}`,
			true, payments.HostedCheckoutRefusalInvalidEmail, "email_invalid"},
		{"other 400", http.StatusBadRequest,
			`{"error":{"type":"invalid_request_error","code":"parameter_invalid_integer","message":"unit_amount must be an integer"}}`,
			true, "", "parameter_invalid_integer"},
		{"401 bad key", http.StatusUnauthorized,
			`{"error":{"type":"invalid_request_error","message":"Invalid API Key provided"}}`,
			true, "", "invalid_request_error"},
		{"500", http.StatusInternalServerError,
			`{"error":{"type":"api_error","message":"boom"}}`, false, "", ""},
		{"429", http.StatusTooManyRequests,
			`{"error":{"type":"invalid_request_error","code":"rate_limit","message":"slow down"}}`, false, "", ""},
		{"409", http.StatusConflict,
			`{"error":{"type":"idempotency_error","message":"in flight"}}`, false, "", ""},
		{"4xx without stripe body", http.StatusForbidden, `forbidden`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := newStripeStub(t, c.status, c.body)
			_, err := stub.adapter().CreateCheckoutSession(context.Background(), sampleHostedRequest())
			if err == nil {
				t.Fatal("want an error")
			}
			var refused *payments.HostedCheckoutRefusedError
			typed := errors.As(err, &refused)
			if typed != c.wantTyped {
				t.Fatalf("typed refusal = %v, want %v (err %v)", typed, c.wantTyped, err)
			}
			if !typed {
				return
			}
			if refused.Reason != c.wantReason || refused.Code != c.wantCode {
				t.Errorf("reason %q code %q, want %q %q", refused.Reason, refused.Code, c.wantReason, c.wantCode)
			}
		})
	}
}
