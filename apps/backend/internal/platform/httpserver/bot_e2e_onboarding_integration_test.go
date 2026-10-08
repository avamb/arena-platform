//go:build integration

package httpserver

// End-to-end proof of the organizer application through the Telegram bot
// (08_architecture/34_onboarding_applications_ru.md §10): a stranger opens the
// bot, applies with the contact button and an e-mailed code, answers the form
// with buttons and text, submits, and is told the decision in the chat — the
// real router, the real bot, only Telegram stubbed. Run against a migrated
// database:
//
//	DATABASE_URL=... go test -tags integration ./apps/backend/internal/platform/httpserver/ -run BotE2E_Onboarding

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// e2eContactAs is a message carrying Telegram's contact button payload.
func e2eContactAs(userID, contactUserID int64, phone string) string {
	return fmt.Sprintf(`"message":{"message_id":%d,"date":1700000000,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"contact":{"phone_number":%q,"first_name":"Ana","user_id":%d}}`,
		time.Now().UnixNano()%100000, userID, userID, phone, contactUserID)
}

func TestBotE2E_OnboardingApplication(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
		c.OnboardingEnabled = true
		c.OnboardingSiteURL = "https://site.bot.test"
	})
	api := httptest.NewServer(srv.router)
	defer api.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const tgUser = int64(9101)
	email := fmt.Sprintf("onb-bot-%d@arena-integration.test", time.Now().UnixNano())
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM worker_jobs WHERE payload->>'email' = $1`, email)
		_, _ = pool.Exec(bg, `DELETE FROM worker_jobs WHERE job_type = 'onboarding.notify' AND payload->>'application_id' IN (SELECT id::text FROM onboarding_applications WHERE applicant_email = $1)`, email)
		_, _ = pool.Exec(bg, `DELETE FROM onboarding_applications WHERE applicant_email = $1`, email)
	})

	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	tg := newStubTelegram(t)
	bot, err := eventbot.New(eventbot.Options{
		Token:             "123:test-token",
		Queries:           gen.New(pool),
		Arena:             eventbot.NewArenaClient(api.URL, botTestServiceToken, api.Client()),
		Minter:            eventbot.NewTokenMinter(botTestJWTSecret, botTestJWTIssuer, botTestJWTAudience),
		Texts:             eventbot.NewTexts(bundle),
		TelegramServerURL: tg.srv.URL,
		HTTPClient:        tg.srv.Client(),
		SelfOnboarding:    true,
		NoticeEvery:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("eventbot.New: %v", err)
	}
	go func() { _ = bot.Run(ctx) }()

	say := func(update, needle string) string {
		t.Helper()
		m := tg.mark()
		tg.push(update)
		return tg.waitSince(t, m, needle)
	}

	// 1. A stranger sees the way to apply, not "you need an invitation".
	say(e2eMessageAs(tgUser, "/start"), "подайте заявку")
	say(e2eCallbackAs(tgUser, "onb:start"), "Как вас зовут")
	say(e2eMessageAs(tgUser, "Ана"), "фамилия")
	say(e2eMessageAs(tgUser, "Перес"), "Поделитесь номером")
	// Somebody else's contact is refused; text instead of the button too.
	say(e2eContactAs(tgUser, tgUser+1, "34600999999"), "не ваш контакт")
	say(e2eMessageAs(tgUser, "+34 600 111 222"), "Поделитесь номером")
	say(e2eContactAs(tgUser, tgUser, "34600111222"), "Ваш e-mail")
	say(e2eMessageAs(tgUser, "not-an-email"), "не похоже")
	say(e2eMessageAs(tgUser, email), "6-значный код")

	var appID, phone, source string
	if err := pool.QueryRow(ctx, `SELECT id::text, applicant_phone, source FROM onboarding_applications WHERE applicant_email = $1`, email).Scan(&appID, &phone, &source); err != nil {
		t.Fatalf("application row: %v", err)
	}
	if phone != "+34600111222" || source != "telegram" {
		t.Fatalf("stored phone %q source %q", phone, source)
	}

	// The form's long, factual middle is filled through the same service-token
	// API the bot uses, so the dialog under test keeps to what it asks itself.
	prefill := `{"telegram_user_id":9101,"answers":{"legal_name":"Bot Events SL","tax_id":"ESB1234567","tax_id_scheme":"vat",
"address_line1":"Calle 1","address_postal_code":"28001","address_city":"Madrid","events_per_year":"1-5",
"tickets_per_year":"<500","payment_provider":"stripe"}}`
	if code, body := onbDo(t, api.Client(), http.MethodPut, api.URL+"/v1/bot/onboarding/applications/"+appID+"/answers", botTestServiceToken, prefill, nil); code != http.StatusOK {
		t.Fatalf("prefill: %d %s", code, body)
	}

	// 2. The e-mailed code: a wrong one is refused, the real one confirms.
	var code string
	if err := pool.QueryRow(ctx, `SELECT payload->>'code' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 ORDER BY created_at DESC LIMIT 1`, email).Scan(&code); err != nil || len(code) != 6 {
		t.Fatalf("queued code %q: %v", code, err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	say(e2eMessageAs(tgUser, wrong), "Код неверный")
	say(e2eMessageAs(tgUser, code), "готово на")

	// 3. Typed and button answers, one question at a time.
	say(e2eMessageAs(tgUser, "Bot Events"), "Страна")
	say(e2eCallbackAs(tgUser, "onb:c:country:ES"), "Какие мероприятия")
	// A toggle only edits the buttons in place; "Done" saves the choice.
	// Wait for the markup edit, or "Done" can overtake the toggle on a slow runner.
	edits := tg.callCount("editMessageReplyMarkup")
	tg.push(e2eCallbackAs(tgUser, "onb:m:event_types:0"))
	tg.waitCall(t, "editMessageReplyMarkup", edits)
	say(e2eCallbackAs(tgUser, "onb:md:event_types"), "Рассадка")
	say(e2eCallbackAs(tgUser, "onb:s:seating:0"), "Последний шаг")
	say(e2eCallbackAs(tgUser, "onb:consent"), "Проверьте заявку")

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM onboarding_applications WHERE id = $1::uuid`, appID).Scan(&status); err != nil || status != "draft" {
		t.Fatalf("before submit: status %q err %v", status, err)
	}
	say(e2eCallbackAs(tgUser, "onb:submit"), "Заявка отправлена")
	if err := pool.QueryRow(ctx, `SELECT status FROM onboarding_applications WHERE id = $1::uuid`, appID).Scan(&status); err != nil || status != "pending_approval" {
		t.Fatalf("after submit: status %q err %v", status, err)
	}

	// 4. Asking again shows the waiting screen, never a second application.
	say(e2eMessageAs(tgUser, "привет"), "рассматривается")
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM onboarding_applications WHERE applicant_email = $1`, email).Scan(&n)
	if n != 1 {
		t.Fatalf("applications for the applicant: %d", n)
	}

	// 5. The decision reaches the chat once.
	m := tg.mark()
	if _, err := pool.Exec(ctx, `UPDATE onboarding_applications SET status = 'rejected', updated_at = now() WHERE id = $1::uuid`, appID); err != nil {
		t.Fatal(err)
	}
	tg.waitSince(t, m, "не можем открыть организацию")
	time.Sleep(700 * time.Millisecond)
	count := 0
	tg.mu.Lock()
	for _, s := range tg.sent[m:] {
		if strings.Contains(s, "не можем открыть организацию") {
			count++
		}
	}
	tg.mu.Unlock()
	if count != 1 {
		t.Fatalf("the rejection was announced %d times", count)
	}
}
