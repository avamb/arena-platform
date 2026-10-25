//go:build integration

package httpserver

// End-to-end proof of what the bot says after the Telegram mini app saved a
// text (spec 44, RT-12b: «бот подтверждает сохранение при следующем действии
// организатора»). The mini app talks to arena-api only, so the test plays it
// with the manager's own JWT and the real event PATCH, between two presses of
// the bot:
//
//   - the letter text screen: the press of «Править в редакторе» keeps what the
//     event holds, the editor saves a new text, the next button says «Изменения
//     из редактора сохранены» ONCE, draws the saved text and does not carry out
//     that press; the press after it goes on as usual;
//   - the preview of an edited description: the editor saves, the press of
//     «Save as shown» does NOT write the old preview over the editor's text, it
//     says the note and previews the saved text instead.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
)

const (
	e2eMiniappSaveNoteUser = 790
	e2eMiniappSaveDescUser = 791
	e2eMiniappSaveSiteUser = 792
	miniappSavedNote       = "Изменения из редактора сохранены"
)

func TestBotE2E_MiniappSaveIsConfirmedOnTheNextAction(t *testing.T) {
	for _, tc := range []struct {
		name string
		user int64
		run  func(t *testing.T, e *miniappSaveEnv)
	}{
		{"letter text screen", e2eMiniappSaveNoteUser, miniappSaveLetterText},
		{"description preview", e2eMiniappSaveDescUser, miniappSaveDescription},
		{"description preview, the site changed the event too", e2eMiniappSaveSiteUser, miniappSaveDescriptionWithAnOutsideEdit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, newMiniappSaveEnv(t, tc.user))
		})
	}
}

// miniappSaveEnv is everything the two scenarios share.
type miniappSaveEnv struct {
	t       *testing.T
	user    int64
	eventID uuid.UUID
	orgID   uuid.UUID
	userID  uuid.UUID
	api     *httptest.Server
	tg      *stubTelegram
	ids     string
	query   func(sql string, dst any, args ...any)
	exec    func(sql string, args ...any)
}

func newMiniappSaveEnv(t *testing.T, user int64) *miniappSaveEnv {
	t.Helper()
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	model := newTextModel(t)
	model.html = func(text string) string { return "<p>" + textFmtWords(text) + "</p>" }
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.PosterLLMAPIKey = "sk-ant-test-stub"
		c.PosterLLMBaseURL = model.srv.URL
		c.TextFormatLLMModel = "claude-haiku-5-5"
		c.TextFormatsPerUserPerDay = 50
		c.TextFormatsPerOrgPerDay = 500
		c.EventCenterPublicURL = "https://events.example.test"
	})
	api := httptest.NewServer(srv.router)
	t.Cleanup(api.Close)
	ctx := context.Background()

	managerEmail := f.newEmail("mnsave")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, user)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM bot_telegram_links WHERE telegram_user_id = $1`, user).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	eventID, venueID, sessionID, tierID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	eventName := "Сохранение-концерт " + uuid.NewString()[:6]
	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, f.orgID, "Save Hall"}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{eventID, f.orgID, eventName}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{sessionID, eventID, venueID}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'Standard', 'fixed', 2500, 'EUR', 0)`, []any{tierID, sessionID}},
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
			{`DELETE FROM bot_dialogs WHERE telegram_user_id = $1`, []any{user}},
			{`DELETE FROM bot_drafts WHERE telegram_user_id = $1`, []any{user}},
			{`DELETE FROM web_login_codes WHERE user_id = $1`, []any{userID}},
			{`DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, []any{user}},
			{`DELETE FROM event_email_notes WHERE event_id = $1`, []any{eventID}},
			{`DELETE FROM i18n_text WHERE key = $1`, []any{eventID.String()}},
			{`DELETE FROM compatibility_id_map WHERE platform_id IN ($1, $2, $3, $4)`, []any{eventID, sessionID, tierID, venueID}},
			{`DELETE FROM inventory_ledger WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM session_seats WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM ticket_tiers WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM sessions WHERE id = $1`, []any{sessionID}},
			{`DELETE FROM events WHERE id = $1`, []any{eventID}},
			{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
			{`DELETE FROM audit_events WHERE metadata->>'org_id' = $1 OR actor_id = $2`, []any{f.orgID.String(), userID}},
		} {
			if _, err := pool.Exec(c, stmt.sql, stmt.args...); err != nil {
				t.Logf("miniapp save e2e cleanup: %s: %v", stmt.sql, err)
			}
		}
	})

	tg := newStubTelegram(t)
	textFmtBot(t, pool, api, tg, 3*time.Second, func(o *eventbot.Options) { o.MiniappEditor = true })
	return &miniappSaveEnv{
		t: t, user: user, eventID: eventID, orgID: f.orgID, userID: userID, api: api, tg: tg, ids: eventID.String(),
		query: func(sql string, dst any, args ...any) {
			t.Helper()
			if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		},
		exec: func(sql string, args ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		},
	}
}

func (e *miniappSaveEnv) say(text, expect string) string {
	e.t.Helper()
	m := e.tg.mark()
	e.tg.push(e2eMessageAs(e.user, text))
	return e.tg.waitSince(e.t, m, expect)
}

func (e *miniappSaveEnv) press(data, expect string) string {
	e.t.Helper()
	e.tg.waitButton(e.user, data, 2*time.Second)
	m := e.tg.mark()
	e.tg.push(e2eCallbackAs(e.user, data))
	return e.tg.waitSince(e.t, m, expect)
}

// editorSaves is the mini app: a PATCH of the event with the manager's own token.
func (e *miniappSaveEnv) editorSaves(body string) {
	e.t.Helper()
	jwt, err := eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience).Mint(e.userID)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPatch, e.api.URL+"/v1/organizations/"+e.orgID.String()+"/events/"+e.ids, bytes.NewReader([]byte(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Channel", "telegram_bot")
	res, err := e.api.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("the editor's save answered %d", res.StatusCode)
	}
}

func miniappSaveLetterText(t *testing.T, e *miniappSaveEnv) {
	e.say("/events", "Идут")
	e.press("event:"+e.ids+":1", "Сохранение-концерт")
	e.press("tf:n:"+e.ids, "Текст в письме")
	e.press("tf:m:"+e.ids, "Ссылка действует 5 минут")

	// The editor saves a text while the screen stays in the chat.
	e.editorSaves(`{"email_note":"<p>Из <strong>редактора</strong></p>","email_note_format":"art"}`)

	// The next button says so once, draws the saved text and is NOT carried out:
	// the organizer did not start writing a text.
	note := e.press("tf:e:"+e.ids, miniappSavedNote)
	if !strings.Contains(note, miniappSavedNote) {
		t.Fatalf("the note: %q", note)
	}
	_, shown, _ := e.tg.lastOf(e.user)
	if !strings.Contains(shown, "редактора") || !strings.Contains(shown, "Текст в письме") || strings.Contains(shown, "оформлю") {
		t.Fatalf("the screen after the note must show the saved text and not ask for a new one:\n%s", shown)
	}
	// Once: the same button now does what it always did.
	if ask := e.press("tf:e:"+e.ids, "оформлю"); strings.Contains(ask, miniappSavedNote) {
		t.Fatalf("the note was said twice:\n%s", ask)
	}
	var stored string
	e.query(`SELECT note FROM event_email_notes WHERE event_id = $1`, &stored, e.eventID)
	if stored != "<p>Из <strong>редактора</strong></p>" {
		t.Fatalf("the stored letter text changed: %q", stored)
	}
}

const (
	miniappCanon   = "<p>Исходное описание</p>"
	miniappEnglish = "<p>English <em>translation</em></p>"
	miniappRussian = "<p>Русский перевод</p>"
	miniappEdited  = "<p>Из <strong>редактора</strong></p>"
)

// miniappSeedTranslations gives the event a canonical description and an
// English and a Russian translation. The bot sends no language, so the list it
// reads shows the English translation as `description`: the bot must work on
// description_base, or the editor's save of the canonical text would go unseen.
func miniappSeedTranslations(e *miniappSaveEnv) {
	e.exec(`UPDATE events SET description = $2, description_format = 'art' WHERE id = $1`, e.eventID, miniappCanon)
	e.exec(`INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('event.description', $1, 'en', $2), ('event.description', $1, 'ru', $3)`,
		e.ids, miniappEnglish, miniappRussian)
}

// miniappEditorSavesDuringPreview walks the wizard to the description preview,
// opens the editor, lets it save and presses «Save as shown» once: the bot says
// the editor saved. It returns with the saved text previewed.
func miniappEditorSavesDuringPreview(t *testing.T, e *miniappSaveEnv, alsoOutside bool) {
	t.Helper()
	miniappSeedTranslations(e)
	e.say("/events", "Идут")
	e.press("event:"+e.ids+":1", "Сохранение-концерт")
	intro := e.press("wz:edit:"+e.ids, "Правим")
	// The card shows the canonical description, not the English translation the list answers.
	if strings.Contains(intro, "translation") {
		t.Fatalf("the edit card shows the translation:\n%s", intro)
	}
	e.press("wz:e:desc", "Сейчас")
	preview := e.say("Текст из бота", "Сохранить так?")
	if !strings.Contains(preview, "Текст из бота") {
		t.Fatalf("the preview: %q", preview)
	}
	e.press("tf:m", "Ссылка действует 5 минут")

	// The editor saves the canonical description while the preview waits. The list the bot
	// reads still shows the English translation, which the editor did not touch.
	e.editorSaves(`{"description":` + jsonString(miniappEdited) + `,"description_format":"art"}`)
	if alsoOutside {
		// And somebody renames the event on the site, too.
		e.exec(`UPDATE events SET name = name || ' (сайт)', updated_at = now() + interval '1 minute' WHERE id = $1`, e.eventID)
	}

	// «Save as shown» must not write the old preview over it.
	note := e.press("tf:s", miniappSavedNote)
	if !strings.Contains(note, miniappSavedNote) {
		t.Fatalf("the note: %q", note)
	}
	var stored, format string
	e.query(`SELECT COALESCE(description, '') FROM events WHERE id = $1`, &stored, e.eventID)
	e.query(`SELECT description_format FROM events WHERE id = $1`, &format, e.eventID)
	if stored != miniappEdited || format != "art" {
		t.Fatalf("the description in the event is %q / %q: the preview overwrote the editor's text", stored, format)
	}
	// The preview shows the saved text now, and the organizer decides again.
	_, shown, keys := e.tg.lastOf(e.user)
	if !strings.Contains(shown, "редактора") || !hasPrefixData(keys, "tf:s") {
		t.Fatalf("the preview after the note:\n%s\n%v", shown, keys)
	}
}

// miniappTranslationsIntact fails when the editor's save or the wizard's publish
// touched a translation.
func miniappTranslationsIntact(t *testing.T, e *miniappSaveEnv) {
	t.Helper()
	for locale, want := range map[string]string{"en": miniappEnglish, "ru": miniappRussian} {
		var got string
		e.query(`SELECT value FROM i18n_text WHERE namespace = 'event.description' AND key = $1 AND locale = $2`, &got, e.ids, locale)
		if got != want {
			t.Errorf("the %s translation changed: %q", locale, got)
		}
	}
}

func miniappSaveDescription(t *testing.T, e *miniappSaveEnv) {
	miniappEditorSavesDuringPreview(t, e, false)

	// Once: the next press of «Save as shown» goes on: the wizard says the description is kept.
	if back := e.press("tf:s", "Описание: сохранено"); strings.Contains(back, miniappSavedNote) {
		t.Fatalf("the note was said twice: %s", back)
	}

	// RT-12b review: «Publish changes» after a save in the mini app must not warn that the
	// event was changed elsewhere (the editor's save was the change), and must not send the
	// draft's stale copy of the description over it.
	e.press("wz:e:age", "Сейчас")
	e.press("wz:age:18+", "Правка мероприятия")
	e.press("wz:publish", "Что уйдёт на сайт")
	done := e.press("wz:publish:go", "Сохранено")
	if strings.Contains(done, "меняли в другом месте") {
		t.Fatalf("the publish warned about the editor's own save:\n%s", done)
	}
	var stored, age string
	e.query(`SELECT COALESCE(description, '') FROM events WHERE id = $1`, &stored, e.eventID)
	e.query(`SELECT COALESCE(age_rating, '') FROM events WHERE id = $1`, &age, e.eventID)
	if stored != miniappEdited {
		t.Fatalf("the publish changed the description the editor saved: %q", stored)
	}
	if age != "18+" {
		t.Fatalf("the publish did not write the age: %q", age)
	}
	miniappTranslationsIntact(t, e)
}

// When somebody else changed the event too, the warning stays: the editor's save
// must not hide a change the organizer has not seen.
func miniappSaveDescriptionWithAnOutsideEdit(t *testing.T, e *miniappSaveEnv) {
	miniappEditorSavesDuringPreview(t, e, true)
	e.press("tf:s", "Описание: сохранено")
	e.press("wz:e:age", "Сейчас")
	e.press("wz:age:18+", "Правка мероприятия")
	e.press("wz:publish", "Что уйдёт на сайт")
	warn := e.press("wz:publish:go", "меняли в другом месте")
	if !strings.Contains(warn, "меняли в другом месте") {
		t.Fatalf("the warning is gone although the site changed the event too:\n%s", warn)
	}
	miniappTranslationsIntact(t, e)
}
