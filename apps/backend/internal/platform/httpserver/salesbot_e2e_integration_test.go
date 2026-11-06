//go:build integration

package httpserver

// salesbot_e2e_integration_test.go — the delivery group's buttons end to end
// (spec 08_architecture/61 section 7b): a platform superadmin makes a code,
// "/connect <code>" in a group binds it, the worker's Telegram leg sends the
// paid order's card with its buttons, a member presses them, the sales bot
// (package salesbot, the arena-bot poller) reaches the real router under the
// bot service token, the step lands in the database and the card is edited in
// place, by the bot at once and by the worker from the outbox event. Only
// Telegram is a stub.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// salesTG is the Bot API for both the worker's sender (JSON bodies) and the
// bot's library (multipart as soon as a param is nested).
type salesTG struct {
	t       *testing.T
	srv     *httptest.Server
	updates chan string
	mu      sync.Mutex
	calls   []salesTGCall
	seq     int
	upd     int
}

type salesTGCall struct {
	method string
	p      map[string]string
	id     int
}

func newSalesTG(t *testing.T) *salesTG {
	s := &salesTG{t: t, updates: make(chan string, 16), seq: 100}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *salesTG) handle(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	w.Header().Set("Content-Type", "application/json")
	if method == "getUpdates" {
		select {
		case u := <-s.updates:
			s.mu.Lock()
			s.upd++
			id := s.upd
			s.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":%d,%s}]}`, id, u)
		case <-time.After(200 * time.Millisecond):
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		}
		return
	}
	p := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			p[k] = v[0]
		}
	} else {
		body, _ := io.ReadAll(r.Body)
		raw := map[string]any{}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.UseNumber()
		_ = dec.Decode(&raw)
		for k, v := range raw {
			switch x := v.(type) {
			case string:
				p[k] = x
			case json.Number:
				p[k] = x.String()
			default:
				b, _ := json.Marshal(x)
				p[k] = string(b)
			}
		}
	}
	s.mu.Lock()
	s.seq++
	id := s.seq
	if method == "editMessageText" || method == "editMessageReplyMarkup" {
		_, _ = fmt.Sscanf(p["message_id"], "%d", &id)
	}
	s.calls = append(s.calls, salesTGCall{method: method, p: p, id: id})
	s.mu.Unlock()
	switch method {
	case "getMe":
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Sales","username":"ArenaSalesTestBot"}}`))
	case "sendMessage", "editMessageText", "editMessageReplyMarkup":
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":%d,"chat":{"id":%s,"type":"supergroup"},"text":"x"}}`,
			id, time.Now().Unix(), orDefault(p["chat_id"], "1"))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// wait returns the first call after mark that matches.
func (s *salesTG) wait(mark int, within time.Duration, match func(salesTGCall) bool) (salesTGCall, bool) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, c := range s.calls[min(mark, len(s.calls)):] {
			if match(c) {
				s.mu.Unlock()
				return c, true
			}
		}
		s.mu.Unlock()
		time.Sleep(25 * time.Millisecond)
	}
	return salesTGCall{}, false
}

func (s *salesTG) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func tgMessage(chat int64, from int64, text, extra string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"supergroup","title":"Couriers"},`+
		`"from":{"id":%d,"is_bot":false,"first_name":"Op","language_code":"en"},"text":%q%s}`,
		time.Now().UnixNano()%100000, time.Now().Unix(), chat, from, text, extra)
}

func tgPress(chat int64, msgID int, data string) string {
	return fmt.Sprintf(`"callback_query":{"id":"cb%d","from":{"id":42,"is_bot":false,"first_name":"Ivan","last_name":"K"},"chat_instance":"x",`+
		`"data":%q,"message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"supergroup"},"text":"card"}}`,
		time.Now().UnixNano()%100000, data, msgID, time.Now().Unix(), chat)
}

// handleOutbox hands the order's outbox events of a type to the worker's leg.
func handleOutbox(t *testing.T, e *deliveryEnv, d *salesnotify.Dispatcher, orderID uuid.UUID, eventType string) int {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id::text, aggregate_type, aggregate_id, event_type, payload
		FROM outbox_events WHERE aggregate_id = $1 AND event_type = $2 ORDER BY occurred_at, id`, orderID.String(), eventType)
	if err != nil {
		t.Fatal(err)
	}
	var evs []outbox.Event
	for rows.Next() {
		var ev outbox.Event
		var raw []byte
		if err := rows.Scan(&ev.ID, &ev.AggregateType, &ev.AggregateID, &ev.EventType, &raw); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &ev.Payload)
		evs = append(evs, ev)
	}
	rows.Close()
	for _, ev := range evs {
		d.Handle(context.Background(), ev)
	}
	return len(evs)
}

func TestSalesBotE2E_ConnectCardPressReply(t *testing.T) {
	e := newDeliveryEnv(t)
	tg := newSalesTG(t)
	api := httptest.NewServer(e.srv.router)
	t.Cleanup(api.Close)

	bot, err := salesbot.New(salesbot.Options{
		Token: "sales-test-token", API: salesbot.NewHTTPAPI(api.URL, deliverySvcToken, nil),
		Prompts: salesbot.NewPGPrompts(e.pool), TelegramServerURL: tg.srv.URL, HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = bot.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	chat := -1000000000000 - int64(uuid.New().ID()%1000000)
	chatS := fmt.Sprint(chat)

	// 1. A code from the admin screen, "/connect" in the group.
	_, created := e.doHdr(http.MethodPost, "/v1/admin/organizations/"+e.orgA.String()+"/sales-chats/connect-codes", e.super,
		`{"name":"m5-test couriers","kinds":["shop_delivery"]}`, map[string]string{"X-Admin-Reason": "m5 e2e"})
	code, _ := created["code"].(string)
	if code == "" {
		t.Fatalf("no code: %v", created)
	}
	m := tg.mark()
	tg.updates <- tgMessage(chat, 7, "/connect@ArenaSalesTestBot "+code, "")
	reply, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "sendMessage" && c.p["chat_id"] == chatS })
	if !ok || !strings.Contains(reply.p["text"], "Chat connected: Shop A") || !strings.Contains(reply.p["text"], "shop orders to deliver") {
		t.Fatalf("connect reply %+v", reply)
	}
	// A wrong code is refused plainly.
	m = tg.mark()
	tg.updates <- tgMessage(chat, 7, "/connect WRONG-CODE0", "")
	if r, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "sendMessage" }); !ok || !strings.Contains(r.p["text"], "did not work") {
		t.Fatalf("wrong code reply %+v", r)
	}

	// 2. The order is paid: the worker's leg sends the card with buttons.
	_, variant := e.openShop(4500, 10)
	o := e.paidOrder(variant, 1)
	d := salesnotify.NewDispatcher(salesnotify.NewPGStore(e.pool), salesnotify.NewTelegramSender("sales-test-token", tg.srv.URL), nil).WithDeliveryButtons(true)
	m = tg.mark()
	if n := handleOutbox(t, e, d, o.ID, shop.EventOrderPaid); n != 1 {
		t.Fatalf("paid events = %d", n)
	}
	card, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "sendMessage" && c.p["chat_id"] == chatS })
	if !ok || !strings.Contains(card.p["reply_markup"], "sd:p:en:"+o.ID.String()) {
		t.Fatalf("card %+v", card)
	}
	var stored int64
	if err := e.pool.QueryRow(context.Background(), `SELECT message_id FROM sales_notification_cards WHERE order_id = $1`, o.ID).Scan(&stored); err != nil || stored != int64(card.id) {
		t.Fatalf("card message %d %v; want %d", stored, err, card.id)
	}

	// 3. "Packed" from the group: the step, the history, the card edited.
	m = tg.mark()
	tg.updates <- tgPress(chat, card.id, salesnotify.EncodeCardAction(salesnotify.OpPacked, "en", o.ID))
	if c, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "editMessageText" && c.id == card.id }); !ok ||
		!strings.Contains(c.p["text"], "📦 Packed · Ivan K") {
		t.Fatalf("card after packed %+v", c)
	}
	if a, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "answerCallbackQuery" }); !ok || !strings.Contains(a.p["text"], "Done") {
		t.Fatalf("toast %+v", a)
	}
	got, _ := e.q.GetShopOrder(context.Background(), o.ID, e.orgA)
	if shop.CurrentStep(got) != shop.StepPacked {
		t.Fatalf("step = %s", shop.CurrentStep(got))
	}

	// 4. "Handed to courier": a question, the reply carries the number.
	m = tg.mark()
	tg.updates <- tgPress(chat, card.id, salesnotify.EncodeCardAction(salesnotify.OpHandedOver, "en", o.ID))
	q, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool {
		return c.method == "sendMessage" && strings.Contains(c.p["reply_markup"], "force_reply")
	})
	if !ok {
		t.Fatal("no question for the number")
	}
	m = tg.mark()
	tg.updates <- tgMessage(chat, 43, "TRK-9", fmt.Sprintf(`,"reply_to_message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"supergroup"},"text":"q"}`, q.id, time.Now().Unix(), chat))
	if _, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "deleteMessage" }); !ok {
		t.Fatal("the question was not removed")
	}
	got, _ = e.q.GetShopOrder(context.Background(), o.ID, e.orgA)
	if shop.CurrentStep(got) != shop.StepHandedOver || got.TrackingNote != "TRK-9" || got.Status != shop.StatusShipped {
		t.Fatalf("after the reply: %s %q %s", shop.CurrentStep(got), got.TrackingNote, got.Status)
	}

	// 5. The worker edits the card from the outbox events too.
	m = tg.mark()
	if n := handleOutbox(t, e, d, o.ID, shop.EventOrderFulfillmentChanged); n != 2 {
		t.Fatalf("fulfillment events = %d", n)
	}
	if c, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "editMessageText" && c.id == card.id }); !ok ||
		!strings.Contains(c.p["text"], "No. TRK-9") || !strings.Contains(c.p["reply_markup"], "sd:d:en:") {
		t.Fatalf("worker's card edit %+v", c)
	}

	// 6. A chat that is not this organization's delivery group may not press.
	m = tg.mark()
	tg.updates <- tgPress(chat-7, 5, salesnotify.EncodeCardAction(salesnotify.OpDelivered, "en", o.ID))
	if a, ok := tg.wait(m, 5*time.Second, func(c salesTGCall) bool { return c.method == "answerCallbackQuery" }); !ok || !strings.Contains(a.p["text"], "not connected") {
		t.Fatalf("foreign chat toast %+v", a)
	}

	// 7. The bot is removed from the group: nothing more goes there.
	m = tg.mark()
	tg.updates <- fmt.Sprintf(`"my_chat_member":{"chat":{"id":%d,"type":"supergroup"},"from":{"id":7,"is_bot":false,"first_name":"Op"},"date":%d,`+
		`"old_chat_member":{"status":"member","user":{"id":1,"is_bot":true,"first_name":"S"}},`+
		`"new_chat_member":{"status":"kicked","user":{"id":1,"is_bot":true,"first_name":"S"},"until_date":0}}`, chat, time.Now().Unix())
	deadline := time.Now().Add(5 * time.Second)
	for {
		var removed bool
		_ = e.pool.QueryRow(context.Background(), `SELECT bot_removed_at IS NOT NULL FROM sales_notification_subscriptions WHERE chat_id = $1`, chatS).Scan(&removed)
		if removed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the removal was not recorded")
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = m
}
