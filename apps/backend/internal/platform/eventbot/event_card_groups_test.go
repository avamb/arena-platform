package eventbot

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

func cardTestIDs() (owner, manager, operator, agent *Identity) {
	return &Identity{Current: &Membership{Role: membershipRoleOwner}},
		&Identity{Current: &Membership{Role: membershipRoleManager}},
		&Identity{Current: &Membership{Role: "agent"}, Superadmin: true},
		&Identity{Current: &Membership{Role: "agent"}}
}

// flat is every callback of some rows, in order.
func flat(rows [][]models.InlineKeyboardButton) []string {
	return rowData(rows)
}

// The card has FIVE buttons at most, in the order of the first-button rule.
func TestCardRows_FirstButtonRule(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	e := ev.String()
	owner, manager, operator, agent := cardTestIDs()
	pub := "ec:ev:" + e + ":" + evsActPublish
	lk := "ec:lk:" + e
	g := func(s string) string { return "ec:g:" + e + ":" + s }

	cases := []struct {
		name string
		f    cardFacts
		id   *Identity
		want []string
	}{
		{"draft: publish first", cardFacts{Status: "draft", SalesState: "draft", Dates: 1, SingleLive: true}, owner, []string{pub, g("s"), g("e"), g("p"), g("m")}},
		{"draft manager", cardFacts{Status: "draft", SalesState: "draft"}, manager, []string{pub, g("s"), g("e"), g("p"), g("m")}},
		{"draft operator", cardFacts{Status: "draft", SalesState: "draft"}, operator, []string{pub, g("s"), g("e"), g("p"), g("m")}},
		{"on sale: link first", cardFacts{Status: "published", SalesState: "on_sale", HasLink: true}, owner, []string{lk, g("s"), g("e"), g("p"), g("m")}},
		{"on sale manager", cardFacts{Status: "published", SalesState: "on_sale", HasLink: true}, manager, []string{lk, g("s"), g("e"), g("p"), g("m")}},
		{"on sale without a link", cardFacts{Status: "published", SalesState: "on_sale"}, owner, []string{g("s"), g("e"), g("p"), g("m")}},
		{"past: sales first", cardFacts{Status: "published", SalesState: "archived", HasLink: true}, owner, []string{g("s"), lk, g("e"), g("p"), g("m")}},
		{"past manager", cardFacts{Status: "published", SalesState: "archived", HasLink: true}, manager, []string{g("s"), lk, g("e"), g("p"), g("m")}},
		{"archived: sales first, no link", cardFacts{Status: "archived", SalesState: "archived"}, owner, []string{g("s"), g("e"), g("p"), g("m")}},
		// A role without sales rights keeps Edit (sample, sessions, materials) and More (copy).
		{"agent draft", cardFacts{Status: "draft", SalesState: "draft"}, agent, []string{g("e"), g("m")}},
		{"agent on sale", cardFacts{Status: "published", SalesState: "on_sale", HasLink: true}, agent, []string{g("e"), g("m")}},
		{"agent archived", cardFacts{Status: "archived", SalesState: "archived"}, agent, []string{g("e"), g("m")}},
	}
	for _, c := range cases {
		got := flat(b.cardRows("en", c.id, ev, c.f))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, c.want)
		}
		if len(got) > 5 {
			t.Errorf("%s: %d buttons, the card has five at most", c.name, len(got))
		}
	}
}

// Every group shows what the role may use and what applies to the event.
func TestCardGroupRows(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	e := ev.String()
	owner, manager, _, agent := cardTestIDs()
	act := func(a string) string { return "ec:ev:" + e + ":" + a }

	published := cardFacts{Status: "published", SalesState: "on_sale", Dates: 1, SingleLive: true}
	multi := cardFacts{Status: "published", SalesState: "on_sale", Dates: 3}
	draft := cardFacts{Status: "draft", SalesState: "draft", Dates: 1, SingleLive: true}
	past := cardFacts{Status: "published", SalesState: "archived", Dates: 1, SingleLive: true}
	archived := cardFacts{Status: "archived", SalesState: "archived", Dates: 1, SingleLive: true}
	unknown := cardFacts{Status: "published", SalesState: "on_sale"} // no summary

	salesBase := []string{"ec:es:" + e, "or:e:" + e, "ec:gl:" + e, "ec:ce:" + e}
	editFull := []string{"wz:edit:" + e, "ses:list:" + e, "ct:e:" + e, "ec:mt:" + e, "sample:" + e, "tf:n:" + e, "pr:ev:" + e}
	editMulti := []string{"wz:edit:" + e, "ses:list:" + e, "ec:mt:" + e, "sample:" + e, "tf:n:" + e, "pr:ev:" + e}
	promo := []string{"pm:l:" + e, "iv:e:" + e, "rf:o:" + e}

	cases := []struct {
		name  string
		id    *Identity
		group string
		f     cardFacts
		want  []string
	}{
		{"sales", owner, cardGroupSales, published, salesBase},
		{"sales multi-date toggle", owner, cardGroupSales, multi, append(append([]string{}, salesBase...), "ec:o:"+e+":1")},
		{"sales manager", manager, cardGroupSales, published, salesBase},
		{"sales summary unknown", owner, cardGroupSales, unknown, salesBase},
		{"sales agent", agent, cardGroupSales, published, nil},
		{"edit single live date", owner, cardGroupEdit, published, editFull},
		{"edit several dates", owner, cardGroupEdit, multi, editMulti},
		{"edit manager", manager, cardGroupEdit, published, editFull},
		{"edit agent", agent, cardGroupEdit, published, []string{"wz:edit:" + e, "ses:list:" + e, "ec:mt:" + e, "sample:" + e}},
		{"promotion", owner, cardGroupPromo, published, promo},
		{"promotion agent", agent, cardGroupPromo, published, nil},
		{"more draft", owner, cardGroupMore, draft, []string{"wz:copy:" + e, act(evsActPublish), act(evsActDelete)}},
		{"more on sale", owner, cardGroupMore, published, []string{"wz:copy:" + e, act(evsActOff), act(evsActArchive), act(evsActDelete)}},
		{"more past", manager, cardGroupMore, past, []string{"wz:copy:" + e, act(evsActOff), act(evsActArchive), act(evsActDelete)}},
		{"more archived", owner, cardGroupMore, archived, []string{"wz:copy:" + e, act(evsActDelete)}},
		{"more agent", agent, cardGroupMore, published, []string{"wz:copy:" + e}},
	}
	for _, c := range cases {
		got := flat(b.cardGroupRows("en", c.id, ev, c.group, c.f))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, c.want)
		}
	}
}

// The dates toggle names the number of dates.
func TestCardGroupRows_DatesToggleText(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	owner, _, _, _ := cardTestIDs()
	rows := b.cardGroupRows("en", owner, uuid.New(), cardGroupSales, cardFacts{Status: "published", Dates: 4})
	last := rows[len(rows)-1][0]
	if !strings.Contains(last.Text, "4") || !strings.HasSuffix(last.CallbackData, ":1") {
		t.Errorf("dates toggle = %+v", last)
	}
}

// Nothing the old card offered is lost: each action is on the card or one
// press away in a group (two presses at most), whatever the status.
func TestCardAllOldActionsReachableInTwoPresses(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	e := ev.String()
	owner, manager, operator, _ := cardTestIDs()
	old := []string{
		"ec:es:" + e, "ec:ce:" + e, "ec:o:" + e + ":1", "or:e:" + e, "ses:list:" + e,
		"wz:edit:" + e, "wz:copy:" + e, "sample:" + e, "tf:n:" + e, "ct:e:" + e, "ec:mt:" + e,
		"pm:l:" + e, "iv:e:" + e, "rf:o:" + e, "pr:ev:" + e, "ec:gl:" + e,
	}
	for _, id := range []*Identity{owner, manager, operator} {
		for _, status := range []string{"draft", "published", "cancelled", "archived"} {
			f := cardFacts{Status: status, SalesState: "on_sale", HasLink: status == "published", Dates: 3, SingleLive: false}
			reach := map[string]bool{}
			for _, d := range flat(b.cardRows("en", id, ev, f)) {
				reach[d] = true // one press
			}
			for _, group := range []string{cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore} {
				for _, d := range flat(b.cardGroupRows("en", id, ev, group, f)) {
					reach[d] = true // two presses
				}
			}
			// The categories sit in Edit for a single live date.
			f.SingleLive, f.Dates = true, 1
			for _, d := range flat(b.cardGroupRows("en", id, ev, cardGroupEdit, f)) {
				reach[d] = true
			}
			for _, d := range old {
				if !reach[d] {
					t.Errorf("status %s: the old button %q is not reachable in two presses", status, d)
				}
			}
			// Publish, take off sale, archive and delete follow the lifecycle.
			for act, want := range map[string]bool{
				evsActPublish: status == "draft", evsActOff: status == "published",
				evsActArchive: status == "published" || status == "cancelled", evsActDelete: true,
			} {
				if got := reach["ec:ev:"+e+":"+act]; got != want {
					t.Errorf("status %s: act %s reachable = %v, want %v", status, act, got, want)
				}
			}
		}
	}
}

// The dangerous actions are never on the card itself, and every callback fits
// into Telegram's 64 bytes.
func TestCardDangerousActionsOnlyInMore(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	ev := uuid.New()
	owner, manager, operator, agent := cardTestIDs()
	for _, id := range []*Identity{owner, manager, operator, agent} {
		for _, status := range []string{"draft", "published", "cancelled", "archived"} {
			f := cardFacts{Status: status, SalesState: "on_sale", HasLink: true, Dates: 3}
			for _, d := range flat(b.cardRows("en", id, ev, f)) {
				for _, act := range []string{evsActOff, evsActArchive, evsActDelete, evsActOffYes, evsActArchiveYes} {
					if strings.HasSuffix(d, ":"+act) {
						t.Errorf("status %s: %q is dangerous and must be in More only", status, d)
					}
				}
				if len(d) > 64 {
					t.Errorf("callback %q is %d bytes", d, len(d))
				}
			}
			for _, group := range []string{cardGroupSales, cardGroupEdit, cardGroupPromo, cardGroupMore} {
				for _, d := range flat(b.cardGroupRows("en", id, ev, group, f)) {
					if len(d) > 64 {
						t.Errorf("callback %q is %d bytes", d, len(d))
					}
					if group != cardGroupMore && strings.HasPrefix(d, "ec:ev:") {
						t.Errorf("group %s carries the status button %q", group, d)
					}
				}
			}
		}
	}
}

// Every text of the grouped card renders in every language.
func TestCardGroupTextsRenderInEveryLanguage(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	args := map[string]any{"Name": "Swan Lake", "Title": "T", "Text": "X", "URL": "https://example.test/e", "N": 3}
	for _, loc := range SupportedLocales {
		for _, key := range cardGroupKeys {
			got := b.texts.T(loc, key, args)
			if got == "" || got == key || strings.Contains(got, "<no value>") {
				t.Errorf("%s: %s rendered %q", loc, key, got)
			}
		}
		for _, w := range []string{"promoter", "Promoter", "promotor", "Promotor", "промоутер", "Промоутер"} {
			for _, key := range []string{"bot.prom.event_line", "bot.prom.event_line_org", "bot.prom.event_btn", "bot.prom.btn", "bot.prom.list_title", "bot.prom.card"} {
				if strings.Contains(b.texts.T(loc, key, args), w) {
					t.Errorf("%s: %s still says %q", loc, key, w)
				}
			}
		}
	}
}
