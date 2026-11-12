package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// decodeErr reads the error envelope of a recorded answer.
func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) (code string, details map[string]any) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code, env.Error.Details
}

// TestCheckoutStart_MalformedEmailIsRefusedBeforeAnyWrite proves a malformed
// buyer e-mail answers 400 checkout.invalid_email before the handler touches
// anything: the Handler here has no database at all, so reaching any read
// would panic (F-BR-03).
func TestCheckoutStart_MalformedEmailIsRefusedBeforeAnyWrite(t *testing.T) {
	for _, email := range []string{
		"assistant+fc043@abhteam.comRUN1109-FC FC-0",
		"assistant+fc043@abhteam.comRUN1109-FC",
		"anna@localhost",
		"anna example.com",
	} {
		t.Run(email, func(t *testing.T) {
			body := fmt.Sprintf(`{"session_id":%q,"buyer":{"email":%q},"ga_items":[{"tier_id":%q,"quantity":1}]}`,
				uuid.NewString(), email, uuid.NewString())
			r := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/tok/checkout/start", strings.NewReader(body))
			rec := httptest.NewRecorder()
			(&Handler{}).publicFeedCheckoutStart(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			code, details := decodeErr(t, rec)
			if code != ErrCodeInvalidEmail {
				t.Fatalf("code = %q, want %q", code, ErrCodeInvalidEmail)
			}
			if details["field"] != "holder_email" {
				t.Fatalf("details = %v, want field holder_email", details)
			}
		})
	}
}

// TestCheckoutStart_EmptyEmailKeepsItsOwnCode keeps the older, more precise
// answer for an address that is only spaces.
func TestCheckoutStart_EmptyEmailKeepsItsOwnCode(t *testing.T) {
	body := fmt.Sprintf(`{"session_id":%q,"holder_email":"   ","ga_items":[{"tier_id":%q,"quantity":1}]}`,
		uuid.NewString(), uuid.NewString())
	r := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/tok/checkout/start", strings.NewReader(body))
	rec := httptest.NewRecorder()
	(&Handler{}).publicFeedCheckoutStart(rec, r)
	if code, _ := decodeErr(t, rec); rec.Code != http.StatusBadRequest || code != "checkout.missing_holder_email" {
		t.Fatalf("status %d code %q, want 400 checkout.missing_holder_email", rec.Code, code)
	}
}

// refusingStarter answers every hosted-checkout start with err.
type refusingStarter struct{ err error }

func (s refusingStarter) CheckPaymentConfigured(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s refusingStarter) StartHostedCheckout(context.Context, HostedCheckoutRequest) (*HostedCheckoutResult, error) {
	return nil, s.err
}

// TestStartHostedPayment_ProviderRefusals maps the provider's answer to the
// buyer's: an e-mail the provider refused is 400 checkout.invalid_email,
// anything else stays 503 checkout.payment_start_failed (never 502, see the
// Cloudflare note in startHostedPayment).
func TestStartHostedPayment_ProviderRefusals(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"stripe email_invalid", fmt.Errorf("stripe hosted checkout (config c, mode test): %w",
			&payments.HostedCheckoutRefusedError{Reason: payments.HostedCheckoutRefusalInvalidEmail, Code: "email_invalid", Message: "Invalid email address"}),
			http.StatusBadRequest, ErrCodeInvalidEmail},
		{"other definitive refusal", &payments.HostedCheckoutRefusedError{Code: "parameter_invalid_integer", Message: "x"},
			http.StatusServiceUnavailable, ErrCodePaymentStartFailed},
		{"unknown outcome", errors.New("stripe: http: timeout"),
			http.StatusServiceUnavailable, ErrCodePaymentStartFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handler{
				logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
				paymentStarter: refusingStarter{err: c.err},
			}
			r := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/tok/checkout/start", nil)
			rec := httptest.NewRecorder()
			if _, ok := h.startHostedPayment(r.Context(), rec, r, hostedPaymentInput{}); ok {
				t.Fatal("startHostedPayment reported ok for a refusal")
			}
			code, _ := decodeErr(t, rec)
			if rec.Code != c.wantStatus || code != c.wantCode {
				t.Fatalf("got %d %q, want %d %q", rec.Code, code, c.wantStatus, c.wantCode)
			}
		})
	}
}
