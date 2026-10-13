//go:build integration

// public_feed_seated_promo_integration_test.go — a refused promo code is
// answered the same way whatever the cart holds. The seated branch of
// checkout/start refuses a code from inside its hold transaction, and used to
// answer the platform's internal verdicts there ("expired", "not active") and
// not to count the refusal against the refused-code budget, which the general
// admission branch and the quote did.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicSeatedPromo
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestPublicSeatedPromo_StartRefusesLikeTheQuote(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "spr")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	hybrid, _, _ := f.addHybridSession(t, ctx)

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL(), func(c *config.Config) {
		c.PublicPromoFailIPLimit = 100 // room for the quotes and the starts compared below
		c.PublicPromoFailTokenLimit = 0
	})

	f.insertPromo(t, ctx, "EXPIRED", "percent", 10,
		", valid_from, valid_until", ", now() - interval '10 days', now() - interval '1 day'")
	f.insertPromo(t, ctx, "SUSPENDED", "percent", 10, ", status", ", 'paused'")
	f.insertPromo(t, ctx, "HUGEMIN", "percent", 10, ", min_order_amount", ", 100000000")
	f.insertPromo(t, ctx, "TEN", "percent", 10, "", "")

	seatCart := func(promo string) map[string]any {
		return map[string]any{
			"session_id": hybrid.String(),
			"seats":      []string{"A|1|1", "A|1|2"},
			"promo_code": promo,
		}
	}
	startCart := func(promo string) map[string]any {
		p := seatCart(promo)
		p["buyer"] = map[string]any{"email": f.buyerMail, "name": "Seated Buyer", "phone": f.buyerPhone}
		return p
	}
	held := func() int {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM session_seats WHERE session_id = $1 AND status <> 'available'`, hybrid).Scan(&n); err != nil {
			t.Fatalf("count taken seats: %v", err)
		}
		return n
	}

	// Each refusal reads exactly like the quote's, with no internal code.
	cases := []struct{ code, want string }{
		{"EXPIRED", "promo.not_applicable"},
		{"SUSPENDED", "promo.not_applicable"},
		{"HUGEMIN", "promo.minimum_not_reached"},
	}
	for _, c := range cases {
		qs, qb := f.quote(t, srv, seatCart(c.code))
		_ = qs
		var q quoteErrBody
		_ = json.Unmarshal(qb, &q)

		status, body := f.postJSON(t, srv, f.feedToken, "/checkout/start", startCart(c.code))
		var s quoteErrBody
		_ = json.Unmarshal(body, &s)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("seated start with %s = %d, want 422; body: %s", c.code, status, body)
		}
		if s.Error.Code != c.want || s.Error.Code != q.Error.Code || s.Error.Message != q.Error.Message {
			t.Errorf("seated start with %s answered %+v, quote answered %+v; want %s on both", c.code, s.Error, q.Error, c.want)
		}
		if strings.HasPrefix(s.Error.Code, "promo.expired") || s.Error.Code == "promo.not_active" || s.Error.Code == "promo.invalid_order_amount" {
			t.Errorf("seated start leaked the internal verdict %q", s.Error.Code)
		}
	}
	// The refusals rolled the holds back: nothing stays taken.
	if n := held(); n != 0 {
		t.Errorf("seats taken after refused starts = %d, want 0", n)
	}

	f.nothingCreated(t, ctx, stripe)
}

// The seated start alone spends the budget: three refusals through it, with no
// quote involved, lock the address out.
func TestPublicSeatedPromo_ThreeSeatedStartRefusalsLockTheAddressOut(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "sp3")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	hybrid, _, _ := f.addHybridSession(t, ctx)

	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL(), func(c *config.Config) {
		c.PublicPromoFailIPLimit = 3
		c.PublicPromoFailTokenLimit = 0
	})
	f.insertPromo(t, ctx, "EXPIRED", "percent", 10,
		", valid_from, valid_until", ", now() - interval '10 days', now() - interval '1 day'")
	f.insertPromo(t, ctx, "TEN", "percent", 10, "", "")

	start := func(promo string) (int, []byte) {
		return f.postJSON(t, srv, f.feedToken, "/checkout/start", map[string]any{
			"session_id": hybrid.String(),
			"seats":      []string{"A|1|1"},
			"promo_code": promo,
			"buyer":      map[string]any{"email": f.buyerMail, "name": "Seated Buyer", "phone": f.buyerPhone},
		})
	}
	for i := 0; i < 3; i++ {
		if status, body := start("EXPIRED"); status != http.StatusUnprocessableEntity {
			t.Fatalf("refusal %d = %d, want 422; body: %s", i+1, status, body)
		}
	}
	if status, body := start("TEN"); status != http.StatusTooManyRequests {
		t.Errorf("after three seated refusals a valid code = %d %s, want 429", status, body)
	}

	// The spent budget turns a request away BEFORE any database read: a session
	// that is not on this feed would otherwise be answered 403 by the lookup.
	foreign := map[string]any{
		"session_id": uuid.New().String(),
		"ga_items":   []map[string]any{{"tier_id": uuid.New().String(), "quantity": 1}},
		"promo_code": "TEN",
	}
	if status, body := f.quote(t, srv, foreign); status != http.StatusTooManyRequests {
		t.Errorf("quote naming a code, budget spent, foreign session = %d %s, want 429 before any lookup", status, body)
	}
	foreign["buyer"] = map[string]any{"email": f.buyerMail}
	if status, body := f.postJSON(t, srv, f.feedToken, "/checkout/start", foreign); status != http.StatusTooManyRequests {
		t.Errorf("start naming a code, budget spent, foreign session = %d %s, want 429 before any lookup", status, body)
	}
}
