package eventbot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func tierFixture(name, kind string) openapi.TicketTierItem {
	t := openapi.TicketTierItem{
		Id: uuid.New(), Name: name, Currency: "EUR", PriceAmount: 2500, PricingMode: "fixed",
		Quantity: i32p(50), Sold: i32p(3), Held: i32p(1), Available: i32p(46),
	}
	if kind != "" {
		t.Kind = &kind
	}
	return t
}

func TestCategoryCaps(t *testing.T) {
	t.Parallel()
	ga := tierFixture("Standing", "ga")
	seated := tierFixture("Stalls", "seated")
	free := tierFixture("Gift", "ga")
	free.PricingMode = "free"
	pwyw := tierFixture("Donation", "ga")
	pwyw.PricingMode = "pwyw"
	head, tail := tierFixture("Early", "ga"), tierFixture("Late", "ga")
	head.NextTierId = &tail.Id
	stepped := tierFixture("Step", "ga")
	stepped.SellLimit = i32p(30)
	planless := tierFixture("Mapping", "")
	all := []openapi.TicketTierItem{ga, seated, free, pwyw, head, tail, stepped, planless}

	cases := []struct {
		name string
		t    openapi.TicketTierItem
		want categoryCaps
	}{
		{"ga", ga, categoryCaps{Toggle: true, Price: true, Window: true, Qty: true}},
		{"seated: no quantity", seated, categoryCaps{Toggle: true, Price: true, Window: true, Seated: true}},
		{"free: no price", free, categoryCaps{Toggle: true, Window: true, Qty: true, NotFixed: true}},
		{"pwyw: no price", pwyw, categoryCaps{Toggle: true, Window: true, Qty: true, NotFixed: true}},
		{"chain head: price only", head, categoryCaps{Price: true, Chain: true}},
		{"chain target: price only", tail, categoryCaps{Price: true, Chain: true}},
		{"quantity step: price only", stepped, categoryCaps{Price: true, Chain: true}},
		{"no kind: no quantity", planless, categoryCaps{Toggle: true, Price: true, Window: true}},
	}
	for _, c := range cases {
		if got := categoryCapsFor(c.t, all); got != c.want {
			t.Errorf("%s: caps = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseCategoryQuantity(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int32{"1": 1, " 120 ": 120, "1 000": 1000, "1000000": 1000000} {
		if got, ok := parseCategoryQuantity(in); !ok || got != want {
			t.Errorf("%q = (%d, %v), want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "0", "-5", "12.5", "ten", "1000001", "1e3"} {
		if _, ok := parseCategoryQuantity(in); ok {
			t.Errorf("%q must be refused", in)
		}
	}
}

func TestCategoryScheduledPrice(t *testing.T) {
	t.Parallel()
	now := time.Date(2099, 5, 10, 12, 0, 0, 0, time.UTC)
	to := now.Add(24 * time.Hour)
	ws := []openapi.TierPriceWindow{
		{ValidFrom: now.Add(-48 * time.Hour), ValidTo: ptrTime(now.Add(-24 * time.Hour)), PriceAmount: 1000},
		{ValidFrom: now.Add(-time.Hour), ValidTo: &to, PriceAmount: 2000},
		{ValidFrom: to, PriceAmount: 3000},
	}
	i := activeWindow(ws, now)
	if i != 1 {
		t.Fatalf("active window = %d, want 1", i)
	}
	out := withWindowPrice(ws, i, 2222)
	if out[1].PriceAmount != 2222 || ws[1].PriceAmount != 2000 || out[0].PriceAmount != 1000 || out[2].PriceAmount != 3000 {
		t.Errorf("only the active window may change, and the input must stay as it was: %+v / %+v", out, ws)
	}
	if got := activeWindow(ws[:1], now); got != -1 {
		t.Errorf("no window covers now, got %d", got)
	}
	if got := activeWindow(nil, now); got != -1 {
		t.Errorf("no schedule, got %d", got)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestWindowEdges_UseTheVenuesMidnight(t *testing.T) {
	t.Parallel()
	const tz = "Europe/Prague"
	start, ok := windowEdge("2099-11-05", tz, false)
	if !ok {
		t.Fatal("start edge")
	}
	end, ok := windowEdge("2099-11-05", tz, true)
	if !ok {
		t.Fatal("end edge")
	}
	if end.Sub(start) != 24*time.Hour {
		t.Errorf("an inclusive last day ends 24h after the day starts: %v", end.Sub(start))
	}
	// 2099-11-05 00:00 in Prague (UTC+1 in November) is 23:00 UTC the day before.
	if got := start.UTC().Format(time.RFC3339); got != "2099-11-04T23:00:00Z" {
		t.Errorf("start = %s", got)
	}
	if got := windowEdgeLabel(start, tz, false); got != "05.11.2099" {
		t.Errorf("start label = %q", got)
	}
	if got := windowEdgeLabel(end, tz, true); got != "05.11.2099" {
		t.Errorf("an inclusive end prints the last day, got %q", got)
	}
	if got := windowEdgeLabel(start.Add(19*time.Hour+30*time.Minute), tz, true); !strings.Contains(got, "19:30") {
		t.Errorf("an edge that is not a midnight keeps its clock, got %q", got)
	}
	if _, ok := windowEdge("not a date", tz, false); ok {
		t.Error("a bad date must be refused")
	}
}

func TestCategoryErrKey(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"tier.quantity_below_used": "bot.cat.err_qty_below",
		"tier.seated_category":     "bot.cat.err_qty_seated",
		"tier.invalid_sale_window": "bot.cat.err_window",
		"tier.invalid_capacity":    "bot.cat.err_qty",
		"tier.capacity_required":   "bot.cat.err_qty",
		"tier.not_found":           "",
	} {
		if got := categoryErrKey(&APIError{Status: 409, Code: code}); got != want {
			t.Errorf("%s -> %q, want %q", code, got, want)
		}
	}
}

// The list line, the card and the window labels render in every language for
// every kind of category: no key name, no missing value.
func TestCategoryScreens_RenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	st := categoryDialog{EventName: "Swan <Lake>", When: "05.11.2099 19:00", Tz: "Europe/Prague", SalesEnd: "2099-11-05T18:00:00Z"}
	ga := tierFixture("Standing <A>", "ga")
	first, _ := windowEdge("2099-10-01", st.Tz, false)
	last, _ := windowEdge("2099-10-31", st.Tz, true)
	ga.SaleWindowStart, ga.SaleWindowEnd = &first, &last
	closed := tierFixture("Closed", "ga")
	closed.IsOpen = new(bool)
	next := time.Date(2099, 10, 20, 8, 0, 0, 0, time.UTC)
	closed.NextPriceChangeAt = &next
	seated := tierFixture("Stalls", "seated")
	seated.SaleWindowStart = ga.SaleWindowStart
	free := tierFixture("Gift", "ga")
	free.PricingMode = "free"
	pwyw := tierFixture("Donation", "ga")
	pwyw.PricingMode = "pwyw"
	head, tail := tierFixture("Early", "ga"), tierFixture("Late", "ga")
	head.NextTierId = &tail.Id
	bare := tierFixture("Bare", "")
	bare.Quantity, bare.Sold, bare.Held, bare.Available = nil, nil, nil, nil
	all := []openapi.TicketTierItem{ga, closed, seated, free, pwyw, head, tail, bare}
	for _, loc := range SupportedLocales {
		for _, tier := range all {
			line := b.categoryLine(loc, tier)
			card := b.categoryCardText(loc, st, tier, all)
			for label, text := range map[string]string{"line": line, "card": card} {
				if strings.TrimSpace(text) == "" || strings.Contains(text, "<no value>") || strings.Contains(text, "bot.cat.") {
					t.Errorf("%s %s of %s = %q", loc, label, tier.Name, text)
				}
			}
			if !strings.Contains(line, Esc(tier.Name)) {
				t.Errorf("%s: the line lacks the escaped name: %q", loc, line)
			}
		}
		if card := b.categoryCardText(loc, st, ga, all); !strings.Contains(card, "Swan") && !strings.Contains(card, "05.11.2099 19:00") {
			t.Errorf("%s: the card lacks the date: %q", loc, card)
		}
		card := b.categoryCardText(loc, st, closed, all)
		if !strings.Contains(card, "20.10.2099") {
			t.Errorf("%s: the scheduled price change must be dated: %q", loc, card)
		}
		if c := b.categoryCardText(loc, st, head, all); !strings.Contains(c, "Late") {
			t.Errorf("%s: a chain head names the category it hands over to: %q", loc, c)
		}
		if c := b.categoryCardText(loc, st, ga, all); !strings.Contains(c, "01.10.2099") || !strings.Contains(c, "31.10.2099") {
			t.Errorf("%s: the window is printed with its last day inclusive: %q", loc, c)
		}
	}
}

func TestCategoryCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	pid := uuid.NewString()
	for _, data := range []string{
		"ct:e:" + pid, "ct:b", "ct:back", "ct:home", "ct:p:12", "ct:o:4", "ct:noop", "ct:v:" + pid, "ct:op:" + pid, "ct:cl:" + pid,
		"ct:pr:" + pid, "ct:ps:" + pid, "ct:qt:" + pid, "ct:wn:" + pid, "ct:ws:" + pid, "ct:we:" + pid, "ct:wx:" + pid,
		"ct:cal:2099-11", "ct:d:2099-11-05", "ct:dc:2099-11-05", "ct:dc:retry",
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

func TestCategoryEventButton_OnlyForRolesThatManageCategories(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	ev := uuid.New()
	for role, want := range map[string]bool{"org_admin": true, "organizer": true, "agent": false, "": false} {
		row := b.categoryEventRow("en", &Identity{Current: &Membership{Role: role}}, ev)
		if (row != nil) != want {
			t.Errorf("button for %q = %v, want %v", role, row != nil, want)
		}
		if row != nil && row[0].CallbackData != "ct:e:"+ev.String() {
			t.Errorf("data = %q", row[0].CallbackData)
		}
	}
	if b.categoryEventRow("en", &Identity{Current: &Membership{Role: "agent"}, Superadmin: true}, ev) == nil {
		t.Error("the platform operator manages categories too")
	}
}

func TestCategoryDialog_JSONRoundTripKeepsTheQuestion(t *testing.T) {
	t.Parallel()
	card := uuid.New()
	st := categoryDialog{
		OrgID: uuid.New(), EventID: uuid.New(), SessionID: uuid.New(), EventName: "E", When: "w", Tz: "Europe/Prague",
		MsgID: 7, Page: 2, IDs: []uuid.UUID{card}, CardID: &card, Pending: 1250,
		Cal: &Draft{Version: draftSchemaVersion, Step: stSDate, Sessions: []DraftSession{{Timezone: "Europe/Prague"}}},
	}
	st.Cal.Scratch.CalMonth = "2099-11"
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back categoryDialog
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.CardID == nil || *back.CardID != card || back.Pending != 1250 || back.Page != 2 || back.Cal == nil || back.Cal.Scratch.CalMonth != "2099-11" || back.Cal.Sessions[0].Timezone != "Europe/Prague" {
		t.Errorf("round trip lost state: %+v", back)
	}
}

// Every bot.cat.* key is registered and present in every language.
func TestCategoryKeys_AreRegisteredAndTranslated(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	registered := map[string]bool{}
	for _, k := range MessageKeys {
		registered[k] = true
	}
	vars := map[string]any{
		"Name": "N", "When": "w", "Total": 3, "Page": 1, "Pages": 2, "Chip": "●", "Price": "p", "Value": "v", "State": "s",
		"Sold": 1, "Held": 1, "Available": 1, "Min": "a", "Max": "b", "Date": "d", "Currency": "EUR", "Old": "o", "New": "n", "Limit": "5",
	}
	for _, key := range categoryKeys {
		if !registered[key] {
			t.Errorf("%s is not in MessageKeys", key)
		}
		en := b.texts.T("en", key, vars)
		for _, loc := range SupportedLocales {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
			if loc != "en" && got == en && key != "bot.cat.line" && key != "bot.cat.card_head" {
				t.Errorf("%s %s is a copy of English: %q", loc, key, got)
			}
		}
	}
}
