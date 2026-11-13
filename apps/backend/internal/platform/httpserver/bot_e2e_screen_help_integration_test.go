//go:build integration

package httpserver

// End-to-end proof of "❓ Что здесь?" (2026-11-13): the help of ONE screen, built
// from the same item list as the keyboard. The test opens the help on "My
// events", on the card, on a group of the card and on a menu section, and comes
// back to the very same screen. The list keeps its filter and the delete
// question is still ended by the help. It logs the help of the card and of the
// list for the owner and for the manager in Russian (go test -v).

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	hpOwnerTG   = int64(7841)
	hpManagerTG = int64(7842)
)

func TestBotE2E_ScreenHelp(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.ShortLinkBaseURL = qlShortBase
		c.PublicTicketsBaseURL = cgBase
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	seed := seedEvsOrg(t, pool, f.orgID)
	linkECBotUser(t, f, srv, hpOwnerTG, "owner")
	linkECBotUser(t, f, srv, hpManagerTG, "manager")

	tg := newStubTelegram(t)
	startCardGroupsBot(t, api, tg, gen.New(pool))
	owner := ecDriver{t, tg, hpOwnerTG}
	manager := ecDriver{t, tg, hpManagerTG}

	draft, draftName, _ := seed.event("draft", "Draft", 1)
	live, liveName, _ := seed.event("published", "Live", 1)
	ev := func(prefix string, id uuid.UUID) string { return prefix + ":" + id.String() }

	// backOnly checks the help has ONE button, « Назад, with the callback given.
	backOnly := func(label string, labels []string) {
		t.Helper()
		if len(labels) != 1 || !strings.Contains(labels[0], "Назад") {
			t.Errorf("%s: the help must have one «Назад» button: %v", label, labels)
		}
	}

	// ── My events: the help follows the filter and « Back keeps it ────────────
	owner.press("el:new", "Идут · 2")
	_, labels := cardShot(t, owner, "СПРАВКА «МОИ МЕРОПРИЯТИЯ» (владелец)", "hp:el", "Что здесь: 📅 Мои мероприятия")
	backOnly("list help", labels)
	owner.press("el:f:arc", "Архив")
	helpText, labels := cardShot(t, owner, "СПРАВКА «МОИ МЕРОПРИЯТИЯ» (владелец, архив)", "hp:el", "Что здесь: 📅 Мои мероприятия")
	backOnly("list help in the archive", labels)
	if !strings.Contains(helpText, "Как сюда вернуться: ☰ Меню, затем 📅 Мои мероприятия.") {
		t.Errorf("the way back to the list:\n%s", helpText)
	}
	if st := readECListState(t, pool, hpOwnerTG); st.Step != "list" || st.Filter != "arc" {
		t.Errorf("the help must keep the list's state: %+v", st)
	}
	owner.press("el:b", "Архив") // « Back: the same filter
	if st := readECListState(t, pool, hpOwnerTG); st.Step != "list" || st.Filter != "arc" {
		t.Errorf("after « Back the list changed: %+v", st)
	}
	owner.press("el:f:run", "Идут · 2")
	cardShot(t, manager, "СПРАВКА «МОИ МЕРОПРИЯТИЯ» (менеджер)", "hp:el", "Что здесь: 📅 Мои мероприятия")

	// ── the card of a draft and of an event on sale, owner and manager ───────
	owner.press(ev("ec:o", draft), draftName)
	helpText, labels = cardShot(t, owner, "СПРАВКА КАРТОЧКИ ЧЕРНОВИКА (владелец)", ev("hp:ec", draft), "Что здесь: "+draftName)
	backOnly("card help", labels)
	for _, want := range []string{"Опубликовать", "Продажи и заказы", "Изменить", "Продвижение", "Ещё", "Как сюда вернуться: ☰ Меню, затем 📅 Мои мероприятия, затем выберите мероприятие."} {
		if !strings.Contains(helpText, want) {
			t.Errorf("the help of a draft card lacks %q:\n%s", want, helpText)
		}
	}
	if strings.Contains(helpText, "Ссылка для покупателей") {
		t.Errorf("a draft has no link, so its help has no link line:\n%s", helpText)
	}
	manager.press(ev("ec:o", draft), draftName)
	cardShot(t, manager, "СПРАВКА КАРТОЧКИ ЧЕРНОВИКА (менеджер)", ev("hp:ec", draft), "Что здесь: "+draftName)
	owner.press(ev("ec:o", live), liveName)
	helpText, _ = cardShot(t, owner, "СПРАВКА КАРТОЧКИ МЕРОПРИЯТИЯ В ПРОДАЖЕ (владелец)", ev("hp:ec", live), "Что здесь: "+liveName)
	if !strings.Contains(helpText, "Ссылка для покупателей") || strings.Contains(helpText, "Опубликовать") {
		t.Errorf("the help of an event on sale:\n%s", helpText)
	}
	manager.press(ev("ec:o", live), liveName)
	cardShot(t, manager, "СПРАВКА КАРТОЧКИ МЕРОПРИЯТИЯ В ПРОДАЖЕ (менеджер)", ev("hp:ec", live), "Что здесь: "+liveName)

	// « Back is the card itself, and the list state stays on its card step
	owner.press(ev("ec:o", live), liveName)
	if st := readECListState(t, pool, hpOwnerTG); st.Step != "card" {
		t.Errorf("after the card help: step = %q, want card", st.Step)
	}

	// ── a group: Sales and orders, and the way back to the group ──────────────
	owner.press(ev("ec:g", live)+":s", "Продажи и заказы")
	helpText, labels = cardShot(t, owner, "СПРАВКА ГРУППЫ «ПРОДАЖИ И ЗАКАЗЫ» (владелец)", ev("hp:es", live), "Что здесь: 📊 Продажи и заказы")
	backOnly("group help", labels)
	for _, want := range []string{"Сводка", "Заказы", "Список гостей", "Выгрузка CSV", "затем 📊 Продажи и заказы."} {
		if !strings.Contains(helpText, want) {
			t.Errorf("the help of the Sales group lacks %q:\n%s", want, helpText)
		}
	}
	owner.press(ev("ec:g", live)+":s", "Продажи и заказы") // « Back
	if st := readECListState(t, pool, hpOwnerTG); st.Step != "card" {
		t.Errorf("after the group help: step = %q, want card", st.Step)
	}
	cardShot(t, owner, "СПРАВКА ГРУППЫ «ЕЩЁ» (владелец)", ev("hp:em", live), "Что здесь: ⚙️ Ещё")

	// ── the help ends the delete question, like any press that is not its own ──
	owner.press(evsData(live, "del"), "Последний шаг")
	if botDialogCount(t, pool, hpOwnerTG, "evstatus") != 1 {
		t.Fatal("the delete question must be open")
	}
	owner.press(ev("hp:ec", live), "Что здесь: "+liveName)
	if botDialogCount(t, pool, hpOwnerTG, "evstatus") != 0 {
		t.Error("the help must end the delete question")
	}

	// ── a menu section and the same help for the manager ─────────────────────
	owner.press("home", "Что будем делать?")
	owner.press("mn:set", "Настройки")
	helpText, labels = cardShot(t, owner, "СПРАВКА РАЗДЕЛА «НАСТРОЙКИ» (владелец)", "hp:set", "Что здесь: ⚙️ Настройки")
	backOnly("section help", labels)
	if !strings.Contains(helpText, "Данные организации") || !strings.Contains(helpText, "Как сюда вернуться: ☰ Меню, затем ⚙️ Настройки.") {
		t.Errorf("the owner's Settings help:\n%s", helpText)
	}
	owner.press("mn:set", "Настройки") // « Back
	manager.press("home", "Что будем делать?")
	manager.press("mn:set", "Настройки")
	helpText, _ = cardShot(t, manager, "СПРАВКА РАЗДЕЛА «НАСТРОЙКИ» (менеджер)", "hp:set", "Что здесь: ⚙️ Настройки")
	if strings.Contains(helpText, "Данные организации") {
		t.Errorf("a manager must not read the line of a button only the owner has:\n%s", helpText)
	}
	for _, section := range [][2]string{{"ord", "Заказы и возвраты"}, {"pro", "Продвижение"}, {"door", "Вход на мероприятие"}} {
		owner.press("home", "Что будем делать?")
		owner.press("mn:"+section[0], section[1])
		cardShot(t, owner, "СПРАВКА РАЗДЕЛА «"+section[1]+"»", "hp:"+section[0], "Что здесь: ")
		owner.press("mn:"+section[0], section[1])
	}

	// ── the help button sits on every screen it is meant for, and nowhere on the home menu
	owner.press("home", "Что будем делать?")
	if _, _, data := tg.lastOf(hpOwnerTG); strings.Contains(strings.Join(data, " "), "hp:") {
		t.Errorf("the home menu keeps the general help only: %v", data)
	}
}
