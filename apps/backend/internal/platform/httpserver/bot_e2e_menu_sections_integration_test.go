//go:build integration

package httpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// menuScreen logs the text and the buttons of the newest message of the chat
// (the report of the sectioned menu pastes these lines) and returns them.
func menuScreen(t *testing.T, tg *stubTelegram, who string, chat int64, title string) (string, []string, []string) {
	t.Helper()
	id, text, data := tg.lastOf(chat)
	labels := tg.labelsOf(id)
	t.Logf("[%s] %s\n%s\nКнопки: %s", who, title, text, strings.Join(labels, " | "))
	return text, data, labels
}

func containsData(data []string, want string) bool {
	for _, d := range data {
		if d == want {
			return true
		}
	}
	return false
}

func dataWithPrefix(data []string, prefix string) string {
	for _, d := range data {
		if strings.HasPrefix(d, prefix) {
			return d
		}
	}
	return ""
}

// The sectioned main menu and the First steps block: an owner of a fresh
// organization sees four open steps and walks every section to every old
// screen, a manager sees only the steps the server lists and no owner item,
// and the block is gone once the steps are done.
func TestBotE2E_MenuSectionsAndFirstSteps(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	linkECBotUser(t, f, srv, ecManagerTG, "manager")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM sales_links WHERE org_id = $1`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM payment_provider_configs WHERE org_id = $1`,
		} {
			_, _ = pool.Exec(ctx, stmt, f.orgID)
		}
	})

	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	owner := ecDriver{t, tg, ecOwnerTG}
	manager := ecDriver{t, tg, ecManagerTG}

	// ── the owner's home: four open steps above the nine buttons
	owner.say("/start", "Что будем делать?")
	text, data, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "главное меню, новая организация")
	for _, want := range []string{"Первые шаги", "выполнено 0 из 4", "▫️ Данные организации", "▫️ Приём оплаты", "▫️ Первое мероприятие", "▫️ Ссылка продаж"} {
		if !strings.Contains(text, want) {
			t.Errorf("owner home lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "✅") || strings.Contains(text, "Платежи ещё не подключены") {
		t.Errorf("owner home:\n%s", text)
	}
	wantHome := []string{"og:h", "mn:pay", "wz:new", "ql:menu", "wz:new", "el:new", "mn:ord", "mn:pro", "mn:door", "mn:set", "web:open", "help"}
	if strings.Join(data, " ") != strings.Join(wantHome, " ") {
		t.Errorf("owner home buttons = %v, want %v", data, wantHome)
	}

	// ── every section, and from it every old screen
	walk := func(d ecDriver, who, section, sectionText string, screens [][2]string) []string {
		t.Helper()
		d.press("home", "Что будем делать?")
		d.press(section, sectionText)
		_, sdata, _ := menuScreen(t, tg, who, d.id, "раздел "+section)
		if !containsData(sdata, "home") {
			t.Errorf("%s: %s has no way home: %v", who, section, sdata)
		}
		for _, s := range screens {
			cb := s[0]
			if strings.HasSuffix(cb, "*") { // a prefix: the screen's own data
				cb = dataWithPrefix(sdata, strings.TrimSuffix(cb, "*"))
			}
			if !containsData(sdata, cb) {
				t.Errorf("%s: %s lacks the button %q (has %v)", who, section, s[0], sdata)
				continue
			}
			d.press(cb, s[1])
			menuScreen(t, tg, who, d.id, "экран "+cb)
			d.press("home", "Что будем делать?")
			d.press(section, sectionText)
		}
		return sdata
	}

	sdata := walk(owner, "владелец", "mn:ord", "Заказы и возвраты", [][2]string{{"or:new", "Заказов пока нет"}, {"rq:l:*", "Возвраты · "}})
	if dataWithPrefix(sdata, "dp:l:") != "" {
		t.Errorf("an organization without disputes must not show them: %v", sdata)
	}
	walk(owner, "владелец", "mn:pro", "Продвижение", [][2]string{
		{"pm:l", "Кодов: 0"}, {"iv:new", "Бесплатные билеты для гостей"}, {"ql:menu", "Ссылки и QR"}, {"mn:mail", "Рассылки покупателям"},
	})
	owner.press("mn:mail", "Рассылки покупателям")
	if _, mdata, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:mail"); !containsData(mdata, "web:open") {
		t.Errorf("the mail screen has no web button: %v", mdata)
	}
	// every section carries «❓ Что здесь?» and its help returns to the section
	for _, section := range [][2]string{{"ord", "Заказы и возвраты"}, {"pro", "Продвижение"}, {"door", "Вход на мероприятие"}, {"set", "Настройки"}} {
		owner.press("home", "Что будем делать?")
		owner.press("mn:"+section[0], section[1])
		if _, sdata, slabels := menuScreen(t, tg, "владелец", ecOwnerTG, "раздел mn:"+section[0]+" с кнопкой помощи"); !containsData(sdata, "hp:"+section[0]) || !hasLabel(slabels, "Что здесь?") {
			t.Errorf("section %s has no help button: %v", section[0], sdata)
		}
		owner.press("hp:"+section[0], "Что здесь: ")
		menuScreen(t, tg, "владелец", ecOwnerTG, "справка раздела mn:"+section[0])
		owner.press("mn:"+section[0], section[1]) // « Back
	}
	walk(owner, "владелец", "mn:door", "Вход на мероприятие", [][2]string{{"scanner", "Сканер билетов"}, {"mn:gl", "Чтобы его открыть"}})
	owner.press("mn:gl", "Чтобы его открыть")
	gtext, gdata, glabels := menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:gl")
	for _, want := range []string{"Мои мероприятия", "Сеансы: перенос и отмена", "Список гостей"} {
		if !strings.Contains(gtext, want) {
			t.Errorf("the guest list screen does not name %q:\n%s", want, gtext)
		}
	}
	if !containsData(gdata, "el:new") || !containsData(gdata, "gl:on:mn") {
		t.Errorf("the guest list screen buttons: %v", gdata)
	}
	// the toggle redraws the same screen with the other state, and back
	owner.press("gl:on:mn", "Чтобы его открыть")
	if _, d2, l2 := menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:gl после включения"); !containsData(d2, "gl:off:mn") {
		t.Errorf("after the toggle: %v %v (before %v)", d2, l2, glabels)
	}
	owner.press("gl:off:mn", "Чтобы его открыть")
	if _, d3, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:gl после выключения"); !containsData(d3, "gl:on:mn") {
		t.Errorf("after the second toggle: %v", d3)
	}

	// payments: not connected now, with the way to connect
	owner.press("home", "Что будем делать?")
	owner.press("mn:set", "Настройки")
	owner.press("mn:pay", "Оплата не подключена")
	if ptext, _, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:pay, оплата не подключена"); !strings.Contains(ptext, "напишите в поддержку Arena") {
		t.Errorf("the payments screen does not say how to connect:\n%s", ptext)
	}
	sdata = walk(owner, "владелец", "mn:set", "Настройки", [][2]string{
		{"og:h", "Данные организации"}, {"mn:pay", "Оплата не подключена"}, {"team", "Команда"}, {"pr:l", "Бренды"},
		{"mn:ven", "Площадки"}, {"ec:nt", "Список гостей"}, {"lang", "Выберите язык бота."},
	})
	if containsData(sdata, "org") {
		t.Errorf("one organization has no switch: %v", sdata)
	}
	// "Open in browser" keeps its own test (TestBotE2E_WebLogin...): the link
	// needs the web address of the platform, which this harness leaves unset.
	owner.press("home", "Что будем делать?")
	owner.press("help", "Что умеет бот")
	if htext, _, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "помощь"); !strings.Contains(htext, "Вход на мероприятие") {
		t.Errorf("the help does not describe the new sections:\n%s", htext)
	}
	owner.press("home", "Что будем делать?")
	owner.press("el:new", "Мероприятий пока нет")

	// ── the manager: only the steps the server lists, no owner item
	manager.say("/start", "Что будем делать?")
	mtext, mdata, _ := menuScreen(t, tg, "менеджер", ecManagerTG, "главное меню, новая организация")
	if !strings.Contains(mtext, "выполнено 0 из 2") || strings.Contains(mtext, "Данные организации") || strings.Contains(mtext, "Приём оплаты") {
		t.Errorf("manager home:\n%s", mtext)
	}
	if len(mdata) < 2 || mdata[0] != "wz:new" || mdata[1] != "ql:menu" || containsData(mdata, "og:h") || containsData(mdata, "mn:pay") {
		t.Errorf("manager home buttons: %v", mdata)
	}
	manager.press("mn:set", "Настройки")
	_, msdata, _ := menuScreen(t, tg, "менеджер", ecManagerTG, "раздел mn:set")
	for _, bad := range []string{"og:h", "mn:pay", "team:invite"} {
		if containsData(msdata, bad) {
			t.Errorf("the manager sees %q in Settings: %v", bad, msdata)
		}
	}
	if !containsData(msdata, "team") {
		t.Errorf("the manager reads the team: %v", msdata)
	}
	manager.press("home", "Что будем делать?")
	manager.press("mn:ord", "Заказы и возвраты")
	menuScreen(t, tg, "менеджер", ecManagerTG, "раздел mn:ord")
	manager.press("home", "Что будем делать?")
	manager.press("mn:pro", "Продвижение")
	menuScreen(t, tg, "менеджер", ecManagerTG, "раздел mn:pro")
	manager.press("home", "Что будем делать?")
	manager.press("mn:door", "Вход на мероприятие")
	menuScreen(t, tg, "менеджер", ecManagerTG, "раздел mn:door")
	manager.press("mn:pay", "Ваша роль в этой организации") // a made-up press is refused
	manager.press("home", "Что будем делать?")

	// ── the steps get done: the block and the buttons are gone
	for _, stmt := range []string{
		`UPDATE organizations SET legal_name = 'Menu Legal OU', tax_id = 'EE100000002',
			legal_address_line1 = 'Narva mnt 1', legal_address_city = 'Tallinn', legal_address_postal_code = '10117'
			WHERE id = $1`,
		`INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active, verification_status)
			VALUES ($1, 'stripe', 'test', '{}'::jsonb, 'configured', true, 'ok')`,
		`INSERT INTO events (org_id, name, status, visibility) VALUES ($1, 'Menu draft', 'draft', 'public')`,
		`INSERT INTO sales_links (org_id, code, target_kind, label) VALUES ($1, $2, 'org_page', 'Menu link')`,
	} {
		args := []any{f.orgID}
		if strings.Contains(stmt, "$2") {
			// a code is seven letters and digits 2-9
			raw := uuid.New()
			code := make([]byte, 7)
			for i := range code {
				code[i] = 'a' + raw[i]%26
			}
			args = append(args, string(code))
		}
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	owner.press("home", "Что будем делать?")
	otext, odata, _ := menuScreen(t, tg, "владелец", ecOwnerTG, "главное меню, шаги выполнены")
	if strings.Contains(otext, "Первые шаги") || containsData(odata, "og:h") || odata[0] != "wz:new" || len(odata) != 8 {
		t.Errorf("owner home after the steps:\n%s\n%v", otext, odata)
	}
	owner.press("mn:set", "Настройки")
	owner.press("mn:pay", "Оплата подключена")
	menuScreen(t, tg, "владелец", ecOwnerTG, "экран mn:pay, оплата подключена")
	manager.press("home", "Что будем делать?")
	if mtext, mdata, _ := menuScreen(t, tg, "менеджер", ecManagerTG, "главное меню, шаги выполнены"); strings.Contains(mtext, "Первые шаги") || mdata[0] != "wz:new" || len(mdata) != 8 {
		t.Errorf("manager home after the steps:\n%s\n%v", mtext, mdata)
	}

	// ── a wizard press from the first steps still works
	owner.press("wz:new", "Как называется мероприятие?")
}
