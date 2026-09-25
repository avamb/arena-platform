package salesnotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

const (
	orgVino     = "org-vino"
	orgLampyris = "org-lampyris"
)

func TestRoute_OrganizationChatsOnlySeeTheirOwnSales(t *testing.T) {
	subs := []Subscription{
		{ID: "op", Name: "operator", ChatID: "-1", OnOrderPaid: true, OnTicketRefunded: true},
		{ID: "vino", OrgID: orgVino, ChatID: "-2", OnOrderPaid: true, OnTicketRefunded: true},
		{ID: "lamp", OrgID: orgLampyris, ChatID: "-3", OnOrderPaid: true, OnTicketRefunded: false},
	}
	ids := func(ss []Subscription) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(Route(subs, orgVino, TriggerOrderPaid)); got != "op,vino" {
		t.Fatalf("vino sale routed to %q, want op,vino", got)
	}
	if got := ids(Route(subs, orgLampyris, TriggerTicketRefunded)); got != "op" {
		t.Fatalf("lampyris refund routed to %q, want op only (trigger off)", got)
	}
	if got := ids(Route(subs, "org-other", TriggerOrderPaid)); got != "op" {
		t.Fatalf("unsubscribed org routed to %q, want operator only", got)
	}
}

func TestFormatSale_EventOrderAndBuyerBlocks(t *testing.T) {
	msg := FormatSale(Sale{
		OrgName:     "Vino&Co",
		EventName:   "Sea, opera <wine>",
		VenueName:   "Ashdod Yam",
		StartAt:     time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC), // Israel is back on UTC+2 by then
		TimeZone:    "Asia/Jerusalem",
		OrderNumber: 2602726,
		Source:      "bil24_gateway",
		ChannelName: "Vino&Co WP",
		Currency:    "ILS",
		Total:       50000,
		PromoCode:   "VINO10",
		Categories:  []CategoryCount{{Name: "Entry", Count: 2}, {Name: "VIP", Count: 1}},
		Buyer:       Buyer{Name: "Инна <P>", Email: "inna@example.com", Phone: "+972501234567"},
	})
	for _, want := range []string{
		"🎟 <b>New sale</b> — Vino&amp;Co\n\n<b>Sea, opera &lt;wine&gt;</b>\n",
		"📅 Thu 29 Oct 2026, 20:00\n📍 Ashdod Yam\n", // venue time, not UTC
		"🧾 Order #2602726 · website (Vino&amp;Co WP)\n",
		"🎫 2 × Entry\n🎫 1 × VIP\n",
		"🏷 Promo code: <code>VINO10</code>\n",
		"💰 Total: <b>500.00 ILS</b>\n\n👤 Инна &lt;P&gt;\n✉️ inna@example.com\n📞 +972501234567",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("sale message lacks %q:\n%s", want, msg)
		}
	}
}

func TestFormatSale_PartialOrNoContact(t *testing.T) {
	msg := FormatSale(Sale{OrgName: "Vino&Co", EventName: "Quiz", Currency: "ILS", Total: 100,
		Categories: []CategoryCount{{Name: "Entry", Count: 1}}, Buyer: Buyer{Email: "only@example.com"}})
	if !strings.HasSuffix(msg, "<b>1.00 ILS</b>\n\n✉️ only@example.com") || strings.Contains(msg, "👤") || strings.Contains(msg, "📞") {
		t.Errorf("partial contact rendered wrong:\n%s", msg)
	}
	bare := FormatSale(Sale{OrgName: "Vino&Co", EventName: "Quiz", Currency: "ILS", Total: 100})
	if !strings.HasSuffix(bare, "💰 Total: <b>1.00 ILS</b>") {
		t.Errorf("no contact should end at the total:\n%s", bare)
	}
}

func TestFormatRefund_UnknownZoneFallsBackToLabelledUTC(t *testing.T) {
	start := time.Date(2026, 10, 1, 17, 0, 0, 0, time.UTC)
	msg := FormatRefund(Refund{OrgName: "Vino&Co", EventName: "Quiz", StartAt: start,
		OrderNumber: 1000155528, TicketNumber: 73, Currency: "ILS", Amount: 13500,
		Buyer: Buyer{Name: "Anna", Phone: "+420777000111"}})
	for _, want := range []string{"↩️ <b>Refund</b> — Vino&amp;Co", "📅 Thu 01 Oct 2026, 17:00 UTC", "🧾 Order #1000155528 · ticket #73",
		"💸 Refunded: <b>135.00 ILS</b>\n\n👤 Anna\n📞 +420777000111"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refund message lacks %q:\n%s", want, msg)
		}
	}
}

// ── Dispatcher with fakes ───────────────────────────────────────────────

type fakeStore struct {
	mu       sync.Mutex
	subs     []Subscription
	sales    map[string]Sale
	refunds  map[string]Refund
	claimed  map[string]bool
	chatIDs  map[string]string
	lastErrs map[string]string
}

func newFakeStore(subs ...Subscription) *fakeStore {
	return &fakeStore{subs: subs, sales: map[string]Sale{}, refunds: map[string]Refund{},
		claimed: map[string]bool{}, chatIDs: map[string]string{}, lastErrs: map[string]string{}}
}

func (f *fakeStore) Subscriptions(context.Context) ([]Subscription, error) { return f.subs, nil }
func (f *fakeStore) SaleByOrder(_ context.Context, id string) (Sale, error) {
	s, ok := f.sales[id]
	if !ok {
		return Sale{}, ErrNotFound
	}
	return s, nil
}
func (f *fakeStore) RefundByTicket(_ context.Context, id string) (Refund, error) {
	r, ok := f.refunds[id]
	if !ok {
		return Refund{}, ErrNotFound
	}
	return r, nil
}
func (f *fakeStore) Claim(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed[key] {
		return false, nil
	}
	f.claimed[key] = true
	return true, nil
}
func (f *fakeStore) UpdateChatID(_ context.Context, id, chat string) error {
	f.chatIDs[id] = chat
	return nil
}
func (f *fakeStore) RecordDelivery(_ context.Context, id, lastErr string) error {
	f.lastErrs[id] = lastErr
	return nil
}

type sent struct{ chat, text string }

type fakeSender struct {
	mu      sync.Mutex
	sent    []sent
	failFor map[string]error
}

func (s *fakeSender) Send(_ context.Context, chat, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failFor[chat]; err != nil {
		return err
	}
	s.sent = append(s.sent, sent{chat, text})
	return nil
}

func paidEvent(orderID string) outbox.Event {
	return outbox.Event{ID: "ev-" + orderID, AggregateType: "order", AggregateID: orderID,
		EventType: EventOrderPaid, Payload: map[string]any{"order_id": orderID}}
}

func TestDispatcher_RoutesPerOrganizationAndPostsOnce(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "op", Name: "operator", ChatID: "-100", OnOrderPaid: true, OnTicketRefunded: true},
		Subscription{ID: "vino", OrgID: orgVino, Name: "Vino&Co", ChatID: "-200", OnOrderPaid: true, OnTicketRefunded: true},
	)
	store.sales["o-vino"] = Sale{OrgID: orgVino, OrgName: "Vino&Co", OrderNumber: 2}
	store.sales["o-lamp"] = Sale{OrgID: orgLampyris, OrgName: "Lampyris", OrderNumber: 3}
	store.refunds["t-1"] = Refund{OrgID: orgVino, OrgName: "Vino&Co", OrderNumber: 2, TicketNumber: 73, Currency: "ILS"}
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()

	d.Handle(ctx, paidEvent("o-vino"))
	d.Handle(ctx, paidEvent("o-lamp"))
	// The outbox retries the whole event when another leg fails.
	d.Handle(ctx, paidEvent("o-vino"))
	// A provider refund raises both events for one ticket; the amount only
	// travels on the refunded one.
	d.Handle(ctx, outbox.Event{EventType: EventTicketCancelled, AggregateID: "t-1", Payload: map[string]any{"ticket_id": "t-1"}})
	d.Handle(ctx, outbox.Event{EventType: EventTicketRefunded, AggregateID: "t-1", Payload: map[string]any{"ticket_id": "t-1", "amount": float64(13500)}})

	byChat := map[string][]string{}
	for _, s := range snd.sent {
		byChat[s.chat] = append(byChat[s.chat], s.text)
	}
	if len(byChat["-100"]) != 3 || len(byChat["-200"]) != 2 {
		t.Fatalf("deliveries: operator %d (want 3), Vino %d (want 2)", len(byChat["-100"]), len(byChat["-200"]))
	}
	for _, m := range byChat["-200"] {
		if strings.Contains(m, "Lampyris") {
			t.Fatalf("Vino chat received a Lampyris sale: %s", m)
		}
	}
	if !strings.Contains(byChat["-200"][1], "Ticket cancelled") {
		t.Fatalf("the first event of the refund decides the message: %s", byChat["-200"][1])
	}
}

func TestDispatcher_RefundAmountFromEventWhenTicketHasNone(t *testing.T) {
	store := newFakeStore(Subscription{ID: "vino", OrgID: orgVino, ChatID: "-200", OnTicketRefunded: true})
	store.refunds["t-2"] = Refund{OrgID: orgVino, OrgName: "Vino&Co", OrderNumber: 2, TicketNumber: 74, Currency: "ILS"}
	snd := &fakeSender{}
	NewDispatcher(store, snd, nil).Handle(context.Background(),
		outbox.Event{EventType: EventTicketRefunded, AggregateID: "t-2", Payload: map[string]any{"ticket_id": "t-2", "amount": float64(13500)}})
	if len(snd.sent) != 1 || !strings.Contains(snd.sent[0].text, "Refunded: <b>135.00 ILS</b>") {
		t.Fatalf("got %v", snd.sent)
	}
}

func TestDispatcher_FollowsSupergroupMigrationAndRecordsBrokenChats(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "vino", OrgID: orgVino, Name: "Vino&Co", ChatID: "-200", OnOrderPaid: true},
		Subscription{ID: "gone", OrgID: orgVino, Name: "gone", ChatID: "-300", OnOrderPaid: true},
	)
	store.sales["o-1"] = Sale{OrgID: orgVino, OrderNumber: 2}
	snd := &fakeSender{failFor: map[string]error{
		"-200": &MigratedError{NewChatID: "-1002001"},
		"-300": &PermanentError{Code: 403, Description: "Forbidden: bot was kicked from the group chat"},
	}}
	NewDispatcher(store, snd, nil).Handle(context.Background(), paidEvent("o-1"))
	if store.chatIDs["vino"] != "-1002001" {
		t.Fatalf("migrated chat id not stored: %v", store.chatIDs)
	}
	if len(snd.sent) != 1 || snd.sent[0].chat != "-1002001" {
		t.Fatalf("message not resent to the supergroup: %v", snd.sent)
	}
	if !strings.Contains(store.lastErrs["gone"], "kicked") || store.lastErrs["vino"] != "" {
		t.Fatalf("last errors = %v", store.lastErrs)
	}
}

func TestDispatcher_NeverFailsTheOutboxAndIgnoresOtherEvents(t *testing.T) {
	store := newFakeStore(Subscription{ID: "op", ChatID: "-100", OnOrderPaid: true})
	d := NewDispatcher(store, &fakeSender{}, nil)
	for _, ev := range []outbox.Event{
		{EventType: "v1.event.published", AggregateID: "e"},
		paidEvent("missing-order"),
	} {
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("Dispatch(%s) = %v, want nil", ev.EventType, err)
		}
	}
	if len(d.queue) != 1 {
		t.Fatalf("queued %d events, want only the order.paid one", len(d.queue))
	}
	// An unknown order is dropped quietly, never claimed.
	d.Handle(context.Background(), <-d.queue)
	if len(store.claimed) != 0 {
		t.Fatalf("claimed %v for a missing order", store.claimed)
	}
	// Without a bot token nothing is queued at all.
	off := NewDispatcher(store, nil, nil)
	_ = off.Dispatch(context.Background(), paidEvent("x"))
	if len(off.queue) != 0 {
		t.Fatal("a disabled notifier must not queue")
	}
}

// ── TelegramSender against a stub Bot API ────────────────────────────────

func TestTelegramSender_ClassifiesBotAPIAnswers(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botSECRET/sendMessage") {
			t.Errorf("path %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		switch got["chat_id"] {
		case "-1":
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "-2":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234}}`)
		case "-3":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member of the group chat"}`)
		default:
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		}
	}))
	defer srv.Close()
	s := NewTelegramSender("SECRET", srv.URL)
	s.sleep = func(context.Context, time.Duration) {}
	ctx := context.Background()

	if err := s.Send(ctx, "-1", "<b>hi</b>"); err != nil {
		t.Fatalf("ok answer: %v", err)
	}
	if got["parse_mode"] != "HTML" {
		t.Fatalf("parse_mode = %v", got["parse_mode"])
	}
	var mig *MigratedError
	if err := s.Send(ctx, "-2", "x"); !errors.As(err, &mig) || mig.NewChatID != "-1001234" {
		t.Fatalf("migration: %v", err)
	}
	var perm *PermanentError
	if err := s.Send(ctx, "-3", "x"); !errors.As(err, &perm) {
		t.Fatalf("not a member: %v", err)
	}
	err := s.Send(ctx, "-4", "x")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("bad token must fail without leaking the token: %v", err)
	}
}
