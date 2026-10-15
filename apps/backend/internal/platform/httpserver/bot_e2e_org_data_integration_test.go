//go:build integration

package httpserver

// End-to-end proof of the owner's "Organization data" screens (HP-4a) through
// the real router, the real bot and the stub Telegram: the owner fills every
// legal field one question at a time (malformed answers refused in plain
// words, a bare NIF under "EU VAT" pointed to "Another type of number"),
// restarts the bot in the middle of a question, opens the sales page card,
// is told exactly what is missing when the switch is pressed too early, gets
// the personal-mailbox hint, switches the seller data on and off; the
// manager is refused; an owner of ANOTHER organization cannot touch this
// organization's channel. The stub records message TEXT only, so every
// effect is read from the database.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	ogOwnerTG    = int64(91601)
	ogManagerTG  = int64(91602)
	ogOutsiderTG = int64(91603)
)

func TestBotE2E_OrganizationData(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	other := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, ogOwnerTG, "owner")
	linkECBotUser(t, f, srv, ogManagerTG, "manager")
	linkECBotUser(t, other, srv, ogOutsiderTG, "owner")
	hosted, _ := sellerChannelFixture(t, f, "OG")
	sellerChannelFixture(t, other, "OG other")
	ctx := context.Background()
	t.Cleanup(func() {
		for _, tg := range []int64{ogOwnerTG, ogManagerTG, ogOutsiderTG} {
			_, _ = pool.Exec(context.Background(), `DELETE FROM bot_dialogs WHERE telegram_user_id = $1`, tg)
		}
	})

	tg := newStubTelegram(t)
	stop := startPromoTestBot(t, pool, api, tg)
	owner := ecDriver{t, tg, ogOwnerTG}
	manager := ecDriver{t, tg, ogManagerTG}
	outsider := ecDriver{t, tg, ogOutsiderTG}
	org := func(col string) string {
		t.Helper()
		var v *string
		if err := pool.QueryRow(ctx, `SELECT `+col+` FROM organizations WHERE id = $1`, f.orgID).Scan(&v); err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	setting := func(path string) string {
		t.Helper()
		var v *string
		if err := pool.QueryRow(ctx, `SELECT settings #>> $2::text[] FROM sales_channels WHERE id = $1`, hosted, "{"+path+"}").Scan(&v); err != nil {
			t.Fatalf("read setting %s: %v", path, err)
		}
		if v == nil {
			return "<nil>"
		}
		return *v
	}

	// ── the card: one sentence on where buyers see it, and what is missing ────
	text := owner.press("og:h", "Данные организации")
	for _, want := range []string{"в письмах с билетами", "заполните", "юридическое название", "налоговый номер"} {
		if !strings.Contains(text, want) {
			t.Errorf("card lacks %q:\n%s", want, text)
		}
	}

	// ── the manager does not hold org.update ──────────────────────────────────
	manager.press("og:h", "только владелец")
	manager.press("og:on:"+hosted.String(), "только владелец")

	// ── legal name and form ───────────────────────────────────────────────────
	owner.press("og:e:n", "Юридическое название")
	owner.say(strings.Repeat("я", 201), "до 200 символов")
	owner.say("Teatro Bot S.L.", "Сохранено")
	if got := org("legal_name"); got != "Teatro Bot S.L." {
		t.Errorf("legal_name = %s", got)
	}
	owner.press("og:e:f", "Форма")
	owner.say("S.L.", "Сохранено")
	if got := org("legal_form"); got != "S.L." {
		t.Errorf("legal_form = %s", got)
	}

	// ── tax number: a bare NIF under "EU VAT" is pointed elsewhere ────────────
	owner.press("og:t", "Выберите его вид")
	owner.press("og:ts:eu_vat", "НДС-номер ЕС")
	owner.say("B12345678", "Другой вид номера")
	if got := org("tax_id"); got != "<nil>" {
		t.Fatalf("a refused number must not be stored: %s", got)
	}
	owner.say("es b-1234 5678", "Сохранено")
	if got, scheme := org("tax_id"), org("tax_id_scheme"); got != "ESB12345678" || scheme != "eu_vat" {
		t.Errorf("tax = %s %s", got, scheme)
	}

	// ── address, and the country by its button ────────────────────────────────
	owner.press("og:e:a", "Улица и дом")
	owner.say("Calle Mayor 1", "Сохранено")
	owner.press("og:e:c", "Город")
	owner.say("Madrid", "Сохранено")
	owner.press("og:e:z", "Почтовый индекс")
	owner.say("28013", "Сохранено")
	owner.press("og:e:k", "две латинские буквы")
	owner.say("Spain", "две латинские буквы")
	owner.press("og:k:CZ", "Сохранено")
	if got := org("legal_address_country"); got != "CZ" {
		t.Errorf("country = %s", got)
	}

	// ── a restart in the middle of a question loses nothing ───────────────────
	owner.press("og:e:r", "Регистрационный номер")
	stop()
	time.Sleep(400 * time.Millisecond) // let the stub's long poll end
	startPromoTestBot(t, pool, api, tg)
	owner.say("Hoja M-1", "Сохранено")
	if got := org("registration_number"); got != "Hoja M-1" {
		t.Errorf("after the restart registration_number = %s", got)
	}

	// ── the sales page (P1a): not shown while data is missing, shown by itself
	// once it is complete, and the old on/off buttons only redraw the card ────
	text = owner.press("og:cv:"+hosted.String(), "Страница продаж")
	if !strings.Contains(text, "Страница не показывает e-mail") || !strings.Contains(text, "пока не показаны") {
		t.Errorf("the page card must say where buyers see it and that the block is not shown yet:\n%s", text)
	}
	text = owner.press("og:on:"+hosted.String(), "заполнены не полностью")
	if !strings.Contains(text, "публичный e-mail этой страницы продаж") || strings.Contains(text, "юридическое название") {
		t.Errorf("only the e-mail is missing now:\n%s", text)
	}
	if got := setting("hosted_page,show_seller"); got == "true" {
		t.Fatalf("a retired button must not write: show_seller = %s", got)
	}
	owner.press("og:ce:"+hosted.String(), "Публичный e-mail")
	owner.say("not-an-email", "не похоже на адрес e-mail")
	owner.say("Teatro.Tickets@Gmail.com", "Похоже на личный ящик")
	if got := setting("hosted_page,public_contact,email"); got != "teatro.tickets@gmail.com" {
		t.Errorf("email = %s", got)
	}
	owner.press("og:cp:"+hosted.String(), "Публичный телефон")
	owner.say("позвоните", "не похоже на номер телефона")
	owner.say("+34 600 111 222", "Сохранено")
	text = owner.press("og:cv:"+hosted.String(), "показаны на странице")
	if strings.Contains(text, "пока не показаны") {
		t.Errorf("a complete profile must be shown with no switch:\n%s", text)
	}
	if got := setting("hosted_page,show_seller"); got == "true" {
		t.Errorf("the bot wrote the legacy flag: show_seller = %s", got)
	}
	if got := setting("other_key"); got != "stays" {
		t.Errorf("an unrelated setting changed: %s", got)
	}

	// ── another organization's owner cannot touch this channel ────────────────
	outsider.press("og:off:"+hosted.String(), "больше нет в списке")
	if got := setting("hosted_page,show_seller"); got == "true" {
		t.Errorf("a foreign press changed the channel: %s", got)
	}

	// ── clearing a required field takes the block off the page, and the card
	// says what is missing ────────────────────────────────────────────────────
	text = owner.press("og:tx", "заполнены не полностью")
	if !strings.Contains(text, "налоговый номер") || !strings.Contains(text, "пока не показаны") {
		t.Errorf("after clearing the tax number the card must say what is missing:\n%s", text)
	}
	text = owner.press("og:off:"+hosted.String(), "заполнены не полностью")
	if !strings.Contains(text, "налоговый номер") {
		t.Errorf("a retired button redraws the card with what is missing:\n%s", text)
	}
	if got := setting("hosted_page,show_seller"); got == "true" {
		t.Errorf("show_seller after a retired button = %s", got)
	}

	// ── the operator hides it: the owner sees who did ─────────────────────────
	owner.press("og:t", "Выберите его вид")
	owner.press("og:ts:eu_vat", "НДС-номер ЕС")
	owner.say("es b-1234 5678", "Сохранено")
	if _, err := pool.Exec(ctx, `UPDATE sales_channels
		SET settings = jsonb_set(settings, '{hosted_page,seller_hidden}', 'true'::jsonb) WHERE id = $1`, hosted); err != nil {
		t.Fatal(err)
	}
	text = owner.press("og:cv:"+hosted.String(), "скрыл оператор платформы")
	if !strings.Contains(text, "оператор платформы") {
		t.Errorf("the page card must say the operator hid the block:\n%s", text)
	}

	// ── buyer messages of the contact form: the sales-chat switch ─────────────
	// The manager's screens were sent before the restart and are out of date
	// (owner decision 2026-10-14): the manager starts from the menu.
	manager.say("☰ Меню", "Что будем делать")
	manager.press("og:tg:0", "только владелец")
	if got := org("contact_form_to_telegram::text"); got != "true" {
		t.Errorf("a manager press changed the switch: %s", got)
	}
	owner.press("og:tg:0", "только на e-mail")
	if got := org("contact_form_to_telegram::text"); got != "false" {
		t.Errorf("contact_form_to_telegram after off = %s", got)
	}
	owner.press("og:tg:1", "и в ваш чат продаж")
	if got := org("contact_form_to_telegram::text"); got != "true" {
		t.Errorf("contact_form_to_telegram after on = %s", got)
	}

	// ── an answer after the dialog ran out is reported once ───────────────────
	owner.press("og:e:n", "Юридическое название")
	if _, err := pool.Exec(ctx, `UPDATE bot_dialogs SET expires_at = now() - interval '1 hour' WHERE telegram_user_id = $1 AND kind = 'orgdata'`, ogOwnerTG); err != nil {
		t.Fatal(err)
	}
	owner.say("Too Late S.L.", "устарел")
	if got := org("legal_name"); got != "Teatro Bot S.L." {
		t.Errorf("an expired dialog must not rename: %s", got)
	}

	// ── the audit row names the Telegram bot as the client ────────────────────
	var via *string
	if err := pool.QueryRow(ctx, `SELECT metadata->>'via' FROM audit_events WHERE action = 'v1.channel.seller_display_update' AND resource_id = $1 ORDER BY occurred_at DESC LIMIT 1`, hosted.String()).Scan(&via); err != nil || via == nil || *via != "telegram_bot" {
		t.Errorf("audit via = %v (%v), want telegram_bot", via, err)
	}
}
