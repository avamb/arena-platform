//go:build integration

package httpserver

// shop_review_integration_test.go — regressions found in the review of the
// shop core (PR #275): the last-unit race, the late payment of an expired
// order, the checkout limits and the switch that opens the storefront.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestShop_LastUnitRace: N buyers reach the checkout for the last unit at the
// same moment. The variant rows are locked in their own statement before the
// held quantity is read, so exactly one buyer gets the unit and every other
// one is refused with 409 shop.out_of_stock — never two holds on one unit.
func TestShop_LastUnitRace(t *testing.T) {
	f := newShopFixture(t)
	_, variantID := f.openShop(4500, 1)

	// Hold the variant row locked while every buyer's checkout starts, so all
	// of them queue on the lock with a snapshot taken before the winner's
	// order exists. That is the window the old single-statement lock missed.
	ctx := context.Background()
	gate, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := gate.Exec(ctx, `SELECT id FROM shop_variants WHERE id = $1 FOR UPDATE`, variantID); err != nil {
		t.Fatalf("lock variant: %v", err)
	}

	const buyers = 8
	codes := make([]int, buyers)
	errs := make([]string, buyers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			code, body := f.checkout(variantID, 1)
			codes[i], errs[i] = code, shopErrCode(body)
		}(i)
	}
	close(start)
	time.Sleep(700 * time.Millisecond)
	if err := gate.Commit(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	wg.Wait()

	won := 0
	for i := range codes {
		switch {
		case codes[i] == http.StatusCreated:
			won++
		case codes[i] == http.StatusConflict && errs[i] == "shop.out_of_stock":
		default:
			t.Errorf("buyer %d: %d %q; want 201 or 409 shop.out_of_stock", i, codes[i], errs[i])
		}
	}
	if won != 1 {
		t.Fatalf("%d buyers got the last unit, want exactly 1 (codes %v)", won, codes)
	}
	if v := f.variant(variantID); v.Held != 1 || v.Stock != 1 {
		t.Fatalf("variant after the race: stock %d held %d, want 1 and 1", v.Stock, v.Held)
	}
}
