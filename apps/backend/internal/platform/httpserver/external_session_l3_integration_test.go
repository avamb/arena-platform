//go:build integration

// external_session_l3_integration_test.go — spec 63 §14.2 (migration 0218):
// a session a partner box office sells (sessions.sales_mode = 'external')
// is never held through the public widget surface:
//
//   - POST /v1/public/feeds/{token}/checkout/start and .../checkout/quote
//     answer 409 session.sold_externally and create NO hold;
//   - switched back to the platform, the same quote prices again.
//
// Prerequisites: DATABASE_URL against a migrated database.
package httpserver

import (
	"net/http"
	"testing"
)

func TestL3_ExternalSession_PublicCheckoutRefused(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newW1A6cFixture(t, ctx, pool)
	defer f.cleanup()
	defer enableStripeForChannel(t, ctx, pool, f.orgID, f.channelID)()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	startBody := map[string]any{
		"session_id": f.sessionID.String(),
		"return_url": hostedTicketsBaseURL + "/embed",
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"buyer": map[string]any{
			"email": f.buyerMail,
			"name":  "Partner Buyer",
			"phone": f.buyerPhone,
		},
	}
	quoteBody := map[string]any{
		"session_id": f.sessionID.String(),
		"ga_items":   []map[string]any{{"tier_id": f.tierID.String(), "quantity": 1}},
	}

	if rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", quoteBody); rec.Code != http.StatusOK {
		t.Fatalf("baseline quote = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	if _, err := pool.Exec(ctx, `UPDATE sessions SET sales_mode = 'external',
	        external_ticket_url = 'https://www.eventim.de/l3', external_seller_name = 'Eventim'
	  WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("make the session external: %v", err)
	}
	beforeRes, beforeTaken := uxh23HoldRows(t, ctx, pool, f)

	for _, tc := range []struct {
		name, path string
		body       map[string]any
	}{
		{"checkout/start", "/v1/public/feeds/" + f.feedToken + "/checkout/start", startBody},
		{"checkout/quote", "/v1/public/feeds/" + f.feedToken + "/checkout/quote", quoteBody},
	} {
		t.Run(tc.name+" answers 409 session.sold_externally", func(t *testing.T) {
			rec := uxh23Post(t, srv, tc.path, tc.body)
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s = %d, want 409; body: %s", tc.name, rec.Code, rec.Body.String())
			}
			if code := uxh23ErrorCode(t, rec); code != "session.sold_externally" {
				t.Errorf("error code = %q, want session.sold_externally", code)
			}
		})
	}

	res, taken := uxh23HoldRows(t, ctx, pool, f)
	if res != beforeRes || taken != beforeTaken {
		t.Errorf("reservations %d -> %d, taken places %d -> %d; a refused request must change nothing",
			beforeRes, res, beforeTaken, taken)
	}

	if _, err := pool.Exec(ctx, `UPDATE sessions SET sales_mode = 'platform',
	        external_ticket_url = NULL, external_seller_name = NULL
	  WHERE id = $1`, f.sessionID); err != nil {
		t.Fatalf("back to the platform: %v", err)
	}
	if rec := uxh23Post(t, srv, "/v1/public/feeds/"+f.feedToken+"/checkout/quote", quoteBody); rec.Code != http.StatusOK {
		t.Fatalf("quote after switching back = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}
