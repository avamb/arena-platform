//go:build integration

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestCheckoutBuyerContext_StartStoresContextWithServerSideIPAndUA covers П-8:
// checkout/start keeps the bounded buyer_context beside the order, adds the
// peer IP (never a forged header) and the User-Agent, and a garbage object
// never costs the sale.
func TestCheckoutBuyerContext_StartStoresContextWithServerSideIPAndUA(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	cases := []struct {
		name    string
		bc      any
		wantRow bool
	}{
		{"full context", map[string]any{
			"fbp": "fb.1.1.2", "fbc": "fb.1.1.abc", "fbclid": "abc", "gclid": "g1", "ga_client_id": "111.222",
			"tracking_consent": true, "tracking_consent_text_version": "2026-11",
			"client_ip": "6.6.6.6", // a forged field is ignored, the peer IP wins
		}, true},
		{"not an object", 42, false},
		{"absent", nil, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFreeBuyerFieldsFixture(t, ctx, pool, "bctx"+string(rune('a'+i)), false, false)
			defer f.cleanup()
			body := map[string]any{
				"session_id": f.sessionID.String(),
				"tier_id":    f.tierID.String(),
				"qty":        1,
				"buyer":      map[string]any{"email": uuid.NewString()[:8] + "-" + f.buyerMail},
				"return_url": hostedTicketsBaseURL + "/shows",
			}
			if tc.bc != nil {
				body["buyer_context"] = tc.bc
			}
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "TestAgent/1.0")
			req.Header.Set("X-Forwarded-For", "6.6.6.6")
			req.RemoteAddr = "203.0.113.7:4444"
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("checkout/start = %d; body: %s", rec.Code, rec.Body.String())
			}
			var start hostedStartResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
				t.Fatal(err)
			}
			csID := uuid.MustParse(start.CheckoutSession.ID)
			order, err := q.GetOrderByCheckoutSession(ctx, csID)
			if err != nil {
				t.Fatal(err)
			}
			var stored []byte
			err = pool.QueryRow(ctx, `SELECT context FROM checkout_buyer_contexts WHERE order_id = $1`, order.ID).Scan(&stored)
			if !tc.wantRow {
				if err == nil {
					t.Fatalf("a context was stored for %s: %s", tc.name, stored)
				}
				return
			}
			if err != nil {
				t.Fatalf("no stored context: %v", err)
			}
			var got map[string]any
			_ = json.Unmarshal(stored, &got)
			if got["fbp"] != "fb.1.1.2" || got["ga_client_id"] != "111.222" || got["tracking_consent"] != true ||
				got["tracking_consent_text_version"] != "2026-11" {
				t.Errorf("stored context: %s", stored)
			}
			if got["client_ip"] != "203.0.113.7" || got["user_agent"] != "TestAgent/1.0" {
				t.Errorf("server-side ip/ua: %s", stored)
			}
			if strings.Contains(string(stored), "6.6.6.6") {
				t.Errorf("a forged IP leaked in: %s", stored)
			}
		})
	}
}
