package hfeed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestPrepareRequestRefusal(t *testing.T) {
	caps := []string{CapabilityStripeElements}
	promo := "SAVE10"
	blank := "  "
	cases := []struct {
		name string
		req  PublicFeedCheckoutStartRequest
		want string
	}{
		{"plain cart", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps}, ""},
		{"blank promo is no promo", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, PromoCode: &blank}, ""},
		{"no capability", PublicFeedCheckoutStartRequest{Prepare: true}, prepareReasonNoCapability},
		{"other capability", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: []string{"x"}}, prepareReasonNoCapability},
		{"buyer object", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, Buyer: &PublicBuyerInfo{}}, prepareReasonBuyer},
		{"holder email", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, HolderEmail: "a@b.co"}, prepareReasonBuyer},
		{"promo code", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, PromoCode: &promo}, prepareReasonPromo},
		{"marketing tick", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, MarketingConsent: &PublicMarketingConsent{}}, prepareReasonMarketing},
		{"consent", PublicFeedCheckoutStartRequest{Prepare: true, Capabilities: caps, Consent: &PublicCheckoutConsent{Accepted: true}}, prepareReasonConsent},
	}
	for _, tc := range cases {
		if got := prepareRequestRefusal(tc.req); got != tc.want {
			t.Errorf("%s: refusal = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPrepareRequestDecodesPrepareFlag(t *testing.T) {
	var req PublicFeedCheckoutStartRequest
	if err := json.Unmarshal([]byte(`{"prepare":true,"capabilities":["stripe_elements"]}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.Prepare || prepareRequestRefusal(req) != "" {
		t.Fatalf("decoded %+v", req)
	}
}

func TestWritePrepareUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	writePrepareUnavailable(rec, httptest.NewRequest(http.MethodPost, "/", nil), http.StatusConflict, prepareReasonFree)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.Code != ErrCodePrepareUnavailable || out.Error.Details["reason"] != prepareReasonFree {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// Without a database the attach route answers 503, uncached.
func TestHandlePublicCheckoutBuyer_NoDatabase(t *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/checkout/tok/buyer", bytes.NewReader([]byte(`{"buyer":{"email":"x"}}`)))
	h.HandlePublicCheckoutBuyer(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("no database: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

// The attach closes with the PAYMENT window (hold expiry minus the grace),
// not with the hold.
func TestPaymentWindowOpen(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	grace := 2 * time.Minute
	cases := []struct {
		name string
		hold time.Time
		want bool
	}{
		{"well inside", now.Add(10 * time.Minute), true},
		{"hold live, inside the grace", now.Add(time.Minute), false},
		{"exactly at payment_expires_at", now.Add(grace), false},
		{"one second before payment_expires_at", now.Add(grace + time.Second), true},
		{"hold over", now.Add(-time.Second), false},
	}
	for _, tc := range cases {
		if got := paymentWindowOpen(tc.hold, grace, now); got != tc.want {
			t.Errorf("%s: open = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// No feed context or no buyer flags refuses the attach; it never proceeds
// with every channel flag false.
func TestAttachLookupOutcome(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		token string
		want  lookupOutcome
	}{
		{"found", nil, "tok", lookupFound},
		{"no row", pgx.ErrNoRows, "", lookupMissing},
		{"wrapped no row", fmt.Errorf("x: %w", pgx.ErrNoRows), "tok", lookupMissing},
		{"row without a token", nil, "  ", lookupMissing},
		{"database error", errors.New("boom"), "tok", lookupFailed},
	}
	for _, tc := range cases {
		if got := attachLookupOutcome(tc.err, tc.token); got != tc.want {
			t.Errorf("%s: outcome = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func testPaymentContext() gen.CheckoutPaymentContextRow {
	return gen.CheckoutPaymentContextRow{HoldExpiresAt: time.Now().Add(30 * time.Minute)}
}

func TestAnswerExistingBuyer(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	rec := httptest.NewRecorder()
	h.answerExistingBuyer(rec, r, "tok", testPaymentContext(), "Buyer@Example.com ", "buyer@example.com")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("same e-mail = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.answerExistingBuyer(rec, r, "tok", testPaymentContext(), "buyer@example.com", "other@example.com")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), ErrCodeBuyerAlreadySet) {
		t.Fatalf("other e-mail = %d %s", rec.Code, rec.Body.String())
	}
}
