//go:build integration

// shop_payment_integration_test.go — a merchandise shop payment (migration
// 0172) has no checkout session, so the "payment succeeded but checkout not
// completed" check must judge it by its shop order: a paid order is complete,
// an order still pending (or expired) behind a succeeded payment is the very
// condition the check exists for.
package opswatchdog_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

func TestPaymentNotCompleted_LiveDB_ShopPaymentJudgedByItsOrder(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()
	f := newWDFixture(t, ctx, pool)
	defer f.cleanup()

	piID, orderID := uuid.New(), uuid.New()
	token := hex.EncodeToString(append(orderID[:], piID[:]...))
	fingerprint := "payment_not_completed:" + piID.String()
	defer cleanupOpsAlerts(t, ctx, pool, fingerprint)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM shop_orders WHERE id = $1`, orderID)
	}()

	if _, err := pool.Exec(ctx, `INSERT INTO payment_intents (id, org_id, provider, provider_payment_id, amount, currency, state, succeeded_at)
		VALUES ($1, $2, 'stripe', $3, 14000, 'ILS', 'succeeded', now() - interval '1 hour')`,
		piID, f.orgID, "cs_wd_shop_"+piID.String()[:8]); err != nil {
		t.Fatalf("insert payment intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO shop_orders (id, org_id, order_token, status, currency, subtotal, shipping_fee, total, payment_intent_id, paid_at)
		VALUES ($1, $2, $3, 'paid', 'ILS', 9000, 5000, 14000, $4, now() - interval '1 hour')`,
		orderID, f.orgID, token, piID); err != nil {
		t.Fatalf("insert shop order: %v", err)
	}

	handler := opswatchdog.NewHandler(opswatchdog.Options{Pool: pool, Notifier: &recordingNotifier{}, Logger: quietTestLogger()})
	store := opswatchdog.NewPGAlertStore(pool)

	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if row, err := store.Get(ctx, fingerprint); err != nil || (row != nil && row.ResolvedAt == nil) {
		t.Fatalf("a paid shop order raised payment_not_completed: %+v (%v)", row, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE shop_orders SET status = 'expired' WHERE id = $1`, orderID); err != nil {
		t.Fatalf("expire order: %v", err)
	}
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	row, err := store.Get(ctx, fingerprint)
	if err != nil || row == nil || row.ResolvedAt != nil {
		t.Fatalf("a succeeded payment behind an expired shop order raised nothing: %+v (%v)", row, err)
	}
}

// TestManualReview_LiveDB_ShopOrderParkedForReview: a late shop payment whose
// goods were gone parks the order in manual_review, and the watchdog alerts
// on it like on a parked ticket order.
func TestManualReview_LiveDB_ShopOrderParkedForReview(t *testing.T) {
	pool := connectWatchdogTestDB(t)
	ctx := context.Background()
	f := newWDFixture(t, ctx, pool)
	defer f.cleanup()

	orderID := uuid.New()
	token := hex.EncodeToString(append(orderID[:], orderID[:]...))
	fingerprint := "manual_review:shop_order:" + orderID.String()
	defer cleanupOpsAlerts(t, ctx, pool, fingerprint)
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM shop_orders WHERE id = $1`, orderID)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO shop_orders (id, org_id, order_token, status, currency, subtotal, shipping_fee, total, paid_at)
		VALUES ($1, $2, $3, 'manual_review', 'ILS', 9000, 5000, 14000, now())`,
		orderID, f.orgID, token); err != nil {
		t.Fatalf("insert shop order: %v", err)
	}

	handler := opswatchdog.NewHandler(opswatchdog.Options{Pool: pool, Notifier: &recordingNotifier{}, Logger: quietTestLogger()})
	if err := handler(ctx, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	row, err := opswatchdog.NewPGAlertStore(pool).Get(ctx, fingerprint)
	if err != nil || row == nil || row.ResolvedAt != nil {
		t.Fatalf("a shop order in manual_review raised no alert: %+v (%v)", row, err)
	}
}
