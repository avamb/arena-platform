//go:build integration

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFunnelV2_ConversionRowsOnlyWithConsent covers spec 68 sections 3.1-3.3
// and 5.1 end to end: AddToCart and InitiateCheckout through checkout/quote,
// AcceptTerms when checkout/start records the tick on the sale terms, and
// AddPaymentInfo through POST .../payment-info. Without the buyer's explicit
// yes to tracking no conversion_deliveries row is written; with it there is
// one row of each kind (Meta only is configured), and a second payment-info
// call adds none. A paid start without the tick is refused and keeps no hold.
func TestFunnelV2_ConversionRowsOnlyWithConsent(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	f := newHostedOrgFixture(t, ctx, pool, "fv2")
	defer f.cleanup()
	if _, err := pool.Exec(ctx, `INSERT INTO org_conversion_settings (org_id, meta_pixel_id, meta_capi_token)
		VALUES ($1, '1352226752766944', 'meta-token-123')`, f.orgID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM conversion_deliveries WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM checkout_buyer_contexts WHERE checkout_session_id IN
			(SELECT id FROM checkout_sessions WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM org_conversion_settings WHERE org_id = $1`, f.orgID)
	}()

	post := func(path string, body any) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if body == nil {
			rd = bytes.NewReader(nil)
		} else {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(http.MethodPost, path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "FunnelV2/1.0")
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	rows := func() map[string]int {
		t.Helper()
		r, err := pool.Query(ctx, `SELECT kind, count(*)::int FROM conversion_deliveries WHERE org_id = $1 GROUP BY kind`, f.orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		m := map[string]int{}
		for r.Next() {
			var k string
			var n int
			if err := r.Scan(&k, &n); err != nil {
				t.Fatal(err)
			}
			m[k] = n
		}
		return m
	}
	feed := "/v1/public/feeds/" + f.feedToken
	quote := func(bc map[string]any, cartID, cartEvent string) {
		t.Helper()
		body := map[string]any{
			"session_id":     f.sessionID.String(),
			"ga_items":       []map[string]any{{"tier_id": f.tierID.String(), "quantity": 2}},
			"buyer_context":  bc,
			"add_to_cart_id": cartID,
		}
		if cartEvent != "" {
			body["cart_event"] = cartEvent
		}
		if code, out := post(feed+"/checkout/quote", body); code != http.StatusOK {
			t.Fatalf("quote %s: %d %v", cartEvent, code, out)
		}
	}
	start := func(bc map[string]any, consent bool) (int, map[string]any) {
		t.Helper()
		body := map[string]any{
			"session_id":    f.sessionID.String(),
			"tier_id":       f.tierID.String(),
			"qty":           2,
			"buyer":         map[string]any{"email": f.buyerMail, "name": "Funnel Buyer", "phone": f.buyerPhone},
			"return_url":    hostedTicketsBaseURL + "/shows",
			"buyer_context": bc,
		}
		if consent {
			body["consent"] = map[string]any{"accepted": true, "locale": "en"}
		}
		return post(feed+"/checkout/start", body)
	}
	no := map[string]any{"tracking_consent": false, "tracking_consent_text_version": "v1", "fbp": "fb.1.1.2"}
	yes := map[string]any{"tracking_consent": true, "tracking_consent_text_version": "v1", "fbp": "fb.1.1.2"}

	// A paid sale without the tick is refused and keeps no hold (spec 68 5.1).
	if code, out := start(yes, false); code != http.StatusUnprocessableEntity || prepErrCode(out) != "checkout.consent_required" {
		t.Fatalf("paid start without consent: %d %v", code, out)
	}
	if n := liveHolds(t, ctx, pool, f.sessionID); n != 0 {
		t.Fatalf("a refused start kept %d holds", n)
	}

	// ── No tracking consent: nothing is queued. ──────────────────────────────
	quote(no, "cart-no", "")
	quote(no, "cart-no", "initiate_checkout")
	code, out := start(no, true)
	if code != http.StatusCreated {
		t.Fatalf("start without tracking consent: %d %v", code, out)
	}
	tokNo, _ := out["checkout_token"].(string)
	if c, _ := post("/v1/public/checkout/"+tokNo+"/payment-info", map[string]any{}); c != http.StatusNoContent {
		t.Fatalf("payment-info: %d", c)
	}
	if got := rows(); len(got) != 0 {
		t.Fatalf("without tracking consent rows = %v, want none", got)
	}

	// ── With tracking consent: one row of each kind. ─────────────────────────
	quote(yes, "cart-yes", "")
	quote(yes, "cart-yes", "initiate_checkout")
	quote(yes, "cart-yes", "initiate_checkout") // a repeat dedups
	quote(yes, "cart-yes", "bogus")             // an unknown cart_event queues nothing
	code, out = start(yes, true)
	if code != http.StatusCreated {
		t.Fatalf("start with tracking consent: %d %v", code, out)
	}
	tok, _ := out["checkout_token"].(string)
	if c, _ := post("/v1/public/checkout/"+tok+"/payment-info", nil); c != http.StatusNoContent {
		t.Fatalf("payment-info: %d", c)
	}
	want := map[string]int{"add_to_cart": 1, "initiate_checkout": 1, "accept_terms": 1, "add_payment_info": 1}
	got := rows()
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want exactly %v", got, want)
	}
	// The event ids name the cart and the checkout.
	var ids []string
	r, err := pool.Query(ctx, `SELECT event_id FROM conversion_deliveries WHERE org_id = $1 ORDER BY event_id`, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var s string
		_ = r.Scan(&s)
		ids = append(ids, s)
	}
	r.Close()
	csID, _ := out["checkout_session"].(map[string]any)["id"].(string)
	wantIDs := map[string]bool{"atc:cart-yes": true, "ic:cart-yes": true, "terms:" + csID: true, "api:" + csID: true}
	for _, id := range ids {
		if !wantIDs[id] {
			t.Fatalf("unexpected event id %q (all: %v)", id, ids)
		}
	}

	// A second payment-info call adds nothing.
	if c, _ := post("/v1/public/checkout/"+tok+"/payment-info", map[string]any{}); c != http.StatusNoContent {
		t.Fatalf("repeat payment-info: %d", c)
	}
	if again := rows(); again["add_payment_info"] != 1 {
		t.Fatalf("repeat payment-info rows = %v", again)
	}

	// An unknown checkout token is 404.
	if c, _ := post("/v1/public/checkout/no-such-token/payment-info", nil); c != http.StatusNotFound {
		t.Fatalf("unknown token: %d", c)
	}
}
