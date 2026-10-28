//go:build integration

package httpserver

// shop_review_integration_test.go — regressions found in the review of the
// shop core (PR #275): the last-unit race, the late payment of an expired
// order, the checkout limits and the switch that opens the storefront.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hshop"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
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

// checkoutAs places a one-line cart for the given buyer e-mail.
func (f *shopFixture) checkoutAs(variantID string, qty int, email string) (int, map[string]any) {
	f.t.Helper()
	code, body, _ := f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
		"items":[{"variant_id":%q,"qty":%d}],
		"buyer":{"name":"Anna","email":%q},
		"ship":{"address1":"Herzl 1","city":"Haifa","country":"IL"},
		"return_url":"%s/checkout"}`, variantID, qty, email, shopTestBase))
	return code, body
}

// TestShop_CheckoutLimits: the per-line cap from the settings and the cap on
// one buyer's orders waiting for payment.
func TestShop_CheckoutLimits(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 100)

	if code, body := f.checkout(variant, shop.DefaultMaxLineQty+1); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_items" {
		t.Fatalf("qty over the default cap = %d %v; want 422 shop.invalid_items", code, body)
	}
	code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		`{"currency":"ILS","shipping_fee_minor":5000,"max_line_qty":2}`)
	if code != http.StatusOK || body["max_line_qty"].(float64) != 2 {
		t.Fatalf("PUT settings with max_line_qty = %d %v", code, body)
	}
	if code, body := f.checkout(variant, 3); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_items" {
		t.Fatalf("qty over the shop's cap = %d %v; want 422", code, body)
	}
	if code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA,
		`{"currency":"ILS","max_line_qty":100}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("max_line_qty 100 = %d %v; want 422", code, body)
	}

	email := "same-" + uuid.NewString()[:6] + "@example.test"
	for i := 0; i < shop.MaxPendingPerBuyer; i++ {
		if code, body := f.checkoutAs(variant, 1, strings.ToUpper(email[:1])+email[1:]); code != http.StatusCreated {
			t.Fatalf("pending order %d = %d %v", i+1, code, body)
		}
	}
	if code, body := f.checkoutAs(variant, 1, email); code != http.StatusConflict || shopErrCode(body) != "shop.too_many_pending" {
		t.Fatalf("one pending order too many = %d %v; want 409 shop.too_many_pending", code, body)
	}
	// An abandoned page stops counting as soon as its hold ends.
	f.exec(`UPDATE shop_orders SET expires_at = now() - interval '1 second'
	        WHERE id = (SELECT id FROM shop_orders WHERE org_id = $1 AND lower(buyer_email) = lower($2) LIMIT 1)`, f.orgA, email)
	if code, body := f.checkoutAs(variant, 1, email); code != http.StatusCreated {
		t.Fatalf("checkout after one hold ended = %d %v; want 201", code, body)
	}
}

// TestShop_CheckoutHasItsOwnAddressBudget: an address may send
// DefaultCheckoutIPLimit well-formed checkouts a minute to one shop; a body
// that is not JSON is not counted, and another shop has its own budget.
func TestShop_CheckoutHasItsOwnAddressBudget(t *testing.T) {
	f := newShopFixture(t)
	path := "/v1/public/shop/" + f.orgASlug + "/checkout"
	for i := 0; i < 5; i++ {
		if code, _, _ := f.do(http.MethodPost, path, "", `not json`); code != http.StatusBadRequest {
			t.Fatalf("broken body = %d; want 400", code)
		}
	}
	for i := 0; i < hshop.DefaultCheckoutIPLimit; i++ {
		if code, body, _ := f.do(http.MethodPost, path, "", `{}`); code == http.StatusTooManyRequests {
			t.Fatalf("checkout %d refused inside the budget: %v", i+1, body)
		}
	}
	code, body, hdr := f.do(http.MethodPost, path, "", `{}`)
	if code != http.StatusTooManyRequests || shopErrCode(body) != "shop.rate_limited" || hdr.Get("Retry-After") == "" {
		t.Fatalf("checkout over the budget = %d %v; want 429 shop.rate_limited with Retry-After", code, body)
	}
	if code, _, _ := f.do(http.MethodPost, "/v1/public/shop/another-"+f.orgASlug+"/checkout", "", `{}`); code == http.StatusTooManyRequests {
		t.Fatal("another shop shares the budget")
	}
	// The catalog is not on the checkout's budget.
	if code, _, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", ""); code == http.StatusTooManyRequests {
		t.Fatal("the catalog was refused by the checkout's budget")
	}
}

// TestShop_StorefrontOpensOnlyWhenEnabled: saved settings alone do not open
// the storefront; the owner cannot flip the switch.
func TestShop_StorefrontOpensOnlyWhenEnabled(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	f.exec(`UPDATE shop_settings SET enabled = false WHERE org_id = $1`, f.orgA)

	if code, body, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", ""); code != http.StatusNotFound || shopErrCode(body) != "shop.not_found" {
		t.Fatalf("catalog of a closed shop = %d %v; want 404 shop.not_found", code, body)
	}
	if code, body := f.checkout(variant, 1); code != http.StatusNotFound {
		t.Fatalf("checkout in a closed shop = %d %v; want 404", code, body)
	}
	code, body, _ := f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA, `{"currency":"ILS","enabled":true}`)
	if code != http.StatusForbidden || shopErrCode(body) != "shop.enabled_superadmin_only" {
		t.Fatalf("owner opening the shop = %d %v; want 403 shop.enabled_superadmin_only", code, body)
	}
	// Repeating the current value is not a change.
	code, body, _ = f.do(http.MethodPut, f.orgPath(f.orgA, "/settings"), f.ownerA, `{"currency":"ILS","enabled":false}`)
	if code != http.StatusOK || body["enabled"] != false {
		t.Fatalf("owner saving with the current switch = %d %v", code, body)
	}
	f.exec(`UPDATE shop_settings SET enabled = true WHERE org_id = $1`, f.orgA)
	if code, _, _ := f.do(http.MethodGet, "/v1/public/shop/"+f.orgASlug, "", ""); code != http.StatusOK {
		t.Fatalf("catalog of an open shop = %d; want 200", code)
	}
}

// TestShop_CheckoutResolvesTheCustomer: the order carries the buyer's
// customer, linked to the organization.
func TestShop_CheckoutResolvesTheCustomer(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	o := f.placed(variant, 1)
	if o.CustomerID == nil {
		t.Fatal("the order has no customer")
	}
	var linked int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM customer_org_links
		WHERE customer_id = $1 AND org_id = $2`, *o.CustomerID, f.orgA).Scan(&linked); err != nil || linked != 1 {
		t.Fatalf("customer_org_links = %d (%v); want 1", linked, err)
	}
	var email string
	if err := f.pool.QueryRow(context.Background(), `SELECT value_normalized FROM customer_identities
		WHERE customer_id = $1 AND kind = 'email'`, *o.CustomerID).Scan(&email); err != nil || !strings.EqualFold(email, o.BuyerEmail) {
		t.Fatalf("customer e-mail identity = %q (%v); want %q", email, err, o.BuyerEmail)
	}
}

// TestShop_PaymentFallsBackToAChannelThatCanTakeIt: the settings name no
// channel and the organization's oldest channel is on a provider that is not
// set up; the checkout takes the next channel that can host a payment page.
func TestShop_PaymentFallsBackToAChannelThatCanTakeIt(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 2)
	f.exec(`INSERT INTO sales_channels (id, org_id, name, provider, created_at)
	        VALUES ($1, $2, 'Old Flitt channel', 'flitt', now() - interval '1 year')`, uuid.New(), f.orgA)
	code, body := f.checkout(variant, 1)
	if code != http.StatusCreated || body["payment_url"] == "" {
		t.Fatalf("checkout with an unconfigured oldest channel = %d %v; want 201 with a payment page", code, body)
	}
}

// TestShop_PendingRefundCountsOnlyOnceItSucceeds: a refund the provider
// accepted as pending changes nothing on the order; when it later succeeds
// (here through the operator's resolution, which settles it like the sweep
// does) the refund engine projects it onto the order.
func TestShop_PendingRefundCountsOnlyOnceItSucceeds(t *testing.T) {
	f := newShopFixture(t)
	f.stripe.refundHandler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"re_pending_%s","status":"pending"}`, uuid.NewString()[:8])
	}
	_, variant := f.openShop(4500, 3)
	o := f.placed(variant, 2)
	f.pay(o.OrderToken)

	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund"), f.ownerA, `{"amount_minor":5000}`)
	if code != http.StatusOK {
		t.Fatalf("refund = %d %v", code, body)
	}
	order := body["order"].(map[string]any)
	if order["status"] != shop.StatusPaid || order["refunded_minor"].(float64) != 0 {
		t.Fatalf("order with a pending refund: status %v refunded %v; want paid and 0", order["status"], order["refunded_minor"])
	}
	refundID := uuid.MustParse(body["refund"].(map[string]any)["id"].(string))

	// The provider never confirmed it; the operator checked the dashboard.
	f.exec(`UPDATE refunds SET state = 'manual_review' WHERE id = $1`, refundID)
	if _, err := f.srv.refundEngine().Resolve(context.Background(), refunds.ResolveInput{
		OrgID: f.orgA, RefundID: refundID, Outcome: refunds.ResolveRefunded,
		Actor: refunds.Actor{Type: "user", ID: uuid.NewString()}, Via: "test", Note: "Stripe dashboard shows the refund succeeded",
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusPartiallyRefunded || got.RefundedMinor != 5000 {
		t.Fatalf("order after the refund succeeded: status %s refunded %d; want partially_refunded and 5000", got.Status, got.RefundedMinor)
	}
	if n := f.outboxCount(shop.EventOrderRefunded, o.ID); n != 1 {
		t.Fatalf("refunded events = %d; want 1", n)
	}
	// Reading the order writes nothing.
	f.exec(`UPDATE shop_orders SET updated_at = '2020-01-01' WHERE id = $1`, o.ID)
	if code, _, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/orders/"+o.ID.String()), f.ownerA, ""); code != http.StatusOK {
		t.Fatalf("GET order = %d", code)
	}
	again, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if again.UpdatedAt.Year() != 2020 {
		t.Fatalf("GET order wrote the order (updated_at %v)", again.UpdatedAt)
	}
}

// TestShop_ReversedRefundReturnsTheOrder: a succeeded refund the provider
// later reverses no longer counts; the projection brings the order back.
func TestShop_ReversedRefundReturnsTheOrder(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	o := f.placed(variant, 1)
	f.pay(o.OrderToken)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund"), f.ownerA, ``)
	if code != http.StatusOK || body["order"].(map[string]any)["status"] != shop.StatusRefunded {
		t.Fatalf("full refund = %d %v", code, body)
	}
	f.exec(`UPDATE refunds SET state = 'manual_review' WHERE payment_intent_id = $1`, *o.PaymentIntentID)

	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := shop.New(f.pool, nil).ProjectPaymentTx(ctx, tx, *o.PaymentIntentID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("project: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got, _ := f.q.GetShopOrder(ctx, o.ID, f.orgA)
	if got.Status != shop.StatusPaid || got.RefundedMinor != 0 {
		t.Fatalf("order after the reversal: status %s refunded %d; want paid and 0", got.Status, got.RefundedMinor)
	}
}

// TestShop_RefundNeedsTheRefundPermissions: shop.manage alone does not
// refund; the refund permissions a ticket refund needs are required too.
func TestShop_RefundNeedsTheRefundPermissions(t *testing.T) {
	f := newShopFixture(t)
	ctx := context.Background()
	owner, err := f.q.InsertUser(ctx, "shop-keyowner-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	f.users = append(f.users, owner.ID)
	_, key, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(f.q), apikeys.IssueInput{
		OrgID: f.orgA, Name: "shop-only-" + uuid.NewString()[:6], Scopes: []string{"shop.read", "shop.manage"}, CreatedBy: owner.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+uuid.NewString()+"/refund"), key, `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("refund with shop.manage only = %d %v; want 403", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+uuid.NewString()+"/ship"), key, `{}`); code != http.StatusNotFound {
		t.Fatalf("ship with shop.manage = %d %v; want the route's own 404", code, body)
	}
}

// shopMedia inserts a media object of organization A with the given owner type.
func (f *shopFixture) shopMedia(ownerType string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	f.exec(`INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
	        VALUES ($1, $2, $3, 'local', $4, 'application/pdf', 10, repeat('b', 64))`, id, f.orgA, ownerType, "shop-test/"+id.String()+".pdf")
	return id
}

// TestShop_DigitalProductCheckoutAndDownloads: spec 47 section 1b. A digital
// product sells a shop_digital file, never tracks stock, needs no address and
// no shipping; its file is offered only once the order is paid.
func TestShop_DigitalProductCheckoutAndDownloads(t *testing.T) {
	f := newShopFixture(t)
	f.openShop(4500, 3) // settings with a 5000 shipping fee, storefront open
	file, picture := f.shopMedia(shop.DigitalOwnerType), f.shopMedia(shop.PictureOwnerType)

	// A picture is never a paid file, a paid file never a picture.
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		`{"title":"Tasting notes %s","status":"active","kind":"digital","digital_media_id":%q}`, uuid.NewString()[:6], picture))
	if code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_digital_media" {
		t.Fatalf("picture as the paid file = %d %v; want 422 shop.invalid_digital_media", code, body)
	}
	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		`{"title":"Tasting notes %s","status":"active","kind":"digital","digital_media_id":%q,
		  "option_names":["Language"],
		  "variants":[{"title":"English","price_minor":1500,"stock":7,"option1":"en"}]}`, uuid.NewString()[:6], file))
	if code != http.StatusCreated {
		t.Fatalf("POST digital product = %d %v", code, body)
	}
	productID := body["id"].(string)
	if body["kind"] != shop.KindDigital || body["digital_media_id"] != file.String() ||
		len(body["option_names"].([]any)) != 1 || body["option_names"].([]any)[0] != "Language" {
		t.Fatalf("digital product = %v", body)
	}
	v := body["variants"].([]any)[0].(map[string]any)
	variantID := v["id"].(string)
	if v["track_stock"] != false || v["available"] != nil {
		t.Fatalf("digital variant tracks stock: %v", v)
	}
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products/"+productID+"/images"), f.ownerA,
		fmt.Sprintf(`{"media_id":%q}`, file)); code != http.StatusUnprocessableEntity {
		t.Fatalf("paid file as a picture = %d %v; want 422", code, body)
	}

	// The storefront sees the kind and the option names, never the file.
	slug := body["slug"]
	code, body, _ = f.do(http.MethodGet, fmt.Sprintf("/v1/public/shop/%s/products/%s", f.orgASlug, slug), "", "")
	if code != http.StatusOK {
		t.Fatalf("public product = %d %v", code, body)
	}
	pub := body["product"].(map[string]any)
	if pub["kind"] != shop.KindDigital || pub["option_names"].([]any)[0] != "Language" {
		t.Fatalf("public product = %v", pub)
	}
	if _, leaked := pub["digital_media_id"]; leaked {
		t.Fatal("the storefront sees the paid file")
	}

	// Checkout without an address and without shipping.
	code, body, _ = f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
		"items":[{"variant_id":%q,"qty":2}],
		"buyer":{"name":"Anna","email":"digital-%s@example.test"},
		"return_url":"%s/checkout"}`, variantID, uuid.NewString()[:6], shopTestBase))
	if code != http.StatusCreated || body["shipping_fee"].(float64) != 0 || body["total"].(float64) != 3000 || body["delivery_kind"] != "none" {
		t.Fatalf("digital checkout = %d %v; want 201 with no shipping, total 3000, delivery_kind none", code, body)
	}
	token := body["order_token"].(string)
	o, _ := f.q.GetShopOrderByToken(context.Background(), token)
	if o.DeliveryKind != "none" || o.ShipAddress1 != "" {
		t.Fatalf("digital order delivery %q address %q; want none and empty", o.DeliveryKind, o.ShipAddress1)
	}
	var moves int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM shop_stock_moves WHERE variant_id = $1`, variantID).Scan(&moves); err != nil || moves != 0 {
		t.Fatalf("stock moves of a digital variant = %d (%v); want 0", moves, err)
	}

	downloads := "/v1/public/shop/checkout/" + token + "/downloads"
	if code, body, _ := f.do(http.MethodGet, downloads, "", ""); code != http.StatusNotFound {
		t.Fatalf("downloads before payment = %d %v; want 404", code, body)
	}
	f.pay(token)
	code, body, hdr := f.do(http.MethodGet, downloads, "", "")
	if code != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("downloads after payment = %d %v", code, body)
	}
	list := body["downloads"].([]any)
	if len(list) != 1 {
		t.Fatalf("downloads = %v; want one file", list)
	}
	d := list[0].(map[string]any)
	if d["product_title"] == "" || !strings.Contains(d["url"].(string), "/v1/media-files/"+file.String()) {
		t.Fatalf("download = %v", d)
	}
	if v := f.variant(variantID); v.Stock != 0 || v.TrackStock {
		t.Fatalf("digital variant after the sale: stock %d track %v; want 0 and false", v.Stock, v.TrackStock)
	}
}

// TestShop_MixedCartNeedsAnAddress: one physical line is enough to need the
// address and the shipping fee.
func TestShop_MixedCartNeedsAnAddress(t *testing.T) {
	f := newShopFixture(t)
	_, physical := f.openShop(4500, 3)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		`{"title":"Workshop %s","status":"active","kind":"service","variants":[{"price_minor":2000,"track_stock":false}]}`, uuid.NewString()[:6]))
	if code != http.StatusCreated {
		t.Fatalf("POST service = %d %v", code, body)
	}
	service := body["variants"].([]any)[0].(map[string]any)["id"].(string)
	cart := func(withShip bool) (int, map[string]any) {
		ship := ""
		if withShip {
			ship = `"ship":{"address1":"Herzl 1","city":"Haifa","country":"IL"},`
		}
		code, body, _ := f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(`{
			"items":[{"variant_id":%q,"qty":1},{"variant_id":%q,"qty":1}],
			"buyer":{"name":"Anna","email":"mixed-%s@example.test"},%s
			"return_url":"%s/checkout"}`, physical, service, uuid.NewString()[:6], ship, shopTestBase))
		return code, body
	}
	if code, body := cart(false); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.invalid_address" {
		t.Fatalf("mixed cart without an address = %d %v; want 422 shop.invalid_address", code, body)
	}
	code, body = cart(true)
	if code != http.StatusCreated || body["shipping_fee"].(float64) != 5000 || body["total"].(float64) != 11500 {
		t.Fatalf("mixed cart = %d %v; want 201 with shipping 5000 and total 11500", code, body)
	}
}

// TestShop_PaymentAtTheHoldBoundaryNeverOversells: the order's hold ends on
// the database clock (expires_at = now()), another buyer takes and pays the
// last unit, then the first payment arrives. It is parked, never sold twice.
func TestShop_PaymentAtTheHoldBoundaryNeverOversells(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.placed(variant, 1)
	f.exec(`UPDATE shop_orders SET expires_at = now() WHERE id = $1`, late.ID)
	f.pay(f.placed(variant, 1).OrderToken)
	f.pay(late.OrderToken)
	got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA)
	if got.Status != shop.StatusManualReview {
		t.Fatalf("payment at the boundary: status %s; want manual_review", got.Status)
	}
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock = %d; want 0", v.Stock)
	}
}

// TestShop_SameCallbackTwiceAtOnce: two deliveries of one paid callback race;
// the order is paid once and its goods leave the shelf once.
func TestShop_SameCallbackTwiceAtOnce(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	o := f.placed(variant, 2)
	pi, err := f.q.GetPaymentIntentByID(context.Background(), *o.PaymentIntentID)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	event := sessionCompletedEvent(*pi.ProviderPaymentID, "pi_shop_race_"+uuid.NewString()[:8], "paid")
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = postSignedWebhookTo(t, f.srv, "/v1/payment-intents/webhook/"+f.configA.String(), event, f.webhookSecretA)
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c >= 500 {
			t.Fatalf("delivery %d = %d", i, c)
		}
	}
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusPaid {
		t.Fatalf("status = %s; want paid", got.Status)
	}
	if n := f.outboxCount(shop.EventOrderPaid, o.ID); n != 1 {
		t.Fatalf("paid events = %d; want 1", n)
	}
	if v := f.variant(variant); v.Stock != 1 {
		t.Fatalf("stock = %d; want 1", v.Stock)
	}
}

// TestShop_ShipOfAParkedOrderNeedsFreeGoods: a parked order cannot take the
// unit another pending order holds; once stock is added it ships.
func TestShop_ShipOfAParkedOrderNeedsFreeGoods(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 1)
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)
	holder := f.placed(variant, 1) // pending, holds the last unit
	f.pay(late.OrderToken)
	if got, _ := f.q.GetShopOrder(context.Background(), late.ID, f.orgA); got.Status != shop.StatusManualReview {
		t.Fatalf("late order = %s; want manual_review", got.Status)
	}
	ship := f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/ship")
	code, body, _ := f.do(http.MethodPost, ship, f.ownerA, `{}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.out_of_stock" {
		t.Fatalf("ship over another order's hold = %d %v; want 409 shop.out_of_stock", code, body)
	}
	f.pay(holder.OrderToken)
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after the holder paid = %d; want 0", v.Stock)
	}
	f.exec(`UPDATE shop_variants SET stock = stock + 1 WHERE id = $1`, variant)
	if code, body, _ := f.do(http.MethodPost, ship, f.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("ship with the goods back = %d %v", code, body)
	}
	if v := f.variant(variant); v.Stock != 0 {
		t.Fatalf("stock after shipping = %d; want 0", v.Stock)
	}
}
