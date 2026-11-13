package eventbot

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// helpScreens builds the item lists of every screen that has a help, for the
// roles and the states that change them.
func helpScreens(t *testing.T, b *Bot, loc string) map[string]screenItems {
	t.Helper()
	ctx := context.Background()
	owner, manager, operator, agent := cardTestIDs()
	owner2 := menuIdentity("org_admin", false, 2)
	ev := uuid.New()
	published := cardFacts{Status: "published", SalesState: "on_sale", HasLink: true, Dates: 3, SingleLive: false}
	single := cardFacts{Status: "published", SalesState: "on_sale", HasLink: true, Dates: 1, SingleLive: true}
	draft := cardFacts{Status: "draft", SalesState: "draft", Dates: 1, SingleLive: true}
	past := cardFacts{Status: "published", SalesState: "archived", HasLink: true, Dates: 1, SingleLive: true}
	archived := cardFacts{Status: "archived", SalesState: "archived"}

	out := map[string]screenItems{}
	for name, f := range map[string]cardFacts{"published": published, "single": single, "draft": draft, "past": past, "archived": archived} {
		for who, id := range map[string]*Identity{"owner": owner, "manager": manager, "operator": operator, "agent": agent} {
			out["card/"+name+"/"+who] = b.cardItems(loc, id, ev, f)
			for _, g := range []string{cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore} {
				out["group-"+g+"/"+name+"/"+who] = b.cardGroupItems(loc, id, ev, g, f)
			}
		}
	}

	shopOn := &openapi.ShopSettings{Enabled: true}
	shopOff := &openapi.ShopSettings{Enabled: false}
	out["shop/open"], out["shop/closed"], out["shop/orders-only"] = b.shopHomeItems(loc, shopOn), b.shopHomeItems(loc, shopOff), b.shopHomeItems(loc, nil)

	for who, id := range map[string]*Identity{"owner": owner2, "manager": manager, "operator": operator, "agent": agent} {
		out["orders/"+who] = b.ordersSectionItems(ctx, loc, id, "")
		out["promo/"+who] = b.promoSectionItems(loc, id)
		out["door/"+who] = b.doorSectionItems(loc, id)
		out["settings/"+who] = b.settingsSectionItems(ctx, loc, id, "jwt")
	}

	// the list of events: a filter, a search, several pages
	now := time.Now()
	var events []openapi.EventItem
	for i := 0; i < 7; i++ {
		events = append(events, openapi.EventItem{Id: uuid.New(), Name: "Swan Lake", SalesState: salesStateOnSale, NextSessionAt: &now})
	}
	st := newEventsDialog(uuid.New())
	out["list/plain"], _, _, _, _ = b.eventsListItems(loc, st, events)
	st.Query = "swan"
	out["list/search"], _, _, _, _ = b.eventsListItems(loc, st, events)
	out["list/empty-search"], _, _, _, _ = b.eventsListItems(loc, eventsDialog{OrgID: st.OrgID, Filter: evFilterArc, Page: 1}, events)
	return out
}

// Every item of every screen with a help has a line in every language, and
// every id of the registry is used by some screen.
func TestScreenHelp_EveryItemHasALine(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	known := map[string]bool{}
	for _, id := range helpItemIDs {
		known[id] = true
	}
	used := map[string]bool{}
	for _, loc := range SupportedLocales {
		for screen, items := range helpScreens(t, b, loc) {
			for _, it := range items {
				used[it.ID] = true
				if !known[it.ID] {
					t.Errorf("%s: %s has an item %q that is not in helpItemIDs", loc, screen, it.ID)
				}
				key := helpItemKey(it.ID)
				got := b.texts.T(loc, key, nil)
				if got == "" || got == key || strings.Contains(got, "<no value>") {
					t.Errorf("%s: item %q of %s has no help line (%s rendered %q)", loc, it.ID, screen, key, got)
				}
				if it.label() == "" {
					t.Errorf("%s: item %q of %s has no label", loc, it.ID, screen)
				}
			}
		}
	}
	for _, id := range helpItemIDs {
		if !used[id] {
			t.Errorf("id %q is in helpItemIDs but no screen produces it", id)
		}
		for _, loc := range SupportedLocales {
			if got := b.texts.T(loc, helpItemKey(id), nil); got == "" || got == helpItemKey(id) {
				t.Errorf("%s: %s is missing", loc, helpItemKey(id))
			}
		}
	}
	for _, loc := range SupportedLocales {
		for _, key := range helpKeys {
			got := b.texts.T(loc, key, map[string]any{"Screen": "X", "Path": "P"})
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s: %s rendered %q", loc, key, got)
			}
		}
	}
}

var helpLabelRe = regexp.MustCompile(`(?m)^<b>(.*?)</b>: `)

// labelsOf is the bold labels of a help text, in order.
func labelsOf(text string) []string {
	var out []string
	for _, m := range helpLabelRe.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// A person reads exactly the lines of the buttons they see, in the order of the
// screen: an owner, a manager and a role without sales rights differ.
func TestScreenHelp_LinesAreTheVisibleButtons(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	ctx := context.Background()
	owner, manager, _, agent := cardTestIDs()
	ev := uuid.New()
	onSale := cardFacts{Status: "published", SalesState: "on_sale", HasLink: true, Dates: 3}
	draft := cardFacts{Status: "draft", SalesState: "draft", Dates: 1, SingleLive: true}

	cases := []struct {
		name  string
		items screenItems
		want  []string
	}{
		{"card on sale, owner", b.cardItems("en", owner, ev, onSale), []string{"ec.link", "ec.s", "ec.e", "ec.p", "ec.m"}},
		{"card on sale, manager", b.cardItems("en", manager, ev, onSale), []string{"ec.link", "ec.s", "ec.e", "ec.p", "ec.m"}},
		{"card on sale, no sales rights", b.cardItems("en", agent, ev, onSale), []string{"ec.e", "ec.m"}},
		{"card draft: publish, no link", b.cardItems("en", owner, ev, draft), []string{"ec.publish", "ec.s", "ec.e", "ec.p", "ec.m"}},
		{"card past: sales first", b.cardItems("en", owner, ev, cardFacts{Status: "published", SalesState: "archived", HasLink: true}), []string{"ec.s", "ec.link", "ec.e", "ec.p", "ec.m"}},
		{"sales, owner", b.cardGroupItems("en", owner, ev, cardGroupSales, onSale), []string{"gs.summary", "gs.orders", "gs.guests", "gs.csv", "gs.dates"}},
		{"sales, no sales rights", b.cardGroupItems("en", agent, ev, cardGroupSales, onSale), nil},
		{"edit, owner, one live date", b.cardGroupItems("en", owner, ev, cardGroupEdit, draft), []string{"ge.edit", "ge.sessions", "ge.cat", "ge.materials", "ge.sample", "ge.text", "ge.promoter"}},
		{"edit, no sales rights", b.cardGroupItems("en", agent, ev, cardGroupEdit, draft), []string{"ge.edit", "ge.sessions", "ge.materials", "ge.sample"}},
		{"promotion, owner", b.cardGroupItems("en", owner, ev, cardGroupPromo, onSale), []string{"gp.promo", "gp.invite", "gp.links", "gp.referral"}},
		{"more, draft", b.cardGroupItems("en", owner, ev, cardGroupMore, draft), []string{"gm.copy", "gm.publish", "gm.delete"}},
		{"more, on sale", b.cardGroupItems("en", manager, ev, cardGroupMore, onSale), []string{"gm.copy", "gm.off", "gm.archive", "gm.delete"}},
		{"more, no sales rights", b.cardGroupItems("en", agent, ev, cardGroupMore, onSale), []string{"gm.copy"}},
		{"orders, owner", b.ordersSectionItems(ctx, "en", owner, ""), []string{"so.orders", "so.refunds", "so.disputes"}},
		{"promotion section, owner", b.promoSectionItems("en", owner), []string{"sp.promo", "sp.invite", "sp.link", "sp.mail"}},
		{"promotion section, no sales rights", b.promoSectionItems("en", agent), nil},
		{"door, owner", b.doorSectionItems("en", owner), []string{"sd.scanner", "sd.guests"}},
		{"door, no sales rights", b.doorSectionItems("en", agent), []string{"sd.scanner"}},
		{"settings, owner of two organizations", b.settingsSectionItems(ctx, "en", menuIdentity("org_admin", false, 2), "jwt"),
			[]string{"ss.org", "ss.pay", "ss.team_add", "ss.promoter", "ss.venues", "ss.notif", "ss.lang", "ss.switch"}},
		{"settings, manager", b.settingsSectionItems(ctx, "en", menuIdentity("organizer", false, 1), "jwt"),
			[]string{"ss.team", "ss.promoter", "ss.venues", "ss.notif", "ss.lang"}},
		{"settings, operator", b.settingsSectionItems(ctx, "en", menuIdentity("agent", true, 1), "jwt"),
			[]string{"ss.org", "ss.pay", "ss.promoter", "ss.venues", "ss.notif", "ss.lang"}},
		{"shop, storefront open", b.shopHomeItems("en", &openapi.ShopSettings{Enabled: true}), []string{"sh.orders", "sh.close", "sh.settings", "sh.web"}},
		{"shop, storefront closed", b.shopHomeItems("en", &openapi.ShopSettings{}), []string{"sh.orders", "sh.open", "sh.settings", "sh.web"}},
		{"shop, orders only", b.shopHomeItems("en", nil), []string{"sh.orders", "sh.web"}},
	}
	for _, c := range cases {
		if got := c.items.ids(); !sameList(got, c.want) {
			t.Errorf("%s: items = %v, want %v", c.name, got, c.want)
			continue
		}
		if len(c.items) == 0 {
			continue
		}
		text := b.screenHelpText("en", "Screen", c.items, []string{"a", "b"})
		var wantLabels []string
		for _, it := range c.items {
			wantLabels = append(wantLabels, Esc(it.label()))
		}
		if got := labelsOf(text); !sameList(got, wantLabels) {
			t.Errorf("%s: help lines = %v, want %v\n%s", c.name, got, wantLabels, text)
		}
		if !strings.Contains(text, "What is here: Screen") || !strings.Contains(text, "To come back here: a, then b.") {
			t.Errorf("%s: no title or no way back:\n%s", c.name, text)
		}
	}

	// A manager never reads the line of a button only the owner has.
	managerSettings := b.screenHelpText("en", "Settings", b.settingsSectionItems(ctx, "en", menuIdentity("organizer", false, 1), "jwt"), nil)
	for _, key := range []string{"bot.og.btn", "bot.mn.pay_btn"} {
		if label := b.texts.T("en", key, nil); strings.Contains(managerSettings, label) {
			t.Errorf("the manager's help names %q:\n%s", label, managerSettings)
		}
	}
}

// The help of the list of events follows its filter, search and page.
func TestScreenHelp_ListControls(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	now := time.Now()
	var events []openapi.EventItem
	for i := 0; i < 7; i++ {
		events = append(events, openapi.EventItem{Id: uuid.New(), Name: "Swan", SalesState: salesStateOnSale, NextSessionAt: &now})
	}
	st := newEventsDialog(uuid.New())
	items, _, shown, page, pages := b.eventsListItems("en", st, events)
	if got, want := items.ids(), []string{"el.filters", "el.search", "el.event", "el.paging"}; !sameList(got, want) {
		t.Errorf("list ids = %v, want %v", got, want)
	}
	if len(shown) != listPageSize || page != 1 || pages != 2 {
		t.Errorf("page = %d of %d with %d rows", page, pages, len(shown))
	}
	// the search hint has no button
	if rows := items.rows(); len(rows) != 1+len(shown)+1 {
		t.Errorf("rows = %d, want chips, %d events, paging", len(rows), len(shown))
	}
	st.Query = "swan"
	items, _, _, _, _ = b.eventsListItems("en", st, events)
	if got, want := items.ids(), []string{"el.filters", "el.search", "el.event", "el.paging", "el.clear"}; !sameList(got, want) {
		t.Errorf("list with a search = %v, want %v", got, want)
	}
	st.Filter, st.Query = evFilterArc, ""
	items, _, _, _, _ = b.eventsListItems("en", st, events)
	if got, want := items.ids(), []string{"el.filters", "el.search"}; !sameList(got, want) {
		t.Errorf("an empty archive = %v, want %v", got, want)
	}
}

// The help button carries a short stateless callback, and the events dialog
// owns the help of its own screens only.
func TestScreenHelp_CallbacksAndOwners(t *testing.T) {
	t.Parallel()
	ev := uuid.NewString()
	for _, data := range []string{
		helpData(hpList, ""), helpData(hpCard, ev), helpData(hpSales, ev), helpData(hpEdit, ev), helpData(hpPromo, ev), helpData(hpMore, ev),
		helpData(hpOrders, ""), helpData(hpPromoSec, ""), helpData(hpDoor, ""), helpData(hpSettings, ""), helpData(hpShop, ""),
	} {
		if len(data) > 64 {
			t.Errorf("%q is %d bytes", data, len(data))
		}
		if !strings.HasPrefix(data, helpPrefix+":") {
			t.Errorf("%q has no prefix", data)
		}
	}
	for _, data := range []string{helpData(hpList, ""), helpData(hpCard, ev), helpData(hpSales, ev), helpData(hpMore, ev)} {
		if !isEventsHelp(data) || !dialogOwners[eventsDialogKind].owns(data) {
			t.Errorf("%q must keep the events dialog", data)
		}
		if dialogOwners[evsDialogKind].owns(data) {
			t.Errorf("%q must end the delete question", data)
		}
	}
	for _, data := range []string{helpData(hpOrders, ""), helpData(hpSettings, ""), helpData(hpShop, ""), "hp:", "hp"} {
		if isEventsHelp(data) || dialogOwners[eventsDialogKind].owns(data) {
			t.Errorf("%q must end the events dialog like any other press", data)
		}
	}
	// every group has its screen, and back
	for _, g := range []string{cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore} {
		if back, ok := helpScreenGroup(helpGroupScreen(g)); !ok || back != g {
			t.Errorf("group %s: screen %s -> %s", g, helpGroupScreen(g), back)
		}
	}
}

// The way back reads the real labels of the person's language.
func TestScreenHelp_PathInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	for _, loc := range SupportedLocales {
		path := []string{b.texts.T(loc, menuButtonKey, nil), b.texts.T(loc, "bot.btn_events", nil), b.texts.T(loc, "bot.hp.p_event", nil)}
		text := b.screenHelpText(loc, "Swan <Lake>", b.cardItems(loc, &Identity{Current: &Membership{Role: membershipRoleOwner}}, uuid.New(), cardFacts{Status: "published", HasLink: true}), path)
		if !strings.Contains(text, "Swan &lt;Lake&gt;") || strings.Contains(text, "<Lake>") {
			t.Errorf("%s: the name is not escaped:\n%s", loc, text)
		}
		if !strings.Contains(text, path[0]+b.texts.T(loc, "bot.hp.path_sep", nil)+path[1]) {
			t.Errorf("%s: the way back lacks the menu and the list:\n%s", loc, text)
		}
		t.Logf("%s:\n%s", loc, text)
	}
}

// /menu sends the persistent keyboard again, the typed button never does.
func TestHandleMessage_MenuCommandForcesTheKeyboard(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("bot.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "b.openMenu(ctx, chatID, from, true)") {
		t.Error("/menu and /start must force the menu keyboard")
	}
	if !strings.Contains(text, "b.openMenu(ctx, chatID, from, false)") {
		t.Error("the typed menu button must not force the keyboard")
	}
	if strings.Contains(text, `cmd == "/start")`) {
		t.Error("only /start forces the keyboard")
	}
}

// A platform superadmin has no browser link, yet Venues and Messages stay: their
// screens say the work is done in the admin console.
func TestSections_WebScreensForTheSuperadmin(t *testing.T) {
	t.Parallel()
	b := menuTestBot(t, menuStub{})
	owner := menuIdentity("org_admin", false, 1)
	super := menuIdentity("agent", true, 1)
	for _, key := range []string{"bot.mn.mail_text", "bot.mn.venues_text"} {
		for _, loc := range SupportedLocales {
			text, rows := b.webSectionText(loc, owner, key)
			if d := rowData(rows); !sameList(d, []string{"web:open"}) || text != b.texts.T(loc, key, nil) {
				t.Errorf("%s %s owner: rows %v, text %q", loc, key, d, text)
			}
			text, rows = b.webSectionText(loc, super, key)
			if len(rows) != 0 || text != b.texts.T(loc, key+"_super", nil) || text == b.texts.T(loc, key, nil) ||
				strings.Contains(text, b.texts.T(loc, "bot.web.superadmin", nil)) {
				t.Errorf("%s %s superadmin: rows %v, text %q", loc, key, rows, text)
			}
			if loc == "en" && (strings.Contains(text, "browser") || strings.Contains(text, "link")) {
				t.Errorf("the superadmin text of %s sends him to the browser: %q", key, text)
			}
		}
	}
	// every role that could see the two items sees them, the operator too
	if d := rowData(b.promoSectionRows("en", super)); !strings.Contains(strings.Join(d, " "), "mn:mail") {
		t.Errorf("the operator lost Messages: %v", d)
	}
	if d := rowData(b.settingsSectionRows(context.Background(), "en", super, "jwt")); !strings.Contains(strings.Join(d, " "), "mn:ven") {
		t.Errorf("the operator lost Venues: %v", d)
	}
}
