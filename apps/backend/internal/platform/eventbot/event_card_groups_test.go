package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// linkStub is a sales-links API that pages its list two links at a time.
type linkStub struct {
	mu      sync.Mutex
	links   []map[string]any
	creates int
}

func (s *linkStub) add(eventID uuid.UUID, placement string) map[string]any {
	l := map[string]any{
		"id": uuid.NewString(), "code": "abc" + strconv.Itoa(len(s.links)), "placement_kind": placement,
		"target": map[string]any{"kind": "event", "event_id": eventID.String()},
	}
	s.links = append(s.links, l)
	return l
}

func (s *linkStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		time.Sleep(20 * time.Millisecond) // a slow create widens the race
		s.creates++
		var in struct {
			Target map[string]any `json:"target"`
			Place  string         `json:"placement_kind"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		id, _ := uuid.Parse(in.Target["event_id"].(string))
		_ = json.NewEncoder(w).Encode(s.add(id, in.Place))
	case http.MethodGet:
		start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		end := start + 2
		next := strconv.Itoa(end)
		if end >= len(s.links) {
			end, next = len(s.links), ""
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": s.links[start:end], "next_cursor": next})
	default:
		http.NotFound(w, r)
	}
}

func linkStubBot(t *testing.T, s *linkStub) *Bot {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return &Bot{texts: NewTexts(bundle), arena: NewArenaClient(srv.URL, "", nil), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Two presses of the link button at once make ONE link.
func TestEventSalesLink_DoublePressMakesOneLink(t *testing.T) {
	t.Parallel()
	s := &linkStub{}
	b := linkStubBot(t, s)
	ev := openapi.EventItem{Id: uuid.New(), Name: "Swan Lake"}
	var wg sync.WaitGroup
	got := make([]openapi.SalesLink, 2)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, found, err := b.eventSalesLink(context.Background(), "jwt", uuid.New(), ev, true)
			if err != nil || !found {
				t.Errorf("press %d: found = %v, err = %v", i, found, err)
			}
			got[i] = l
		}()
	}
	wg.Wait()
	if s.creates != 1 || len(s.links) != 1 {
		t.Fatalf("creates = %d, links = %d, want one", s.creates, len(s.links))
	}
	if got[0].Id != got[1].Id {
		t.Errorf("the two presses got different links: %v and %v", got[0].Id, got[1].Id)
	}
}

// The search reads every page of the event's links before it decides to make one.
func TestEventSalesLink_ReadsAllPagesAndCreatesOnlyOnSale(t *testing.T) {
	t.Parallel()
	s := &linkStub{}
	ev := openapi.EventItem{Id: uuid.New(), Name: "Swan Lake"}
	for i := 0; i < 5; i++ {
		s.add(ev.Id, "poster")
	}
	want := s.add(ev.Id, cardLinkPlacement) // on the third page
	b := linkStubBot(t, s)
	l, found, err := b.eventSalesLink(context.Background(), "jwt", uuid.New(), ev, true)
	if err != nil || !found || l.Id.String() != want["id"] || s.creates != 0 {
		t.Fatalf("found = %v, err = %v, link = %v, creates = %d", found, err, l.Id, s.creates)
	}
	// An event with no link of its own: no create without the permission.
	other := openapi.EventItem{Id: uuid.New(), Name: "Past"}
	if _, found, err := b.eventSalesLink(context.Background(), "jwt", uuid.New(), other, false); err != nil || found || s.creates != 0 {
		t.Fatalf("create=false: found = %v, err = %v, creates = %d", found, err, s.creates)
	}
	// ... and an existing link is reused without it.
	if _, found, err := b.eventSalesLink(context.Background(), "jwt", uuid.New(), ev, false); err != nil || !found {
		t.Fatalf("create=false must reuse the link: found = %v, err = %v", found, err)
	}
}

// The keyed mutex frees its entries.
func TestKeyedMutex_FreesEntries(t *testing.T) {
	t.Parallel()
	var k keyedMutex
	key := uuid.New()
	unlock := k.lock(key)
	unlock()
	k.lock(uuid.New())()
	if len(k.m) != 0 {
		t.Errorf("%d entries left", len(k.m))
	}
}

// The help of a section screen applies the screen's own role gate.
func TestScreenHelp_SectionGatesMatchTheScreens(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	owner, _, _, agent := cardTestIDs()
	for _, c := range []struct {
		screen  string
		id      *Identity
		refused bool
	}{
		{hpOrders, owner, false}, {hpOrders, agent, true},
		{hpPromoSec, owner, false}, {hpPromoSec, agent, true},
		{hpDoor, owner, false}, {hpDoor, agent, false},
		{hpSettings, owner, false}, {hpSettings, agent, false},
	} {
		if got := b.sectionHelpRefused("en", c.id, c.screen); got != c.refused {
			t.Errorf("hp:%s role %s: refused = %v, want %v", c.screen, c.id.Current.Role, got, c.refused)
		}
	}
	// The same decision as the screen: no sales rights, no Orders items either way.
	if canViewSales(agent) {
		t.Fatal("the test needs an identity without sales rights")
	}
	// A card group the role cannot open has no items, so its help refuses too.
	ev := uuid.New()
	for _, g := range []string{cardGroupSales, cardGroupPromo} {
		if items := b.cardGroupItems("en", agent, ev, g, cardFacts{Status: "published"}); len(items) != 0 {
			t.Errorf("group %s: the agent sees %v", g, items.ids())
		}
	}
}

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
	promo := []string{"pm:l:" + e, "iv:e:" + e, "ql:e:" + e, "rf:o:" + e}

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
		"ql:e:" + e, // the QR / link dialog, also for an event that has no public link
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
