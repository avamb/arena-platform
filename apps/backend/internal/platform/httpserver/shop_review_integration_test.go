//go:build integration

package httpserver

// shop_review_integration_test.go — regressions found in the review of the
// shop core (PR #275): the last-unit race, the late payment of an expired
// order, the checkout limits and the switch that opens the storefront.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
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

// expireOrder pushes an order past its hold and runs the expiry sweep.
func (f *shopFixture) expireOrder(id uuid.UUID) {
	f.t.Helper()
	f.exec(`UPDATE shop_orders SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	if err := shop.NewSweepHandler(shop.SweepOptions{Service: shop.New(f.pool, nil)})(context.Background(), nil); err != nil {
		f.t.Fatalf("sweep: %v", err)
	}
}

func (f *shopFixture) placed(variantID string, qty int) gen.ShopOrderRow {
	f.t.Helper()
	code, body := f.checkout(variantID, qty)
	if code != http.StatusCreated {
		f.t.Fatalf("checkout = %d %v", code, body)
	}
	o, err := f.q.GetShopOrderByToken(context.Background(), body["order_token"].(string))
	if err != nil {
		f.t.Fatalf("order of token: %v", err)
	}
	return o
}

// TestShop_LatePaymentForGoneGoodsIsParked: an order expires, another buyer
// takes the last unit, and then the first buyer's payment arrives. The order
// is parked in manual_review with the payment recorded and the stock left
// alone; a full refund closes it.
func TestShop_LatePaymentForGoneGoodsIsParked(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)

	other := f.placed(variant, 1)
	f.pay(other.OrderToken)
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after the second buyer paid = %d; want 0", v.Stock)
	}

	f.pay(late.OrderToken)
	got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
	if got.Status != shop.StatusManualReview || got.PaidAt == nil {
		t.Fatalf("late order status %s paid_at %v; want manual_review with paid_at", got.Status, got.PaidAt)
	}
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after the late payment = %d; want 0 (never oversold)", v.Stock)
	}
	if n := f.outboxCount(shop.EventOrderManualReview, late.ID); n != 1 {
		t.Fatalf("manual_review events = %d; want 1", n)
	}
	if n := f.outboxCount(shop.EventOrderPaid, late.ID); n != 0 {
		t.Fatalf("paid events for the parked order = %d; want 0", n)
	}
	f.pay2xIdempotent(late.OrderToken)
	if n := f.outboxCount(shop.EventOrderManualReview, late.ID); n != 1 {
		t.Fatalf("manual_review events after a replay = %d; want 1", n)
	}
	code, body, _ := f.do(http.MethodGet, "/v1/public/shop/checkout/"+late.OrderToken, "", "")
	if code != http.StatusOK || body["status"] != "paid" {
		t.Fatalf("buyer's view of the parked order = %d %v; want paid", code, body)
	}

	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/refund"), f.ownerA, ``)
	if code != http.StatusOK {
		t.Fatalf("refund of the parked order = %d %v", code, body)
	}
	if st := body["order"].(map[string]any)["status"]; st != shop.StatusRefunded {
		t.Fatalf("parked order after a full refund = %v; want refunded", st)
	}
}

// TestShop_LatePaymentWhileGoodsRemainIsPaid: the order expired but the goods
// are still on the shelf, so its late payment pays it as usual.
func TestShop_LatePaymentWhileGoodsRemainIsPaid(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)
	f.pay(late.OrderToken)

	got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
	if got.Status != shop.StatusPaid {
		t.Fatalf("late order status = %s; want paid", got.Status)
	}
	if !strings.Contains(strings.ReplaceAll(string(got.Metadata), " ", ""), `"paid_after":"expired"`) {
		t.Fatalf("metadata = %s; want paid_after expired", got.Metadata)
	}
	if v := f.variant(variant); v.Stock != 1 {
		t.Fatalf("stock = %d; want 1", v.Stock)
	}
}

// TestShop_ManualReviewOrderShippedTakesTheGoods: the operator found the
// goods for a parked order and ships it; they come off the shelf then.
func TestShop_ManualReviewOrderShippedTakesTheGoods(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)
	f.pay(f.placed(variant, 1).OrderToken)
	f.pay(late.OrderToken)

	f.exec(`UPDATE shop_variants SET stock = stock + 1 WHERE id = $1`, variant)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/ship"), f.ownerA, `{"tracking_note":"found one"}`)
	if code != http.StatusOK || body["status"] != shop.StatusShipped {
		t.Fatalf("ship of the parked order = %d %v", code, body)
	}
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after shipping the parked order = %d; want 0", v.Stock)
	}
}

// TestShop_CancelClosesTheHostedPage: cancelling a pending order asks Stripe
// to expire its checkout session.
func TestShop_CancelClosesTheHostedPage(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	o := f.placed(variant, 1)
	pi, err := f.q.GetPaymentIntentByID(context.Background(), *o.PaymentIntentID)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/cancel"), f.ownerA, "")
	if code != http.StatusOK {
		t.Fatalf("cancel = %d %v", code, body)
	}
	expired := f.stripe.expired()
	if len(expired) != 1 || expired[0] != *pi.ProviderPaymentID {
		t.Fatalf("expired sessions = %v; want [%s]", expired, *pi.ProviderPaymentID)
	}
}

// TestShop_SweepLeavesAProcessingPaymentAlone: an asynchronous payment still
// on its way keeps its order pending past the hold.
func TestShop_SweepLeavesAProcessingPaymentAlone(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	o := f.placed(variant, 1)
	f.exec(`UPDATE payment_intents SET state = 'processing' WHERE id = $1`, *o.PaymentIntentID)
	f.expireOrder(o.ID)
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusPendingPayment {
		t.Fatalf("status after the sweep = %s; want pending_payment", got.Status)
	}
}

// TestShop_VariantsRefuseAStaleStock: the editor sends the stock it loaded;
// once a sale moved it, the write is refused as a whole.
func TestShop_VariantsRefuseAStaleStock(t *testing.T) {
	f := newShopFixture(t)
	product, variant := f.openShop(4500, 5)
	path := f.orgPath(f.orgA, "/products/"+product+"/variants")

	code, body, _ := f.do(http.MethodPut, path, f.ownerA,
		`{"variants":[{"id":"`+variant+`","price_minor":4600,"stock":6,"expected_stock":5}]}`)
	if code != http.StatusOK {
		t.Fatalf("write with the loaded stock = %d %v", code, body)
	}
	f.pay(f.placed(variant, 2).OrderToken)

	code, body, _ = f.do(http.MethodPut, path, f.ownerA,
		`{"variants":[{"id":"`+variant+`","price_minor":9900,"stock":10,"expected_stock":6}]}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.stock_changed" {
		t.Fatalf("write with a stale stock = %d %v; want 409 shop.stock_changed", code, body)
	}
	if v := f.variant(variant); v.Stock != 4 || v.PriceMinor != 4600 {
		t.Fatalf("variant after the refused write: stock %d price %d; want 4 and 4600", v.Stock, v.PriceMinor)
	}
	if code, body, _ := f.do(http.MethodPut, path, f.ownerA,
		`{"variants":[{"price_minor":100,"stock":1,"expected_stock":1}]}`); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
		t.Fatalf("expected_stock without an id = %d %v; want a validation error", code, body)
	}
}
