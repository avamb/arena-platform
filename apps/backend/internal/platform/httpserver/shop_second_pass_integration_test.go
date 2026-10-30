//go:build integration

package httpserver

// shop_second_pass_integration_test.go — findings of the second pass of the
// shop audit (docs/reviews/shop_module_audit_2026-10-30_second_pass_ru.md),
// through the real router and a live database. Run against a FRESH migrated
// and seeded database:
//
//	go test -tags integration -run TestShopSecondPass ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// SB-01: an order parked because its PAYMENT waits for a human (SA-09) is not
// shipped while that payment is unsettled; once the payment succeeded, the
// operator ships it and the goods leave the shelf.
func TestShopSecondPass_PaymentReviewOrderShipsOnlyAfterThePaymentSucceeded(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 1)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	o, err := f.q.GetShopOrderByToken(ctx, body["order_token"].(string))
	if err != nil || o.PaymentIntentID == nil {
		t.Fatalf("order: %v", err)
	}
	// The webhook parked the payment and, with it, the order.
	f.exec(`UPDATE payment_intents SET state = 'manual_review' WHERE id = $1`, *o.PaymentIntentID)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shop.New(f.pool, nil).MarkPaymentReviewTx(ctx, tx, *o.PaymentIntentID, nil); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA); got.Status != shop.StatusManualReview {
		t.Fatalf("order status = %s; want manual_review", got.Status)
	}

	ship := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship")
	code, body, _ = f.do(http.MethodPost, ship, f.ownerA, `{}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.payment_unconfirmed" {
		t.Fatalf("ship with the payment in review = %d %v; want 409 shop.payment_unconfirmed", code, body)
	}
	if v := f.variant(variant); v.Stock != 3 {
		t.Fatalf("stock = %d; want 3 (nothing shipped)", v.Stock)
	}
	if n := f.outboxCount(shop.EventOrderShipped, o.ID); n != 0 {
		t.Fatalf("shipped events = %d; want 0", n)
	}

	// The operator settled the payment: now the goods may go.
	f.exec(`UPDATE payment_intents SET state = 'succeeded' WHERE id = $1`, *o.PaymentIntentID)
	code, body, _ = f.do(http.MethodPost, ship, f.ownerA, `{"tracking_note":"TRK2"}`)
	if code != http.StatusOK || body["status"] != shop.StatusShipped {
		t.Fatalf("ship after the payment succeeded = %d %v; want 200 shipped", code, body)
	}
	if v := f.variant(variant); v.Stock != 2 {
		t.Fatalf("stock = %d; want 2", v.Stock)
	}
}
