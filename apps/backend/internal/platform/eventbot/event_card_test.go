package eventbot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func ecTestBot(t *testing.T) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	return &Bot{texts: NewTexts(bundle)}
}

const ecPlaces = `{"ga":{"available":30,"held":2,"sold":12,"sold_upstream":0,"total":50,"unavailable":6},"seats":{"available":0,"held":0,"sold":0,"sold_upstream":0,"total":0,"unavailable":0}}`

func ecSession(day string) string {
	return `{"id":"` + uuid.NewString() + `","start_at":"2099-01-` + day + `T19:00:00Z","end_at":"2099-01-` + day + `T21:00:00Z","status":"scheduled","capacity_total":50,
	  "venue_name":"Hall <1>","venue_timezone":"Europe/Madrid","has_seating_plan":false,
	  "complimentary":{"orders":0,"tickets":0},"entered":{"total":0,"used":0},"money":[{"currency":"EUR","discount":0,"net":5000,"paid":5000,"paid_orders":2,"pending":0,"pending_orders":0,"refunded":0,"service_charge":0}],
	  "orders":[],"places":` + ecPlaces + `,"promos":[],"refunds":[],"tickets":{"active":2,"cancelled":0,"complimentary":0,"transferred":0,"used":0},"tiers":[]}`
}

func ecEventSummary(t *testing.T, sessions int) openapi.EventSummary {
	t.Helper()
	var parts []string
	for i := 0; i < sessions; i++ {
		parts = append(parts, ecSession("1"+string(rune('0'+i))))
	}
	list := strings.Join(parts, ",")
	raw := `{"event":{"id":"` + uuid.NewString() + `","name":"Swan <Lake>","org_id":"` + uuid.NewString() + `","session_count":` + "2" + `,"status":"published"},
	 "complimentary":{"orders":1,"tickets":2},"entered":{"total":10,"used":4},
	 "money":[{"currency":"EUR","discount":500,"net":8700,"paid":10000,"paid_orders":4,"pending":2500,"pending_orders":1,"refunded":1300,"service_charge":200}],
	 "orders":[],"places":` + ecPlaces + `,
	 "promos":[{"code":"SPRING<","currency":"EUR","discount":500,"id":"` + uuid.NewString() + `","orders":2,"redemptions":2}],
	 "refunds":[{"amount":1000,"currency":"EUR","refunds":1,"settlement":"provider","state":"succeeded"},
	            {"amount":300,"currency":"EUR","refunds":1,"settlement":"external","state":"succeeded"},
	            {"amount":999,"currency":"EUR","refunds":3,"settlement":"provider","state":"requested"}],
	 "sessions":[` + list + `],
	 "tickets":{"active":8,"cancelled":2,"complimentary":2,"transferred":1,"used":4},
	 "tiers":[{"currency":"EUR","is_open":true,"kind":"ga","name":"Standing <b>","paid_items":8,"paid_revenue":9000,"places":{"available":30,"held":2,"sold":12,"sold_upstream":0,"total":50,"unavailable":0},"price_amount":2500,"sessions":2},
	          {"currency":"EUR","is_open":false,"kind":"ga","name":"VIP","paid_items":0,"paid_revenue":0,"places":{"available":0,"held":0,"sold":0,"sold_upstream":0,"total":10,"unavailable":0},"price_amount":9000,"sessions":2}]}`
	var s openapi.EventSummary
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return s
}

func assertCallbacksFit(t *testing.T, label string, rows [][]models.InlineKeyboardButton) {
	t.Helper()
	for _, row := range rows {
		for _, b := range row {
			if len([]byte(b.CallbackData)) > 64 {
				t.Errorf("%s: callback %q is %d bytes, over Telegram's 64", label, b.CallbackData, len([]byte(b.CallbackData)))
			}
		}
	}
}

func TestEventCard_Figures_En(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	eventID := uuid.New()
	var sb strings.Builder
	rows := b.writeCardFigures(&sb, "en", eventID, ecEventSummary(t, 2), false)
	text := sb.String()
	for _, want := range []string{
		"All dates (2)", "Next date: 10.01.2099 20:00",
		"Sold 12 of 50, available 30, held 2",
		"Paid 100 EUR in 4 orders",
		"Refunds: 2 for 13 EUR, net 87 EUR", // two succeeded refunds, the requested one is not counted
		"Entered: 4 of 10",
		"Invitations: 2",
		"SPRING&lt; — used 2 times, discount 5 EUR",
		"Categories:",
		"• Standing &lt;b&gt; — 25 EUR — 12/50 — 90 EUR",
		"• VIP (closed) — 90 EUR — 0/10 — 0 EUR",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Hall") {
		t.Errorf("collapsed card must not list the dates:\n%s", text)
	}
	// Summary + CSV, then the dates toggle.
	if len(rows) != 2 || rows[0][0].CallbackData != "ec:es:"+eventID.String() || rows[0][1].CallbackData != "ec:ce:"+eventID.String() ||
		rows[1][0].CallbackData != "ec:o:"+eventID.String()+":1" {
		t.Errorf("collapsed rows = %+v", rows)
	}
	assertCallbacksFit(t, "collapsed", rows)
}

func TestEventCard_ExpandedListsEveryDate(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	eventID := uuid.New()
	var sb strings.Builder
	rows := b.writeCardFigures(&sb, "en", eventID, ecEventSummary(t, 2), true)
	text := sb.String()
	if strings.Count(text, "Hall &lt;1&gt;") != 2 || !strings.Contains(text, "10.01.2099 20:00") || !strings.Contains(text, "11.01.2099 20:00") {
		t.Errorf("expanded card must list both dates in the venue zone:\n%s", text)
	}
	// summary+csv, one row per date (summary, csv), hide.
	if len(rows) != 4 || rows[1][0].Text != "📊 10.01 20:00" || !strings.HasPrefix(rows[1][0].CallbackData, "ec:ss:") ||
		!strings.HasPrefix(rows[1][1].CallbackData, "ec:cs:") || rows[3][0].CallbackData != "ec:o:"+eventID.String()+":0" {
		t.Errorf("expanded rows = %+v", rows)
	}
	assertCallbacksFit(t, "expanded", rows)
}

func TestEventCard_SingleDateNamesTheDateAndHasNoToggle(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	var sb strings.Builder
	rows := b.writeCardFigures(&sb, "en", uuid.New(), ecEventSummary(t, 1), false)
	text := sb.String()
	if !strings.Contains(text, "<b>10.01.2099 20:00</b> Hall &lt;1&gt;") || strings.Contains(text, "All dates") {
		t.Errorf("single-date card:\n%s", text)
	}
	if len(rows) != 1 {
		t.Errorf("a single-date event needs only summary and CSV, got %+v", rows)
	}
}

func TestEventCard_NoSessions(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	var sb strings.Builder
	rows := b.writeCardFigures(&sb, "en", uuid.New(), ecEventSummary(t, 0), false)
	if !strings.Contains(sb.String(), "no dates yet") || len(rows) != 1 {
		t.Errorf("no-sessions card: %q rows=%d", sb.String(), len(rows))
	}
}

func TestSummaryText_MoneyDownToTheNet(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	sum := ecEventSummary(t, 2)
	text := b.summaryText("en", summaryView{Title: sum.Event.Name, Scope: "All dates (2)", Figures: figuresFromEvent(sum)})
	for _, want := range []string{
		"Summary: Swan &lt;Lake&gt;",
		"Tickets: valid 8, cancelled or returned 2, entered 4, transferred 1",
		"Orders: paid 4, awaiting payment 1",
		"Places: sold 12, available 30, held 2, withheld 6, total 50",
		"Paid 100 EUR in 4 orders (discounts 5 EUR, service charge 2 EUR)",
		"Refunds: 2 for 13 EUR, of which 3 EUR outside the system",
		"Net: <b>87 EUR</b>",
		"Awaiting payment: 25 EUR in 1 orders",
		"• Standing &lt;b&gt;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("summary lacks %q:\n%s", want, text)
		}
	}
}

func TestSessionFigures_ConvertAnonymousStructs(t *testing.T) {
	t.Parallel()
	raw := `{"complimentary":{"orders":1,"tickets":1},"entered":{"total":3,"used":1},
	 "money":[{"currency":"CZK","discount":0,"net":4000,"paid":5000,"paid_orders":2,"pending":0,"pending_orders":0,"refunded":1000,"service_charge":0}],
	 "orders":[],"places":` + ecPlaces + `,"promos":[{"code":"A","currency":"CZK","discount":100,"id":"` + uuid.NewString() + `","orders":1,"redemptions":1}],
	 "refunds":[{"amount":1000,"currency":"CZK","refunds":1,"settlement":"provider","state":"succeeded"}],
	 "session":{"capacity_total":50,"event_id":"` + uuid.NewString() + `","event_name":"E","has_seating_plan":false,"id":"` + uuid.NewString() + `","org_id":"` + uuid.NewString() + `","start_at":"2099-01-10T19:00:00Z","status":"scheduled","venue_name":null,"venue_timezone":null},
	 "tickets":{"active":3,"cancelled":0,"complimentary":1,"transferred":0,"used":1},
	 "tiers":[{"currency":"CZK","id":"` + uuid.NewString() + `","is_open":true,"kind":"ga","name":"Std","paid_items":2,"paid_revenue":5000,"places":{"available":30,"held":2,"sold":12,"sold_upstream":0,"total":50,"unavailable":0},"price_amount":2500}]}`
	var s openapi.SessionSummary
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	f := figuresFromSession(s)
	if f.Places.Sold != 12 || f.Places.Unavailable != 6 || len(f.Money) != 1 || f.Money[0].Net != 4000 ||
		len(f.Promos) != 1 || f.Promos[0].Code != "A" || len(f.Refunds) != 1 || len(f.Tiers) != 1 || f.Tiers[0].Revenue != 5000 ||
		f.Entered.Used != 1 || f.Comp.Tickets != 1 || f.Tickets.Active != 3 {
		t.Errorf("figures = %+v", f)
	}
	if count, amount, external := refundTotals(f.Refunds, "CZK"); count != 1 || amount != 1000 || external != 0 {
		t.Errorf("refundTotals = %d %d %d", count, amount, external)
	}
	if count, _, _ := refundTotals(f.Refunds, "EUR"); count != 0 {
		t.Errorf("another currency must not count: %d", count)
	}
}

// Every screen of the stage renders on all three languages without a raw key
// or a missing template value (the wizard has the same test for its steps).
func TestEventCenterScreens_RenderOnEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		sum := ecEventSummary(t, 2)
		var card strings.Builder
		b.writeCardFigures(&card, loc, uuid.New(), sum, true)
		screens := map[string]string{
			"card":      card.String(),
			"summary":   b.summaryText(loc, summaryView{Title: "N", Scope: "S", Figures: figuresFromEvent(sum)}),
			"empty":     b.summaryText(loc, summaryView{Title: "N", Scope: "S"}),
			"owner":     b.texts.T(loc, "bot.ec.notif_owner", map[string]any{"Bot": "@" + SalesBotUsername}),
			"manager":   b.texts.T(loc, "bot.ec.notif_manager", map[string]any{"Bot": "@" + SalesBotUsername}),
			"empty_run": b.emptyListText(loc, newEventsDialog(uuid.New())),
			"empty_arc": b.emptyListText(loc, eventsDialog{Filter: evFilterArc}),
			"none":      b.emptyListText(loc, eventsDialog{Filter: evFilterRun, Query: "zzz"}),
		}
		for name, text := range screens {
			if strings.TrimSpace(text) == "" || strings.Contains(text, "bot.ec.") || strings.Contains(text, "bot.money") ||
				strings.Contains(text, "<no value>") {
				t.Errorf("%s/%s renders badly:\n%s", loc, name, text)
			}
		}
		if loc == "ru" && !strings.Contains(screens["card"], "Возвраты: 2 на 13 EUR, нетто 87 EUR") {
			t.Errorf("ru card lacks the refunds line:\n%s", screens["card"])
		}
		if loc == "es" && !strings.Contains(screens["card"], "Devoluciones: 2 por 13 EUR, neto 87 EUR") {
			t.Errorf("es card lacks the refunds line:\n%s", screens["card"])
		}
	}
}

func TestNotificationsLink(t *testing.T) {
	t.Parallel()
	if got := salesBotURL(); got != "https://t.me/ArenaSoldOutSalesBot" {
		t.Errorf("salesBotURL = %q", got)
	}
}

func TestClipMessage(t *testing.T) {
	t.Parallel()
	short := "a\nb"
	if got := clipMessage(short, 100); got != short {
		t.Errorf("short text changed: %q", got)
	}
	long := strings.Repeat("line of text\n", 400)
	got := clipMessage(long, 3800)
	if len([]rune(got)) > 3810 || !strings.HasSuffix(got, "\n…") || strings.HasSuffix(strings.TrimSuffix(got, "\n…"), "\n") {
		t.Errorf("clip: %d runes, ends %q", len([]rune(got)), got[len(got)-12:])
	}
}

func TestCanViewSales(t *testing.T) {
	t.Parallel()
	mk := func(role string, super bool) *Identity {
		return &Identity{Current: &Membership{Role: role}, Superadmin: super}
	}
	for _, c := range []struct {
		role  string
		super bool
		want  bool
	}{
		{"org_admin", false, true}, {"organizer", false, true}, {"agent", false, false}, {"", false, false}, {"agent", true, true},
	} {
		if got := canViewSales(mk(c.role, c.super)); got != c.want {
			t.Errorf("canViewSales(%q, super=%v) = %v, want %v", c.role, c.super, got, c.want)
		}
	}
	if canViewSales(nil) || canViewSales(&Identity{}) {
		t.Error("no identity, no sales")
	}
}
