//go:build integration

// promo_max_uses_race_p0_integration_test.go — ORD-08 of the P0 gates
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): two buyers start
// a widget checkout at the same moment with the same session-scoped code of
// max_uses=1, then both pay. Expected (plan ORD-08): one discount, one
// recorded redemption.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestORD08_TwoConcurrentOrdersWithAMaxUsesOneCode(t *testing.T) {
	if os.Getenv("ARENA_P0_KNOWN_GAPS") != "1" {
		t.Skip("known gap P0-1/D-02: max_uses is checked against recorded redemptions only; ARENA_P0_KNOWN_GAPS=1 runs it")
	}
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "p0ord8")
	defer f.cleanup()
	defer f.cleanupPromos()
	// The checkouts point at the code too; cleanupPromos does not know that.
	defer func() {
		_, _ = pool.Exec(context.Background(), `UPDATE checkout_sessions SET promo_code_id = NULL WHERE org_id = $1`, f.orgID)
	}()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	code := "ONE" + f.orgID.String()[:6]
	promoID := f.insertPromo(t, ctx, code, "percent", 50,
		", max_uses, applies_to_session_ids", ", 1, $6::uuid[]", []uuid.UUID{f.sessionID})

	// Two different buyers: identities are global, so they are per run.
	buyers := make([]string, 2)
	for i := range buyers {
		buyers[i] = fmt.Sprintf("p0ord8-%d-%s@arena-integration.test", i, f.orgID.String()[:8])
	}
	t.Cleanup(func() {
		for _, b := range buyers {
			_, _ = pool.Exec(context.Background(), `DELETE FROM customers WHERE id IN
				(SELECT customer_id FROM customer_identities WHERE value_normalized = $1)`, b)
		}
	})

	type started struct {
		status int
		body   []byte
	}
	results := make([]started, len(buyers))
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i, mail := range buyers {
		wg.Add(1)
		go func(i int, mail string) {
			defer wg.Done()
			p := f.gaCart(1, code, "")
			p["buyer"] = map[string]any{
				"email": mail, "name": fmt.Sprintf("Buyer %d", i),
				"phone": fmt.Sprintf("+3633%07d", (time.Now().UnixNano()+int64(i))%10_000_000),
			}
			p["return_url"] = hostedTicketsBaseURL + "/p0"
			<-gate
			results[i].status, results[i].body = f.postJSON(t, srv, f.feedToken, "/checkout/start", p)
		}(i, mail)
	}
	close(gate)
	wg.Wait()

	// Every started checkout is paid, as both buyers would.
	discounted := 0
	for i, r := range results {
		if r.status != http.StatusCreated {
			t.Logf("buyer %d: checkout/start = %d %s", i, r.status, r.body)
			continue
		}
		var s hostedStartResponse
		if err := json.Unmarshal(r.body, &s); err != nil {
			t.Fatal(err)
		}
		csID := uuid.MustParse(s.CheckoutSession.ID)
		cs, err := q.GetCheckoutSessionByID(ctx, csID)
		if err != nil {
			t.Fatal(err)
		}
		if cs.Discount != nil && *cs.Discount > 0 {
			discounted++
		}
		intents, err := q.ListPaymentIntentsByCheckout(ctx, csID)
		if err != nil || len(intents) != 1 || intents[0].ProviderPaymentID == nil {
			t.Fatalf("payment intent of buyer %d: %v", i, err)
		}
		paid := sessionCompletedEvent(*intents[0].ProviderPaymentID, "pi_p0ord8_"+uuid.NewString()[:8], "paid")
		if c, b := postSignedWebhookTo(t, srv, f.configWebhookPath(), paid, f.webhookSecret); c != http.StatusOK || !processed(t, b) {
			t.Fatalf("buyer %d paid event = %d; body: %s", i, c, b)
		}
	}

	var redemptions, discountedOrders int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM promo_code_redemptions WHERE promo_code_id = $1),
		(SELECT count(*) FROM orders WHERE org_id = $2 AND status = 'paid' AND promo_code_id = $1)`,
		promoID, f.orgID).Scan(&redemptions, &discountedOrders); err != nil {
		t.Fatal(err)
	}
	t.Logf("starts: %d/%d; discounted checkouts %d, paid discounted orders %d, redemptions %d",
		results[0].status, results[1].status, discounted, discountedOrders, redemptions)
	if discounted != 1 {
		t.Errorf("checkouts priced with the max_uses=1 code = %d, want exactly 1", discounted)
	}
	if discountedOrders != 1 {
		t.Errorf("paid orders carrying the code = %d, want exactly 1", discountedOrders)
	}
	if redemptions != 1 {
		t.Errorf("redemptions recorded = %d, want exactly 1", redemptions)
	}
}
