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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
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

func TestFormatSale_EnglishAndNoBuyerData(t *testing.T) {
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
		Categories:  []CategoryCount{{Name: "Entry", Count: 2}},
	})
	for _, want := range []string{
		"🎟 <b>New sale</b> · Vino&amp;Co",
		"<b>Sea, opera &lt;wine&gt;</b>",
		"Thu 29 Oct 2026, 20:00 · Ashdod Yam", // venue time, not UTC
		"Order #2602726 · website · Vino&amp;Co WP",
		"Tickets: 2 (Entry × 2)",
		"Promo code: VINO10",
		"Total: <b>500.00 ILS</b>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("sale message lacks %q:\n%s", want, msg)
		}
	}
}

func TestFormatRefund_UnknownZoneFallsBackToLabelledUTC(t *testing.T) {
	start := time.Date(2026, 10, 1, 17, 0, 0, 0, time.UTC)
	msg := FormatRefund(Refund{OrgName: "Vino&Co", EventName: "Quiz", StartAt: &start,
		OrderNumber: 1000155528, TicketNumber: 73, Currency: "ILS", Amount: 13500})
	for _, want := range []string{"↩️ <b>Refund</b>", "Thu 01 Oct 2026, 17:00 UTC", "Order #1000155528 · ticket #73", "Amount: <b>135.00 ILS</b>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refund message lacks %q:\n%s", want, msg)
		}
	}
}

// ── RunOnce with fakes ──────────────────────────────────────────────────

type fakeStore struct {
	subs     []Subscription
	sales    []Sale
	refunds  []Refund
	chatIDs  map[string]string
	lastErrs map[string]string
}

func (f *fakeStore) Subscriptions(context.Context) ([]Subscription, error) { return f.subs, nil }
func (f *fakeStore) SalesSince(_ context.Context, ts time.Time, id string, _ int) ([]Sale, error) {
	var out []Sale
	for _, s := range f.sales {
		if s.At.After(ts) || (s.At.Equal(ts) && s.ID > id) {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) RefundsSince(_ context.Context, ts time.Time, id string, _ int) ([]Refund, error) {
	var out []Refund
	for _, r := range f.refunds {
		if r.At.After(ts) || (r.At.Equal(ts) && r.ID > id) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) UpdateChatID(_ context.Context, id, chat string) error {
	f.chatIDs[id] = chat
	return nil
}
func (f *fakeStore) RecordDelivery(_ context.Context, id, lastErr string) error {
	f.lastErrs[id] = lastErr
	return nil
}

type memCursors struct{ m map[string]opswatchdog.Cursor }

func (c *memCursors) Get(_ context.Context, k string) (*opswatchdog.Cursor, error) {
	v, ok := c.m[k]
	if !ok {
		return nil, nil
	}
	return &v, nil
}
func (c *memCursors) Set(_ context.Context, k string, v opswatchdog.Cursor) error {
	c.m[k] = v
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

func TestRunOnce_FirstRunNeverReplaysHistoryThenDeliversPerOrg(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		subs: []Subscription{
			{ID: "op", Name: "operator", ChatID: "-100", OnOrderPaid: true, OnTicketRefunded: true},
			{ID: "vino", OrgID: orgVino, Name: "Vino&Co", ChatID: "-200", OnOrderPaid: true, OnTicketRefunded: true},
		},
		sales:    []Sale{{ID: "a", At: t0.Add(-time.Hour), OrgID: orgVino, OrderNumber: 1}},
		chatIDs:  map[string]string{},
		lastErrs: map[string]string{},
	}
	cur := &memCursors{m: map[string]opswatchdog.Cursor{}}
	snd := &fakeSender{}
	now := t0
	opts := Options{Store: store, Cursors: cur, Sender: snd, Now: func() time.Time { return now }}

	if err := RunOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatalf("first run replayed history: %v", snd.sent)
	}

	store.sales = append(store.sales,
		Sale{ID: "b", At: t0.Add(time.Minute), OrgID: orgVino, OrderNumber: 2},
		Sale{ID: "c", At: t0.Add(2 * time.Minute), OrgID: orgLampyris, OrderNumber: 3})
	store.refunds = []Refund{{ID: "r", At: t0.Add(3 * time.Minute), OrgID: orgVino, OrderNumber: 2, Amount: 100}}
	if err := RunOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	byChat := map[string]int{}
	for _, s := range snd.sent {
		byChat[s.chat]++
		if s.chat == "-200" && strings.Contains(s.text, "#3") {
			t.Fatalf("Vino chat received a Lampyris sale: %s", s.text)
		}
	}
	if byChat["-100"] != 3 || byChat["-200"] != 2 {
		t.Fatalf("deliveries per chat = %v, want operator 3 and Vino 2", byChat)
	}

	snd.sent = nil
	if err := RunOnce(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 0 {
		t.Fatalf("second pass resent: %v", snd.sent)
	}
}

func TestRunOnce_FollowsSupergroupMigrationAndRecordsBrokenChats(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		subs: []Subscription{
			{ID: "vino", OrgID: orgVino, Name: "Vino&Co", ChatID: "-200", OnOrderPaid: true},
			{ID: "lamp", OrgID: orgVino, Name: "gone", ChatID: "-300", OnOrderPaid: true},
		},
		sales:    []Sale{{ID: "b", At: t0.Add(time.Minute), OrgID: orgVino, OrderNumber: 2}},
		chatIDs:  map[string]string{},
		lastErrs: map[string]string{},
	}
	past := t0
	cur := &memCursors{m: map[string]opswatchdog.Cursor{cursorSales: {TS: &past}, cursorRefunds: {TS: &past}}}
	snd := &fakeSender{failFor: map[string]error{
		"-200": &MigratedError{NewChatID: "-1002001"},
		"-300": &PermanentError{Code: 403, Description: "Forbidden: bot was kicked from the group chat"},
	}}
	if err := RunOnce(context.Background(), Options{Store: store, Cursors: cur, Sender: snd, Now: func() time.Time { return t0 }}); err != nil {
		t.Fatal(err)
	}
	if store.chatIDs["vino"] != "-1002001" {
		t.Fatalf("migrated chat id not stored: %v", store.chatIDs)
	}
	if len(snd.sent) != 1 || snd.sent[0].chat != "-1002001" {
		t.Fatalf("message not resent to the supergroup: %v", snd.sent)
	}
	if !strings.Contains(store.lastErrs["lamp"], "kicked") || store.lastErrs["vino"] != "" {
		t.Fatalf("last errors = %v", store.lastErrs)
	}
	if c := cur.m[cursorSales]; c.ID != "b" {
		t.Fatalf("a broken chat must not hold the cursor back, cursor = %+v", c)
	}
}

func TestRunOnce_BurstBecomesOneDigestPerChat(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		subs:     []Subscription{{ID: "vino", OrgID: orgVino, ChatID: "-200", OnOrderPaid: true}},
		chatIDs:  map[string]string{},
		lastErrs: map[string]string{},
	}
	for i := 0; i < digestThreshold+2; i++ {
		store.sales = append(store.sales, Sale{ID: string(rune('a' + i)), At: t0.Add(time.Duration(i+1) * time.Second),
			OrgID: orgVino, OrderNumber: int64(i + 1), Currency: "ILS", Total: 1000,
			Categories: []CategoryCount{{Name: "Entry", Count: 1}}})
	}
	past := t0
	cur := &memCursors{m: map[string]opswatchdog.Cursor{cursorSales: {TS: &past}, cursorRefunds: {TS: &past}}}
	snd := &fakeSender{}
	if err := RunOnce(context.Background(), Options{Store: store, Cursors: cur, Sender: snd, Now: func() time.Time { return t0 }}); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 || !strings.Contains(snd.sent[0].text, "12 new sales</b>, 12 tickets") ||
		!strings.Contains(snd.sent[0].text, "120.00 ILS") {
		t.Fatalf("want one digest, got %v", snd.sent)
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
