//go:build integration

package httpserver

// shop_third_pass_integration_test.go — SB-02 and SB-04 of the second pass of
// the shop audit (docs/reviews/shop_module_audit_2026-10-30_second_pass_ru.md):
// an order parked because its payment is unconfirmed does not look paid, can
// become paid (or end) when the payment is settled, and an order parked
// because its goods were gone is settled by the operator's resolve action.
// Run against a FRESH migrated and seeded database:
//
//	go test -tags integration -run TestShopSB ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// parkUnconfirmed places an order and parks its payment, and the order with
// it, the way the webhook does for an amount mismatch.
func (f *shopFixture) parkUnconfirmed(variant string, qty int) gen.ShopOrderRow {
	f.t.Helper()
	ctx := context.Background()
	o := f.placed(variant, qty)
	f.exec(`UPDATE payment_intents SET state = 'manual_review' WHERE id = $1`, *o.PaymentIntentID)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := shop.New(f.pool, nil).MarkPaymentReviewTx(ctx, tx, *o.PaymentIntentID, nil); err != nil {
		_ = tx.Rollback(ctx)
		f.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatal(err)
	}
	got, err := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// transition posts the operator's transition of a payment intent.
func (f *shopFixture) transition(pi uuid.UUID, state string) (int, map[string]any) {
	f.t.Helper()
	// The organization owner holds payment_intent.update for their own
	// organization's payments.
	code, body, _ := f.do(http.MethodPost, "/v1/payment-intents/"+pi.String()+"/transition", f.ownerA, `{"state":"`+state+`"}`)
	return code, body
}

// SB-02: an order whose payment is not confirmed is not paid anywhere.
func TestShopSB02_UnconfirmedPaymentDoesNotLookPaid(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	o := f.parkUnconfirmed(variant, 1)

	if o.Status != shop.StatusManualReview || o.PaidAt != nil || o.ReviewReason == nil || *o.ReviewReason != shop.ReviewPaymentUnconfirmed {
		t.Fatalf("parked order: status %s paid_at %v reason %v; want manual_review, no paid_at, payment_unconfirmed", o.Status, o.PaidAt, o.ReviewReason)
	}
	// The buyer is told the payment is being checked, never "paid".
	code, body, _ := f.do(http.MethodGet, "/v1/public/shop/checkout/"+o.OrderToken, "", "")
	if code != http.StatusOK || body["status"] != "pending" || body["review_reason"] != shop.ReviewPaymentUnconfirmed ||
		body["paid_at"] != nil || body["payment_url"] != "" || body["expires_at"] != nil {
		t.Fatalf("buyer's view = %d %v; want pending, review payment_unconfirmed, no paid_at, no payment page, no countdown", code, body)
	}
	if msg, _ := body["review_message"].(string); !strings.Contains(strings.ToLower(msg), "payment") && !strings.Contains(msg, "платёж") {
		t.Fatalf("review_message = %q; want a line about the payment being checked", msg)
	}
	// The manager's order carries the cause and no paid_at.
	code, body, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/orders/"+o.ID.String()), f.ownerA, "")
	if code != http.StatusOK || body["review_reason"] != shop.ReviewPaymentUnconfirmed || body["paid_at"] != nil {
		t.Fatalf("manager's order = %d %v; want review_reason payment_unconfirmed and no paid_at", code, body)
	}
	// The logistician's message does not say the order is paid.
	so := salesnotify.ShopOrder{OrgName: "Org", Order: o}
	msg := salesnotify.FormatShopOrderReview(so)
	if strings.Contains(msg, "Paid") || !strings.Contains(msg, "not confirmed") {
		t.Fatalf("telegram text for an unconfirmed payment = %q; want 'not confirmed' and no 'Paid'", msg)
	}
	// A late payment with the goods gone keeps the 'Paid' text.
	reason := shop.ReviewLatePaymentNoStock
	so.Order.ReviewReason = &reason
	if msg := salesnotify.FormatShopOrderReview(so); !strings.Contains(msg, "Paid") {
		t.Fatalf("telegram text for a late payment = %q; want the 'Paid' line", msg)
	}
	// The order list's counts and views are untouched by paid_at.
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 0 {
		t.Fatalf("paid events = %d; want 0", n)
	}
}

// SB-04: the payment of a parked order succeeds (the operator settles it): the
// order is paid like any other, with its letter and event, and the goods leave
// the shelf.
func TestShopSB04_SettledPaymentPaysTheParkedOrder(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, variant := f.openShop(4500, 3)
	o := f.parkUnconfirmed(variant, 2)

	// Shipping is refused until the payment is settled (SB-01).
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{}`); code != http.StatusConflict {
		t.Fatalf("ship before the payment is settled = %d %v; want 409", code, body)
	}
	code, body := f.transition(*o.PaymentIntentID, "succeeded")
	if code != http.StatusOK {
		t.Fatalf("transition to succeeded = %d %v", code, body)
	}
	got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if got.Status != shop.StatusPaid || got.PaidAt == nil || got.ReviewReason != nil {
		t.Fatalf("order after the payment succeeded: status %s paid_at %v reason %v; want paid with paid_at and no review reason", got.Status, got.PaidAt, got.ReviewReason)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("paid events = %d; want 1", n)
	}
	if jobs := f.emailJobs(o.ID); len(jobs) != 1 || jobs[0].Kind != shop.EmailReceived {
		t.Fatalf("e-mail jobs = %+v; want one 'received' letter", jobs)
	}
	if v := f.variant(variant); v.Stock != 1 {
		t.Fatalf("stock = %d; want 1 (two sold)", v.Stock)
	}
	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+o.OrderToken, "", "")
	if code != http.StatusOK || body["status"] != "paid" || body["paid_at"] == nil || body["review_reason"] != nil {
		t.Fatalf("buyer's view after payment = %d %v; want paid", code, body)
	}
	// The same event again changes nothing.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shop.New(f.pool, nil).FulfillPaidOrderTx(ctx, tx, *o.PaymentIntentID, nil); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("paid events after a repeat = %d; want 1", n)
	}
}

// SB-04: the payment of a parked order is settled as failed: the order ends
// and its goods are free.
func TestShopSB04_FailedPaymentEndsTheParkedOrder(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, variant := f.openShop(4500, 3)
	o := f.parkUnconfirmed(variant, 1)
	if code, body := f.transition(*o.PaymentIntentID, "failed"); code != http.StatusOK {
		t.Fatalf("transition to failed = %d %v", code, body)
	}
	got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if got.Status != shop.StatusExpired || got.PaidAt != nil {
		t.Fatalf("order after the payment failed: status %s paid_at %v; want expired with no paid_at", got.Status, got.PaidAt)
	}
	if v := f.variant(variant); v.Stock != 3 || v.Held != 0 {
		t.Fatalf("stock %d held %d; want 3 and 0", v.Stock, v.Held)
	}
}

// SB-04: payment succeeds for a parked order whose goods are gone by then: it
// is parked again as a late payment (money taken, goods gone).
func TestShopSB04_SettledPaymentWithGoodsGoneIsALatePayment(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	_, variant := f.openShop(4500, 1)
	o := f.parkUnconfirmed(variant, 1)
	f.pay(f.placed(variant, 1).OrderToken) // another buyer takes the last unit
	if code, body := f.transition(*o.PaymentIntentID, "succeeded"); code != http.StatusOK {
		t.Fatalf("transition to succeeded = %d %v", code, body)
	}
	got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if got.Status != shop.StatusManualReview || got.ReviewReason == nil || *got.ReviewReason != shop.ReviewLatePaymentNoStock || got.PaidAt == nil {
		t.Fatalf("order: status %s reason %v paid_at %v; want manual_review, late_payment_no_stock, paid_at set", got.Status, got.ReviewReason, got.PaidAt)
	}
	if n := f.outboxCount(shop.EventOrderManualReview, o.ID); n != 2 {
		t.Fatalf("manual_review events = %d; want 2 (unconfirmed, then late)", n)
	}
}

// lateParked returns an order parked as a late payment with its goods gone.
func (f *shopFixture) lateParked(variant string) gen.ShopOrderRow {
	f.t.Helper()
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)
	f.pay(f.placed(variant, 1).OrderToken)
	f.pay(late.OrderToken)
	got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
	if got.Status != shop.StatusManualReview || got.ReviewReason == nil || *got.ReviewReason != shop.ReviewLatePaymentNoStock || got.PaidAt == nil {
		f.t.Fatalf("late order: status %s reason %v paid_at %v", got.Status, got.ReviewReason, got.PaidAt)
	}
	return got
}

// SB-04: resolve fulfil pays a late order once the stock is back; a repeat with
// the same key answers the first decision.
func TestShopSB04_ResolveFulfil(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.lateParked(variant)
	path := f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/resolve")

	if code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"action":"fulfil"}`, ""); code != http.StatusBadRequest || shopErrCode(body) != "shop.idempotency_key_required" {
		t.Fatalf("resolve without a key = %d %v; want 400 shop.idempotency_key_required", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"action":"ship_it"}`); code != http.StatusBadRequest || shopErrCode(body) != "shop.invalid_action" {
		t.Fatalf("resolve with a bad action = %d %v; want 400 shop.invalid_action", code, body)
	}
	// No stock yet: refused, nothing changes.
	code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"action":"fulfil"}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.out_of_stock" {
		t.Fatalf("resolve fulfil without stock = %d %v; want 409 shop.out_of_stock", code, body)
	}
	if got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA); got.Status != shop.StatusManualReview {
		t.Fatalf("order after the refused resolve = %s; want manual_review", got.Status)
	}

	f.exec(`UPDATE shop_variants SET stock = stock + 1 WHERE id = $1`, variant)
	key := uuid.NewString()
	code, body, _ = f.doKeyed(http.MethodPost, path, f.ownerA, `{"action":"fulfil"}`, key)
	if code != http.StatusOK || body["action"] != "fulfil" || body["replayed"] != false || body["refund"] != nil ||
		body["order"].(map[string]any)["status"] != shop.StatusPaid {
		t.Fatalf("resolve fulfil = %d %v; want 200, order paid", code, body)
	}
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after fulfil = %d; want 0", v.Stock)
	}
	if n := f.outboxCount(shop.EventOrderPaid, late.ID); n != 1 {
		t.Fatalf("paid events = %d; want 1", n)
	}
	if jobs := f.emailJobs(late.ID); len(jobs) != 1 || jobs[0].Kind != shop.EmailReceived {
		t.Fatalf("e-mail jobs = %+v; want one 'received' letter", jobs)
	}
	// The same key again: the first decision, nothing written.
	code, body, _ = f.doKeyed(http.MethodPost, path, f.ownerA, `{"action":"fulfil"}`, key)
	if code != http.StatusOK || body["replayed"] != true {
		t.Fatalf("repeat with the same key = %d %v; want 200 replayed", code, body)
	}
	if n := f.outboxCount(shop.EventOrderPaid, late.ID); n != 1 {
		t.Fatalf("paid events after the repeat = %d; want 1", n)
	}
	// Another key on an order that is no longer parked.
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"action":"fulfil"}`); code != http.StatusConflict || shopErrCode(body) != "shop.order_not_in_review" {
		t.Fatalf("resolve of a paid order = %d %v; want 409 shop.order_not_in_review", code, body)
	}
	// It ships now.
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/ship"), f.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("ship after fulfil = %d %v", code, body)
	}
}

// SB-04: resolve refund_and_cancel returns the whole payment through the refund
// engine and the order ends refunded; a repeat answers the first refund.
func TestShopSB04_ResolveRefundAndCancel(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.lateParked(variant)
	path := f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/resolve")

	key := uuid.NewString()
	code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"action":"refund_and_cancel"}`, key)
	if code != http.StatusOK || body["action"] != "refund_and_cancel" || body["replayed"] != false {
		t.Fatalf("resolve refund_and_cancel = %d %v", code, body)
	}
	refund, _ := body["refund"].(map[string]any)
	if refund == nil || refund["amount"].(float64) != float64(late.Total) {
		t.Fatalf("refund = %v; want the whole order total %d", refund, late.Total)
	}
	if st := body["order"].(map[string]any)["status"]; st != shop.StatusRefunded {
		t.Fatalf("order after the refund = %v; want refunded", st)
	}
	code, body, _ = f.doKeyed(http.MethodPost, path, f.ownerA, `{"action":"refund_and_cancel"}`, key)
	if code != http.StatusOK || body["replayed"] != true {
		t.Fatalf("repeat with the same key = %d %v; want 200 replayed", code, body)
	}
	if n, _ := f.refundRows(*late.PaymentIntentID); n != 1 {
		t.Fatalf("refund rows = %d; want 1", n)
	}
}

// SB-04: an order parked for its payment has no goods decision to make.
func TestShopSB04_ResolveRefusesAnUnconfirmedPayment(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	o := f.parkUnconfirmed(variant, 1)
	path := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/resolve")
	for _, action := range []string{"fulfil", "refund_and_cancel"} {
		code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"action":"`+action+`"}`)
		if code != http.StatusConflict || shopErrCode(body) != "shop.order_not_resolvable" {
			t.Fatalf("resolve %s of an unconfirmed payment = %d %v; want 409 shop.order_not_resolvable", action, code, body)
		}
	}
	// Another organization's order is the route's own 404.
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgB, "/orders/"+o.ID.String()+"/resolve"), f.ownerB, `{"action":"fulfil"}`); code != http.StatusNotFound {
		t.Fatalf("resolve of a foreign order = %d %v; want 404", code, body)
	}
}
