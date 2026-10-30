//go:build integration

package httpserver

// shop_audit_integration_test.go — the order-side findings of the shop audit
// of 2026-10-30 (SA-01, SA-09, SA-10, SA-11, SA-17/25, SA-30, SA-32), through
// the real router and a live database. Run against a FRESH migrated and
// seeded database (AGENTS.md CI-Integration recipe):
//
//	go test -tags integration -run TestShopAudit ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// paidOrder checks out qty units and pays the order.
func (f *shopFixture) paidOrder(variant string, qty int) gen.ShopOrderRow {
	f.t.Helper()
	code, body := f.checkout(variant, qty)
	if code != http.StatusCreated {
		f.t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	f.pay(token)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		f.t.Fatalf("order by token: %v", err)
	}
	return o
}

func (f *shopFixture) refundRows(paymentIntentID uuid.UUID) (n int, keys []string) {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT COALESCE(idempotency_key, '') FROM refunds WHERE payment_intent_id = $1 ORDER BY created_at`, paymentIntentID)
	if err != nil {
		f.t.Fatalf("refund rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			f.t.Fatal(err)
		}
		n++
		keys = append(keys, k)
	}
	return n, keys
}

// SA-01: the refund route needs an Idempotency-Key, and a repeat with the
// same key answers the first refund instead of returning the money twice.
func TestShopAudit_RefundNeedsAKeyAndARepeatIsOneRefund(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 5)
	o := f.paidOrder(variant, 2) // 9000 + 5000 shipping = 14000
	path := f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund")

	if code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"amount_minor":5000}`, ""); code != http.StatusBadRequest ||
		shopErrCode(body) != "shop.idempotency_key_required" {
		t.Fatalf("refund without a key = %d %v; want 400 shop.idempotency_key_required", code, body)
	}
	if code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"amount_minor":5000}`, strings.Repeat("k", 256)); code != http.StatusBadRequest ||
		shopErrCode(body) != "shop.idempotency_key_required" {
		t.Fatalf("refund with a 256-character key = %d %v; want 400", code, body)
	}
	if n, _ := f.refundRows(*o.PaymentIntentID); n != 0 {
		t.Fatalf("refunds after the refused calls = %d; want 0", n)
	}

	key := uuid.NewString()
	code, first, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"amount_minor":5000,"reason":"broken"}`, key)
	if code != http.StatusOK || first["replayed"] != false {
		t.Fatalf("first refund = %d %v; want 200, replayed false", code, first)
	}
	firstID := first["refund"].(map[string]any)["id"]

	// The click again, the answer lost: 50 of 140 must not become 100.
	code, again, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"amount_minor":5000,"reason":"broken"}`, key)
	if code != http.StatusOK || again["replayed"] != true || again["refund"].(map[string]any)["id"] != firstID {
		t.Fatalf("repeated refund = %d %v; want the first refund %v with replayed true", code, again, firstID)
	}
	if n, keys := f.refundRows(*o.PaymentIntentID); n != 1 || keys[0] != key {
		t.Fatalf("refund rows after a repeat = %d %v; want exactly one carrying the key", n, keys)
	}
	if got := again["order"].(map[string]any)["refunded_minor"].(float64); got != 5000 {
		t.Fatalf("refunded_minor = %v; want 5000", got)
	}
	if n := f.outboxCount(shop.EventOrderRefunded, o.ID); n != 1 {
		t.Fatalf("refunded events = %d; want 1", n)
	}

	// The same key for another amount, or for another order, is refused.
	if code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, `{"amount_minor":6000}`, key); code != http.StatusConflict ||
		shopErrCode(body) != "shop.idempotency_key_reused" {
		t.Fatalf("same key, other amount = %d %v; want 409 shop.idempotency_key_reused", code, body)
	}
	o2 := f.paidOrder(variant, 1)
	if code, body, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o2.ID.String()+"/refund"), f.ownerA, `{"amount_minor":5000}`, key); code != http.StatusConflict ||
		shopErrCode(body) != "shop.idempotency_key_reused" {
		t.Fatalf("same key, other order = %d %v; want 409 shop.idempotency_key_reused", code, body)
	}
	if n, _ := f.refundRows(*o2.PaymentIntentID); n != 0 {
		t.Fatalf("the other order got %d refund rows; want 0", n)
	}

	// A new key is a new refund. The last one takes the rest; a repeat of it
	// afterwards still answers the refund, not "nothing is left".
	key2 := uuid.NewString()
	if code, body, _ := f.doKeyed(http.MethodPost, path, f.ownerA, ``, key2); code != http.StatusOK {
		t.Fatalf("refund of the rest = %d %v", code, body)
	}
	code, replay, _ := f.doKeyed(http.MethodPost, path, f.ownerA, ``, key2)
	if code != http.StatusOK || replay["replayed"] != true {
		t.Fatalf("repeat after the order was fully refunded = %d %v; want 200 replayed", code, replay)
	}
	if n, _ := f.refundRows(*o.PaymentIntentID); n != 2 {
		t.Fatalf("refund rows = %d; want 2", n)
	}
	var refunded int64
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount), 0) FROM refunds
		WHERE payment_intent_id = $1 AND state = 'succeeded'`, *o.PaymentIntentID).Scan(&refunded); err != nil || refunded != 14000 {
		t.Fatalf("succeeded refunds = %d (%v); want 14000", refunded, err)
	}
}

// cuttingDriver is a refund driver whose provider call is cut: the caller's
// context ends while it works, as when a connection drops.
type cuttingDriver struct{ cancel context.CancelFunc }

func (d cuttingDriver) Drive(ctx context.Context, _ uuid.UUID) (refunds.Refund, error) {
	d.cancel()
	return refunds.Refund{}, ctx.Err()
}

// SA-01: a request that ends mid-way leaves a complete, approved refund (never
// a half-written one) that the same Idempotency-Key reads and finishes.
func TestShopAudit_CutRequestLeavesNoHalfWrittenRefund(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 5)
	o := f.paidOrder(variant, 2)
	key := uuid.NewString()

	// A context that is already over writes nothing at all.
	dead, kill := context.WithCancel(context.Background())
	kill()
	in := shop.RefundInput{OrgID: f.orgA, OrderID: o.ID, Amount: ptrInt64(5000), IdempotencyKey: key, RequestedBy: "test"}
	if _, err := shop.New(f.pool, nil).Refund(dead, in); err == nil {
		t.Fatal("refund on a finished context succeeded")
	}
	if n, _ := f.refundRows(*o.PaymentIntentID); n != 0 {
		t.Fatalf("refund rows after a refused call = %d; want 0", n)
	}

	// The connection drops during the provider call: the refund is recorded
	// and approved (the sweep or the repeat sends it), not left as a request.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := shop.New(f.pool, nil).WithRefunds(cuttingDriver{cancel: cancel})
	if _, err := svc.Refund(ctx, in); err == nil {
		t.Fatal("refund whose context ended returned no error")
	}
	var state string
	var stored string
	if err := f.pool.QueryRow(context.Background(), `SELECT state, idempotency_key FROM refunds WHERE payment_intent_id = $1`,
		*o.PaymentIntentID).Scan(&state, &stored); err != nil {
		t.Fatalf("the cut call left no refund row: %v", err)
	}
	if state != refunds.StateProviderPending || stored != key {
		t.Fatalf("refund after a cut call = %s key %s; want provider_pending with the key", state, stored)
	}

	// The client repeats with the same key: the same refund, now driven.
	code, body, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund"), f.ownerA, `{"amount_minor":5000}`, key)
	if code != http.StatusOK || body["replayed"] != true {
		t.Fatalf("repeat = %d %v; want 200 replayed", code, body)
	}
	if st := body["refund"].(map[string]any)["state"]; st != "succeeded" {
		t.Fatalf("refund state after the repeat = %v; want succeeded", st)
	}
	if n, _ := f.refundRows(*o.PaymentIntentID); n != 1 {
		t.Fatalf("refund rows = %d; want 1", n)
	}
	if got := body["order"].(map[string]any)["refunded_minor"].(float64); got != 5000 {
		t.Fatalf("refunded_minor = %v; want 5000", got)
	}
}

func ptrInt64(v int64) *int64 { return &v }

// SA-09: a payment parked in manual_review parks its shop order with it: the
// order leaves pending_payment (the sweep never expires it), the hold is
// released, v1.shop.order.manual_review is written and no letter is queued.
// A payment later decided as failed ends the order.
func TestShopAudit_PaymentManualReviewParksTheOrder(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 1)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	o, err := f.q.GetShopOrderByToken(context.Background(), body["order_token"].(string))
	if err != nil || o.PaymentIntentID == nil {
		t.Fatalf("order: %v", err)
	}
	svc := shop.New(f.pool, nil)

	inTx := func(fn func(tx pgx.Tx) error) {
		t.Helper()
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// A payment that is not a shop payment is not handled.
	inTx(func(tx pgx.Tx) error {
		handled, err := svc.MarkPaymentReviewTx(context.Background(), tx, uuid.New(), nil)
		if handled || err != nil {
			return fmt.Errorf("foreign payment: handled %v err %v", handled, err)
		}
		return nil
	})
	inTx(func(tx pgx.Tx) error {
		handled, err := svc.MarkPaymentReviewTx(context.Background(), tx, *o.PaymentIntentID, map[string]any{"event_type": "test"})
		if !handled || err != nil {
			return fmt.Errorf("shop payment: handled %v err %v", handled, err)
		}
		return nil
	})
	got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA)
	if got.Status != shop.StatusManualReview || !strings.Contains(string(got.Metadata), shop.ReviewReasonPayment) {
		t.Fatalf("order after the payment went to review = %s %s; want manual_review with the payment reason", got.Status, got.Metadata)
	}
	if n := f.outboxCount(shop.EventOrderManualReview, o.ID); n != 1 {
		t.Fatalf("manual_review events = %d; want 1", n)
	}
	if jobs := f.emailJobs(o.ID); len(jobs) != 0 {
		t.Fatalf("e-mail jobs = %+v; want none for an unconfirmed payment", jobs)
	}
	if v := f.variant(variant); v.Stock != 3 {
		t.Fatalf("stock = %d; want 3 (nothing sold)", v.Stock)
	}

	// The sweep never touches it, even when its hold is long over.
	f.exec(`UPDATE shop_orders SET expires_at = now() - interval '2 hours' WHERE id = $1`, o.ID)
	if err := shop.NewSweepHandler(shop.SweepOptions{Service: svc})(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA); got.Status != shop.StatusManualReview {
		t.Fatalf("order after the sweep = %s; want manual_review", got.Status)
	}
	// The same event again changes nothing.
	inTx(func(tx pgx.Tx) error {
		_, err := svc.MarkPaymentReviewTx(context.Background(), tx, *o.PaymentIntentID, nil)
		return err
	})
	if n := f.outboxCount(shop.EventOrderManualReview, o.ID); n != 1 {
		t.Fatalf("manual_review events after a repeat = %d; want 1", n)
	}

	// The operator decides the payment failed: the order ends.
	inTx(func(tx pgx.Tx) error {
		_, err := svc.MarkPaymentFailedTx(context.Background(), tx, *o.PaymentIntentID)
		return err
	})
	if got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA); got.Status != shop.StatusExpired {
		t.Fatalf("order after the payment failed = %s; want expired", got.Status)
	}
}

// SA-10: shipping an order that was partly refunded before it was handed over
// keeps the refund status; SA-32: a digital or service order cannot be shipped.
func TestShopAudit_ShipAfterPartialRefundKeepsTheRefundStatus(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 5)
	o := f.paidOrder(variant, 2)
	base := "/orders/" + o.ID.String()

	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, base+"/refund"), f.ownerA, `{"amount_minor":5000}`)
	if code != http.StatusOK || body["order"].(map[string]any)["status"] != shop.StatusPartiallyRefunded {
		t.Fatalf("partial refund = %d %v", code, body)
	}
	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, base+"/ship"), f.ownerA, `{"tracking_note":"TRK1"}`)
	if code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	if body["status"] != shop.StatusPartiallyRefunded || body["refunded_minor"].(float64) != 5000 || body["shipped_at"] == nil {
		t.Fatalf("after shipping status %v refunded %v shipped_at %v; want partially_refunded, 5000 and a shipping time",
			body["status"], body["refunded_minor"], body["shipped_at"])
	}
	if n := f.outboxCount(shop.EventOrderShipped, o.ID); n != 1 {
		t.Fatalf("shipped events = %d; want 1", n)
	}
	// The refunds tab still lists it, the to-ship tab does not.
	_, list, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/search"), f.ownerA, `{"view":"refunds"}`, "")
	if len(list["orders"].([]any)) != 1 {
		t.Fatalf("refunds tab = %v; want the order", list)
	}
	_, list, _ = f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/search"), f.ownerA, `{"view":"to_ship"}`, "")
	if len(list["orders"].([]any)) != 0 {
		t.Fatalf("to-ship tab = %v; want empty", list)
	}
}

func TestShopAudit_NothingToShipIsRefused(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 5)
	o := f.paidOrder(variant, 1)
	f.exec(`UPDATE shop_orders SET delivery_kind = 'none' WHERE id = $1`, o.ID)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.order_not_shippable" {
		t.Fatalf("ship of an order with nothing to ship = %d %v; want 409 shop.order_not_shippable", code, body)
	}
	if n := f.outboxCount(shop.EventOrderShipped, o.ID); n != 0 {
		t.Fatalf("shipped events = %d; want 0", n)
	}
	if got, _ := f.q.GetShopOrder(context.Background(), o.ID, f.orgA); got.Status != shop.StatusPaid {
		t.Fatalf("order status = %s; want paid", got.Status)
	}
}

// SA-17 and SA-25: POST .../orders/search takes the filters in the body, reads
// day boundaries as RFC 3339 with a zone, and has no empty last page.
func TestShopAudit_SearchByBodyZonesAndPages(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 9)
	var orders []gen.ShopOrderRow
	for i := 0; i < 3; i++ {
		code, body := f.checkout(variant, 1)
		if code != http.StatusCreated {
			t.Fatalf("checkout %d = %d %v", i, code, body)
		}
		o, _ := f.q.GetShopOrderByToken(context.Background(), body["order_token"].(string))
		orders = append(orders, o)
	}
	search := func(body string) (int, map[string]any) {
		code, out, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/search"), f.ownerA, body, "")
		return code, out
	}
	ids := func(out map[string]any) []string {
		var r []string
		for _, o := range out["orders"].([]any) {
			r = append(r, o.(map[string]any)["id"].(string))
		}
		return r
	}

	// The buyer's e-mail in the body finds exactly that order, like GET ?q=.
	target := orders[1]
	code, out := search(fmt.Sprintf(`{"q":%q}`, target.BuyerEmail))
	if code != http.StatusOK || len(ids(out)) != 1 || ids(out)[0] != target.ID.String() {
		t.Fatalf("search by e-mail = %d %v; want the one order", code, out)
	}
	_, viaGet, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/orders?q="+target.BuyerEmail), f.ownerA, "")
	if g := ids(viaGet); len(g) != 1 || g[0] != target.ID.String() {
		t.Fatalf("GET with the same filter = %v; want the same order", viaGet)
	}
	if code, out := search(`{"q":"x","bogus":1}`); code != http.StatusBadRequest || shopErrCode(out) != "shop.invalid_body" {
		t.Fatalf("unknown member = %d %v; want 400 shop.invalid_body", code, out)
	}
	if code, out := search(`{"from":"yesterday"}`); code != http.StatusBadRequest || shopErrCode(out) != "shop.invalid_filter" {
		t.Fatalf("bad date = %d %v; want 400 shop.invalid_filter", code, out)
	}
	// An empty body is the whole list.
	if code, out := search(``); code != http.StatusOK || len(ids(out)) != 3 {
		t.Fatalf("empty body = %d %v; want the 3 orders", code, out)
	}

	// Day boundaries with a zone: the order was created "now"; a day that
	// starts an hour after it, written in the zone of Jerusalem, excludes it,
	// one that started an hour before includes it.
	jer := time.FixedZone("IDT", 3*3600)
	at := target.CreatedAt
	window := func(from, to time.Time) []string {
		_, out := search(fmt.Sprintf(`{"q":%q,"from":%q,"to":%q}`, target.BuyerEmail, from.In(jer).Format(time.RFC3339), to.In(jer).Format(time.RFC3339)))
		return ids(out)
	}
	if got := window(at.Add(-time.Hour), at.Add(time.Hour)); len(got) != 1 {
		t.Fatalf("window around the order = %v; want it", got)
	}
	if got := window(at.Add(time.Hour), at.Add(2*time.Hour)); len(got) != 0 {
		t.Fatalf("window after the order = %v; want none", got)
	}
	if got := window(at.Add(-2*time.Hour), at.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("window before the order = %v; want none", got)
	}
	// A plain date still works (UTC midnight).
	if code, out := search(`{"from":"2000-01-01","to":"2999-12-31"}`); code != http.StatusOK || len(ids(out)) != 3 {
		t.Fatalf("date-only bounds = %d %v", code, out)
	}

	// Pages: 3 orders, 3 per page = no next page; 2 per page = one more.
	_, out = search(`{"limit":3}`)
	if out["next_offset"] != nil {
		t.Fatalf("a page that holds all orders has next_offset %v; want null", out["next_offset"])
	}
	_, out = search(`{"limit":2}`)
	if len(ids(out)) != 2 || out["next_offset"] != float64(2) {
		t.Fatalf("first page = %v; want 2 orders and next_offset 2", out)
	}
	_, out = search(`{"limit":2,"offset":2}`)
	if len(ids(out)) != 1 || out["next_offset"] != nil {
		t.Fatalf("second page = %v; want 1 order and no next_offset", out)
	}
	// The same through GET.
	_, out, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/orders?limit=3"), f.ownerA, "")
	if out["next_offset"] != nil {
		t.Fatalf("GET page that holds all orders has next_offset %v; want null", out["next_offset"])
	}
	// Another organization's key reads its own (empty) list.
	code, out, _ = f.doKeyed(http.MethodPost, f.orgPath(f.orgB, "/orders/search"), f.keyB, `{}`, "")
	if code != http.StatusOK || len(ids(out)) != 0 {
		t.Fatalf("org B's search = %d %v; want empty", code, out)
	}
	if code, _, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/search"), f.keyB, `{}`, ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("org B's key on org A's search = %d; want 403 or 404", code)
	}
}

// SA-11: a shop letter is recorded in email_letters like every buyer letter,
// tied to the shop order.
func TestShopAudit_ShopLetterIsRecorded(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 3)
	o := f.paidOrder(variant, 1)
	jobs := f.emailJobs(o.ID)
	if len(jobs) != 1 || jobs[0].Kind != shop.EmailReceived {
		t.Fatalf("e-mail jobs = %+v; want the received letter", jobs)
	}
	mailer := &recordingMailer{}
	handler := delivery.NewShopEmailHandler(delivery.ShopEmailOptions{
		HandlerOptions: delivery.HandlerOptions{Sender: mailer},
		DB:             f.pool,
		ShopBaseURL:    "https://shop.platform.test",
	})
	raw, _ := json.Marshal(jobs[0])
	if err := handler(context.Background(), raw); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(mailer.msgs) != 1 {
		t.Fatalf("mails = %d; want 1", len(mailer.msgs))
	}
	var kind, recipient, status, subject string
	var orderID *uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT kind, recipient, status, COALESCE(subject, ''), order_id
		FROM email_letters WHERE shop_order_id = $1`, o.ID).Scan(&kind, &recipient, &status, &subject, &orderID); err != nil {
		t.Fatalf("the shop letter was not recorded: %v", err)
	}
	if kind != "shop_received" || recipient != o.BuyerEmail || status != "sent" || subject != mailer.msgs[0].Subject || orderID != nil {
		t.Fatalf("letter row = %s %s %s %q order_id %v", kind, recipient, status, subject, orderID)
	}
	// A redelivered job sends nothing and records nothing.
	if err := handler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_letters WHERE shop_order_id = $1`, o.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("letters after a redelivery = %d; want 1", n)
	}
}

// SA-30: erasing a user erases the buyer's and delivery details of their
// finished shop orders, and leaves an order that still has to be handled.
func TestShopAudit_ErasingAUserErasesTheirFinishedShopOrders(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 9)
	ctx := context.Background()
	email := "gdpr-" + uuid.NewString()[:8] + "@example.test"
	u, err := f.q.InsertUser(ctx, email, "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	f.users = append(f.users, u.ID)

	shipped := f.paidOrder(variant, 1)
	waiting := f.paidOrder(variant, 1)
	other := f.paidOrder(variant, 1)
	f.exec(`UPDATE shop_orders SET buyer_email = $2 WHERE id = ANY($1)`, []uuid.UUID{shipped.ID, waiting.ID}, strings.ToUpper(email))
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+shipped.ID.String()+"/ship"), f.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}

	n, err := f.q.ForgetUserShopOrders(ctx, u.ID)
	if err != nil || n != 1 {
		t.Fatalf("ForgetUserShopOrders = %d, %v; want 1", n, err)
	}
	got, _ := f.q.GetShopOrder(ctx, shipped.ID, f.orgA)
	if got.BuyerName != "" || got.BuyerEmail != "" || got.BuyerPhone != "" || got.ShipName != "" || got.ShipAddress1 != "" ||
		got.ShipCity != "" || got.ShipPostal != "" || got.DeliveryNote != "" {
		t.Fatalf("shipped order still carries personal data: %+v", got)
	}
	if got.Total != shipped.Total || got.Status != shop.StatusShipped || got.Number != shipped.Number {
		t.Fatalf("the order itself changed: %+v", got)
	}
	if w, _ := f.q.GetShopOrder(ctx, waiting.ID, f.orgA); w.BuyerName == "" || w.ShipAddress1 == "" {
		t.Fatalf("an order still to be handed over lost its details: %+v", w)
	}
	if o, _ := f.q.GetShopOrder(ctx, other.ID, f.orgA); o.BuyerName == "" {
		t.Fatalf("another buyer's order was erased")
	}
	// Idempotent.
	if n, err := f.q.ForgetUserShopOrders(ctx, u.ID); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v", n, err)
	}
}
