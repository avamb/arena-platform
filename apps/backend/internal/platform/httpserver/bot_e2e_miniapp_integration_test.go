//go:build integration

package httpserver

// End-to-end proof of the «Править в редакторе» button (spec 44, RT-12a) and
// of its LAZY code: with BOT_MINIAPP_EDITOR_ENABLED off (the default) the
// "Text in the letter" screen and its preview are exactly what they were, no
// code is drawn and a stray press draws none; with it on, both screens carry an
// ordinary callback button and NO code exists until it is pressed. A press
// draws exactly one one-time code of purpose miniapp (a later press takes the
// earlier open one back, a "web" code or a notice is never made) and turns that
// button of that message into a web_app button with the fresh link.
//
// The shape of the address (https, length) is covered by the unit tests of
// eventbot and by the API integration test; here the stub records the address
// of the web_app button and the test checks its prefix and its fragment.

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
			// onLast reports whether the newest message of the chat carries a
			// button with this callback data.
			onLast := func(data string) bool {
				t.Helper()
				_, _, got := tg.lastOf(tc.user)
				for _, d := range got {
					if d == data {
						return true
					}
				}
				return false
			}
			ids := eventID.String()
			openCodes := "purpose = 'miniapp' AND used_at IS NULL AND revoked_at IS NULL"

			// The "Text in the letter" screen: the button is drawn, no code is.
			say("/events", "Идут")
			press("event:"+ids+":1", eventName)
			press("tf:n:"+ids, "Текст в письме")
			screenID, _, _ := tg.lastOf(tc.user)
			onScreen := onLast("tf:m:" + ids)
			if tc.enable {
				if !onScreen {
					t.Fatalf("flag on: no button on the letter screen")
				}
				if n := count("purpose = 'miniapp'"); n != 0 {
					t.Fatalf("flag on: %d codes before any press", n)
				}
				// The press draws exactly one code and turns the button of THIS
				// message into a web_app button with the fresh link.
				press("tf:m:"+ids, "Ссылка действует 5 минут")
				if n := count("purpose = 'miniapp'"); n != 1 {
					t.Fatalf("flag on: %d codes after one press, want 1", n)
				}
				if n := count(openCodes); n != 1 {
					t.Fatalf("flag on: %d open codes after one press, want 1", n)
				}
				apps := tg.webAppsOf(screenID)
				prefix := "https://events.example.test/tg/edit#code="
				if len(apps) != 1 || !strings.HasPrefix(apps[0], prefix) || !strings.HasSuffix(apps[0], "&kind=email_note&event="+ids) {
					t.Fatalf("web_app buttons of the letter screen: %v", apps)
				}
				if hasPrefixData(tg.keyboardOf(screenID), "tf:m") {
					t.Fatalf("the callback button stayed next to the web_app one: %v", tg.keyboardOf(screenID))
				}
				if !hasPrefixData(tg.keyboardOf(screenID), "tf:e:") {
					t.Fatalf("the other buttons of the screen were lost: %v", tg.keyboardOf(screenID))
				}
			} else {
				if onScreen {
					t.Fatalf("flag off: a button on the letter screen")
				}
				for _, label := range tg.labelsOf(screenID) {
					if strings.Contains(label, miniappLabelRU) {
						t.Fatalf("flag off: the label %q is on the letter screen", label)
					}
				}
			}

			// The preview of a typed text: again a button and no new code.
			press("tf:e:"+ids, "оформлю")
			say("Парковка бесплатная", "Сохранить так?")
			previewID, _, _ := tg.lastOf(tc.user)
			if tc.enable {
				if !onLast("tf:m:" + ids) {
					t.Fatalf("flag on: no button on the preview")
				}
				if n := count("purpose = 'miniapp'"); n != 1 {
					t.Fatalf("flag on: drawing the preview wrote a code (%d in all, want 1)", n)
				}
				// A press later draws a new code and takes the old one back.
				press("tf:m:"+ids, "Ссылка действует 5 минут")
				if n := count("purpose = 'miniapp'"); n != 2 {
					t.Fatalf("flag on: %d codes after the second press, want 2", n)
				}
				if n := count(openCodes); n != 1 {
					t.Fatalf("flag on: %d open codes after the second press, want 1", n)
				}
				apps := tg.webAppsOf(previewID)
				if len(apps) != 1 || !strings.HasSuffix(apps[0], "&kind=email_note&event="+ids) {
					t.Fatalf("web_app buttons of the preview: %v", apps)
				}
			} else {
				if onLast("tf:m:" + ids) {
					t.Fatalf("flag off: a button on the preview")
				}
				// A press that still arrives (an old screen) draws nothing.
				m := tg.mark()
				tg.push(e2eCallbackAs(tc.user, "tf:m:"+ids))
				tg.waitSince(t, m, "открыть редактор")
				if n := count("purpose = 'miniapp'"); n != 0 {
					t.Fatalf("flag off: a press drew %d codes", n)
				}
			}
			// In both cases no "Open in browser" code was drawn behind the user's
			// back, the four buttons of the preview are all there and saving works.
			if n := count("purpose = 'web'"); n != 0 {
				t.Fatalf("%d web codes drawn", n)
			}
			id, _, data := tg.lastOf(tc.user)
			if !tc.enable {
				// the made-up press above answered with a plain line: the preview is the one before it
				id, data = previewID, tg.keyboardOf(previewID)
			}
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
