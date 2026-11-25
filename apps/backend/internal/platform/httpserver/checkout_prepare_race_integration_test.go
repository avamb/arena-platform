//go:build integration

// checkout_prepare_race_integration_test.go — spec 65 early order, two
// money-path fixes:
//
//   - two attaches with the same e-mail on the same session at the same time
//     both answer 200 and keep both orders payable;
//   - the reminder's "newer order" check compares the moment each order got
//     its buyer, so an order prepared first and attached later is the newer.
package httpserver

import (
	"net/http"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/cartreminder"
)

// customers.Resolve serializes the two attaches, so this test proves that a
// second tab with the same e-mail neither cancels the first payable order nor
// fails: both answer 200 and both orders stay pending_payment, and exactly one
// of them carries the customer of that e-mail. It does not reproduce the 23505
// window: the savepoint in checkout_buyer_attach.go is a defence for that
// window.
func TestCheckoutPrepare_SameEmailAttachesBothAnswer200KeepBothPayable(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prr")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	a := prepareOK(t, srv, f)
	b := prepareOK(t, srv, f)
	buyer := map[string]any{"email": f.buyerMail, "name": "Parallel Buyer", "phone": f.buyerPhone}

	codes := make([]int, 2)
	outs := make([]map[string]any, 2)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i, tok := range []string{a.CheckoutToken, b.CheckoutToken} {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			<-gate
			codes[i], outs[i] = attachBuyer(t, srv, tok, buyer, nil)
		}(i, tok)
	}
	close(gate)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("attach %d = %d %v, want 200", i, code, outs[i])
		}
	}
	linked := 0
	for _, csID := range []string{a.CheckoutSess.ID, b.CheckoutSess.ID} {
		o := orderOf(t, ctx, pool, csID)
		if o.Status != "pending_payment" {
			t.Errorf("order of %s status = %q, want pending_payment", csID, o.Status)
		}
		if o.BuyerEmail == nil || *o.BuyerEmail != f.buyerMail {
			t.Errorf("order of %s buyer e-mail = %v, want %q", csID, o.BuyerEmail, f.buyerMail)
		}
		if o.CustomerID == nil {
			continue
		}
		linked++
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM customer_identities
			WHERE kind = 'email' AND value_normalized = lower($1) AND customer_id = $2`,
			f.buyerMail, *o.CustomerID).Scan(&n); err != nil || n != 1 {
			t.Errorf("order of %s customer %v is not the customer of %q (rows %d, err %v)", csID, *o.CustomerID, f.buyerMail, n, err)
		}
	}
	if linked != 1 {
		t.Errorf("orders with customer_id set = %d, want exactly 1", linked)
	}
}

// An order prepared first and attached later is the newer one: the letter of
// the order attached earlier sees it as a newer order.
func TestCheckoutPrepare_ReminderNewerOrderCountsFromAttach(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f, _ := embeddedFixture(t, ctx, pool, "prn")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	first := prepareOK(t, srv, f)
	second := prepareOK(t, srv, f)
	buyer := map[string]any{"email": f.buyerMail, "name": "Newer Buyer", "phone": f.buyerPhone}
	if code, out := attachBuyer(t, srv, second.CheckoutToken, buyer, nil); code != http.StatusOK {
		t.Fatalf("attach second = %d %v", code, out)
	}
	if code, out := attachBuyer(t, srv, first.CheckoutToken, buyer, nil); code != http.StatusOK {
		t.Fatalf("attach first = %d %v", code, out)
	}
	firstOrd := orderOf(t, ctx, pool, first.CheckoutSess.ID)
	secondOrd := orderOf(t, ctx, pool, second.CheckoutSess.ID)

	// Fixed times: the first sheet opened 30 minutes ago and got its buyer 5
	// minutes ago; the second opened 20 and got its buyer 15 minutes ago.
	if _, err := pool.Exec(ctx, `UPDATE orders SET created_at = now() - interval '30 minutes' WHERE id = $1`, firstOrd.ID); err != nil {
		t.Fatalf("backdate first order: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET created_at = now() - interval '20 minutes' WHERE id = $1`, secondOrd.ID); err != nil {
		t.Fatalf("backdate second order: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE order_events SET created_at = now() - interval '5 minutes'
		WHERE order_id = $1 AND type = 'buyer_attached'`, firstOrd.ID); err != nil {
		t.Fatalf("backdate first attach: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE order_events SET created_at = now() - interval '15 minutes'
		WHERE order_id = $1 AND type = 'buyer_attached'`, secondOrd.ID); err != nil {
		t.Fatalf("backdate second attach: %v", err)
	}
	// The first order's attach may supersede the second; the first stays a
	// live order, which is what the check of the second looks for.
	got, err := cartreminder.LoadOrder(ctx, pool, secondOrd.ID)
	if err != nil {
		t.Fatalf("LoadOrder second: %v", err)
	}
	if !got.NewerOrder {
		t.Errorf("second order: NewerOrder = false, want true — the first order got its buyer later")
	}
	gotFirst, err := cartreminder.LoadOrder(ctx, pool, firstOrd.ID)
	if err != nil {
		t.Fatalf("LoadOrder first: %v", err)
	}
	if gotFirst.NewerOrder {
		t.Errorf("first order: NewerOrder = true, want false — it got its buyer last")
	}
}
