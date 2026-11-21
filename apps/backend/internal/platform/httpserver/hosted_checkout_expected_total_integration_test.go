//go:build integration

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// startCheckoutExpecting is startCheckout with an expected_total and an
// optional Idempotency-Key.
func (f *hostedOrgFixture) startCheckoutExpecting(t *testing.T, srv *Server, expected int64, idemKey string) (int, http.Header, []byte) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        2,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Hosted Checkout Buyer",
			"phone": f.buyerPhone,
		},
		"return_url":     hostedTicketsBaseURL + "/shows",
		"expected_total": expected,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

// TestHostedCheckout_ExpectedTotalMismatchIsRefusedBeforeAnyHold proves the
// site's stated total is checked against arena's own pricing: a mismatch is
// 409 checkout.price_changed with both totals, takes no inventory, creates
// no hosted page, and is not replayed for its Idempotency-Key; the matching
// total then goes through.
func TestHostedCheckout_ExpectedTotalMismatchIsRefusedBeforeAnyHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "xt")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())
	before := f.footprint(t, ctx)
	key := "xt-" + uuid.NewString()

	var actual int64
	for i := 0; i < 2; i++ { // the same key twice: re-checked, never replayed
		code, hdr, body := f.startCheckoutExpecting(t, srv, 1, key)
		if code != http.StatusConflict {
			t.Fatalf("attempt %d: checkout/start with a stale expected_total = %d, want 409; body: %s", i, code, body)
		}
		if hdr.Get("Idempotent-Replay") != "" {
			t.Fatalf("attempt %d: the 409 was replayed from the idempotency store", i)
		}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					ExpectedTotal int64  `json:"expected_total"`
					ActualTotal   int64  `json:"actual_total"`
					Currency      string `json:"currency"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		if env.Error.Code != "checkout.price_changed" || env.Error.Details.ExpectedTotal != 1 ||
			env.Error.Details.ActualTotal <= 1 || env.Error.Details.Currency != "EUR" {
			t.Fatalf("error = %+v; want checkout.price_changed with both totals and EUR", env.Error)
		}
		actual = env.Error.Details.ActualTotal
	}
	if len(stripe.sessions) != 0 {
		t.Error("a hosted page was created for a refused cart")
	}
	f.assertNoInventoryTaken(t, ctx, before)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("count idempotency_keys: %v", err)
	}
	if n != 0 {
		t.Errorf("idempotency_keys rows for the refused key = %d, want 0", n)
	}

	code, _, body := f.startCheckoutExpecting(t, srv, actual, "xt-"+uuid.NewString())
	if code != http.StatusCreated {
		t.Fatalf("checkout/start with the corrected expected_total %d = %d, want 201; body: %s", actual, code, body)
	}
}
