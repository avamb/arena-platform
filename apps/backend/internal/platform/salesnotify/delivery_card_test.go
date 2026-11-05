package salesnotify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

func cardSample(step string) CardState {
	s := shopSample()
	s.Order.ID = uuid.MustParse("0190f1a0-0000-7000-8000-000000000001")
	s.Order.Status = shop.StatusPaid
	s.Order.FulfillmentStep = &step
	return CardState{Shop: s, Now: time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)}
}

func rowData(rows [][]Button) []string {
	var out []string
	for _, r := range rows {
		for _, b := range r {
			out = append(out, b.Data[:strings.LastIndex(b.Data, ":")])
		}
	}
	return out
}

// The card offers the steps that may follow, "not delivered" in a row of its
// own, and nothing once the order is delivered or refunded.
func TestRenderDeliveryCard_ButtonsPerStep(t *testing.T) {
	cases := map[string]string{
		shop.StepWaiting:    "sd:p:en,sd:h:en,sd:f:en",
		shop.StepPacked:     "sd:h:en,sd:f:en",
		shop.StepHandedOver: "sd:d:en,sd:f:en",
		shop.StepFailed:     "sd:h:en",
		shop.StepDelivered:  "",
	}
	for step, want := range cases {
		c := RenderDeliveryCard(cardSample(step))
		if got := strings.Join(rowData(c.Rows), ","); got != want {
			t.Errorf("%s: buttons %q, want %q", step, got, want)
		}
		if c.Rows == nil {
			t.Errorf("%s: rows must be an empty list, not nil (it removes the keyboard)", step)
		}
	}
	waiting := RenderDeliveryCard(cardSample(shop.StepWaiting))
	if len(waiting.Rows) != 2 || len(waiting.Rows[1]) != 1 || !strings.Contains(waiting.Rows[1][0].Text, "Not delivered") {
		t.Errorf("not delivered is not on its own row: %+v", waiting.Rows)
	}
	// A waiting card that never moved is exactly the courier card.
	if waiting.Text != FormatShopOrderPaid(cardSample(shop.StepWaiting).Shop) {
		t.Errorf("a new card carries a status line:\n%s", waiting.Text)
	}
	refunded := cardSample(shop.StepPacked)
	refunded.Shop.Order.Status = shop.StatusRefunded
	c := RenderDeliveryCard(refunded)
	if len(c.Rows) != 0 || !strings.Contains(c.Text, "do not ship") {
		t.Errorf("refunded card: rows %+v\n%s", c.Rows, c.Text)
	}
}

func TestStatusLine_NameTimeTrackingReason(t *testing.T) {
	st := cardSample(shop.StepHandedOver)
	st.Shop.Locale = "ru"
	st.Shop.Order.TrackingNote = "RR123"
	st.Zone, _ = time.LoadLocation("Asia/Jerusalem")
	st.Last = &gen.ShopOrderEventRow{Step: shop.StepHandedOver, ActorName: "Иван <x>",
		CreatedAt: time.Date(2026, 11, 5, 12, 20, 0, 0, time.UTC)}
	if got, want := StatusLine(st), "🚚 Передан курьеру · Иван &lt;x&gt; · 14:20 · № RR123"; got != want {
		t.Errorf("status line %q, want %q", got, want)
	}
	st.Last.CreatedAt = time.Date(2026, 11, 3, 12, 20, 0, 0, time.UTC)
	if got := StatusLine(st); !strings.Contains(got, "03.11 14:20") {
		t.Errorf("another day carries no date: %q", got)
	}
	f := cardSample(shop.StepFailed)
	f.Last = &gen.ShopOrderEventRow{Step: shop.StepFailed, Reason: shop.ReasonNotHome, ActorName: "Ivan", CreatedAt: f.Now}
	if got := StatusLine(f); !strings.HasPrefix(got, "❌ Not delivered: Nobody home · Ivan · ") {
		t.Errorf("failed line %q", got)
	}
	f.Last.Reason = "dog <bites>"
	if got := StatusLine(f); !strings.Contains(got, "dog &lt;bites&gt;") {
		t.Errorf("a typed reason is not escaped: %q", got)
	}
}

// Every callback stays inside Telegram's 64 bytes and decodes back.
func TestCardAction_LengthAndRoundTrip(t *testing.T) {
	id := uuid.New()
	ops := []string{OpPacked, OpHandedOver, OpHandedOverPlain, OpDelivered, OpFailed, OpBack, OpReasonOther, "rn", "ra", "rw", "rr"}
	for _, loc := range []string{"en", "ru", "he", "pt-BR", ""} {
		for _, op := range ops {
			data := EncodeCardAction(op, loc, id)
			if len(data) > 64 {
				t.Errorf("%q is %d bytes", data, len(data))
			}
			a, ok := DecodeCardAction(data)
			if !ok || a.Op != op || a.OrderID != id || a.Locale != shopLocale(loc) {
				t.Errorf("%q decodes to %+v %v", data, a, ok)
			}
		}
	}
	for _, bad := range []string{"", "sd:", "sd:x:en:" + id.String(), "sd:p:en:not-a-uuid", "sd:rz:en:" + id.String(), "wz:p:en:" + id.String()} {
		if _, ok := DecodeCardAction(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
	if ReasonOf("ra") != shop.ReasonNotHome || ReasonOf("p") != "" {
		t.Error("ReasonOf")
	}
	for _, rows := range [][][]Button{ReasonButtons("ru", id), TrackingButtons("he", id), StepButtons("es", shop.StepWaiting, id)} {
		for _, r := range rows {
			for _, b := range r {
				if _, ok := DecodeCardAction(b.Data); !ok || b.Text == "" {
					t.Errorf("button %+v", b)
				}
			}
		}
	}
}

// ── the dispatcher with cards ──────────────────────────────────────────────

type cardStore struct {
	*fakeStore
	state CardState
	mu    sync.Mutex
	saved map[string]int64 // subscription -> message id
}

func (c *cardStore) CardState(context.Context, string, string) (CardState, error) {
	return c.state, nil
}
func (c *cardStore) SaveCard(_ context.Context, _, sub, _ string, msg int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saved[sub] = msg
	return nil
}
func (c *cardStore) Cards(context.Context, string) ([]CardRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []CardRef
	for sub, msg := range c.saved {
		out = append(out, CardRef{SubscriptionID: sub, ChatID: "-2", MessageID: msg})
	}
	return out, nil
}

type cardSender struct {
	fakeSender
	cards []DeliveryCard
	edits []DeliveryCard
}

func (s *cardSender) SendCard(_ context.Context, chat, text string, rows [][]Button) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sent{chat, text})
	s.cards = append(s.cards, DeliveryCard{Text: text, Rows: rows})
	return 500 + int64(len(s.cards)), nil
}

func (s *cardSender) EditCard(_ context.Context, _ string, _ int64, text string, rows [][]Button) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits = append(s.edits, DeliveryCard{Text: text, Rows: rows})
	return nil
}

func TestDispatcher_CardWithButtons_EditedOnStep_FailedToSales(t *testing.T) {
	store := &cardStore{fakeStore: newFakeStore(shopChats()...), saved: map[string]int64{}}
	store.shopOrds["o-1"] = shopSample()
	store.state = cardSample(shop.StepWaiting)
	snd := &cardSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil))
	d.Handle(ctx, shopEvent(EventShopOrderPaid, "o-1", nil)) // retry
	if len(snd.cards) != 1 || len(snd.cards[0].Rows) != 2 || store.saved["delivery"] != 501 {
		t.Fatalf("cards %+v saved %v", snd.cards, store.saved)
	}
	if len(sentTo(&snd.fakeSender, "-1")) != 1 {
		t.Fatalf("the sales chat got %v", sentTo(&snd.fakeSender, "-1"))
	}

	store.state = cardSample(shop.StepPacked)
	d.Handle(ctx, shopEvent(EventShopOrderFulfillmentChanged, "o-1", map[string]any{"fulfillment_step": "packed", "event_id": "e1"}))
	if len(snd.edits) != 1 || strings.Join(rowData(snd.edits[0].Rows), ",") != "sd:h:en,sd:f:en" {
		t.Fatalf("edits %+v", snd.edits)
	}
	if n := len(sentTo(&snd.fakeSender, "-1")); n != 1 {
		t.Fatalf("a packed step reached the sales chat (%d messages)", n)
	}

	store.state = cardSample(shop.StepFailed)
	ev := shopEvent(EventShopOrderFulfillmentChanged, "o-1", map[string]any{"fulfillment_step": "failed", "event_id": "e2", "reason": shop.ReasonWrongAddress})
	d.Handle(ctx, ev)
	d.Handle(ctx, ev) // retry
	sales := sentTo(&snd.fakeSender, "-1")
	if len(sales) != 2 || !strings.Contains(sales[1], "Order No. 1000777: Wrong address") || strings.Contains(sales[1], "anna@example.com") {
		t.Fatalf("sales chat after failed: %v", sales)
	}
	if len(sentTo(&snd.fakeSender, "-2")) != 1 {
		t.Fatalf("the delivery group got a second message instead of an edit")
	}

	// A refund edits the card: "do not ship", no buttons.
	refunded := cardSample(shop.StepPacked)
	refunded.Shop.Order.Status = shop.StatusRefunded
	store.state = refunded
	store.shopOrds["o-1"] = refunded.Shop
	d.Handle(ctx, shopEvent(EventShopOrderRefunded, "o-1", map[string]any{"refunded_minor": float64(23000)}))
	last := snd.edits[len(snd.edits)-1]
	if len(last.Rows) != 0 || !strings.Contains(last.Text, "do not ship") {
		t.Fatalf("refunded card edit %+v", last)
	}
}
