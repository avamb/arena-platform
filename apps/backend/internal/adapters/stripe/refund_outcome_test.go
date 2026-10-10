// refund_outcome_test.go — PAY-03: the refund engine must tell "Stripe
// refused, no money moved" (fail the refund, keep the tickets) from "we do
// not know" (keep it pending, retry with the same idempotency key), and must
// be able to reach the pi_… behind a hosted session whose webhook never
// stored it.
package stripe_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripe"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

func newRefundAdapter(t *testing.T, h http.HandlerFunc) *stripe.Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return stripe.New(stripe.Config{SecretKey: "sk_test_x", BaseURL: srv.URL + "/v1"})
}

func refundReq() payments.RefundRequest {
	return payments.RefundRequest{ProviderChargeRef: "pi_1", AmountMinor: 500, PaymentAmountMinor: 1000, Currency: "EUR", IdempotencyKey: "k"}
}

// TestStripeRefund_DefinitiveRefusalIsDeclined: a 4xx carrying a parsed
// Stripe error object is a refusal — and "already refunded" is one a human
// must check (NeedsReview), never a plain failure (PAY-03 review M2).
func TestStripeRefund_DefinitiveRefusalIsDeclined(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusPaymentRequired, http.StatusNotFound, http.StatusUnauthorized} {
		a := newRefundAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"amount_too_large","message":"Refund amount is greater than unrefunded amount."}}`)
		})
		_, err := a.Refund(context.Background(), refundReq())
		code, msg, declined := payments.RefundDeclined(err)
		if !declined || code != "amount_too_large" || !strings.Contains(msg, "greater than") {
			t.Errorf("status %d: declined=%v code=%q msg=%q err=%v", status, declined, code, msg, err)
		}
		if payments.RefundDeclineNeedsReview(err) {
			t.Errorf("status %d: a plain refusal was flagged for review", status)
		}
	}
}

func TestStripeRefund_AlreadyRefundedNeedsReview(t *testing.T) {
	a := newRefundAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"charge_already_refunded","message":"Charge has already been refunded."}}`)
	})
	_, err := a.Refund(context.Background(), refundReq())
	if code, _, declined := payments.RefundDeclined(err); !declined || code != "charge_already_refunded" {
		t.Fatalf("declined=%v code=%q err=%v", declined, code, err)
	}
	if !payments.RefundDeclineNeedsReview(err) {
		t.Fatalf("charge_already_refunded must need review: %v", err)
	}
}

// TestStripeRefund_UnknownOutcomes: anything that does not PROVE Stripe
// refused is unknown — the engine retries with the same idempotency key.
func TestStripeRefund_UnknownOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"idempotency_error", http.StatusBadRequest, `{"error":{"type":"idempotency_error","message":"Keys for idempotent requests can only be used with the same parameters"}}`},
		{"4xx without Stripe JSON (a proxy page)", http.StatusForbidden, `<html>Forbidden</html>`},
		{"408 plain text", http.StatusRequestTimeout, `timeout`},
		{"4xx JSON without a type", http.StatusBadRequest, `{"error":{"message":"something"}}`},
		{"409", http.StatusConflict, `{"error":{"type":"invalid_request_error","message":"conflict"}}`},
		{"429", http.StatusTooManyRequests, `{"error":{"type":"invalid_request_error","message":"rate limited"}}`},
		{"500", http.StatusInternalServerError, `{"error":{"type":"api_error","message":"boom"}}`},
		{"503 no body", http.StatusServiceUnavailable, ``},
	}
	for _, c := range cases {
		a := newRefundAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = fmt.Fprint(w, c.body)
		})
		_, err := a.Refund(context.Background(), refundReq())
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if _, _, declined := payments.RefundDeclined(err); declined {
			t.Errorf("%s: an unknown outcome was reported as declined: %v", c.name, err)
		}
	}
}

func TestStripeRefund_TransientFailureIsNotDeclined(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		a := newRefundAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error":{"type":"api_error","message":"try again"}}`)
		})
		_, err := a.Refund(context.Background(), refundReq())
		if err == nil {
			t.Fatalf("status %d: no error", status)
		}
		if _, _, declined := payments.RefundDeclined(err); declined {
			t.Errorf("status %d: an unknown outcome was reported as declined: %v", status, err)
		}
	}
}

func TestStripeRefund_ErrorTextUnchanged(t *testing.T) {
	a := newRefundAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"type":"api_error","code":"x","message":"boom"}}`)
	})
	_, err := a.Refund(context.Background(), refundReq())
	if err == nil || !strings.Contains(err.Error(), "stripe: API error (status 500, type api_error, code x): boom") {
		t.Fatalf("error text changed: %v", err)
	}
}

func TestStripeResolveChargeRef(t *testing.T) {
	var paths []string
	a := newRefundAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkout/sessions/cs_paid"):
			_, _ = fmt.Fprint(w, `{"id":"cs_paid","payment_intent":"pi_behind"}`)
		case strings.HasSuffix(r.URL.Path, "/checkout/sessions/cs_expanded"):
			_, _ = fmt.Fprint(w, `{"id":"cs_expanded","payment_intent":{"id":"pi_expanded"}}`)
		case strings.HasSuffix(r.URL.Path, "/checkout/sessions/cs_unpaid"):
			_, _ = fmt.Fprint(w, `{"id":"cs_unpaid","payment_intent":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such checkout session"}}`)
		}
	})
	ctx := context.Background()
	if got, err := a.ResolveChargeRef(ctx, "pi_direct"); err != nil || got != "pi_direct" {
		t.Errorf("pi_ passthrough: %q %v", got, err)
	}
	if len(paths) != 0 {
		t.Errorf("a pi_ must not call Stripe: %v", paths)
	}
	if got, err := a.ResolveChargeRef(ctx, "cs_paid"); err != nil || got != "pi_behind" {
		t.Errorf("cs_paid: %q %v", got, err)
	}
	if got, err := a.ResolveChargeRef(ctx, "cs_expanded"); err != nil || got != "pi_expanded" {
		t.Errorf("cs_expanded: %q %v", got, err)
	}
	for _, id := range []string{"cs_unpaid", "cs_gone", "ch_weird"} {
		_, err := a.ResolveChargeRef(ctx, id)
		var declined *payments.RefundDeclinedError
		if !errors.As(err, &declined) {
			t.Errorf("%s: want a RefundDeclinedError, got %v", id, err)
		}
	}
}
