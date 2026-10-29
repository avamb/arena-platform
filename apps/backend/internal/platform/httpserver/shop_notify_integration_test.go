//go:build integration

package httpserver

// shop_notify_integration_test.go — lane L5: a PAID shop order queues the
// buyer's e-mail job in the payment webhook's own transaction and is announced
// once to the Telegram chats whose subscription kinds include "shop", and
// nowhere else; shipping queues the second letter. The e-mail handler is run
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
	f.t.Helper()
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT payload FROM outbox_events
		WHERE event_type = $1 AND aggregate_type = 'shop_order' AND aggregate_id = $2::text`,
		shop.EventOrderPaid, orderID).Scan(&payload); err != nil {
		f.t.Fatalf("paid event: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		f.t.Fatal(err)
	}
	return outbox.Event{ID: "it-" + orderID.String(), AggregateType: shop.AggregateType, AggregateID: orderID.String(),
		EventType: shop.EventOrderPaid, Payload: p}
}

func TestShopNotify_PaidOrderQueuesTheEmailAndAnnouncesToShopChatsOnly(t *testing.T) {
	f := newShopFixture(t)
	nonce := uuid.NewString()[:8]
	logistics, ticketsOnly, both := "shopnotify-"+nonce+"-L", "shopnotify-"+nonce+"-T", "shopnotify-"+nonce+"-B"
	f.subscribe(logistics, "{shop}")
	f.subscribe(ticketsOnly, "{tickets}")
	f.subscribe(both, "{tickets,shop}")
	// The kinds column refuses anything else.
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
		VALUES ($1, 'bad', $2, '{other}')`, f.orgA, "shopnotify-"+nonce+"-X"); err == nil {
		t.Fatal("a subscription kind outside tickets/shop must be refused by the CHECK")
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
	if len(tg.sent[logistics]) != 1 || len(tg.sent[both]) != 1 {
		t.Fatalf("shop chats got %d and %d messages, want 1 each", len(tg.sent[logistics]), len(tg.sent[both]))
	}
	if len(tg.sent[ticketsOnly]) != 0 {
		t.Fatalf("a chat without the shop kind got %v", tg.sent[ticketsOnly])
	}
	msg := tg.sent[logistics][0]
	for _, want := range []string{"New shop order", "Order #", "Herzl 1", "Haifa", "3100000", "2 × Rosé", "Shipping: 50.0000a0₪", "call before", o.BuyerPhone} {
		if !strings.Contains(msg, want) {
			t.Errorf("announcement lacks %q:\n%s", want, msg)
		}
	}
	var claims int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_notification_deliveries WHERE key = $1`,
		"shop_paid:"+o.ID.String()).Scan(&claims); err != nil || claims != 1 {
		t.Fatalf("delivery claims = %d, %v; want 1", claims, err)
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
	var claims int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_notification_deliveries WHERE key = $1`,
		"shop_paid:"+o.ID.String()).Scan(&claims)
	if claims != 0 {
		t.Fatalf("nothing was routed, yet the announcement was claimed %d time(s)", claims)
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
	chat := "shopnotify-" + uuid.NewString()[:8] + "-L"
	f.subscribe(chat, "{shop}")
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
	if len(tg.sent[chat]) != 1 || !strings.Contains(tg.sent[chat][0], "arrived after the hold ended") {
		t.Fatalf("shop chat got %v; want one review notice", tg.sent[chat])
	}
}
