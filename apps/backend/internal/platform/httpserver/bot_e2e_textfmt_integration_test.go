//go:build integration

package httpserver

// End-to-end proof of the bot's "type the text, see it formatted, decide" loop
// (spec 44, RT-05): an organizer types a description at the wizard's question,
// the bot asks arena-api (real router, real gates, the real textformat client
// against a stub Messages API) to format it, shows the preview, and only a
// button under the preview stores it. Every way the model can fail ends with
// valid ART in the database: changed words, a substituted link, a slow model,
// a spent daily cap. The preview survives a restart of the bot.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

const e2eTextFmtTelegramUser = 783

// textFmtBot runs a bot against the stub Telegram, waiting at most
// formatTimeout for the format route. stop ends it.
func textFmtBot(t *testing.T, pool *pgxpool.Pool, api *httptest.Server, tg *stubTelegram, formatTimeout time.Duration, tweak ...func(*eventbot.Options)) (stop func()) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg.restartGap()
	opts := eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()).WithFormatTimeout(formatTimeout),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		TicketsBaseURL:    "https://tickets.test",
		MessageRateLimit:  -1,
	}
	for _, fn := range tweak {
		fn(&opts)
	}
	bot, err := eventbot.New(opts)
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("bot.Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("bot did not stop after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

// textFmtWords is the text the model stub is asked to mark up: the organizer's
// text sits between <text> and </text> of the user message.
func textFmtWords(user string) string {
	start := strings.Index(user, "<text>\n")
	end := strings.LastIndex(user, "\n</text>")
	if start < 0 || end < start {
		return ""
	}
	return user[start+len("<text>\n") : end]
}

func TestBotE2E_DescriptionIsFormattedAndPreviewedBeforeItIsSaved(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	model := newTextModel(t)
	const (
		textA = "Двери открываются в 19:00. Билеты: https://example.com/t\n\n- вино\n- сыр"
		textB = "Скидка 20% на второй билет"
		textC = "Подробности на https://example.com/info"
		textS = "Медленный ответ модели"
		textD = "Модель не отвечает"
		textE = "Дневной лимит исчерпан"
	)
	var bCalls int
	longText := strings.TrimSpace(strings.Repeat("слово ", 665)) // 3 989 characters
	model.html = func(user string) string {
		text := textFmtWords(user)
		switch text {
		case textA:
			return `<p>Двери открываются в <strong>19:00</strong>. Билеты: <a href="https://example.com/t">https://example.com/t</a></p><ul><li>вино</li><li>сыр</li></ul>`
		case textB:
			bCalls++
			if bCalls == 1 {
				return `<p><strong>Скидка 25%</strong> на второй билет</p>` // a number changed
			}
			return `<p><strong>Скидка 20%</strong> на второй билет</p>`
		case textC:
			return `<p><strong>Подробности</strong> на <a href="https://evil.example/x">https://example.com/info</a></p>` // another address
		case textS:
			time.Sleep(2 * time.Second)
			return `<p><em>Медленный</em> ответ модели</p>`
		case textD:
			time.Sleep(5 * time.Second)
			return `<p>Модель не отвечает</p>`
		case longText:
			return "<p>" + longText + "</p>"
		}
		return "<p>" + text + "</p>"
	}
	srv := textFormatServer(t, pool, model, 8, 500)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx := context.Background()
	q := gen.New(pool)

	t.Cleanup(func() {
		c := context.Background()
		exec := func(label, sql string, args ...any) {
			if _, err := pool.Exec(c, sql, args...); err != nil {
				t.Logf("textfmt cleanup: %s: %v", label, err)
			}
		}
		exec("dialogs", `DELETE FROM bot_dialogs WHERE telegram_user_id = $1`, int64(e2eTextFmtTelegramUser))
		exec("drafts", `DELETE FROM bot_drafts WHERE telegram_user_id = $1`, int64(e2eTextFmtTelegramUser))
		exec("links", `DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, int64(e2eTextFmtTelegramUser))
		exec("compat_map_tiers", `DELETE FROM compatibility_id_map WHERE platform_id IN (
		          SELECT tt.id FROM ticket_tiers tt JOIN sessions s ON s.id = tt.session_id
		          JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("compat_map_sessions", `DELETE FROM compatibility_id_map WHERE platform_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("compat_map_events", `DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
		exec("compat_map_venues", `DELETE FROM compatibility_id_map WHERE platform_id IN (SELECT id FROM venues WHERE org_id = $1)`, f.orgID)
		exec("session_external_refs", `DELETE FROM session_external_refs WHERE org_id = $1`, f.orgID)
		exec("session_seats", `DELETE FROM session_seats WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("inventory_ledger", `DELETE FROM inventory_ledger WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("ticket_tiers", `DELETE FROM ticket_tiers WHERE session_id IN (
		          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
		exec("sessions", `DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
		exec("event_promoters", `DELETE FROM event_promoters WHERE org_id = $1`, f.orgID)
		exec("events", `DELETE FROM events WHERE org_id = $1`, f.orgID)
		exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
		exec("audit", `DELETE FROM audit_events WHERE metadata->>'org_id' = $1`, f.orgID.String())
	})

	managerEmail := f.newEmail("tfd")
	if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
	}
	code, _ := f.queuedCode(managerEmail)
	if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, e2eTextFmtTelegramUser)); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	var countryID, cityID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM countries WHERE iso2 = 'EE'`).Scan(&countryID); err != nil {
		t.Fatalf("country EE: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM cities WHERE slug = 'tallinn'`).Scan(&cityID); err != nil {
		t.Fatalf("city tallinn: %v", err)
	}

	tg := newStubTelegram(t)
	stop := textFmtBot(t, pool, api, tg, 3*time.Second)
	say := func(text, expect string) string {
		t.Helper()
		m := tg.mark()
		tg.push(e2eMessageAs(e2eTextFmtTelegramUser, text))
		return tg.waitSince(t, m, expect)
	}
	press := func(data, expect string) string {
		t.Helper()
		tg.waitButton(e2eTextFmtTelegramUser, data, 2*time.Second)
		m := tg.mark()
		tg.push(e2eCallbackAs(e2eTextFmtTelegramUser, data))
		return tg.waitSince(t, m, expect)
	}
	sayDate := func(text, iso, expect string) string {
		t.Helper()
		say(text, "Вы написали")
		return press("wz:dc:"+iso, expect)
	}
	descriptionOf := func(eventID uuid.UUID) (string, string) {
		t.Helper()
		var d, fmtName string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(description, ''), description_format FROM events WHERE id = $1`, eventID).Scan(&d, &fmtName); err != nil {
			t.Fatalf("description row: %v", err)
		}
		return d, fmtName
	}
	// Whatever a scenario stores must be valid ART for the description
	// profile, byte for byte what the filter would write again.
	assertValidART := func(label, stored, format string) {
		t.Helper()
		again, err := richtext.Normalize(stored, richtext.FormatART, richtext.ProfileDescription)
		if format != "art" || err != nil || again != stored || stored == "" {
			t.Fatalf("%s: stored %q / %q is not valid ART (%q, %v)", label, stored, format, again, err)
		}
	}
	venueName := "Fmt hall " + uuid.NewString()[:6]
	eventName := "Формат-концерт " + uuid.NewString()[:6]

	// ── The event, created through the wizard; its description is scenario A:
	// the model marks the text up correctly and the preview shows it.
	press("wz:new", "Как называется")
	say(eventName, "Возраст")
	press("wz:age:16+", "От чьего имени")
	press("wz:prom:org", "афишу")
	press("wz:skip", "Дата сеанса 1")
	sayDate("15.12.2099", "2099-12-15", "Время начала")
	press("wz:default", "Продажа билетов закроется в начале")
	press("wz:se:60", "Во сколько открывается вход")
	press("wz:dr:30", "В какой части мира")
	press("wz:region:europe", "В какой стране")
	press("wz:country:"+countryID.String(), "В каком городе")
	press("wz:city:"+cityID.String(), "Где проходит")
	press("wz:venue:new", "Название площадки")
	say(venueName, "Адрес площадки")
	say("Vabaduse väljak 1", "Сколько мест продаём")
	say("120", "Сеанс 1: 15.12.2099 20:00")
	press("wz:next", "Билеты одинаковые")
	press("wz:mode:single", "Как назвать билет")
	press("wz:default", "Цена билета")
	say("25", "Цена меняется")
	press("wz:no", "Описание мероприятия")

	// The question now says what the bot does with the text.
	preview := say(textA, "Сохранить так?")
	for _, want := range []string{"<b>19:00</b>", `<a href="https://example.com/t">`, "• вино", "• сыр"} {
		if !strings.Contains(preview, want) {
			t.Errorf("the preview lacks %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, "Здесь текст без выделений") {
		t.Errorf("a formatted answer must not carry the plain-text note:\n%s", preview)
	}
	// Nothing is in the draft until a button is pressed: the draft still sits
	// on the description question and the preview is in bot_dialogs.
	if row, err := q.GetBotDraft(ctx, e2eTextFmtTelegramUser, f.orgID); err != nil || row.Step != "x_description" {
		t.Fatalf("draft while the preview is open: %+v %v", row, err)
	}
	var dialogStep string
	if err := pool.QueryRow(ctx, `SELECT step FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'textfmt'`, int64(e2eTextFmtTelegramUser)).Scan(&dialogStep); err != nil || dialogStep != "preview" {
		t.Fatalf("preview dialog: step %q err %v", dialogStep, err)
	}
	press("tf:s", "Валюта цен")
	press("wz:cur:EUR", "Открыть продажи сразу")
	press("wz:pub:now", "Проверьте и опубликуйте")
	press("wz:publish", "Готово!")
	var eventID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM events WHERE org_id = $1`, f.orgID).Scan(&eventID); err != nil {
		t.Fatalf("event: %v", err)
	}
	descA, formatA := descriptionOf(eventID)
	assertValidART("A", descA, formatA)
	if !strings.Contains(descA, "<strong>19:00</strong>") || !strings.Contains(descA, `<a href="https://example.com/t"`) || !strings.Contains(descA, "<ul><li>вино</li><li>сыр</li></ul>") {
		t.Fatalf("A: the approved markup was not stored: %q", descA)
	}
	if n := model.calls(); n != 1 {
		t.Fatalf("the model was called %d times for the first description", n)
	}

	// edit changes the description of the published event and saves it.
	edit := func(text, previewNeedle string) string {
		t.Helper()
		press("wz:edit:"+eventID.String(), "Правим")
		ask := press("wz:e:desc", "Сейчас")
		if !strings.Contains(ask, "оформлю") {
			t.Fatalf("the question does not promise formatting:\n%s", ask)
		}
		return say(text, previewNeedle)
	}
	saveEdit := func() {
		t.Helper()
		press("wz:publish", "Что уйдёт на сайт")
		press("wz:publish:go", "Сохранено")
	}

	// ── B: the model changed a number. The bot shows the text as typed with a
	// plain note and no error. "Format again" asks once more and the second
	// answer is right. The preview is in bot_dialogs, so it survives a restart
	// of the bot: the buttons of the old message are refused as out of date (a
	// restart ends every screen, feed.go), but the state is there, and the
	// organizer just types the text again.
	shown := edit(textB, "Здесь текст без выделений")
	if strings.Contains(shown, "<b>") || strings.Contains(shown, "25%") {
		t.Fatalf("B: the changed answer reached the preview:\n%s", shown)
	}
	if strings.Contains(strings.ToLower(shown), "ошибк") {
		t.Fatalf("B: error wording in the preview:\n%s", shown)
	}
	again := press("tf:a", "Сохранить так?")
	if !strings.Contains(again, "<b>Скидка 20%</b>") || strings.Contains(again, "Здесь текст без выделений") {
		t.Fatalf("B: the second answer was not shown formatted:\n%s", again)
	}
	stop()
	time.Sleep(400 * time.Millisecond) // the stub's long poll
	textFmtBot(t, pool, api, tg, 3*time.Second)
	var savedArt string
	var savedStep string
	if err := pool.QueryRow(ctx, `SELECT step, state->>'art' FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'textfmt'`, int64(e2eTextFmtTelegramUser)).Scan(&savedStep, &savedArt); err != nil ||
		savedStep != "preview" || savedArt != "<p><strong>Скидка 20%</strong> на второй билет</p>" {
		t.Fatalf("B: the preview did not survive the restart: step %q art %q (%v)", savedStep, savedArt, err)
	}
	if _, err := q.GetBotDraft(ctx, e2eTextFmtTelegramUser, f.orgID); err != nil {
		t.Fatalf("B: the draft is gone after the restart: %v", err)
	}
	again = say(textB, "Сохранить так?")
	if !strings.Contains(again, "<b>Скидка 20%</b>") {
		t.Fatalf("B: after the restart the text was not formatted:\n%s", again)
	}
	press("tf:s", "Правка мероприятия")
	saveEdit()
	descB, formatB := descriptionOf(eventID)
	assertValidART("B", descB, formatB)
	if descB != "<p><strong>Скидка 20%</strong> на второй билет</p>" {
		t.Fatalf("B: stored %q", descB)
	}

	// ── C: a substituted address never reaches the text. "Without formatting"
	// stores the typed text as ART.
	shown = edit(textC, "Сохранить так?")
	if strings.Contains(shown, "evil.example") {
		t.Fatalf("C: the substituted address reached the preview:\n%s", shown)
	}
	press("tf:p", "Правка мероприятия")
	saveEdit()
	descC, formatC := descriptionOf(eventID)
	assertValidART("C", descC, formatC)
	if descC != "<p>Подробности на https://example.com/info</p>" || strings.Contains(descC, "evil") {
		t.Fatalf("C: stored %q", descC)
	}

	// ── S: a slow model: the bot says it is formatting, then shows the result.
	m := tg.mark()
	tg.push(e2eCallbackAs(e2eTextFmtTelegramUser, "wz:edit:"+eventID.String()))
	tg.waitSince(t, m, "Правим")
	press("wz:e:desc", "Сейчас")
	m = tg.mark()
	tg.push(e2eMessageAs(e2eTextFmtTelegramUser, textS))
	tg.waitSince(t, m, "Оформляю текст")
	slow := tg.waitSince(t, m, "Сохранить так?")
	if !strings.Contains(slow, "<i>Медленный</i>") {
		t.Fatalf("S: the slow answer was not shown:\n%s", slow)
	}
	press("tf:s", "Правка мероприятия")
	saveEdit()
	descS, formatS := descriptionOf(eventID)
	assertValidART("S", descS, formatS)
	if descS != "<p><em>Медленный</em> ответ модели</p>" {
		t.Fatalf("S: stored %q", descS)
	}

	// ── D: the model does not answer in time. The bot waits no longer than its
	// limit and the typed text goes in as it was.
	started := time.Now()
	shown = edit(textD, "Здесь текст без выделений")
	if took := time.Since(started); took > 4800*time.Millisecond {
		t.Fatalf("D: the bot waited %v for a model that does not answer", took)
	}
	press("tf:s", "Правка мероприятия")
	saveEdit()
	descD, formatD := descriptionOf(eventID)
	assertValidART("D", descD, formatD)
	if descD != "<p>Модель не отвечает</p>" {
		t.Fatalf("D: stored %q", descD)
	}

	// ── F: a long text goes in two kinds of message, none over Telegram's
	// limit: the text itself, then the question with the buttons. "Cancel"
	// changes nothing.
	press("wz:edit:"+eventID.String(), "Правим")
	press("wz:e:desc", "Сейчас")
	m = tg.mark()
	tg.push(e2eMessageAs(e2eTextFmtTelegramUser, longText))
	tg.waitSince(t, m, "Сохранить так?")
	tg.mu.Lock()
	var texts []string
	for _, s := range tg.sent[m:] {
		texts = append(texts, s)
	}
	tg.mu.Unlock()
	var textMessages, questionMessages int
	for _, s := range texts {
		if len([]rune(s)) > 4096 {
			t.Fatalf("F: a message of %d characters", len([]rune(s)))
		}
		if strings.Contains(s, "слово слово") {
			textMessages++
			if strings.Contains(s, "Сохранить так?") {
				t.Fatalf("F: the long text and the question share a message")
			}
		}
		if strings.Contains(s, "Сохранить так?") {
			questionMessages++
		}
	}
	if textMessages != 1 || questionMessages != 1 {
		t.Fatalf("F: %d text messages and %d question messages, want 1 and 1", textMessages, questionMessages)
	}
	press("tf:c", "текст остаётся прежним")
	if d, fm := descriptionOf(eventID); d != descD || fm != formatD {
		t.Fatalf("F: Cancel changed the description: %q / %q", d, fm)
	}
	var dialogs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_dialogs WHERE telegram_user_id = $1 AND kind = 'textfmt'`, int64(e2eTextFmtTelegramUser)).Scan(&dialogs); err != nil || dialogs != 0 {
		t.Fatalf("F: the preview dialog survived Cancel: %d (%v)", dialogs, err)
	}
	press("wz:e:home", "Правка мероприятия")

	// ── E: the daily cap is spent. The route still answers 200 with the plain
	// text, and the bot saves it like any other.
	press("wz:e:desc", "Сейчас")
	shown = say(textE, "Здесь текст без выделений")
	if strings.Contains(strings.ToLower(shown), "лимит") && !strings.Contains(shown, textE) {
		t.Fatalf("E: the preview names the cap:\n%s", shown)
	}
	press("tf:s", "Правка мероприятия")
	saveEdit()
	descE, formatE := descriptionOf(eventID)
	assertValidART("E", descE, formatE)
	if descE != "<p>Дневной лимит исчерпан</p>" {
		t.Fatalf("E: stored %q", descE)
	}
	// Eight calls reached the model (A, B three times, C, S, D, F); the ninth, E,
	// was refused by the cap before the model.
	if n := model.calls(); n != 8 {
		t.Fatalf("the model was called %d times, want 8", n)
	}
}
