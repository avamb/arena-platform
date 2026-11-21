//go:build integration

package httpserver

// End-to-end proof of the grouped event card of the bot (2026-11-13): the card
// has five buttons (link, Sales and orders, Edit, Promotion, More), the first
// one follows the state of the event, every group is one press away, the
// dangerous actions live only in More, and every old callback still works.
// The test walks a draft -> publish -> an event on sale -> every group -> the
// link with its QR code -> archive, as the owner, and shows the cards to the
// manager too. It logs the full text and the buttons of the cards and of each
// group screen in Russian, for the owner's eyes (go test -v).

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

const (
	cgOwnerTG   = int64(7831)
	cgManagerTG = int64(7832)
	cgBase      = "https://tickets.test"
)

// startCardGroupsBot starts the bot with the storefront address set, so an
// event on sale has a link for buyers.
func startCardGroupsBot(t *testing.T, api *httptest.Server, tg *stubTelegram, queries *gen.Queries) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg.restartGap()
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           queries,
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		TicketsBaseURL:    cgBase,
		MessageRateLimit:  -1,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("bot did not stop after cancel")
		}
	})
}

// cardShot presses a button, returns the text of the screen and the labels of
// its buttons, and logs both.
func cardShot(t *testing.T, d ecDriver, title, data, expect string) (string, []string) {
	t.Helper()
	text := d.press(data, expect)
	d.tg.mu.Lock()
	id := 0
	for mid, m := range d.tg.msgs {
		if m.chat == d.id && m.text == text && mid > id {
			id = mid
		}
	}
	d.tg.mu.Unlock()
	labels := d.tg.labelsOf(id)
	t.Logf("%s\n%s\n[кнопки] %s\n", title, text, strings.Join(labels, " | "))
	return text, labels
}

func hasLabel(labels []string, part string) bool {
	for _, l := range labels {
		if strings.Contains(l, part) {
			return true
		}
	}
	return false
}

func TestBotE2E_EventCardGroups(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = cgBase
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedEvsOrg(t, pool, f.orgID)
	t.Cleanup(func() { // runs before the seed's cleanup: the links point at the events
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = $1)`, f.orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = $1`, f.orgID)
	})
	linkECBotUser(t, f, srv, cgOwnerTG, "owner")
	linkECBotUser(t, f, srv, cgManagerTG, "manager")

	tg := newStubTelegram(t)
	startCardGroupsBot(t, api, tg, gen.New(pool))
	owner := ecDriver{t, tg, cgOwnerTG}
	manager := ecDriver{t, tg, cgManagerTG}

	draft, draftName, draftSessions := seed.event("draft", "Draft", 1)
	multi, multiName, multiSessions := seed.event("published", "Multi", 3)
	// Free places make a published event "on sale": only such an event gets a NEW
	// sales link behind its QR code (the sales state reads the free places).
	for _, sid := range append(append([]uuid.UUID{}, draftSessions...), multiSessions...) {
		seed.exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		           SELECT $1, 'ga|t1|' || lpad(gs::text, 6, '0'), '', '', '', tt.id, 'available', 'ga_unit'
		           FROM ticket_tiers tt, generate_series(1, 3) gs WHERE tt.session_id = $1`, sid)
	}
	// A past event: its only date ended a month ago.
	past := uuid.New()
	pastName := "EVS Past " + seed.suffix
	seed.exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, $3, 'published', 'public', $4)`,
		past, f.orgID, pastName, "evs-past-"+seed.suffix)
	pastSession := uuid.New()
	seed.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	           VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 5, 'scheduled', 'EUR', 'override')`,
		pastSession, past, seed.venue, time.Now().UTC().Add(-30*24*time.Hour).Truncate(time.Hour))
	seed.exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	           VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 5, 1, true)`, uuid.New(), pastSession)

	// No card ever carries a dangerous action.
	noDanger := func(label string, labels []string) {
		t.Helper()
		for _, bad := range []string{"Удалить", "В архив", "Снять с продажи"} {
			if hasLabel(labels, bad) {
				t.Errorf("%s: the card shows %q, which belongs in «Ещё»: %v", label, bad, labels)
			}
		}
		if len(labels) > 8 { // five buttons, «Что здесь?», Back, Menu
			t.Errorf("%s: %d buttons: %v", label, len(labels), labels)
		}
	}

	// ── a draft: Publish first, then the four groups ──────────────────────────
	text, labels := cardShot(t, owner, "КАРТОЧКА ЧЕРНОВИКА (владелец)", "ec:o:"+draft.String(), draftName)
	if !strings.Contains(labels[0], "Опубликовать") || !hasLabel(labels, "Продажи и заказы") || !hasLabel(labels, "Изменить") ||
		!hasLabel(labels, "Продвижение") || !hasLabel(labels, "Ещё") || hasLabel(labels, "Ссылка для покупателей") {
		t.Errorf("draft card buttons: %v", labels)
	}
	noDanger("draft card", labels)
	if strings.Contains(text, "Промоутер") {
		t.Errorf("the card still says «Промоутер»:\n%s", text)
	}
	cardShot(t, manager, "КАРТОЧКА ЧЕРНОВИКА (менеджер)", "ec:o:"+draft.String(), draftName)

	// ── each group screen, once ───────────────────────────────────────────────
	g := func(ev uuid.UUID, group string) string { return "ec:g:" + ev.String() + ":" + group }
	_, labels = cardShot(t, owner, "ГРУППА «Продажи и заказы»", g(draft, "s"), "Продажи и заказы")
	for _, w := range []string{"Сводка", "Заказы", "Список гостей", "Выгрузка CSV"} {
		if !hasLabel(labels, w) {
			t.Errorf("group Sales lacks %q: %v", w, labels)
		}
	}
	_, labels = cardShot(t, owner, "ГРУППА «Изменить»", g(draft, "e"), "Измените название")
	for _, w := range []string{"Название, описание и афиша", "Сеансы", "Категории и цены", "Материалы страницы", "Образец билета", "Текст в письме", "Бренд"} {
		if !hasLabel(labels, w) {
			t.Errorf("group Edit lacks %q: %v", w, labels)
		}
	}
	_, labels = cardShot(t, owner, "ГРУППА «Продвижение»", g(draft, "p"), "Привлекайте покупателей")
	for _, w := range []string{"Промокоды", "Бесплатные билеты", "Приведи друга"} {
		if !hasLabel(labels, w) {
			t.Errorf("group Promotion lacks %q: %v", w, labels)
		}
	}
	_, labels = cardShot(t, owner, "ГРУППА «Ещё» (черновик)", g(draft, "m"), "Скопируйте мероприятие")
	for _, w := range []string{"Повторить как новый", "Опубликовать", "Удалить"} {
		if !hasLabel(labels, w) {
			t.Errorf("group More (draft) lacks %q: %v", w, labels)
		}
	}
	if hasLabel(labels, "В архив") || hasLabel(labels, "Снять с продажи") {
		t.Errorf("group More (draft) offers what a draft cannot do: %v", labels)
	}
	// "Back" from a group is the card, and the events dialog is still on the card.
	owner.press("ec:o:"+draft.String(), draftName)
	if st := readECListState(t, pool, cgOwnerTG); st.Step != "card" {
		t.Errorf("the events dialog after a group screen: step = %q, want card", st.Step)
	}

	// ── publish: the card of an event on sale starts with the link ────────────
	owner.press(evsData(draft, "pub"), "опубликован и в продаже")
	_, labels = cardShot(t, owner, "КАРТОЧКА МЕРОПРИЯТИЯ В ПРОДАЖЕ (владелец)", "ec:o:"+draft.String(), draftName)
	if !strings.Contains(labels[0], "Ссылка для покупателей") || hasLabel(labels, "Опубликовать") {
		t.Errorf("card on sale buttons: %v", labels)
	}
	noDanger("card on sale", labels)
	cardShot(t, manager, "КАРТОЧКА МЕРОПРИЯТИЯ В ПРОДАЖЕ (менеджер)", "ec:o:"+draft.String(), draftName)
	_, labels = cardShot(t, owner, "ГРУППА «Ещё» (в продаже)", g(draft, "m"), "Скопируйте мероприятие")
	for _, w := range []string{"Снять с продажи", "В архив", "Удалить"} {
		if !hasLabel(labels, w) {
			t.Errorf("group More (on sale) lacks %q: %v", w, labels)
		}
	}

	// ── the link and its QR code in ONE press ─────────────────────────────────
	photos := tg.callCount("sendPhoto")
	text, labels = cardShot(t, owner, "ССЫЛКА ДЛЯ ПОКУПАТЕЛЕЙ", "ec:lk:"+draft.String(), "QR-код в изображении ниже")
	if !strings.Contains(text, cgBase+"/") {
		t.Errorf("the link screen must carry the page address:\n%s", text)
	}
	tg.waitCall(t, "sendPhoto", photos)
	if !hasLabel(labels, "Другие ссылки и QR-коды") {
		t.Errorf("the link screen lacks the way to more links: %v", labels)
	}
	links := qlLinks(t, pool, f.orgID)
	if len(links) != 1 || links[0].Kind != "event" || links[0].Placement != "other" || links[0].EventID == nil || *links[0].EventID != draft {
		t.Fatalf("the link press must make one event link: %+v", links)
	}
	photos = tg.callCount("sendPhoto")
	owner.press("ec:lk:"+draft.String(), "QR-код в изображении ниже")
	tg.waitCall(t, "sendPhoto", photos)
	if links = qlLinks(t, pool, f.orgID); len(links) != 1 {
		t.Errorf("a second press must reuse the link, got %d links", len(links))
	}
	owner.press("ql:e:"+draft.String(), "Где вы разместите код") // "More links and QR codes"
	// A draft has no link: an old button says so.
	owner.press("ec:lk:"+multi.String(), "QR-код в изображении ниже") // published with a slug: has a link
	noLink, _, _ := seed.event("draft", "NoLink", 1)
	owner.press("ec:lk:"+noLink.String(), "пока нет ссылки для покупателей")

	// ── a multi-date event: the dates toggle is in Sales, the expanded card keeps its dates
	_, labels = cardShot(t, owner, "КАРТОЧКА С ТРЕМЯ ДАТАМИ", "ec:o:"+multi.String(), multiName)
	noDanger("multi-date card", labels)
	_, labels = cardShot(t, owner, "ГРУППА «Продажи и заказы» (три даты)", g(multi, "s"), "Продажи и заказы")
	if !hasLabel(labels, "Даты (3)") {
		t.Errorf("the dates toggle of a multi-date event: %v", labels)
	}
	_, labels = cardShot(t, owner, "КАРТОЧКА С РАСКРЫТЫМИ ДАТАМИ", "ec:o:"+multi.String()+":1", "Все даты (3)")
	if !hasLabel(labels, "Скрыть даты") || !hasLabel(labels, "📊") {
		t.Errorf("an expanded card keeps its per-date rows and the hide button: %v", labels)
	}
	// The guest list: several dates open the dates, a single date answers at once.
	owner.press("ec:gl:"+multi.String(), "Сеансы мероприятия")
	owner.press("ec:gl:"+draft.String(), "Список гостей пуст")

	// ── a past event shows its sales first ────────────────────────────────────
	_, labels = cardShot(t, owner, "КАРТОЧКА ПРОШЕДШЕГО МЕРОПРИЯТИЯ (владелец)", "ec:o:"+past.String(), pastName)
	if !strings.Contains(labels[0], "Продажи и заказы") {
		t.Errorf("a past event starts with its sales: %v", labels)
	}
	noDanger("past card", labels)
	cardShot(t, manager, "КАРТОЧКА ПРОШЕДШЕГО МЕРОПРИЯТИЯ (менеджер)", "ec:o:"+past.String(), pastName)

	// ── the old callbacks still answer ────────────────────────────────────────
	owner.press("ec:es:"+draft.String(), "Сводка")
	owner.press("ses:list:"+draft.String(), "Сеансы мероприятия")
	owner.press("ec:mt:"+draft.String(), "Материалы")
	owner.press("pm:l:"+draft.String(), "Для мероприятия")
	owner.press("ct:e:"+draft.String(), "Категорий: 1")

	// ── archive: one question in More, one confirming press ───────────────────
	owner.press(evsData(draft, "arc"), "в архив?")
	if seed.status(t, draft) != "published" {
		t.Fatal("the question must not archive")
	}
	owner.press(evsData(draft, "arcy"), "в архиве")
	if seed.status(t, draft) != "archived" {
		t.Fatalf("archive: status = %s", seed.status(t, draft))
	}
	_, labels = cardShot(t, owner, "КАРТОЧКА АРХИВНОГО МЕРОПРИЯТИЯ (владелец)", "ec:o:"+draft.String(), draftName)
	if !strings.Contains(labels[0], "Продажи и заказы") || hasLabel(labels, "Ссылка для покупателей") {
		t.Errorf("an archived event starts with its sales and has no link: %v", labels)
	}
	noDanger("archived card", labels)
	cardShot(t, manager, "КАРТОЧКА АРХИВНОГО МЕРОПРИЯТИЯ (менеджер)", "ec:o:"+draft.String(), draftName)
	_, labels = cardShot(t, owner, "ГРУППА «Ещё» (архив)", g(draft, "m"), "Скопируйте мероприятие")
	if !hasLabel(labels, "Удалить") || hasLabel(labels, "Снять с продажи") {
		t.Errorf("group More (archived): %v", labels)
	}

	// ── a foreign press: another organization's event is "not found" ──────────
	other := newBotInviteFixture(t, pool)
	linkECBotUser(t, other, srv, cgOwnerTG+10, "owner")
	out := ecDriver{t, tg, cgOwnerTG + 10}
	ownLinks, otherLinks := len(qlLinks(t, pool, f.orgID)), len(qlLinks(t, pool, other.orgID))
	for _, data := range []string{
		g(multi, "s"), g(multi, "m"), "ec:lk:" + multi.String(), "ec:gl:" + multi.String(),
		"hp:ec:" + multi.String(), "hp:es:" + multi.String(), "hp:ee:" + multi.String(), "hp:ep:" + multi.String(), "hp:em:" + multi.String(),
	} {
		got := out.press(data, "Не найдено")
		if strings.Contains(got, multiName) {
			t.Errorf("%s leaked the other organization's event:\n%s", data, got)
		}
	}
	if n := len(qlLinks(t, pool, f.orgID)); n != ownLinks {
		t.Errorf("a foreign press changed the links of the event's organization: %d -> %d", ownLinks, n)
	}
	if n := len(qlLinks(t, pool, other.orgID)); n != otherLinks || n != 0 {
		t.Errorf("a foreign press made a sales link in the other organization: %d -> %d", otherLinks, n)
	}

	// ── a past event never gets a NEW sales link, and says so ──────────────────
	before := len(qlLinks(t, pool, f.orgID))
	text, _ = cardShot(t, owner, "ССЫЛКА ПРОШЕДШЕГО МЕРОПРИЯТИЯ", "ec:lk:"+past.String(), "не в продаже")
	if !strings.Contains(text, cgBase+"/") {
		t.Errorf("a past event still shows the page address:\n%s", text)
	}
	if n := len(qlLinks(t, pool, f.orgID)); n != before {
		t.Errorf("the press of a past event made a sales link: %d -> %d", before, n)
	}
	// The QR / link dialog is in Promotion for an event without a public link.
	_, labels = cardShot(t, owner, "ГРУППА «Продвижение» (черновик)", g(noLink, "p"), "Привлекайте покупателей")
	if !hasLabel(labels, "Ссылки и QR-коды") {
		t.Errorf("Promotion lacks «Ссылки и QR-коды»: %v", labels)
	}
	owner.press("ql:e:"+noLink.String(), "Где вы разместите код")
}
