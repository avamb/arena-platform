//go:build integration

package httpserver

// End-to-end proof of the «Править в редакторе» button (spec 44, RT-12a): with
// BOT_MINIAPP_EDITOR_ENABLED off (the default) the "Text in the letter" screen
// and its preview are exactly what they were and no sign-in code is drawn; with
// it on, both carry the button and each one drew a one-time code of purpose
// miniapp for the person, without a "web" code or a notice.
//
// The Telegram stub records the text and the buttons' labels, not the address of
// a web_app button, so the button is read by its label and the code by the
// database. The address itself (shape, https, length) is covered by the unit
// tests of eventbot and by the API integration test.

import (
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
	e2eMiniappOffUser = 786
	e2eMiniappOnUser  = 787
	miniappLabelRU    = "Править в редакторе"
)

func TestBotE2E_MiniappButtonFollowsTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		user   int64
		enable bool
	}{
		{"flag off (default)", e2eMiniappOffUser, false},
		{"flag on", e2eMiniappOnUser, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := onboardingIntegrationPool(t)
			f := newBotInviteFixture(t, pool)
			model := newTextModel(t)
			model.html = func(user string) string { return "<p>" + textFmtWords(user) + "</p>" }
			srv := buildBotIntegrationServerCfg(t, pool, func(c *config.Config) {
				c.PosterLLMAPIKey = "sk-ant-test-stub"
				c.PosterLLMBaseURL = model.srv.URL
				c.TextFormatLLMModel = "claude-haiku-5-5"
				c.TextFormatsPerUserPerDay = 50
				c.TextFormatsPerOrgPerDay = 500
				c.EventCenterPublicURL = "https://events.example.test"
			})
			api := httptest.NewServer(srv.router)
			defer api.Close()
			ctx := context.Background()

			managerEmail := f.newEmail("mini")
			if rec := f.invite(srv, f.ownerID, fmt.Sprintf(`{"email":%q,"role":"manager","locale":"ru"}`, managerEmail)); rec.Code != http.StatusCreated {
				t.Fatalf("invite: %d %s", rec.Code, rec.Body.String())
			}
			code, _ := f.queuedCode(managerEmail)
			if rec := f.accept(srv, botTestServiceToken, fmt.Sprintf(`{"code":%q,"email":%q,"telegram_user_id":%d,"locale":"ru"}`, code, managerEmail, tc.user)); rec.Code != http.StatusOK {
				t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
			}
			var userID uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT user_id FROM bot_telegram_links WHERE telegram_user_id = $1`, tc.user).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			eventID, venueID, sessionID := uuid.New(), uuid.New(), uuid.New()
			eventName := "Миниапп-концерт " + uuid.NewString()[:6]
			for i, step := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venueID, f.orgID, "Mini Hall"}},
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
					{`DELETE FROM bot_dialogs WHERE telegram_user_id = $1`, []any{tc.user}},
					{`DELETE FROM web_login_codes WHERE user_id = $1`, []any{userID}},
					{`DELETE FROM bot_telegram_links WHERE telegram_user_id = $1`, []any{tc.user}},
					{`DELETE FROM event_email_notes WHERE event_id = $1`, []any{eventID}},
					{`DELETE FROM inventory_ledger WHERE session_id = $1`, []any{sessionID}},
					{`DELETE FROM sessions WHERE id = $1`, []any{sessionID}},
					{`DELETE FROM events WHERE id = $1`, []any{eventID}},
					{`DELETE FROM venues WHERE id = $1`, []any{venueID}},
					{`DELETE FROM audit_events WHERE metadata->>'org_id' = $1 OR actor_id = $2`, []any{f.orgID.String(), userID}},
				} {
					if _, err := pool.Exec(c, stmt.sql, stmt.args...); err != nil {
						t.Logf("miniapp e2e cleanup: %s: %v", stmt.sql, err)
					}
				}
			})

			tg := newStubTelegram(t)
			textFmtBot(t, pool, api, tg, 3*time.Second, func(o *eventbot.Options) { o.MiniappEditor = tc.enable })
			say := func(text, expect string) string {
				t.Helper()
				m := tg.mark()
				tg.push(e2eMessageAs(tc.user, text))
				return tg.waitSince(t, m, expect)
			}
			press := func(data, expect string) string {
				t.Helper()
				tg.waitButton(tc.user, data, 2*time.Second)
				m := tg.mark()
				tg.push(e2eCallbackAs(tc.user, data))
				return tg.waitSince(t, m, expect)
			}
			count := func(where string) int {
				t.Helper()
				var n int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE user_id = $1 AND `+where, userID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			hasEditButton := func() bool {
				t.Helper()
				id, _, _ := tg.lastOf(tc.user)
				for _, label := range tg.labelsOf(id) {
					if strings.Contains(label, miniappLabelRU) {
						return true
					}
				}
				return false
			}
			ids := eventID.String()

			// The "Text in the letter" screen.
			say("/events", "Идут")
			press("event:"+ids+":1", eventName)
			press("tf:n:"+ids, "Текст в письме")
			onScreen := hasEditButton()
			openAfterScreen := count("purpose = 'miniapp' AND used_at IS NULL AND revoked_at IS NULL")

			// The preview of a typed text.
			press("tf:e:"+ids, "оформлю")
			say("Парковка бесплатная", "Сохранить так?")
			onPreview := hasEditButton()
			openAfterPreview := count("purpose = 'miniapp' AND used_at IS NULL AND revoked_at IS NULL")
			total := count("purpose = 'miniapp'")

			if tc.enable {
				if !onScreen || !onPreview {
					t.Fatalf("flag on: button on the screen %v, on the preview %v", onScreen, onPreview)
				}
				// One open code at a time: the preview's code took the screen's back.
				if openAfterScreen != 1 || openAfterPreview != 1 || total != 2 {
					t.Fatalf("flag on: open codes %d then %d, %d drawn", openAfterScreen, openAfterPreview, total)
				}
			} else {
				if onScreen || onPreview {
					t.Fatalf("flag off: button on the screen %v, on the preview %v", onScreen, onPreview)
				}
				if total != 0 {
					t.Fatalf("flag off: %d codes drawn", total)
				}
			}
			// In both cases no "Open in browser" code was drawn behind the user's
			// back, the four buttons of the preview are all there and saving works.
			if n := count("purpose = 'web'"); n != 0 {
				t.Fatalf("%d web codes drawn", n)
			}
			id, _, data := tg.lastOf(tc.user)
			for _, want := range []string{"tf:s", "tf:p", "tf:a", "tf:c"} {
				found := false
				for _, d := range data {
					found = found || d == want
				}
				if !found {
					t.Errorf("the preview %d lacks the callback %s: %v", id, want, data)
				}
			}
			press("tf:s", "Готово, текст в письме обновлён")
			var note string
			if err := pool.QueryRow(ctx, `SELECT note FROM event_email_notes WHERE event_id = $1`, eventID).Scan(&note); err != nil || !strings.Contains(note, "Парковка бесплатная") {
				t.Fatalf("saved note %q (%v)", note, err)
			}
		})
	}
}
