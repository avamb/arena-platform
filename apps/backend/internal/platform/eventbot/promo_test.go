package eventbot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func promoTestBot(t *testing.T) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	texts := NewTexts(bundle)
	return &Bot{texts: texts, wizard: NewWizard(texts, nil)}
}

func TestNormalizePromoCode(t *testing.T) {
	t.Parallel()
	good := map[string]string{
		"summer25": "SUMMER25", "  club-vip_1 ": "CLUB-VIP_1", "a.b": "A.B", "Лето2026": "ЛЕТО2026",
		strings.Repeat("a", 64): strings.Repeat("A", 64),
	}
	for in, want := range good {
		if got, key := normalizePromoCode(in); got != want || key != "" {
			t.Errorf("%q -> %q (%s), want %q", in, got, key, want)
		}
	}
	bad := map[string]string{
		"": "bot.promo.err_code_empty", "   ": "bot.promo.err_code_empty",
		strings.Repeat("a", 65): "bot.promo.err_code_long",
		"two words":             "bot.promo.err_code_chars", "SALE!": "bot.promo.err_code_chars", "10%": "bot.promo.err_code_chars",
	}
	for in, wantKey := range bad {
		if got, key := normalizePromoCode(in); key != wantKey || got != "" {
			t.Errorf("%q -> %q (%s), want refusal %s", in, got, key, wantKey)
		}
	}
}

func TestParsePromoNumbers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int64{"15": 15, "15%": 15, " 100 % ": 100, "1": 1} {
		if got, ok := parsePromoPercent(in); !ok || got != want {
			t.Errorf("percent %q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"0", "101", "150", "-5", "12.5", "abc", "", "%"} {
		if _, ok := parsePromoPercent(in); ok {
			t.Errorf("percent %q must be refused", in)
		}
	}
	for in, want := range map[string]int64{"5": 500, "5.5": 550, "5,50": 550, "0.05": 5, "1 000": 100000, "12,3": 1230, ".5": 50} {
		if got, ok := parsePromoAmount(in); !ok || got != want {
			t.Errorf("amount %q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"0", "0,00", "", "abc", "5.555", "1,000.50", "1.000,5", "-3", "5 EUR", "99999999999"} {
		if got, ok := parsePromoAmount(in); ok {
			t.Errorf("amount %q must be refused, got %d", in, got)
		}
	}
	for in, want := range map[string]int32{"1": 1, "100": 100, "1 000": 1000, "1000000": 1000000} {
		if got, ok := parsePromoLimit(in); !ok || got != want {
			t.Errorf("limit %q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"0", "-1", "1000001", "x", "", "1.5"} {
		if _, ok := parsePromoLimit(in); ok {
			t.Errorf("limit %q must be refused", in)
		}
	}
	if c, ok := parsePromoCurrency(" eur "); !ok || c != "EUR" {
		t.Errorf("currency = %q %v", c, ok)
	}
	for _, in := range []string{"EU", "EURO", "12E", "€", ""} {
		if _, ok := parsePromoCurrency(in); ok {
			t.Errorf("currency %q must be refused", in)
		}
	}
}

func promoItem(status string) openapi.PromoCodeItem {
	return openapi.PromoCodeItem{
		Id: uuid.New(), Code: "SUMMER25", DiscountType: promoTypePercent, DiscountValue: 15,
		Status: openapi.PromoCodeItemStatus(status), AppliesToSessionIds: []uuid.UUID{},
	}
}

func TestPromoState(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	one, two := int32(1), int32(2)
	cases := []struct {
		name string
		mod  func(*openapi.PromoCodeItem)
		want string
	}{
		{"plain", func(*openapi.PromoCodeItem) {}, promoStActive},
		{"paused", func(it *openapi.PromoCodeItem) { it.Status = "paused" }, promoStPaused},
		{"expired", func(it *openapi.PromoCodeItem) { it.ValidUntil = &past }, promoStExpired},
		{"until in the future", func(it *openapi.PromoCodeItem) { it.ValidUntil = &future }, promoStActive},
		{"not started", func(it *openapi.PromoCodeItem) { it.ValidFrom = &future }, promoStScheduled},
		{"used up", func(it *openapi.PromoCodeItem) { it.MaxUses, it.Uses = &one, 1 }, promoStExhausted},
		{"some left", func(it *openapi.PromoCodeItem) { it.MaxUses, it.Uses = &two, 1 }, promoStActive},
		{"paused beats expired", func(it *openapi.PromoCodeItem) { it.Status, it.ValidUntil = "paused", &past }, promoStPaused},
	}
	for _, c := range cases {
		it := promoItem("active")
		c.mod(&it)
		if got := promoState(it, now); got != c.want {
			t.Errorf("%s: state = %s, want %s", c.name, got, c.want)
		}
	}
	for _, st := range []string{promoStActive, promoStPaused, promoStExpired, promoStExhausted, promoStScheduled} {
		if PromoChip(st) == "·" {
			t.Errorf("state %s has no chip", st)
		}
	}
}

func TestPromoEventFilterAndMerge(t *testing.T) {
	t.Parallel()
	s1, s2, s3 := uuid.New(), uuid.New(), uuid.New()
	club := promoItem("active")
	onEvent := promoItem("active")
	onEvent.AppliesToSessionIds = []uuid.UUID{s1}
	elsewhere := promoItem("active")
	elsewhere.AppliesToSessionIds = []uuid.UUID{s3}
	got := promoEventFilter([]openapi.PromoCodeItem{club, onEvent, elsewhere}, uuidSet([]uuid.UUID{s1, s2}))
	if len(got) != 2 || got[0].Id != club.Id || got[1].Id != onEvent.Id {
		t.Errorf("the event's codes are the club code and the one naming its session: %+v", got)
	}

	// Re-choosing the sessions of one event keeps the others' sessions.
	merged := promoMergeSessions([]uuid.UUID{s1, s3}, uuidSet([]uuid.UUID{s1, s2}), []uuid.UUID{s2})
	if len(merged) != 2 || merged[0] != s3 || merged[1] != s2 {
		t.Errorf("merge = %v, want [s3 s2]", merged)
	}
	if got := promoMergeSessions([]uuid.UUID{s1}, uuidSet([]uuid.UUID{s1}), nil); len(got) != 0 {
		t.Errorf("removing the only session leaves an empty list, which the caller must refuse: %v", got)
	}
	if got := promoMergeSessions([]uuid.UUID{s1, s1}, nil, []uuid.UUID{s1}); len(got) != 1 {
		t.Errorf("duplicates collapse: %v", got)
	}
}

// answer feeds one text or button to the draft and returns the refusal key.
func answer(d *promoDraft, text, data string) string {
	return promoApply(d, text, data, d.existing())
}

func sessionsFor(n int, cur string) []promoOption {
	out := make([]promoOption, n)
	for i := range out {
		out[i] = promoOption{ID: uuid.New(), Label: "0" + string(rune('1'+i)) + ".11.2099 20:00", Currency: cur}
	}
	return out
}

func TestPromoDialog_PercentCodeForAllSessions(t *testing.T) {
	t.Parallel()
	d := newPromoDraft()
	d.Names = []string{"TAKEN"}
	if d.Step != pmStepCode {
		t.Fatalf("starts at the code, got %s", d.Step)
	}
	// A taken name (any case) and a bad one are refused plainly and the step stays.
	if key := answer(d, "taken", ""); key != "bot.promo.err_code_dup" || d.Step != pmStepCode {
		t.Errorf("duplicate: key=%s step=%s", key, d.Step)
	}
	if key := answer(d, "two words", ""); key != "bot.promo.err_code_chars" || d.Step != pmStepCode {
		t.Errorf("chars: key=%s step=%s", key, d.Step)
	}
	if key := answer(d, " vip10 ", ""); key != "" || d.Code != "VIP10" || d.Step != pmStepType {
		t.Fatalf("code: key=%s code=%s step=%s", key, d.Code, d.Step)
	}
	// Text where a button is expected changes nothing.
	if key := answer(d, "percent", ""); key != "" || d.Step != pmStepType {
		t.Errorf("text on a button step: key=%s step=%s", key, d.Step)
	}
	answer(d, "", "pct")
	if d.Step != pmStepPercent || d.Type != promoTypePercent {
		t.Fatalf("percent chosen: step=%s type=%s", d.Step, d.Type)
	}
	for _, bad := range []string{"150", "0", "ten", "-5"} {
		if key := answer(d, bad, ""); key != "bot.promo.err_percent" || d.Step != pmStepPercent {
			t.Errorf("percent %q: key=%s step=%s", bad, key, d.Step)
		}
	}
	answer(d, "15%", "")
	if d.Value != 15 || d.Step != pmStepScope {
		t.Fatalf("percent: value=%d step=%s", d.Value, d.Step)
	}
	answer(d, "", "all")
	if !d.All || d.Step != pmStepTotal {
		t.Fatalf("all sessions: all=%v step=%s", d.All, d.Step)
	}
	answer(d, "100", "")
	if d.MaxUses == nil || *d.MaxUses != 100 || d.Step != pmStepPer {
		t.Fatalf("total: %v step=%s", d.MaxUses, d.Step)
	}
	if key := answer(d, "101", ""); key != "bot.promo.err_per_gt_total" || d.Step != pmStepPer {
		t.Errorf("per buyer over the total: key=%s step=%s", key, d.Step)
	}
	answer(d, "", "skip")
	if d.PerBuyer != nil || d.Step != pmStepExpiry {
		t.Fatalf("per buyer skipped: %v step=%s", d.PerBuyer, d.Step)
	}
	if key := answer(d, "", "date:not-a-date"); key == "" {
		t.Error("a malformed date must be refused")
	}
	answer(d, "", "date:2099-12-31")
	if d.Until != "2099-12-31" || d.Step != pmStepStatus {
		t.Fatalf("expiry: %s step=%s", d.Until, d.Step)
	}
	answer(d, "", "st:paused")
	if d.Status != promoStatusPaused || d.Step != pmStepConfirm {
		t.Fatalf("status: %s step=%s", d.Status, d.Step)
	}
	in, ok := d.create()
	if !ok {
		t.Fatal("the finished draft must make a request")
	}
	if in.Code != "VIP10" || in.DiscountType != promoTypePercent || in.DiscountValue != 15 || in.Currency != "" ||
		len(in.SessionIDs) != 0 || in.Status != promoStatusPaused || *in.MaxUses != 100 || in.MaxUsesPerCustomer != nil {
		t.Errorf("request = %+v", in)
	}
	want := time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)
	if in.ValidUntil == nil || !in.ValidUntil.Equal(want) {
		t.Errorf("valid_until = %v, want the end of the chosen day %v", in.ValidUntil, want)
	}

	// "Back" walks the steps in reverse and the answers stay.
	answer(d, "", "back")
	if d.Step != pmStepStatus {
		t.Errorf("back from the summary: %s", d.Step)
	}
	for i := 0; i < 20 && answer(d, "", "back") == ""; i++ {
	}
	if d.Step != pmStepCode || len(d.Hist) != 0 {
		t.Errorf("back to the start: step=%s hist=%v", d.Step, d.Hist)
	}
	if d.Code != "VIP10" {
		t.Error("going back keeps the answers")
	}
}

func TestPromoDialog_FixedCodeAndCurrency(t *testing.T) {
	t.Parallel()
	// One currency on sale: it is taken without asking.
	d := newPromoDraft()
	answer(d, "FIVE", "")
	answer(d, "", "fix")
	d.CurOpts = []string{"EUR"}
	if key := answer(d, "0", ""); key != "bot.promo.err_amount" || d.Step != pmStepAmount {
		t.Errorf("zero amount: key=%s step=%s", key, d.Step)
	}
	answer(d, "5,50", "")
	if d.Value != 550 || d.Currency != "EUR" || d.Step != pmStepScope {
		t.Fatalf("fixed with one currency: value=%d cur=%s step=%s", d.Value, d.Currency, d.Step)
	}

	// Several: asked, by button or by typing.
	d2 := newPromoDraft()
	answer(d2, "TEN", "")
	answer(d2, "", "fix")
	d2.CurOpts = []string{"EUR", "CZK"}
	answer(d2, "10", "")
	if d2.Step != pmStepCurrency {
		t.Fatalf("several currencies must be asked: %s", d2.Step)
	}
	if key := answer(d2, "euros", ""); key != "bot.promo.err_currency" {
		t.Errorf("bad currency: %s", key)
	}
	answer(d2, "", "cur:CZK")
	if d2.Currency != "CZK" || d2.Step != pmStepScope {
		t.Errorf("currency by button: %s step=%s", d2.Currency, d2.Step)
	}

	// None known: typed.
	d3 := newPromoDraft()
	answer(d3, "TYPED", "")
	answer(d3, "", "fix")
	answer(d3, "1", "")
	answer(d3, "usd", "")
	if d3.Currency != "USD" {
		t.Errorf("typed currency: %s", d3.Currency)
	}

	// The request carries the currency, in minor units.
	answer(d, "", "all")
	answer(d, "", "skip")
	answer(d, "", "skip")
	answer(d, "", "skip")
	answer(d, "", "st:active")
	in, ok := d.create()
	if !ok || in.DiscountType != promoTypeFixed || in.DiscountValue != 550 || in.Currency != "EUR" || in.ValidUntil != nil || in.MaxUses != nil {
		t.Errorf("request = %+v ok=%v", in, ok)
	}
}

func TestPromoDialog_PickedSessions(t *testing.T) {
	t.Parallel()
	ev := uuid.New()
	d := newPromoDraft()
	answer(d, "SOME", "")
	answer(d, "", "fix")
	d.CurOpts = []string{"EUR"}
	answer(d, "5", "")
	// Started from an event: pick goes straight to its sessions.
	d.EventID, d.EventName = &ev, "Swan Lake"
	answer(d, "", "pick")
	if d.Step != pmStepPick {
		t.Fatalf("with an event, pick opens its sessions: %s", d.Step)
	}
	opts := append(sessionsFor(3, "EUR"), promoOption{ID: uuid.New(), Label: "x", Currency: "CZK"})
	d.Pick = newPromoPick(opts, nil)

	if key := answer(d, "", "ok"); key != "bot.promo.err_pick_none" || d.Step != pmStepPick {
		t.Errorf("done with nothing ticked: key=%s step=%s", key, d.Step)
	}
	// A session in another currency cannot take a fixed EUR code.
	if key := answer(d, "", "t:3"); key != "bot.promo.err_session_currency" || d.Pick.Sel[3] {
		t.Errorf("foreign currency: key=%s sel=%v", key, d.Pick.Sel)
	}
	answer(d, "", "t:0")
	answer(d, "", "t:2")
	answer(d, "", "t:2") // toggled off again
	answer(d, "", "t:99")
	if !d.Pick.Sel[0] || d.Pick.Sel[2] || len(d.Pick.checked()) != 1 {
		t.Errorf("selection = %v", d.Pick.Sel)
	}
	answer(d, "", "ok")
	if d.Step != pmStepTotal {
		t.Fatalf("done: %s", d.Step)
	}
	answer(d, "", "skip")
	answer(d, "", "skip")
	answer(d, "", "skip")
	answer(d, "", "st:active")
	in, ok := d.create()
	if !ok || len(in.SessionIDs) != 1 || in.SessionIDs[0] != opts[0].ID {
		t.Errorf("request = %+v ok=%v", in, ok)
	}

	// A draft that says "pick" but has nothing ticked never makes a request:
	// an empty list would silently be a club code.
	d.Pick.Sel[0] = false
	if _, ok := d.create(); ok {
		t.Error("an empty pick must not create a code")
	}
}

func TestPromoPicker_EventThenSessionsAndEdit(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	p := &promoPicker{Events: []promoOption{{ID: a, Label: "A"}, {ID: b, Label: "B"}}}
	if out, _ := p.press("ev:5", "", false); out != pickStay || p.EventID != nil {
		t.Error("a press on a row that is not there changes nothing")
	}
	if out, _ := p.press("ev:1", "", false); out != pickEvent || p.EventID == nil || *p.EventID != b || p.EventName != "B" {
		t.Errorf("event chosen: %+v", p)
	}
	p.Pick = newPromoPick(sessionsFor(7, ""), nil)
	p.press("pg:2", "", false)
	if p.Pick.Page != 2 {
		t.Errorf("page = %d", p.Pick.Page)
	}
	// The edit may end with nothing ticked when other sessions stay.
	if out, key := p.press("ok", "", false); out != pickStay || key == "" {
		t.Errorf("done without a tick is refused by default: %s %s", out, key)
	}
	if out, key := p.press("ok", "", true); out != pickDone || key != "" {
		t.Errorf("done with allowEmpty: %s %s", out, key)
	}
	if out, _ := p.press("chev", "", false); out != pickChevent || p.EventID != nil || p.Pick != nil {
		t.Errorf("another event: %+v", p)
	}
}

func TestPromoDialog_JSONRoundTripKeepsTheDialog(t *testing.T) {
	t.Parallel()
	// A bot restart reloads the state from bot_dialogs: the embedded picker
	// must come back flat and whole.
	ev := uuid.New()
	d := newPromoDraft()
	answer(d, "RESTART", "")
	answer(d, "", "pct")
	answer(d, "20", "")
	d.EventID, d.EventName = &ev, "Swan"
	answer(d, "", "pick")
	d.Pick = newPromoPick(sessionsFor(2, "EUR"), nil)
	answer(d, "", "t:1")
	d.Names = []string{"A", "B"}
	st := promoDialog{OrgID: uuid.New(), Draft: d, Page: 1, MsgID: 42}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back promoDialog
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	bd := back.Draft
	if bd == nil || bd.Step != pmStepPick || bd.Code != "RESTART" || bd.Value != 20 || bd.EventID == nil || *bd.EventID != ev ||
		bd.EventName != "Swan" || bd.Pick == nil || len(bd.Pick.Options) != 2 || !bd.Pick.Sel[1] || len(bd.Hist) != len(d.Hist) || len(bd.Names) != 2 {
		t.Errorf("restored draft = %+v", bd)
	}
	if !strings.Contains(string(raw), `"event_name":"Swan"`) {
		t.Errorf("the embedded picker must be flattened into the draft: %s", raw)
	}
}

func TestPromoAPIErrKey(t *testing.T) {
	t.Parallel()
	for code, want := range map[string]string{
		"promo.duplicate": "bot.promo.err_duplicate", "promo.invalid_currency": "bot.promo.err_currency",
		"promo.currency_required": "bot.promo.err_currency", "promo.invalid_session": "bot.promo.err_session_foreign",
		"promo.invalid_discount_value": "bot.promo.err_percent", "promo.update_failed": "",
	} {
		if got := promoAPIErrKey(code, false); got != want {
			t.Errorf("%s -> %q, want %q", code, got, want)
		}
	}
	if promoAPIErrKey("promo.invalid_discount_value", true) != "bot.promo.err_amount" {
		t.Error("a bad fixed amount names the amount")
	}
}

func TestPromoMenuAndEventButtons_OnlyForRolesThatManageCodes(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	ev := uuid.New()
	for role, want := range map[string]bool{"org_admin": true, "organizer": true, "agent": false, "": false} {
		id := &Identity{Current: &Membership{Role: role}}
		if got := b.promoMenuRow("en", id) != nil; got != want {
			t.Errorf("menu button for %q = %v, want %v", role, got, want)
		}
		row := b.promoEventRow("en", id, ev)
		if (row != nil) != want {
			t.Errorf("event button for %q = %v, want %v", role, row != nil, want)
		}
		if row != nil && row[0].CallbackData != "pm:l:"+ev.String() {
			t.Errorf("event button data = %q", row[0].CallbackData)
		}
	}
	if b.promoMenuRow("en", &Identity{Current: &Membership{Role: "agent"}, Superadmin: true}) == nil {
		t.Error("the platform operator manages codes too")
	}
	if b.promoMenuRow("en", &Identity{}) != nil {
		t.Error("no organization, no button")
	}
}

// Every step of the dialog renders in every language, fits Telegram's
// callback limit and never prints a key or a missing value.
func TestPromoDraftScreens_RenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	ev := uuid.New()
	build := func(step string) *promoDraft {
		d := newPromoDraft()
		d.Code, d.Type, d.Value, d.Currency, d.Status = "SUMMER25", promoTypeFixed, 550, "EUR", promoStatusActive
		d.CurOpts = []string{"EUR", "CZK"}
		d.EventID, d.EventName = &ev, "Swan <Lake>"
		d.Events = []promoOption{{ID: ev, Label: "Swan <Lake>"}, {ID: uuid.New(), Label: "Second"}}
		d.EvPage = 1
		d.Pick = newPromoPick(append(sessionsFor(6, "EUR"), promoOption{ID: uuid.New(), Label: "09.11.2099 20:00", Currency: "CZK"}), nil)
		d.Pick.Sel[0] = true
		d.Until = "2099-12-31"
		mx, pb := int32(100), int32(2)
		d.MaxUses, d.PerBuyer = &mx, &pb
		d.Hist = []string{pmStepCode}
		d.Step = step
		return d
	}
	steps := []string{pmStepCode, pmStepType, pmStepPercent, pmStepAmount, pmStepCurrency, pmStepScope, pmStepEvent, pmStepPick,
		pmStepTotal, pmStepPer, pmStepExpiry, pmStepStatus, pmStepConfirm}
	for _, loc := range SupportedLocales {
		for _, step := range steps {
			for _, all := range []bool{false, true} {
				d := build(step)
				d.All = all
				text, rows := b.promoDraftScreen(loc, d)
				if strings.TrimSpace(text) == "" || strings.Contains(text, "<no value>") || strings.Contains(text, "bot.promo.") || strings.Contains(text, "bot.wz.") {
					t.Errorf("%s %s: text = %q", loc, step, text)
				}
				if len(rows) == 0 {
					t.Errorf("%s %s: no buttons", loc, step)
				}
				for _, r := range rows {
					for _, btn := range r {
						if len(btn.CallbackData) > 64 || btn.CallbackData == "" || btn.Text == "" {
							t.Errorf("%s %s: bad button %+v", loc, step, btn)
						}
						if !strings.HasPrefix(btn.CallbackData, "pm:") {
							t.Errorf("%s %s: button outside the pm: prefix %q", loc, step, btn.CallbackData)
						}
					}
				}
			}
		}
		// A taken-away name and the fixed-amount rule are said where they matter.
		amount, _ := b.promoDraftScreen(loc, build(pmStepAmount))
		if !strings.Contains(amount, "<b>") {
			t.Errorf("%s: the amount question must stress the ORDER: %q", loc, amount)
		}
		confirm, _ := b.promoDraftScreen(loc, build(pmStepConfirm))
		if !strings.Contains(confirm, "SUMMER25") || !strings.Contains(confirm, "EUR") || !strings.Contains(confirm, "31.12.2099") {
			t.Errorf("%s: the summary lacks the code, the currency or the date: %q", loc, confirm)
		}
	}
	// The picker shows the event's name escaped.
	text, _ := b.promoDraftScreen("en", build(pmStepPick))
	if !strings.Contains(text, "Swan &lt;Lake&gt;") {
		t.Errorf("event name must be HTML-escaped: %q", text)
	}
}

func TestPromoCardAndUsageTexts(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cur, last := "EUR", time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	maxUses, per := int32(100), int32(1)
	fixed := openapi.PromoCodeItem{
		Id: uuid.New(), Code: "FIVE<EUR>", DiscountType: promoTypeFixed, DiscountValue: 550, Currency: &cur,
		Status: "active", MaxUses: &maxUses, MaxUsesPerCustomer: &per, ValidUntil: &until,
		Uses: 3, DiscountTotal: 1650, LastUsedAt: &last, AppliesToSessionIds: []uuid.UUID{uuid.New(), uuid.New()},
	}
	for _, loc := range SupportedLocales {
		entry := b.promoEntry(loc, 1, fixed, now)
		for _, want := range []string{"FIVE&lt;EUR&gt;", "5,50", "EUR", "100", "31.12.2026", "09.10.2026", "16,50"} {
			if loc != "ru" && loc != "es" {
				want = strings.ReplaceAll(want, "5,50", "5.50")
				want = strings.ReplaceAll(want, "16,50", "16.50")
			}
			if !strings.Contains(entry, want) {
				t.Errorf("%s entry lacks %q:\n%s", loc, want, entry)
			}
		}
		card := b.promoCardText(loc, fixed, now, []string{"• A · 01.01.2099 20:00"}, 1)
		if !strings.Contains(card, "• A · 01.01.2099 20:00") || strings.Contains(card, "<no value>") || strings.Contains(card, "bot.promo.") {
			t.Errorf("%s card:\n%s", loc, card)
		}
		// The fixed-discount rule is on the card.
		if !strings.Contains(card, b.texts.T(loc, "bot.promo.card_rule", nil)) {
			t.Errorf("%s card lacks the fixed-discount rule", loc)
		}
	}
	// A club code says "all sessions"; a percent code has no rule line.
	club := promoItem("active")
	if text := b.promoCardText("en", club, now, nil, 0); !strings.Contains(text, "all sessions, including future ones") ||
		strings.Contains(text, b.texts.T("en", "bot.promo.card_rule", nil)) {
		t.Errorf("club card:\n%s", text)
	}
	// Used in several currencies: no invented sum.
	mixed := club
	mixed.Uses, mixed.DiscountTotal, mixed.LastUsedAt = 2, 900, &last
	if text := b.promoUsageText("en", mixed); strings.Contains(text, "9") && strings.Contains(text, "900") {
		t.Errorf("mixed currencies must not print a sum: %q", text)
	}
	one := "CZK"
	mixed.DiscountCurrency = &one
	if text := b.promoUsageText("en", mixed); !strings.Contains(text, "CZK") {
		t.Errorf("a single usage currency is printed: %q", text)
	}

	// The usage row names the buyer and the order, and nothing else about them.
	email, name := "buyer@example.test", "Anna <K>"
	num, status := int64(1000000500), "paid"
	row := b.promoUsageRow("en", openapi.PromoRedemptionItem{
		RedeemedAt: last, DiscountAmount: 550, OrderAmount: 2500, Currency: &cur, OrderNumber: &num, OrderStatus: &status,
		BuyerEmail: &email, BuyerName: &name,
	})
	for _, want := range []string{"1000000500", "09.10.2026", "Anna &lt;K&gt;", "5.50 EUR", "25 EUR", "Paid"} {
		if !strings.Contains(row, want) {
			t.Errorf("usage row lacks %q: %s", want, row)
		}
	}
	if strings.Contains(row, "example.test") {
		t.Errorf("the usage list must not show the buyer's e-mail: %s", row)
	}
	if nameless := b.promoUsageRow("en", openapi.PromoRedemptionItem{RedeemedAt: last}); !strings.Contains(nameless, "without a name") {
		t.Errorf("a buyer with no name is said so: %s", nameless)
	}
}

func TestPromoCallbacks_FitTelegram(t *testing.T) {
	t.Parallel()
	code := uuid.NewString()
	for _, data := range []string{
		"pm:l:" + code, "pm:n:" + code, "pm:v:" + code, "pm:ps:" + code, "pm:ac:" + code, "pm:se:" + code, "pm:sa:" + code,
		"pm:so:" + code, "pm:u:" + code, "pm:up:" + code + ":12", "pm:csv:" + code, "pm:del:" + code, "pm:o:4", "pm:p:12",
		"pm:c:dc:2099-12-31", "pm:c:cal:2099-12", "pm:c:cur:EUR", "pm:c:st:active", "pm:k:t:99", "pm:k:evp:3",
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
	}
}

// Every bot.promo.* key is registered and present in every language.
func TestPromoKeys_AreRegisteredAndTranslated(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	registered := map[string]bool{}
	for _, k := range MessageKeys {
		registered[k] = true
	}
	vars := map[string]any{
		"Org": "Org", "Scope": "s", "Total": 1, "Page": 1, "Pages": 2, "Legend": "l", "Name": "N", "Value": 15, "Amount": "5 EUR", "Limit": "5",
		"Date": "d", "Count": 3, "Discount": "1 EUR", "When": "w", "Text": "t", "Currency": "EUR", "Code": "C", "N": 2, "Num": 1,
		"Status": "s", "Word": "W",
	}
	for _, key := range promoKeys {
		if !registered[key] {
			t.Errorf("%s is not in MessageKeys", key)
		}
		en := b.texts.T("en", key, vars)
		for _, loc := range SupportedLocales {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
			if loc != "en" && got == en && !strings.HasSuffix(key, "csv_btn") && !strings.HasSuffix(key, "card_sess_line") {
				t.Errorf("%s %s is a copy of English: %q", loc, key, got)
			}
		}
	}
	// The delete word differs per language and the English one is always accepted.
	if !b.promoIsDeleteWord("ru", "удалить") || !b.promoIsDeleteWord("ru", " УДАЛИТЬ. ") || !b.promoIsDeleteWord("ru", "delete") ||
		!b.promoIsDeleteWord("es", "eliminar") || b.promoIsDeleteWord("ru", "да") || b.promoIsDeleteWord("en", "") {
		t.Error("delete word rules")
	}
}
