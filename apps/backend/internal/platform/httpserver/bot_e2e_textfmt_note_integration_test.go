//go:build integration

package httpserver

// End-to-end proof of the bot's "Text in the letter" screen (spec 44, RT-06):
// the event card has the button, the screen says the letter adds the greeting
// and the date itself, the text goes through the same format-preview loop as
// the description and is saved with the event PATCH as ART, the test letter is
// queued for the manager's own address, and deleting asks first. Whatever the
// model does, the stored text is valid ART.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventnote"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

const e2eLetterTextTelegramUser = 784

func TestBotE2E_LetterTextIsFormattedSavedTestedAndDeleted(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	model := newTextModel(t)
	const (
		text1 = "Парковка бесплатная до 18:00"
		text2 = "Принесите паспорт"
		text3 = "Карта: https://example.com/map"
		text4 = "Модель молчит"
		text5 = "Лимит исчерпан"
	)
	model.html = func(user string) string {
		switch text := textFmtWords(user); text {
		case text1:
			return `<p>Парковка <strong>бесплатная</strong> до 18:00</p>`
		case text2:
			return `<p>Принесите <strong>документ</strong></p>` // a word changed
		case text3:
			return `<p>Карта: <a href="https://evil.example/m">https://example.com/map</a></p>` // another address
		case text4:
			time.Sleep(5 * time.Second)
			return `<p>Модель молчит</p>`
		default:
			return "<p>" + textFmtWords(user) + "</p>"
		}
	}
	srv := textFormatServer(t, pool, model, 4, 500)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx := context.Background()

	managerEmail := f.newEmail("tfl")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2eLetterTextTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	eventID := uuid.New()
	eventName := "Письмо-концерт " + uuid.NewString()[:6]
	venueID, sessionID := uuid.New(), uuid.New()
	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, f.orgID, "Letter Hall"}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{eventID, f.orgID, eventName}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{sessionID, eventID, venueID}},
	} {
		if _, err := pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM bot_dialogs WHERE telegram_user_id = $1`, []any{int64(e2eLetterTextTelegramUser)}},
			{`DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, []any{int64(e2eLetterTextTelegramUser)}},
			{`DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`, []any{eventnote.JobTypeTestEmail, f.orgID.String()}},
			{`DELETE FROM event_email_notes WHERE event_id = $1`, []any{eventID}},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{sessionID}},
			{`DELETE FROM events WHERE id = $1`, []any{eventID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
			{`DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, []any{f.orgID.String()}},
		} {
			if _, err := pool.Exec(c, stmt.sql, stmt.args...); err != nil {
				t.Logf("letter text e2e cleanup: %s: %v", stmt.sql, err)
			}
		}
	})

	tg := newStubTelegram(t)
	textFmtBot(t, pool, api, tg, 3*time.Second)
	say := func(text, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eMessageAs(e2eLetterTextTelegramUser, text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		tg.waitButton(e2eLetterTextTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(e2eCallbackAs(e2eLetterTextTelegramUser, data))
		return tg.waitSince(t, m, expect)
	}
	noteOf := func() (note, format string, found bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT note, note_format FROM event_email_notes WHERE event_id = $1`, eventID).Scan(&note, &format)
		return note, format, err == nil
	}
	assertValidNote := func(label, wantNote string) {
		t.Helper()
		note, format, found := noteOf()
		again, err := richtext.Normalize(note, richtext.FormatART, richtext.ProfileEmailNote)
		if !found || format != "art" || err != nil || again != note || note != wantNote {
			t.Fatalf("%s: stored %q / %q (found %v), want %q; filter says %q %v", label, note, format, found, wantNote, again, err)
		}
	}
	ids := eventID.String()

	// The event card carries the button.
	say("/events", "Идут")
	press("event:"+ids+":1", eventName)
	if !tg.waitButton(e2eLetterTextTelegramUser, "tf:n:"+ids, 3*time.Second) {
		t.Fatal("the event card has no \"Text in the letter\" button")
	}

	// The screen says what the letter does itself and that there is no text.
	screen := press("tf:n:"+ids, "Текст в письме")
	for _, want := range []string{"Приветствие и дату письмо добавит само", "Сейчас текста нет"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen lacks %q:\n%s", want, screen)
		}
	}
	if tg.waitButton(e2eLetterTextTelegramUser, "tf:t:"+ids, 300*time.Millisecond) || tg.waitButton(e2eLetterTextTelegramUser, "tf:d:"+ids, 100*time.Millisecond) {
		t.Fatal("test letter and delete are offered although there is no text")
	}

	// 1. Correct markup: preview, save, the screen shows the new text.
	ask := press("tf:e:"+ids, "оформлю")
	if !strings.Contains(ask, "дату письмо добавит само") || strings.Contains(ask, "{name}") {
		t.Errorf("the question must say the letter adds the greeting and the date:\n%s", ask)
	}
	preview := say(text1, "Сохранить так?")
	if !strings.Contains(preview, "<b>бесплатная</b>") {
		t.Fatalf("1: the preview is not formatted:\n%s", preview)
	}
	saved := press("tf:s", "Готово, текст в письме обновлён")
	if !strings.Contains(saved, "<b>бесплатная</b>") {
		t.Errorf("1: the screen does not show the new text:\n%s", saved)
	}
	assertValidNote("1", "<p>Парковка <strong>бесплатная</strong> до 18:00</p>")

	// 2. A word changed: the plain text is shown and saved, no error wording.
	press("tf:e:"+ids, "оформлю")
	shown := say(text2, "Здесь текст без выделений")
	if strings.Contains(shown, "документ") || strings.Contains(strings.ToLower(shown), "ошибк") {
		t.Fatalf("2: the changed answer or an error reached the preview:\n%s", shown)
	}
	press("tf:s", "Готово, текст в письме обновлён")
	assertValidNote("2", "<p>Принесите паспорт</p>")

	// 3. A substituted address: "Without formatting" stores the typed text, its
	// own address made a link by the letter profile.
	press("tf:e:"+ids, "оформлю")
	shown = say(text3, "Сохранить так?")
	if strings.Contains(shown, "evil.example") {
		t.Fatalf("3: the substituted address reached the preview:\n%s", shown)
	}
	press("tf:p", "Готово, текст в письме обновлён")
	note, _, _ := noteOf()
	if strings.Contains(note, "evil") || !strings.Contains(note, `href="https://example.com/map"`) {
		t.Fatalf("3: stored %q", note)
	}
	assertValidNote("3", note)

	// 4. The model does not answer in time: the typed text goes in as it was.
	press("tf:e:"+ids, "оформлю")
	started := time.Now()
	say(text4, "Здесь текст без выделений")
	if took := time.Since(started); took > 4800*time.Millisecond {
		t.Fatalf("4: the bot waited %v for a model that does not answer", took)
	}
	press("tf:s", "Готово, текст в письме обновлён")
	assertValidNote("4", "<p>Модель молчит</p>")

	// 5. The daily cap is spent: still a plain preview, still a saved text.
	press("tf:e:"+ids, "оформлю")
	say(text5, "Здесь текст без выделений")
	press("tf:s", "Готово, текст в письме обновлён")
	assertValidNote("5", "<p>Лимит исчерпан</p>")
	if n := model.calls(); n != 4 {
		t.Fatalf("the model was called %d times, want 4 (the fifth was refused by the cap)", n)
	}

	// A text over the limit never reaches the model, and "Cancel" changes nothing.
	press("tf:e:"+ids, "оформлю")
	say(strings.Repeat("я", 2001), "до 2000 знаков")
	press("tf:c", "текст остаётся прежним")
	assertValidNote("cancel", "<p>Лимит исчерпан</p>")
	if n := model.calls(); n != 4 {
		t.Fatalf("the model was called for a text that is too long: %d calls", n)
	}

	// The test letter is queued for the manager's own address, with the stored
	// text and the organizer's language.
	press("tf:t:"+ids, "Тестовое письмо отправлено")
	var to, locale string
	if err := pool.QueryRow(ctx, `SELECT payload->>'to', payload->>'locale' FROM worker_jobs WHERE job_type = $1 AND payload->>'org_id' = $2`,
		eventnote.JobTypeTestEmail, f.orgID.String()).Scan(&to, &locale); err != nil || to != managerEmail || locale != "ru" {
		t.Fatalf("test letter job: to %q locale %q (%v)", to, locale, err)
	}

	// Deleting asks first: "No" keeps the text, "Yes" removes it.
	press("tf:d:"+ids, "Удалить текст из писем")
	press("tf:n:"+ids, "Текст в письме")
	if _, _, found := noteOf(); !found {
		t.Fatal("\"No, keep it\" deleted the text")
	}
	press("tf:d:"+ids, "Удалить текст из писем")
	press("tf:dy:"+ids, "Текст из писем удалён")
	if _, _, found := noteOf(); found {
		t.Fatal("\"Yes, delete\" left the text")
	}
}
