package eventbot

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func loTier(name string, price int64, kind string, open bool, sortOrder int) openapi.TicketTierItem {
	k := kind
	o := open
	return openapi.TicketTierItem{
		Id: uuid.New(), Name: name, PriceAmount: price, Currency: "EUR", Kind: &k, IsOpen: &o,
		PricingMode: openapi.TicketTierItemPricingModeFixed, SortOrder: int32(sortOrder),
	}
}

func TestLoTargets_OpenSeatedCategoriesDearestFirst(t *testing.T) {
	t.Parallel()
	cheap := loTier("Balcony", 3000, "seated", true, 2)
	dear := loTier("Stalls", 9000, "seated", true, 1)
	tied := loTier("Boxes", 3000, "seated", true, 1)
	closed := loTier("Closed", 5000, "seated", false, 3)
	ga := loTier("Standing", 1000, "ga", true, 4)
	free := loTier("Press", 0, "seated", true, 5)
	free.PricingMode = openapi.TicketTierItemPricingModeFree
	free.PriceAmount = 777 // a free category ignores its stored price

	got, nClosed := loTargets([]openapi.TicketTierItem{cheap, closed, ga, dear, tied, free})
	if nClosed != 1 {
		t.Errorf("closed = %d, want 1", nClosed)
	}
	var names []string
	for _, g := range got {
		names = append(names, g.Name)
	}
	// dearest first; equal prices by sort order; free last at 0.
	if want := "Stalls,Boxes,Balcony,Press"; strings.Join(names, ",") != want {
		t.Errorf("targets = %v, want %s", names, want)
	}
	if got[3].Price != 0 {
		t.Errorf("a free category must count as price 0, got %d", got[3].Price)
	}
}

func loAlgorithm() openapi.SeatLayoutAlgorithm {
	opts := []openapi.SeatLayoutParamOption{{Value: "pyramid", LabelKey: "a"}, {Value: "equal", LabelKey: "b"}}
	return openapi.SeatLayoutAlgorithm{
		Id: "desirability", LabelKey: "layoutgen.algorithm.desirability",
		RequiredAnnotations: []string{"focal_point"},
		Params: []openapi.SeatLayoutParam{
			{Name: "categories", Type: "integer", Required: true},
			{Name: "prices", Type: "integer_list"},
			{Name: "price_ladder", Type: "enum", Default: "geometric", Options: &[]openapi.SeatLayoutParamOption{{Value: "geometric"}, {Value: "linear"}}},
			{Name: "round_prices", Type: "boolean", Default: true},
			{Name: "share_template", Type: "enum", Default: "pyramid", Options: &opts},
			{Name: "cheaper_row_edges", Type: "boolean", Default: false},
			{Name: "third_switch", Type: "boolean", Default: false},
		},
	}
}

func TestLoAsked_SkipsTheCommonPriceParametersAndCapsAtTwo(t *testing.T) {
	t.Parallel()
	asked := loAsked(loAlgorithm())
	if len(asked) != 2 || asked[0].Name != "share_template" || asked[1].Name != "cheaper_row_edges" {
		t.Fatalf("asked = %+v", asked)
	}
}

func TestLoRequestParams_FillsCategoriesAndPricesFromTheDate(t *testing.T) {
	t.Parallel()
	alg := loAlgorithm()
	targets := []loTarget{{Name: "A", Price: 9000}, {Name: "B", Price: 5000}, {Name: "C", Price: 5000}}
	got := loRequestParams(alg, targets, map[string]any{"share_template": "equal", "cheaper_row_edges": true, "price_ladder": "linear"})
	raw, _ := json.Marshal(got)
	want := `{"categories":3,"cheaper_row_edges":true,"prices":[9000,5000,5000],"share_template":"equal"}`
	if string(raw) != want {
		t.Errorf("params = %s, want %s", raw, want)
	}
	// Defaults stand in for an unanswered option; a wrong type is ignored.
	got = loRequestParams(alg, targets, map[string]any{"share_template": true})
	raw, _ = json.Marshal(got)
	if !strings.Contains(string(raw), `"share_template":"pyramid"`) || !strings.Contains(string(raw), `"cheaper_row_edges":false`) {
		t.Errorf("defaults not used: %s", raw)
	}
	// An algorithm without the common parameters is sent none of them.
	bare := openapi.SeatLayoutAlgorithm{Id: "x"}
	if got := loRequestParams(bare, targets, nil); len(got) != 0 {
		t.Errorf("bare algorithm got %v", got)
	}
}

func TestLoStagePoint_SitsOutsideTheSeatsOnTheChosenSide(t *testing.T) {
	t.Parallel()
	b := PlanBounds{MinX: 100, MaxX: 300, MinY: 50, MaxY: 250, Seats: 10}
	cases := map[string][2]float64{"top": {200, 20}, "bottom": {200, 280}, "left": {70, 150}, "right": {330, 150}}
	for side, want := range cases {
		x, y, ok := loStagePoint(b, side)
		if !ok || x != want[0] || y != want[1] {
			t.Errorf("%s = (%v,%v,%v), want %v", side, x, y, ok, want)
		}
	}
	if _, _, ok := loStagePoint(b, "up"); ok {
		t.Error("an unknown side must not give a point")
	}
	// A single seat still gets a point off the seat.
	one := PlanBounds{MinX: 5, MaxX: 5, MinY: 5, MaxY: 5, Seats: 1}
	if x, y, _ := loStagePoint(one, "top"); x != 5 || y >= 5 {
		t.Errorf("one seat: (%v,%v)", x, y)
	}
}

func suggestFor(slots int, assignments map[string]string, frozen []string) openapi.SeatLayoutSuggestResponse {
	res := openapi.SeatLayoutSuggestResponse{Algorithm: "desirability"}
	for i := 0; i < slots; i++ {
		res.Layout.Slots = append(res.Layout.Slots, openapi.SeatLayoutSlot{Key: "c" + string(rune('1'+i))})
	}
	res.Layout.Assignments = assignments
	res.Layout.FrozenSeatKeys = frozen
	return res
}

func TestLoBuildPlan_SlotsFollowTheCategoriesAndRefuseAMismatch(t *testing.T) {
	t.Parallel()
	targets := []loTarget{{ID: uuid.New(), Name: "A", Price: 9}, {ID: uuid.New(), Name: "B", Price: 5}}
	res := suggestFor(2, map[string]string{"s|1": "c2", "s|2": "c1", "s|3": "c1"}, nil)
	plan, err := loBuildPlan(res, targets)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count[0] != 2 || plan.Count[1] != 1 {
		t.Errorf("counts = %v", plan.Count)
	}
	if len(plan.Changes) != 3 || plan.Changes[0].SeatKey != "s|1" || *plan.Changes[0].TierId != targets[1].ID ||
		*plan.Changes[1].TierId != targets[0].ID {
		t.Errorf("changes = %+v", plan.Changes)
	}
	if _, err := loBuildPlan(suggestFor(3, nil, nil), targets); err == nil {
		t.Error("three slots for two categories must be refused")
	}
	if _, err := loBuildPlan(suggestFor(2, map[string]string{"s|1": "zz"}, nil), targets); err == nil {
		t.Error("an unknown slot must be refused")
	}
}

func TestLoFingerprint_FollowsWhatIsShown(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	targets := []loTarget{{ID: a}, {ID: b}}
	build := func(assign map[string]string) loPlan {
		p, err := loBuildPlan(suggestFor(2, assign, nil), targets)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := build(map[string]string{"s|1": "c1", "s|2": "c2"})
	base := loFingerprint("desirability", p1, []string{"x|1"})
	if got := loFingerprint("desirability", build(map[string]string{"s|2": "c2", "s|1": "c1"}), []string{"x|1"}); got != base {
		t.Error("map order must not change the fingerprint")
	}
	if loFingerprint("desirability", build(map[string]string{"s|1": "c2", "s|2": "c2"}), []string{"x|1"}) == base {
		t.Error("another seat assignment must change the fingerprint")
	}
	if loFingerprint("desirability", p1, []string{"x|1", "x|2"}) == base {
		t.Error("a seat that became sold or held must change the fingerprint")
	}
	if loFingerprint("other", p1, []string{"x|1"}) == base {
		t.Error("another algorithm must change the fingerprint")
	}
	if len(loFingerprintShort(base)) != 10 {
		t.Errorf("short print = %q", loFingerprintShort(base))
	}
}

func TestBoundsOfGeometry(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"sections":[{"rows":[{"seats":[{"x":10,"y":20},{"x":30,"y":5}]},{"seats":[{"x":-4,"y":40}]}]}]}`)
	b, ok := boundsOfGeometry(raw)
	if !ok || b.Seats != 3 || b.MinX != -4 || b.MaxX != 30 || b.MinY != 5 || b.MaxY != 40 {
		t.Errorf("bounds = %+v ok=%v", b, ok)
	}
	if _, ok := boundsOfGeometry(json.RawMessage(`{"sections":[]}`)); ok {
		t.Error("a plan without seats has no bounds")
	}
	if _, ok := boundsOfGeometry(json.RawMessage(`not json`)); ok {
		t.Error("bad JSON has no bounds")
	}
}

func TestLoSchemeURL(t *testing.T) {
	t.Parallel()
	o, e, s := uuid.New(), uuid.New(), uuid.New()
	path := "/o/" + o.String() + "/events/" + e.String() + "/sessions/" + s.String() + "/seats?embed=1"
	if got := loSchemeURL("https://events.example.test/", o, e, s, false); got != "https://events.example.test"+path {
		t.Errorf("url = %s", got)
	}
	if loSchemeURL("", o, e, s, false) != "" || loSchemeURL("", o, e, s, true) != "" {
		t.Error("no address, no link")
	}

	// The Mini App address goes through /tg and carries the path as ONE
	// escaped value of `next`.
	got := loSchemeURL("https://events.example.test/", o, e, s, true)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "events.example.test" || u.Path != "/tg" {
		t.Fatalf("web_app url = %s", got)
	}
	if len(u.Query()) != 1 || u.Query().Get("next") != path {
		t.Fatalf("next = %q, want %q (all of it in one parameter)", u.Query().Get("next"), path)
	}
	if strings.Contains(u.RawQuery, "/") || strings.Contains(u.RawQuery, "?") || strings.Count(u.RawQuery, "&") != 0 {
		t.Errorf("next is not escaped as one value: %s", u.RawQuery)
	}
	// The path never leaves /o/, and Telegram accepts a web_app URL of up to
	// 2048 bytes (the bot's other Mini App link keeps to the same).
	if next := u.Query().Get("next"); !strings.HasPrefix(next, "/o/") || strings.Contains(next, "//") || strings.Contains(next, "..") {
		t.Errorf("next leaves /o/: %q", next)
	}
	if len(got) > 2048 {
		t.Errorf("web_app url is %d bytes", len(got))
	}
}

func TestSchemeButton_FollowsTheSettingAndTheChat(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	o, e, s := uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		mode, url string
		chat      int64
		want      string // "", "url" or "web_app"
	}{
		{"off", "https://x.test", 5, ""},
		{"", "https://x.test", 5, ""},
		{"url", "https://x.test", 5, "url"},
		{"web_app", "https://x.test", 5, "web_app"},
		{"web_app", "https://x.test", -100, ""}, // a group: a web_app button works only in a private chat
		{"url", "", 5, ""},
	} {
		b.schemeMode, b.eventCenterURL = tc.mode, tc.url
		bt, ok := b.schemeButton(tc.chat, "en", o, e, s)
		got := ""
		if ok {
			got = "url"
			if bt.WebApp {
				got = "web_app"
			}
			if bt.URL == "" || bt.Label == "" {
				t.Errorf("%+v: empty button %+v", tc, bt)
			}
			if bt.WebApp != strings.HasPrefix(bt.URL, "https://x.test/tg?next=") {
				t.Errorf("%+v: only a web_app button goes through /tg: %s", tc, bt.URL)
			}
			if !bt.WebApp && !strings.HasPrefix(bt.URL, "https://x.test/o/") {
				t.Errorf("%+v: a url button goes straight to the screen: %s", tc, bt.URL)
			}
		}
		if got != tc.want {
			t.Errorf("%+v: got %q", tc, got)
		}
	}
}

func TestSeatLayoutKeys_AreTranslated(t *testing.T) {
	t.Parallel()
	b := promoTestBot(t)
	vars := map[string]any{
		"Name": "N", "When": "w", "Price": "p", "Count": 3, "Seats": 4, "Share": 25, "Sold": 1, "Held": 2,
		"Money": "m", "Changed": 5, "Same": 6, "Max": 10000,
	}
	for _, key := range append(append([]string{}, seatLayoutKeys...), layoutAlgorithmKeys...) {
		en := b.texts.T("en", key, vars)
		for _, loc := range SupportedLocales {
			got := b.texts.T(loc, key, vars)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s %s rendered %q", loc, key, got)
			}
			if loc != "en" && got == en && key != "bot.sl.head" && key != "bot.sl.cat_line" {
				t.Errorf("%s %s is a copy of English: %q", loc, key, got)
			}
		}
	}
}
