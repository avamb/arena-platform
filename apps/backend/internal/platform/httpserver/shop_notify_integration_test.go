//go:build integration

package httpserver

// shop_notify_integration_test.go — lane L5: a PAID shop order queues the
// buyer's e-mail job in the payment webhook's own transaction and is announced
// once to the Telegram chats whose subscription kinds include shop_sale (the
// fact) and shop_delivery (the card), and nowhere else; shipping queues the
// second letter. The e-mail handler is run
// for real against the database and a recording sender.
//
// Run against a FRESH migrated and seeded database (arena_shop_l5), e.g.
//
//	go test -tags integration -run TestShopNotify ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

type recordingTelegram struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (r *recordingTelegram) Send(_ context.Context, chat, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[string][]string{}
	}
	r.sent[chat] = append(r.sent[chat], text)
	return nil
}

// recordingMailer is a production-looking sender (not DevOnly) that keeps what
// it was asked to send.
type recordingMailer struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (r *recordingMailer) Send(_ context.Context, m email.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (f *shopFixture) emailJobs(orderID uuid.UUID) []shop.OrderEmailPayload {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT payload FROM worker_jobs
		WHERE job_type = $1 AND payload->>'order_id' = $2 ORDER BY created_at, id`, shop.JobTypeOrderEmail, orderID.String())
	if err != nil {
		f.t.Fatalf("worker_jobs: %v", err)
	}
	defer rows.Close()
	var out []shop.OrderEmailPayload
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			f.t.Fatal(err)
		}
		var p shop.OrderEmailPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func (f *shopFixture) subscribe(chat string, kinds string) {
	f.t.Helper()
	f.exec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
	        VALUES ($1, $2, $3, $4::text[])`, f.orgA, "test "+chat, chat, kinds)
}

func (f *shopFixture) paidEvent(orderID uuid.UUID) outbox.Event {
	return f.shopEvent(shop.EventOrderPaid, orderID)
}

// shopEvent is the (latest) outbox event of one type the order wrote.
func (f *shopFixture) shopEvent(eventType string, orderID uuid.UUID) outbox.Event {
	f.t.Helper()
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT payload FROM outbox_events
		WHERE event_type = $1 AND aggregate_type = 'shop_order' AND aggregate_id = $2::text
		ORDER BY id DESC LIMIT 1`,
		eventType, orderID).Scan(&payload); err != nil {
		f.t.Fatalf("%s event: %v", eventType, err)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		f.t.Fatal(err)
	}
	return outbox.Event{ID: "it-" + eventType + "-" + orderID.String() + fmt.Sprint(p["refunded_minor"]), AggregateType: shop.AggregateType,
		AggregateID: orderID.String(), EventType: eventType, Payload: p}
}

// connectShopModule gives organization A the shop module (a period that
// started yesterday), unless it has one already, and removes the period and the
// claims of this test's orders when the test ends. Without a period the shop
// announces only while the organization has an open order (orders-only mode).
func (f *shopFixture) connectShopModule() {
	f.t.Helper()
	f.exec(`INSERT INTO org_module_periods (org_id, module, starts_on, reason)
	        SELECT $1::uuid, 'shop', current_date - 1, 'test'
	         WHERE NOT EXISTS (SELECT 1 FROM org_module_periods WHERE org_id = $1::uuid AND module = 'shop')`, f.orgA)
	f.cleanNotifyRows()
}

// cleanNotifyRows registers the removal of what the notifier wrote about orders
// of organization A: the module periods (an FK without cascade) and the claims
// of the fact messages, which the fixture's own cleanup does not know.
func (f *shopFixture) cleanNotifyRows() {
	f.t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `DELETE FROM sales_notification_deliveries
		  WHERE split_part(key, ':', 2) IN (SELECT id::text FROM shop_orders WHERE org_id = $1)`, f.orgA)
		_, _ = f.pool.Exec(c, `DELETE FROM org_module_periods WHERE org_id = $1`, f.orgA)
	})
}

// claimCount counts the claims whose key is exactly key.
func (f *shopFixture) claimCount(key string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_notification_deliveries WHERE key = $1`, key).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// paidDigitalOrder checks out one file and pays it.
func (f *shopFixture) paidDigitalOrder() gen.ShopOrderRow {
	f.t.Helper()
	file := f.shopMedia(shop.DigitalOwnerType)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		"{\"title\":\"Tasting notes %s\",\"status\":\"active\",\"kind\":\"digital\",\"digital_media_id\":%q,\"variants\":[{\"price_minor\":1500}]}",
		uuid.NewString()[:6], file))
	if code != http.StatusCreated {
		f.t.Fatalf("POST digital product = %d %v", code, body)
	}
	variantID := body["variants"].([]any)[0].(map[string]any)["id"].(string)
	code, body, _ = f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(
		"{\"items\":[{\"variant_id\":%q,\"qty\":1}],\"buyer\":{\"name\":\"Anna\",\"email\":\"digital-%s@example.test\"},\"locale\":\"en\",\"return_url\":\"%s/checkout\"}",
		variantID, uuid.NewString()[:6], shopTestBase))
	if code != http.StatusCreated {
		f.t.Fatalf("digital checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	f.pay(token)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func TestShopNotify_PaidOrderQueuesTheEmailAndAnnouncesToShopChatsOnly(t *testing.T) {
	f := newShopFixture(t)
	f.connectShopModule()
	nonce := uuid.NewString()[:8]
	logistics, ticketsOnly, both, sales := "shopnotify-"+nonce+"-L", "shopnotify-"+nonce+"-T", "shopnotify-"+nonce+"-B", "shopnotify-"+nonce+"-S"
	f.subscribe(logistics, "{shop_delivery}")
	f.subscribe(sales, "{shop_sale}")
	f.subscribe(ticketsOnly, "{tickets}")
	f.subscribe(both, "{tickets,shop_delivery}")
	// The kinds column refuses anything else.
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
		VALUES ($1, 'bad', $2, '{other}')`, f.orgA, "shopnotify-"+nonce+"-X"); err == nil {
		t.Fatal("a subscription kind outside tickets/shop_sale/shop_delivery must be refused by the CHECK")
	}

	_, variant := f.openShop(4500, 3)
	f.exec(`UPDATE shop_settings SET contact_email = 'shop-' || $2::text || '@example.test' WHERE org_id = $1`, f.orgA, nonce)
	code, body := f.checkout(variant, 2)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing is queued while the order waits for its money.
	if jobs := f.emailJobs(o.ID); len(jobs) != 0 {
		t.Fatalf("e-mail jobs before payment = %+v", jobs)
	}
	f.pay(token)
	f.pay2xIdempotent(token)

	jobs := f.emailJobs(o.ID)
	if len(jobs) != 1 || jobs[0].Kind != shop.EmailReceived || jobs[0].OrgID != f.orgA.String() {
		t.Fatalf("e-mail jobs after payment = %+v; want exactly one received", jobs)
	}

	// Telegram: the shop chats hear it, once, with the courier form.
	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	ev := f.paidEvent(o.ID)
	d.Handle(context.Background(), ev)
	d.Handle(context.Background(), ev) // an outbox retry
	if len(tg.sent[logistics]) != 1 || len(tg.sent[both]) != 1 || len(tg.sent[sales]) != 1 {
		t.Fatalf("shop chats got %d, %d and %d messages, want 1 each", len(tg.sent[logistics]), len(tg.sent[both]), len(tg.sent[sales]))
	}
	// The sales chat hears the fact only: no buyer, no address.
	if fact := tg.sent[sales][0]; !strings.Contains(fact, "Shop sale") || !strings.Contains(fact, " ×2") ||
		strings.Contains(fact, o.BuyerPhone) || strings.Contains(fact, o.BuyerEmail) || strings.Contains(fact, "Herzl") || strings.Contains(fact, "Anna") {
		t.Errorf("the sales chat got:\n%s", fact)
	}
	if card := tg.sent[both][0]; !strings.Contains(card, "New shop order") || !strings.Contains(card, "Herzl 1") {
		t.Errorf("a chat with both kinds must get the card:\n%s", card)
	}
	if len(tg.sent[ticketsOnly]) != 0 {
		t.Fatalf("a chat without the shop kind got %v", tg.sent[ticketsOnly])
	}
	msg := tg.sent[logistics][0]
	for _, want := range []string{"New shop order", "Order #", "Herzl 1", "Haifa", "3100000", "2 × Rosé", "Shipping: 50.00 ₪", "call before", o.BuyerPhone} {
		if !strings.Contains(msg, want) {
			t.Errorf("announcement lacks %q:\n%s", want, msg)
		}
	}
	for _, k := range []string{"shop_paid:", "shop_sale_paid:"} {
		if n := f.claimCount(k + o.ID.String()); n != 1 {
			t.Fatalf("delivery claims of %s = %d; want 1", k, n)
		}
	}

	// The e-mail handler, for real: reads the order, renders in Russian (the
	// checkout sent locale ru), mails the buyer with the shop contact as Reply-To.
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
	if err := handler(context.Background(), raw); err != nil { // redelivery
		t.Fatalf("handler redelivery: %v", err)
	}
	if len(mailer.msgs) != 1 {
		t.Fatalf("mails sent = %d; want exactly 1 (a redelivered job must not mail twice)", len(mailer.msgs))
	}
	m := mailer.msgs[0]
	if m.To != o.BuyerEmail || m.ReplyTo != "shop-"+nonce+"@example.test" {
		t.Errorf("to %q reply-to %q", m.To, m.ReplyTo)
	}
	if !strings.Contains(m.Subject, "№") || !strings.Contains(m.TextBody, "Herzl 1") ||
		!strings.Contains(m.TextBody, "https://shop.platform.test/"+f.orgASlug+"/checkout/result?checkout_token="+token) {
		t.Errorf("letter wrong:\nsubject %q\n%s", m.Subject, m.TextBody)
	}

	// Shipping queues the second letter, once.
	code, body, _ = f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{"tracking_note":"TRK-9"}`)
	if code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{"tracking_note":"TRK-10"}`) // only edits the note
	jobs = f.emailJobs(o.ID)
	if len(jobs) != 2 || jobs[1].Kind != shop.EmailShipped {
		t.Fatalf("e-mail jobs after shipping = %+v; want received + one shipped", jobs)
	}
	raw, _ = json.Marshal(jobs[1])
	if err := handler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(mailer.msgs) != 2 || !strings.Contains(mailer.msgs[1].TextBody, "TRK-10") {
		t.Fatalf("shipped letter missing or without the tracking note: %d mails", len(mailer.msgs))
	}
}

func TestShopNotify_SubscriptionWithoutShopKindGetsNothing(t *testing.T) {
	f := newShopFixture(t)
	f.connectShopModule()
	nonce := uuid.NewString()[:8]
	chat := "shopnotify-" + nonce + "-T"
	f.subscribe(chat, "{tickets}")
	_, variant := f.openShop(4500, 3)
	code, body := f.checkout(variant, 1)
	if code != http.StatusCreated {
		t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	f.pay(token)

	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	d.Handle(context.Background(), f.paidEvent(o.ID))
	if len(tg.sent[chat]) != 0 {
		t.Fatalf("a tickets-only chat got %v", tg.sent[chat])
	}
	for _, k := range []string{"shop_paid:", "shop_sale_paid:"} {
		if n := f.claimCount(k + o.ID.String()); n != 0 {
			t.Fatalf("nothing was routed, yet %s was claimed %d time(s)", k, n)
		}
	}
}

// A digital-only order: the received letter carries the downloads link and no
// address or shipping, and shipping it queues no letter.
func TestShopNotify_DigitalOnlyOrderLetter(t *testing.T) {
	f := newShopFixture(t)
	f.openShop(4500, 3)
	file := f.shopMedia(shop.DigitalOwnerType)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products"), f.ownerA, fmt.Sprintf(
		"{\"title\":\"Tasting notes %s\",\"status\":\"active\",\"kind\":\"digital\",\"digital_media_id\":%q,\"variants\":[{\"price_minor\":1500}]}",
		uuid.NewString()[:6], file))
	if code != http.StatusCreated {
		t.Fatalf("POST digital product = %d %v", code, body)
	}
	variantID := body["variants"].([]any)[0].(map[string]any)["id"].(string)
	code, body, _ = f.do(http.MethodPost, "/v1/public/shop/"+f.orgASlug+"/checkout", "", fmt.Sprintf(
		"{\"items\":[{\"variant_id\":%q,\"qty\":1}],\"buyer\":{\"name\":\"Anna\",\"email\":\"digital-%s@example.test\"},\"locale\":\"en\",\"return_url\":\"%s/checkout\"}",
		variantID, uuid.NewString()[:6], shopTestBase))
	if code != http.StatusCreated {
		t.Fatalf("digital checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	o, _ := f.q.GetShopOrderByToken(context.Background(), token)
	f.pay(token)

	jobs := f.emailJobs(o.ID)
	if len(jobs) != 1 || jobs[0].Kind != shop.EmailReceived {
		t.Fatalf("e-mail jobs = %+v; want one received", jobs)
	}
	mailer := &recordingMailer{}
	handler := delivery.NewShopEmailHandler(delivery.ShopEmailOptions{
		HandlerOptions: delivery.HandlerOptions{Sender: mailer}, DB: f.pool, ShopBaseURL: "https://shop.platform.test",
	})
	raw, _ := json.Marshal(jobs[0])
	if err := handler(context.Background(), raw); err != nil || len(mailer.msgs) != 1 {
		t.Fatalf("handler err %v, mails %d", err, len(mailer.msgs))
	}
	text := mailer.msgs[0].TextBody
	link := "https://shop.platform.test/" + f.orgASlug + "/checkout/result?checkout_token=" + token
	if !strings.Contains(text, link) || !strings.Contains(text, "Your files") {
		t.Errorf("the letter lacks the downloads link or block:\n%s", text)
	}
	for _, gone := range []string{"Delivery address", "Shipping:", "Pickup"} {
		if strings.Contains(text, gone) {
			t.Errorf("a digital-only letter must not mention %q:\n%s", gone, text)
		}
	}
	// Nothing ships: no second letter even if the organizer marks it shipped.
	f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, "{\"tracking_note\":\"x\"}")
	if jobs := f.emailJobs(o.ID); len(jobs) != 1 {
		t.Errorf("e-mail jobs after shipping a digital order = %+v; want still one", jobs)
	}
}

// A payment that arrives after the hold ended, for goods that are gone, sends
// the buyer no letter and tells the shop chats once.
func TestShopNotify_ManualReviewTellsTheOrganizerNotTheBuyer(t *testing.T) {
	f := newShopFixture(t)
	f.connectShopModule()
	nonce := uuid.NewString()[:8]
	chat, group := "shopnotify-"+nonce+"-S", "shopnotify-"+nonce+"-L"
	f.subscribe(chat, "{shop_sale}")
	f.subscribe(group, "{shop_delivery}")
	_, variant := f.openShop(4500, 1)
	late := f.placed(variant, 1)
	f.expireOrder(late.ID)
	other := f.placed(variant, 1)
	f.pay(other.OrderToken)
	f.pay(late.OrderToken)

	if jobs := f.emailJobs(late.ID); len(jobs) != 0 {
		t.Fatalf("a parked order got e-mail jobs %+v", jobs)
	}
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT payload FROM outbox_events WHERE event_type = $1 AND aggregate_id = $2::text",
		shop.EventOrderManualReview, late.ID).Scan(&payload); err != nil {
		t.Fatalf("manual_review event: %v", err)
	}
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	ev := outbox.Event{ID: "it-review-" + late.ID.String(), AggregateType: shop.AggregateType, AggregateID: late.ID.String(),
		EventType: shop.EventOrderManualReview, Payload: p}
	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	d.Handle(context.Background(), ev)
	d.Handle(context.Background(), ev)
	if len(tg.sent[chat]) != 1 || !strings.Contains(tg.sent[chat][0], "arrived after the hold ended") || strings.Contains(tg.sent[chat][0], "@example.test") {
		t.Fatalf("sales chat got %v; want one review notice without buyer data", tg.sent[chat])
	}
	if len(tg.sent[group]) != 0 {
		t.Fatalf("the delivery group heard an order that is not paid: %v", tg.sent[group])
	}

	// Resolved in favour of shipping, the order is announced as paid: the
	// delivery group gets the card now (resolve writes v1.shop.order.paid).
	f.exec(`UPDATE shop_variants SET stock = 5 WHERE id = $1`, variant)
	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+late.ID.String()+"/resolve"), f.ownerA, `{"action":"fulfil"}`)
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %v", code, body)
	}
	d.Handle(context.Background(), f.paidEvent(late.ID))
	if len(tg.sent[group]) != 1 || !strings.Contains(tg.sent[group][0], "New shop order") {
		t.Fatalf("after the resolution the delivery group got %v; want the card", tg.sent[group])
	}
}

// A sales chat and a delivery group of one organization: the paid shipping
// order gives the fact to the first and the card to the second. A partial
// refund before shipping says "do not ship", a refund after it "already
// shipped"; the sales chat hears each refund as a fact.
func TestShopNotify_SalesChatAndDeliveryGroup_PaidAndRefunds(t *testing.T) {
	f := newShopFixture(t)
	f.connectShopModule()
	nonce := uuid.NewString()[:8]
	sales, group := "shopnotify-"+nonce+"-S", "shopnotify-"+nonce+"-G"
	f.subscribe(sales, "{tickets,shop_sale}")
	f.subscribe(group, "{shop_delivery}")
	_, variant := f.openShop(4500, 5)
	o := f.paidOrder(variant, 2)

	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	ctx := context.Background()
	d.Handle(ctx, f.paidEvent(o.ID))
	if len(tg.sent[sales]) != 1 || !strings.Contains(tg.sent[sales][0], "Shop sale") || strings.Contains(tg.sent[sales][0], "Herzl") {
		t.Fatalf("sales chat after payment: %v", tg.sent[sales])
	}
	if len(tg.sent[group]) != 1 || !strings.Contains(tg.sent[group][0], "New shop order") || !strings.Contains(tg.sent[group][0], "Herzl 1") {
		t.Fatalf("delivery group after payment: %v", tg.sent[group])
	}

	refund := func(amount int) {
		t.Helper()
		code, body, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund"), f.ownerA,
			fmt.Sprintf(`{"amount_minor":%d}`, amount), uuid.NewString())
		if code != http.StatusOK {
			t.Fatalf("refund %d = %d %v", amount, code, body)
		}
	}
	refund(5000)
	ev := f.shopEvent(shop.EventOrderRefunded, o.ID)
	d.Handle(ctx, ev)
	d.Handle(ctx, ev) // an outbox retry
	if len(tg.sent[sales]) != 2 || !strings.Contains(tg.sent[sales][1], "Partly refunded") || strings.Contains(tg.sent[sales][1], "Anna") {
		t.Fatalf("sales chat after a partial refund: %v", tg.sent[sales])
	}
	if len(tg.sent[group]) != 2 || !strings.Contains(tg.sent[group][1], "Do not ship") || !strings.Contains(tg.sent[group][1], " ×2") {
		t.Fatalf("delivery group after a partial refund: %v", tg.sent[group])
	}

	code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/ship"), f.ownerA, `{"tracking_note":"TRK-1"}`)
	if code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	refund(5000)
	d.Handle(ctx, f.shopEvent(shop.EventOrderRefunded, o.ID))
	if len(tg.sent[group]) != 3 || !strings.Contains(tg.sent[group][2], "Already shipped") || strings.Contains(tg.sent[group][2], "Do not ship") {
		t.Fatalf("delivery group after a refund of a shipped order: %v", tg.sent[group])
	}
	if len(tg.sent[sales]) != 3 {
		t.Fatalf("sales chat after the second refund: %v", tg.sent[sales])
	}
	for _, k := range []string{"shop_sale_refund:%s:5000", "shop_sale_refund:%s:10000", "shop_refund:%s:5000", "shop_refund:%s:10000"} {
		if n := f.claimCount(fmt.Sprintf(k, o.ID)); n != 1 {
			t.Errorf("claims of %s = %d; want 1", k, n)
		}
	}
}

// A file or a service has nothing to ship: the delivery group hears nothing
// about it, a sales chat hears the fact of the sale and of the refund.
func TestShopNotify_DigitalOrder_FactToSalesChatNothingToDelivery(t *testing.T) {
	f := newShopFixture(t)
	f.connectShopModule()
	f.openShop(4500, 3)
	nonce := uuid.NewString()[:8]
	sales, group := "shopnotify-"+nonce+"-S", "shopnotify-"+nonce+"-G"
	f.subscribe(sales, "{shop_sale}")
	f.subscribe(group, "{shop_delivery}")
	o := f.paidDigitalOrder()

	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	ctx := context.Background()
	d.Handle(ctx, f.paidEvent(o.ID))
	code, body, _ := f.doKeyed(http.MethodPost, f.orgPath(f.orgA, "/orders/"+o.ID.String()+"/refund"), f.ownerA, ``, uuid.NewString())
	if code != http.StatusOK {
		t.Fatalf("refund = %d %v", code, body)
	}
	d.Handle(ctx, f.shopEvent(shop.EventOrderRefunded, o.ID))
	if len(tg.sent[group]) != 0 {
		t.Fatalf("the delivery group heard a digital order: %v", tg.sent[group])
	}
	if len(tg.sent[sales]) != 2 || !strings.Contains(tg.sent[sales][0], "Shop sale") || !strings.Contains(tg.sent[sales][1], "Refunded in full") {
		t.Fatalf("sales chat: %v", tg.sent[sales])
	}
	if n := f.claimCount("shop_paid:" + o.ID.String()); n != 0 {
		t.Errorf("the card was claimed %d time(s) although nothing was sent", n)
	}
}

// An organization without the shop module: nothing is announced, except while
// it still has an open order (orders-only mode), when the order is announced
// as before.
func TestShopNotify_ModuleLessOrganization(t *testing.T) {
	f := newShopFixture(t) // no module period
	f.cleanNotifyRows()
	f.openShop(4500, 3)
	nonce := uuid.NewString()[:8]
	sales, group := "shopnotify-"+nonce+"-S", "shopnotify-"+nonce+"-G"
	f.subscribe(sales, "{shop_sale}")
	f.subscribe(group, "{shop_delivery}")

	tg := &recordingTelegram{}
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(f.pool), tg, nil)
	ctx := context.Background()

	// A paid file closes at once: no open order, no module, no announcement.
	digital := f.paidDigitalOrder()
	d.Handle(ctx, f.paidEvent(digital.ID))
	if len(tg.sent[sales])+len(tg.sent[group]) != 0 {
		t.Fatalf("a module-less organization announced a closed order: %v", tg.sent)
	}
	if n := f.claimCount("shop_sale_paid:" + digital.ID.String()); n != 0 {
		t.Errorf("claims = %d; want 0", n)
	}

	// A paid parcel waiting to be shipped is an open order: orders-only mode.
	_, variant := f.openShop(4500, 3)
	o := f.paidOrder(variant, 1)
	d.Handle(ctx, f.paidEvent(o.ID))
	if len(tg.sent[sales]) != 1 || len(tg.sent[group]) != 1 {
		t.Fatalf("orders-only mode: sales %d group %d, want 1 each", len(tg.sent[sales]), len(tg.sent[group]))
	}
}
