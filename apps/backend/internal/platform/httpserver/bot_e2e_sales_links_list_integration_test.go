//go:build integration

package httpserver

// End-to-end proof of the bot's list of links (spec 43 §5.2 and §6.3, QL-9)
// through the real router, the real bot and the stub Telegram. Three links on
// two events: the menu shows all three with their numbers, "only this event"
// narrows to two, a row opens its card, the archive moves a link from the
// active list to the archive list, and "back to the list" lands on the same
// filter. Six links make two pages: forward and back by cursor, and a page
// that empties after an archive falls back to the first.
//
// The stub records message TEXT, so state is read from bot_dialogs and the
// buttons of the newest message.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	qlListOwnerTG = int64(7861)
	qlPagerTG     = int64(7862)
)

type qlListState struct {
	Step    string
	Filter  string
	Page    int
	Cursors []string
	Next    string
	Rows    []uuid.UUID
	Picker  []uuid.UUID
	Event   *uuid.UUID
}

func readQLListState(t *testing.T, pool *pgxpool.Pool, tg int64) qlListState {
	t.Helper()
	var step string
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT step, state FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'sales_link'`, tg).Scan(&step, &raw); err != nil {
		t.Fatalf("the sales_link dialog must be stored for %d: %v", tg, err)
	}
	var st struct {
		Filter  string                  `json:"lf"`
		Event   *struct{ ID uuid.UUID } `json:"le"`
		Page    int                     `json:"lp"`
		Cursors []string                `json:"lc"`
		Next    string                  `json:"ln"`
		Rows    []uuid.UUID             `json:"li"`
		Picker  []uuid.UUID             `json:"ids"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode the sales_link dialog: %v", err)
	}
	out := qlListState{Step: step, Filter: st.Filter, Page: st.Page, Cursors: st.Cursors, Next: st.Next, Rows: st.Rows, Picker: st.Picker}
	if st.Event != nil {
		out.Event = &st.Event.ID
	}
	return out
}

var qlRowLine = regexp.MustCompile(`(?m)^\d+\. `)

// qlPlain drops the bold tags of a screen so labels can be compared as typed.
func qlPlain(s string) string {
	return strings.NewReplacer("<b>", "", "</b>", "").Replace(s)
}

// qlNewestButtons returns the callback data of the newest message of a chat.
func qlNewestButtons(tg *stubTelegram, chat int64) []string {
	_, _, kb := tg.lastOf(chat)
	return kb
}

func qlCleanup(t *testing.T, pool *pgxpool.Pool, orgs ...uuid.UUID) {
	t.Cleanup(func() {
		ctx := context.Background()
		for _, org := range orgs {
			_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, org)
			_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = $1`, org)
		}
	})
}

func qlListServer(t *testing.T, pool *pgxpool.Pool) (*Server, *httptest.Server) {
	t.Helper()
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = qlTicketsBase
		c.ShortLinkFallbackURL = "https://home.ql8.test"
	})
	api := httptest.NewServer(srv.router)
	t.Cleanup(api.Close)
	return srv, api
}

// qlMake makes a link to an event the way an organizer does: the event card's
// button, the placement, the name.
func qlMake(d ecDriver, eventID uuid.UUID, label string) {
	d.t.Helper()
	d.press("ql:e:"+eventID.String(), "Где вы разместите код")
	d.press("ql:pk:poster", "Напечатайте короткое название места")
	d.say(label, "Ссылка создана")
}

func TestBotE2E_SalesLinksList(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv, api := qlListServer(t, pool)
	seed := seedPromoOrg(t, pool, f.orgID)
	qlCleanup(t, pool, f.orgID)
	linkECBotUser(t, f, srv, qlListOwnerTG, "owner")

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlListOwnerTG}

	// Two links on event A, one on event B.
	qlMake(owner, seed.eventA, "Афиша A1")
	qlMake(owner, seed.eventA, "Флаер A2")
	qlMake(owner, seed.eventB, "Вывеска B1")

	// ── the menu: all three, newest first, with their numbers ─────────────────
	text := qlPlain(owner.press("ql:menu", "Фильтр: Активные"))
	for _, want := range []string{"Афиша A1", "Флаер A2", "Вывеска B1", "сканов 0 · заказов 0", seed.nameA, seed.nameB, "Страница 1."} {
		if !strings.Contains(text, want) {
			t.Errorf("the list lacks %q:\n%s", want, text)
		}
	}
	if n := len(qlRowLine.FindAllString(text, -1)); n != 3 {
		t.Errorf("rows on the first screen = %d, want 3:\n%s", n, text)
	}
	if strings.Index(text, "Вывеска B1") > strings.Index(text, "Афиша A1") {
		t.Errorf("the newest link must come first:\n%s", text)
	}
	st := readQLListState(t, pool, qlListOwnerTG)
	if st.Step != "list" || st.Filter != "active" || st.Page != 1 || len(st.Rows) != 3 || st.Event != nil {
		t.Errorf("state after the menu = %+v", st)
	}

	// ── only this event: pick it from the organization's events ───────────────
	owner.press("ql:lev", "Выберите мероприятие, чтобы увидеть")
	idx := slices.Index(readQLListState(t, pool, qlListOwnerTG).Picker, seed.eventA)
	if idx < 0 {
		t.Fatalf("event A is not on the picker page: %+v", readQLListState(t, pool, qlListOwnerTG).Picker)
	}
	text = qlPlain(owner.press("ql:fo:"+strconv.Itoa(idx), "Только мероприятие:"))
	if !strings.Contains(text, seed.nameA) || !strings.Contains(text, "Афиша A1") || !strings.Contains(text, "Флаер A2") || strings.Contains(text, "Вывеска B1") {
		t.Errorf("the list narrowed to event A:\n%s", text)
	}
	if n := len(qlRowLine.FindAllString(text, -1)); n != 2 {
		t.Errorf("rows for event A = %d, want 2", n)
	}
	if st := readQLListState(t, pool, qlListOwnerTG); st.Event == nil || *st.Event != seed.eventA || len(st.Rows) != 2 {
		t.Errorf("state after narrowing = %+v", st)
	}

	// ── a row opens the link's card, with a way back to this event's list ─────
	card := qlPlain(owner.press("ql:o:0", "Короткий адрес"))
	if !strings.Contains(card, "Флаер A2") {
		t.Errorf("row 0 must be the newest link of event A:\n%s", card)
	}
	flyer := qlOne(t, pool, f.orgID, "Флаер A2")
	kb := qlNewestButtons(tg, qlListOwnerTG)
	for _, want := range []string{"ql:le:" + seed.eventA.String(), "ql:list", "ql:ar:" + flyer.ID.String()} {
		if !slices.Contains(kb, want) {
			t.Errorf("the card lacks the button %q: %v", want, kb)
		}
	}
	// A row press that names no row of the screen draws the list again.
	owner.press("ql:o:9", "Только мероприятие:")

	// ── archive it: the active list loses it, the archive list has it ─────────
	owner.press("ql:o:0", "Короткий адрес")
	owner.press("ql:ar:"+flyer.ID.String(), "Ссылка в архиве")
	if got := qlOne(t, pool, f.orgID, "Флаер A2").Status; got != "archived" {
		t.Fatalf("status after the archive press = %s", got)
	}
	text = qlPlain(owner.press("ql:list", "Только мероприятие:"))
	if !strings.Contains(text, "Афиша A1") || strings.Contains(text, "Флаер A2") || !strings.Contains(text, "Фильтр: Активные") {
		t.Errorf("back to the list must keep the filters and drop the archived link:\n%s", text)
	}
	text = qlPlain(owner.press("ql:lf:archived", "Фильтр: Архив"))
	if !strings.Contains(text, "Флаер A2") || !strings.Contains(text, "(в архиве)") || strings.Contains(text, "Афиша A1") {
		t.Errorf("the archive list:\n%s", text)
	}
	text = qlPlain(owner.press("ql:lf:all", "Фильтр: Все"))
	if !strings.Contains(text, "Флаер A2") || !strings.Contains(text, "Афиша A1") {
		t.Errorf("the list of all:\n%s", text)
	}

	// ── a new link from the narrowed list starts from that event ──────────────
	text = owner.press("ql:new", "Куда должна вести ссылка")
	if !strings.Contains(text, seed.nameA) {
		t.Errorf("the target choice must start from event A:\n%s", text)
	}
	owner.press("ql:list", "Только мероприятие:") // the list state survived the new flow

	// ── all events again, active only ─────────────────────────────────────────
	text = qlPlain(owner.press("ql:lx", "Вывеска B1"))
	if !strings.Contains(text, "Афиша A1") || strings.Contains(text, "Только мероприятие:") {
		t.Errorf("all events:\n%s", text)
	}
	text = qlPlain(owner.press("ql:lf:active", "Фильтр: Активные"))
	if strings.Contains(text, "Флаер A2") || !strings.Contains(text, "Вывеска B1") || !strings.Contains(text, "Афиша A1") {
		t.Errorf("active links of all events:\n%s", text)
	}
	if n := len(qlRowLine.FindAllString(text, -1)); n != 2 {
		t.Errorf("active rows = %d, want 2", n)
	}

	// ── the event card's own button keeps working and leads back here ─────────
	owner.press("ql:e:"+seed.eventB.String(), "Где вы разместите код")
	owner.press("ql:x", "Что будем делать?")
	text = qlPlain(owner.press("ql:menu", "Фильтр: Активные"))
	if !strings.Contains(text, "Вывеска B1") {
		t.Errorf("the menu after a cancelled new link:\n%s", text)
	}
}

func TestBotE2E_SalesLinksListPages(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv, api := qlListServer(t, pool)
	seed := seedPromoOrg(t, pool, f.orgID)
	qlCleanup(t, pool, f.orgID)
	linkECBotUser(t, f, srv, qlPagerTG, "owner")

	tg := newStubTelegram(t)
	startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, qlPagerTG}

	// Six links, oldest "Плакат 1": the first page holds 6..2, the second 1.
	for i := 1; i <= 6; i++ {
		qlMake(owner, seed.eventA, "Плакат "+strconv.Itoa(i))
	}

	text := qlPlain(owner.press("ql:menu", "Фильтр: Активные"))
	if n := len(qlRowLine.FindAllString(text, -1)); n != 5 || !strings.Contains(text, "Плакат 6") || !strings.Contains(text, "Плакат 2") || strings.Contains(text, "Плакат 1 ") {
		t.Errorf("page 1 (rows %d):\n%s", n, text)
	}
	kb := qlNewestButtons(tg, qlPagerTG)
	if !slices.Contains(kb, "ql:lp:2") || slices.Contains(kb, "ql:lp:0") {
		t.Errorf("page 1 must offer only the next page: %v", kb)
	}
	if st := readQLListState(t, pool, qlPagerTG); st.Page != 1 || st.Next == "" || len(st.Cursors) != 1 {
		t.Errorf("state on page 1 = %+v", st)
	}

	// Forward by the API's cursor.
	text = qlPlain(owner.press("ql:lp:2", "Страница 2."))
	if n := len(qlRowLine.FindAllString(text, -1)); n != 1 || !strings.Contains(text, "6. Плакат 1") {
		t.Errorf("page 2 (rows %d):\n%s", n, text)
	}
	kb = qlNewestButtons(tg, qlPagerTG)
	if !slices.Contains(kb, "ql:lp:1") || slices.Contains(kb, "ql:lp:3") {
		t.Errorf("page 2 must offer only the previous page: %v", kb)
	}
	if st := readQLListState(t, pool, qlPagerTG); st.Page != 2 || st.Next != "" || len(st.Cursors) != 2 || st.Cursors[0] != "" || st.Cursors[1] == "" {
		t.Errorf("state on page 2 = %+v", st)
	}
	// A skipped page number is a press of an older screen: the current page.
	text = qlPlain(owner.press("ql:lp:7", "Страница 2."))
	if !strings.Contains(text, "Плакат 1") {
		t.Errorf("an unknown page must redraw the current one:\n%s", text)
	}

	// Back by the stored cursor.
	text = qlPlain(owner.press("ql:lp:1", "Страница 1."))
	if !strings.Contains(text, "Плакат 6") || strings.Contains(text, "Плакат 1 ") {
		t.Errorf("back to page 1:\n%s", text)
	}

	// The only link of page 2 is archived: "back to the list" falls to page 1.
	owner.press("ql:lp:2", "Страница 2.")
	owner.press("ql:o:0", "Короткий адрес")
	last := qlOne(t, pool, f.orgID, "Плакат 1")
	owner.press("ql:ar:"+last.ID.String(), "Ссылка в архиве")
	text = qlPlain(owner.press("ql:list", "Страница 1."))
	if n := len(qlRowLine.FindAllString(text, -1)); n != 5 || strings.Contains(text, "Плакат 1 ") {
		t.Errorf("after the archive the active list holds 5 rows on one page (rows %d):\n%s", n, text)
	}
	if kb := qlNewestButtons(tg, qlPagerTG); slices.Contains(kb, "ql:lp:2") {
		t.Errorf("one page left, no pager expected: %v", kb)
	}
	if st := readQLListState(t, pool, qlPagerTG); st.Page != 1 || st.Next != "" {
		t.Errorf("state after the fall-back = %+v", st)
	}
}
