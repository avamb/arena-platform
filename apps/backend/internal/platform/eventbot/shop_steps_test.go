package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// shopStepOrder is a paid shipping order at a step, with the history a card shows.
func shopStepOrder(status, step string) openapi.ShopOrder {
	o := shopTestOrder(status)
	if step != "" {
		o.FulfillmentStep = strp(step)
	}
	return o
}

// The step of an order is the server's, and an order from before steps reads
// as handed over when it shipped and as waiting when it is paid.
func TestShopStep_DerivedWhenTheServerSendsNone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		o    openapi.ShopOrder
		want string
	}{
		{"paid, no step", shopStepOrder(shStatusPaid, ""), stepWaiting},
		{"partly refunded, no step", shopStepOrder(shStatusPartRefund, ""), stepWaiting},
		{"shipped, no step", shopStepOrder(shStatusShipped, ""), stepHandedOver},
		{"the server's step wins", shopStepOrder(shStatusPaid, stepPacked), stepPacked},
		{"unpaid", shopStepOrder(shStatusPending, ""), ""},
		{"in review", shopStepOrder(shStatusManual, ""), ""},
	}
	for _, c := range cases {
		if got := shopStep(c.o); got != c.want {
			t.Errorf("%s: step = %q, want %q", c.name, got, c.want)
		}
	}
	none := shopStepOrder(shStatusPaid, "")
	none.DeliveryKind = "none"
	if shopStep(none) != "" {
		t.Error("an order with nothing to hand over has no step")
	}
}

// The buttons mirror the server's transition table.
func TestShopStepOffered_FollowsTheTransitionTable(t *testing.T) {
	t.Parallel()
	all := []string{stepPacked, stepHandedOver, stepDelivered, stepFailed}
	want := map[string][]string{
		stepWaiting:    {stepPacked, stepHandedOver, stepFailed},
		stepPacked:     {stepHandedOver, stepFailed},
		stepHandedOver: {stepDelivered, stepFailed},
		stepDelivered:  nil,
		stepFailed:     {stepHandedOver},
	}
	for cur, steps := range want {
		o := shopStepOrder(shStatusPaid, cur)
		for _, s := range all {
			expect := false
			for _, w := range steps {
				expect = expect || w == s
			}
			if got := shopStepOffered(o, s); got != expect {
				t.Errorf("from %s to %s: offered = %v, want %v", cur, s, got, expect)
			}
		}
	}
	// A refunded, cancelled or unpaid order offers nothing, whatever its step.
	for _, st := range []string{shStatusRefunded, shStatusCancelled, shStatusExpired, shStatusPending, shStatusManual} {
		if shopStepOffered(shopStepOrder(st, stepWaiting), stepPacked) {
			t.Errorf("a %s order must not offer steps", st)
		}
	}
	// A refund never changes the step: a partly refunded order goes on.
	if !shopStepOffered(shopStepOrder(shStatusPartRefund, stepHandedOver), stepDelivered) {
		t.Error("a partly refunded order can still be delivered")
	}
}

// The ship button is the hand-over: from waiting, packed and a failed try, and
// the tracking edit only while the parcel is with the courier.
func TestShopCanShip_AndEditTrackingByStep(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		step       string
		ship, edit bool
	}{
		{stepWaiting, true, false}, {stepPacked, true, false}, {stepFailed, true, false},
		{stepHandedOver, false, true}, {stepDelivered, false, false},
	} {
		status := shStatusPaid
		o := shopStepOrder(status, c.step)
		if c.step == stepHandedOver || c.step == stepDelivered {
			o = shopStepOrder(shStatusShipped, c.step)
			now := time.Now()
			o.ShippedAt = &now
		}
		if c.step == stepFailed {
			// failed after a hand-over: the order is shipped, another try is offered
			o = shopStepOrder(shStatusShipped, c.step)
			now := time.Now()
			o.ShippedAt = &now
		}
		if got := shopCanShip(o); got != c.ship {
			t.Errorf("%s: canShip = %v, want %v", c.step, got, c.ship)
		}
		if got := shopCanEditTracking(o); got != c.edit {
			t.Errorf("%s: canEditTracking = %v, want %v", c.step, got, c.edit)
		}
	}
}

func TestShopTabs_FailedRoundTrips(t *testing.T) {
	t.Parallel()
	tab, ok := shopTabFromCode("f")
	if !ok || tab != shTabFailed || shopTabCode(tab) != "f" || !validShopTab(tab) {
		t.Errorf("failed tab: %q %v", tab, ok)
	}
}

func shopHistory(n int) *[]openapi.ShopOrderEvent {
	var out []openapi.ShopOrderEvent
	steps := []string{stepPacked, stepHandedOver, stepFailed, stepHandedOver, stepDelivered}
	for i := 0; i < n; i++ {
		e := openapi.ShopOrderEvent{
			Id: openapi_types.UUID(uuid.New()), Step: steps[i%len(steps)], ActorName: "Iván <b>", Via: "telegram_bot",
			CreatedAt: time.Date(2026, 11, 5, 14, 20+i, 0, 0, time.UTC),
		}
		if e.Step == stepHandedOver {
			e.Tracking = "TRACK-9"
		}
		if e.Step == stepFailed {
			e.Reason = "not_home"
		}
		out = append(out, e)
	}
	return &out
}

// The card names the step and lists the last lines of the history.
func TestShopCardText_StepAndHistory(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	o := shopStepOrder(shStatusPaid, stepFailed)
	o.Events = shopHistory(4)
	text := b.shopCardText("ru", o, "Europe/Madrid", true)
	for _, want := range []string{"Доставка:</b> Не доставлен", "Передан курьеру · Iván &lt;b&gt;", "номер TRACK-9", "причина: Нет дома", "05.11.2026 15:2"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	// Only the last three lines: the first event (Packed) is gone.
	if strings.Contains(text, "• Собран") {
		t.Errorf("the card shows more than %d lines:\n%s", shopHistoryLines, text)
	}
	if strings.Contains(text, "Iván <b>") {
		t.Error("the actor's name must be escaped")
	}
	// A reason somebody typed is shown as typed and escaped.
	typed := shopStepOrder(shStatusPaid, stepFailed)
	typed.Events = &[]openapi.ShopOrderEvent{{Id: openapi_types.UUID(uuid.New()), Step: stepFailed, Reason: "gate <closed>", ActorName: "Ana", CreatedAt: time.Now()}}
	if got := b.shopCardText("en", typed, "", true); !strings.Contains(got, "reason: gate &lt;closed&gt;") {
		t.Errorf("typed reason:\n%s", got)
	}
	// No delivery: no step block at all.
	none := shopStepOrder(shStatusPaid, "")
	none.DeliveryKind = "none"
	if got := b.shopCardText("en", none, "", true); strings.Contains(got, "Delivery:</b>") {
		t.Errorf("an order with no delivery shows a step:\n%s", got)
	}
}

// The step buttons follow the current step and stay inside Telegram's limit.
func TestShopCardKeyboard_StepButtons(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	cases := []struct {
		step string
		want []string
		not  []string
	}{
		{stepWaiting, []string{"sh:ps:p:", "sh:ps:f:", "sh:sh:"}, []string{"sh:ps:d:"}},
		{stepPacked, []string{"sh:ps:f:", "sh:sh:"}, []string{"sh:ps:p:", "sh:ps:d:"}},
	}
	for _, c := range cases {
		btns := shopCardButtons(t, b, shopStepOrder(shStatusPaid, c.step))
		for _, p := range c.want {
			if !hasPrefixIn(btns, p) {
				t.Errorf("%s lacks %q: %v", c.step, p, btns)
			}
		}
		for _, p := range c.not {
			if hasPrefixIn(btns, p) {
				t.Errorf("%s must not offer %q: %v", c.step, p, btns)
			}
		}
		for _, d := range btns {
			if len(d) > 64 {
				t.Errorf("callback data %q is %d bytes", d, len(d))
			}
		}
	}
	shipped := shopStepOrder(shStatusShipped, stepHandedOver)
	now := time.Now()
	shipped.ShippedAt = &now
	btns := shopCardButtons(t, b, shipped)
	if !hasPrefixIn(btns, "sh:ps:d:") || !hasPrefixIn(btns, "sh:ps:f:") || hasPrefixIn(btns, "sh:ps:p:") {
		t.Errorf("handed over: %v", btns)
	}
	delivered := shopStepOrder(shStatusShipped, stepDelivered)
	delivered.ShippedAt = &now
	btns = shopCardButtons(t, b, delivered)
	if hasPrefixIn(btns, "sh:ps:") || hasPrefixIn(btns, "sh:sh:") {
		t.Errorf("a delivered order has no step buttons: %v", btns)
	}
	// The delivered letter exists once, and only for a delivered order.
	if got := shopLetters(delivered); !strings.Contains(strings.Join(got, ","), "delivered") {
		t.Errorf("delivered order letters = %v", got)
	}
	if got := shopLetters(shipped); strings.Contains(strings.Join(got, ","), "delivered") {
		t.Errorf("a shipped order has no delivered letter: %v", got)
	}
	if c := shopLetterCode("delivered"); c != "d" {
		t.Errorf("delivered letter code = %q", c)
	} else if l, ok := shopLetterFromCode(c); !ok || l != "delivered" {
		t.Errorf("delivered letter from code = %q %v", l, ok)
	}
	// An order in review offers the hand-over only, never packed or failed.
	review := shopCardButtons(t, b, shopTestOrder(shStatusManual))
	if hasPrefixIn(review, "sh:ps:") {
		t.Errorf("an order in review offers no steps: %v", review)
	}
}

// The reason picker: four buttons with codes, "other" and a way back, and every
// payload within 64 bytes.
func TestShopReasonCodes(t *testing.T) {
	t.Parallel()
	for _, r := range shopReasonCodes {
		got, ok := shopReasonFromCode(r.code)
		if !ok || got != r.reason {
			t.Errorf("code %q -> %q %v", r.code, got, ok)
		}
	}
	if _, ok := shopReasonFromCode("o"); ok {
		t.Error("\"other\" has no reason code: the person's text is the reason")
	}
	if d := "sh:fr:o:" + uuid.NewString(); len(d) > 64 {
		t.Errorf("callback data too long: %d", len(d))
	}
}

// fakeShopAPI answers the bot's shop calls and records the fulfillment bodies.
type fakeShopAPI struct {
	mu     sync.Mutex
	bodies []map[string]any
	lists  []string
	orders map[string][]openapi.ShopOrder // by fulfillment_step
}

func (f *fakeShopAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/fulfillment"):
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			f.mu.Lock()
			f.bodies = append(f.bodies, body)
			f.mu.Unlock()
			if r.Header.Get("Authorization") == "" {
				t.Error("fulfillment call without a token")
			}
			o := shopStepOrder(shStatusPaid, body["step"].(string))
			_ = json.NewEncoder(w).Encode(o)
		case strings.HasSuffix(r.URL.Path, "/orders/counts"):
			_, _ = w.Write([]byte(`{"all":9,"to_ship":9,"unpaid":0,"shipped":0,"refunds":0,"steps":{"waiting":3,"packed":2,"handed_over":0,"delivered":0,"failed":1}}`))
		case strings.HasSuffix(r.URL.Path, "/orders") || strings.HasSuffix(r.URL.Path, "/orders/search"):
			step := r.URL.Query().Get("fulfillment_step")
			if r.Method == http.MethodPost {
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				step, _ = body["fulfillment_step"].(string)
			}
			f.mu.Lock()
			f.lists = append(f.lists, step)
			rows := f.orders[step]
			f.mu.Unlock()
			limit := 50
			if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
				limit = n
			}
			resp := map[string]any{"orders": rows}
			if len(rows) > limit {
				resp["orders"] = rows[:limit]
				resp["next_offset"] = limit
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	})
}

func shopOrderAt(num int64, minute int, step string) openapi.ShopOrder {
	o := shopStepOrder(shStatusPaid, step)
	o.Id = openapi_types.UUID(uuid.New())
	o.Number = num
	o.CreatedAt = time.Date(2026, 11, 5, 10, minute, 0, 0, time.UTC)
	return o
}

// The "To ship" tab reads waiting and packed apart and shows them together,
// newest first; the total is the sum of the two step counts.
func TestShopFetchShip_MergesWaitingAndPacked(t *testing.T) {
	api := &fakeShopAPI{orders: map[string][]openapi.ShopOrder{
		stepWaiting: {shopOrderAt(5, 50, stepWaiting), shopOrderAt(3, 30, stepWaiting), shopOrderAt(1, 10, stepWaiting)},
		stepPacked:  {shopOrderAt(4, 40, stepPacked), shopOrderAt(2, 20, stepPacked)},
	}}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	b := newShopMoneyBot(t, srv.URL)
	st := newShopDialog(uuid.New(), "")
	st.Tab = shTabShip
	p, err := b.shopFetchShip(context.Background(), "jwt", &st)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 5 || p.Pages != 1 || len(p.Orders) != 5 {
		t.Fatalf("page = total %d pages %d rows %d", p.Total, p.Pages, len(p.Orders))
	}
	var nums []string
	for _, o := range p.Orders {
		nums = append(nums, strconv.FormatInt(o.Number, 10))
	}
	if got := strings.Join(nums, " "); got != "5 4 3 2 1" {
		t.Errorf("order = %s, want newest first 5 4 3 2 1", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.lists) != 2 || api.lists[0] != stepWaiting || api.lists[1] != stepPacked {
		t.Errorf("steps read = %v", api.lists)
	}
}

// The step counts say how many orders there are, so a page past the end falls
// back to the last page that exists.
func TestShopFetchShip_PageBeyondTheEndFallsBack(t *testing.T) {
	api := &fakeShopAPI{orders: map[string][]openapi.ShopOrder{
		stepWaiting: {shopOrderAt(3, 30, stepWaiting)},
		stepPacked:  {shopOrderAt(2, 20, stepPacked)},
	}}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	b := newShopMoneyBot(t, srv.URL)
	st := newShopDialog(uuid.New(), "")
	st.Tab, st.Page = shTabShip, 4
	p, err := b.shopFetchShip(context.Background(), "jwt", &st)
	if err != nil {
		t.Fatal(err)
	}
	if st.Page != 1 || p.Pages != 1 || len(p.Orders) != 2 {
		t.Errorf("page = %d of %d with %d rows", st.Page, p.Pages, len(p.Orders))
	}
}

// A press of a step goes to the fulfillment route with the step, a reason code
// for a failed delivery and no tracking number.
func TestShopSetStep_SendsTheStepAndReason(t *testing.T) {
	api := &fakeShopAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	b := newShopMoneyBot(t, srv.URL)
	if _, err := b.arena.ShopFulfillment(context.Background(), "jwt", uuid.New(), uuid.New(), stepFailed, nil, "not_home"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.arena.ShopShip(context.Background(), "jwt", uuid.New(), uuid.New(), "TRACK-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.arena.ShopShip(context.Background(), "jwt", uuid.New(), uuid.New(), ""); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.bodies) != 3 {
		t.Fatalf("calls = %d", len(api.bodies))
	}
	if b0 := api.bodies[0]; b0["step"] != "failed" || b0["reason"] != "not_home" || b0["tracking"] != nil {
		t.Errorf("failed body = %v", b0)
	}
	if b1 := api.bodies[1]; b1["step"] != "handed_over" || b1["tracking"] != "TRACK-7" {
		t.Errorf("ship body = %v", b1)
	}
	// "No number" stores an empty number, so a later edit can also clear it.
	if b2 := api.bodies[2]; b2["step"] != "handed_over" || b2["tracking"] != "" {
		t.Errorf("ship without a number = %v", b2)
	}
}

// Every new text exists in every language the bot speaks and the picker labels
// differ from the step words.
func TestShopSteps_TextsInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		for _, s := range []string{stepWaiting, stepPacked, stepHandedOver, stepDelivered, stepFailed} {
			if w := b.shopStepWord(loc, s); w == "" || strings.HasPrefix(w, "bot.sh.") {
				t.Errorf("%s: step %s -> %q", loc, s, w)
			}
		}
		for _, r := range shopReasonCodes {
			if w := b.shopReasonWord(loc, r.reason); w == "" || strings.HasPrefix(w, "bot.sh.") {
				t.Errorf("%s: reason %s -> %q", loc, r.reason, w)
			}
		}
	}
	if b.shopStepWord("ru", stepHandedOver) != "Передан курьеру" {
		t.Error("the ship step is called «Передан курьеру»")
	}
}
