//go:build integration

// public_feed_promo_concurrent_integration_test.go — the refused-code budget
// holds under a burst. The slot is taken when a request names a code, not after
// the code is judged, so concurrent requests cannot all pass a check made before
// any of them was counted.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicPromoBudget
package httpserver

import (
	"net/http"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// burst fires fn n times at once and counts the HTTP statuses it returned.
func burst(n int, fn func(i int) int) map[int]int {
	var mu sync.Mutex
	counts := map[int]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			status := fn(i)
			mu.Lock()
			counts[status]++
			mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()
	return counts
}

func TestPublicPromoBudget_ConcurrentWrongCodesCannotOvershoot(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "pbc")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL(), func(c *config.Config) {
		c.PublicPromoFailIPLimit = 3
		c.PublicPromoFailTokenLimit = 0
	})

	const n = 40
	got := burst(n, func(int) int {
		status, _ := f.quote(t, srv, f.gaCart(1, "WRONG", ""))
		return status
	})
	if got[http.StatusUnprocessableEntity] != 3 || got[http.StatusTooManyRequests] != n-3 {
		t.Errorf("%d concurrent wrong-code quotes at a limit of 3: %v; want 3x422 and %dx429", n, got, n-3)
	}

	// The documented order: body and cart size are validated before the budget
	// answers, so an oversized cart is a 400 even with the budget spent.
	seats := make([]string, 101)
	for i := range seats {
		seats[i] = "A|1|" + string(rune('0'+i%10))
	}
	big := map[string]any{"session_id": f.sessionID.String(), "seats": seats, "promo_code": "WRONG"}
	if status, body := f.quote(t, srv, big); status != http.StatusBadRequest {
		t.Errorf("101 seats with a code, budget spent = %d %s, want 400 (cart size is validated first)", status, body)
	}
}

func TestPublicPromoBudget_ConcurrentWrongCodesOnStartCannotOvershoot(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "pbs")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL(), func(c *config.Config) {
		c.PublicPromoFailIPLimit = 3
		c.PublicPromoFailTokenLimit = 0
	})

	const n = 40
	got := burst(n, func(i int) int {
		// Quote and start draw on the same budget: half of each.
		if i%2 == 0 {
			status, _ := f.quote(t, srv, f.gaCart(1, "WRONG", ""))
			return status
		}
		status, _ := f.startWithPromo(t, srv, 1, "WRONG")
		return status
	})
	if got[http.StatusUnprocessableEntity] != 3 || got[http.StatusTooManyRequests] != n-3 {
		t.Errorf("%d concurrent wrong-code quotes and starts at a limit of 3: %v; want 3x422 and %dx429", n, got, n-3)
	}
}

// An accepted code gives its slot back, so a burst of valid codes leaves the
// budget whole: the visitor still has all of it for real mistakes.
func TestPublicPromoBudget_AcceptedCodesLeaveTheBudgetWhole(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "pba")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL(), func(c *config.Config) {
		c.PublicPromoFailIPLimit = 3
		c.PublicPromoFailTokenLimit = 0
	})
	f.insertPromo(t, ctx, "TEN", "percent", 10, "", "")

	// Concurrent valid codes may briefly meet each other's slots (a 429 is
	// allowed here), but none of them is a refusal.
	burst(20, func(int) int {
		status, _ := f.quote(t, srv, f.gaCart(1, "TEN", ""))
		return status
	})
	for i := 0; i < 3; i++ {
		if status, body := f.quote(t, srv, f.gaCart(1, "WRONG", "")); status != http.StatusUnprocessableEntity {
			t.Fatalf("wrong code %d after the valid burst = %d %s, want 422 (the budget was eaten by accepted codes)", i+1, status, body)
		}
	}
	if status, _ := f.quote(t, srv, f.gaCart(1, "WRONG", "")); status != http.StatusTooManyRequests {
		t.Errorf("fourth wrong code = %d, want 429", status)
	}
}
