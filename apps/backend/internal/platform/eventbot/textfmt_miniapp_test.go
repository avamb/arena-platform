package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// miniappTestBot is a bot whose arena-api is a stub answering every
// POST /v1/bot/web-login-codes with link (or status when it is not 201). The
// returned counter says how many codes were asked for.
func miniappTestBot(t *testing.T, enabled bool, status int, link string) (*Bot, *atomic.Int32, *[]map[string]any) {
	t.Helper()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}
	var calls atomic.Int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusCreated {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"bot.web_login_superadmin","message":"no"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"url":"` + link + `","expires_at":"2026-10-25T12:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	return &Bot{
		texts:         NewTexts(bundle),
		arena:         NewArenaClient(srv.URL, "svc", srv.Client()),
		logger:        slog.Default(),
		miniappEditor: enabled,
	}, &calls, &bodies
}

func flatten(kb *models.InlineKeyboardMarkup) []models.InlineKeyboardButton {
	var out []models.InlineKeyboardButton
	for _, row := range kb.InlineKeyboard {
		out = append(out, row...)
	}
	return out
}

func webAppButtons(kb *models.InlineKeyboardMarkup) []models.InlineKeyboardButton {
	var out []models.InlineKeyboardButton
	for _, btn := range flatten(kb) {
		if btn.WebApp != nil {
			out = append(out, btn)
		}
	}
	return out
}

const miniappTestLink = "https://events.example.test/tg/edit#code=abcDEF0123456789abcDEF0123456789abcDEF01234&kind=description&event=00000000-0000-0000-0000-000000000001"

// With the flag on, the preview of an event's description carries the web_app
// button with the address arena-api made, in every language. The four old
// buttons keep their callbacks and the cancel button stays last.
func TestMiniappRow_FlagOnShowsTheWebAppButtonInEveryLanguage(t *testing.T) {
	ev := uuid.NewString()
	b, calls, bodies := miniappTestBot(t, true, http.StatusCreated, miniappTestLink)
	want := map[string]string{"en": "✏️ Edit in the editor", "ru": "✏️ Править в редакторе", "es": "✏️ Editar en el editor"}
	for _, loc := range SupportedLocales {
		row := b.miniappRow(context.Background(), 42, loc, tfKindDescription, ev)
		if len(row) != 1 || row[0].WebApp == nil || row[0].WebApp.URL != miniappTestLink || row[0].CallbackData != "" || row[0].URL != "" {
			t.Fatalf("%s: row = %+v", loc, row)
		}
		if row[0].Text != want[loc] {
			t.Errorf("%s: label %q, want %q", loc, row[0].Text, want[loc])
		}
		kb := b.tfKeyboard(loc, row)
		if got := webAppButtons(kb); len(got) != 1 {
			t.Errorf("%s: %d web_app buttons", loc, len(got))
		}
		var data []string
		for _, btn := range flatten(kb) {
			if btn.CallbackData != "" {
				data = append(data, btn.CallbackData)
				if len(btn.CallbackData) > 64 {
					t.Errorf("callback %q is longer than 64 bytes", btn.CallbackData)
				}
			}
		}
		if strings.Join(data, ",") != "tf:s,tf:p,tf:a,tf:c" {
			t.Errorf("%s: callbacks = %v", loc, data)
		}
		if last := flatten(kb); last[len(last)-1].CallbackData != "tf:c" {
			t.Errorf("%s: the cancel button is not last", loc)
		}
	}
	if int(calls.Load()) != len(SupportedLocales) {
		t.Errorf("%d code requests", calls.Load())
	}
	for _, body := range *bodies {
		if body["purpose"] != "miniapp" || body["kind"] != "description" || body["event_id"] != ev || body["telegram_user_id"] != float64(42) {
			t.Errorf("request body = %v", body)
		}
	}
}

// With the flag off (the default) nothing is asked for and nothing shows: the
// keyboard is the four buttons of before.
func TestMiniappRow_FlagOffShowsNothingAndAsksForNoCode(t *testing.T) {
	b, calls, _ := miniappTestBot(t, false, http.StatusCreated, miniappTestLink)
	if row := b.miniappRow(context.Background(), 42, "en", tfKindDescription, uuid.NewString()); row != nil {
		t.Fatalf("row with the flag off: %+v", row)
	}
	if row := b.miniappRow(context.Background(), 42, "en", tfKindEmailNote, uuid.NewString()); row != nil {
		t.Fatalf("note row with the flag off: %+v", row)
	}
	if calls.Load() != 0 {
		t.Errorf("%d code requests with the flag off", calls.Load())
	}
	kb := b.tfKeyboard("en", nil)
	if len(flatten(kb)) != 4 || len(webAppButtons(kb)) != 0 {
		t.Errorf("keyboard = %+v", kb)
	}
}

// Where a button cannot work, or the link is not one Telegram takes, there is
// no button and no error: a group chat, an event that does not exist yet, an
// unknown kind, a refusal of arena-api, an http address, a link past the URL
// limit.
func TestMiniappRow_NoButtonWhereItCannotWork(t *testing.T) {
	ctx := context.Background()
	ev := uuid.NewString()

	b, calls, _ := miniappTestBot(t, true, http.StatusCreated, miniappTestLink)
	if row := b.miniappRow(ctx, -100123, "en", tfKindDescription, ev); row != nil {
		t.Errorf("group chat: %+v", row)
	}
	if row := b.miniappRow(ctx, 42, "en", tfKindDescription, ""); row != nil {
		t.Errorf("create wizard, no event yet: %+v", row)
	}
	if row := b.miniappRow(ctx, 42, "en", tfKindDescription, "not-a-uuid"); row != nil {
		t.Errorf("bad event id: %+v", row)
	}
	if row := b.miniappRow(ctx, 42, "en", tfKind("other"), ev); row != nil {
		t.Errorf("unknown kind: %+v", row)
	}
	if calls.Load() != 0 {
		t.Errorf("%d code requests for a screen with no button", calls.Load())
	}

	refused, _, _ := miniappTestBot(t, true, http.StatusForbidden, "")
	if row := refused.miniappRow(ctx, 42, "en", tfKindEmailNote, ev); row != nil {
		t.Errorf("a superadmin got a button: %+v", row)
	}
	plain, _, _ := miniappTestBot(t, true, http.StatusCreated, "http://events.example.test/tg/edit#code=x")
	if row := plain.miniappRow(ctx, 42, "en", tfKindEmailNote, ev); row != nil {
		t.Errorf("http link: %+v", row)
	}
	long, _, _ := miniappTestBot(t, true, http.StatusCreated, "https://events.example.test/tg/edit#code="+strings.Repeat("a", tgButtonURLMax))
	if row := long.miniappRow(ctx, 42, "en", tfKindEmailNote, ev); row != nil {
		t.Errorf("link over the limit: %+v", row)
	}
}

func TestMiniappURLOK(t *testing.T) {
	if !miniappURLOK(miniappTestLink) {
		t.Error("a normal link is refused")
	}
	if len(miniappTestLink) > 200 {
		t.Errorf("a link is %d bytes, far from the URL limit expected", len(miniappTestLink))
	}
	if miniappURLOK("http://events.example.test/tg/edit") || miniappURLOK("") {
		t.Error("a non-https address is accepted")
	}
	if !miniappURLOK("https://e.test/" + strings.Repeat("a", tgButtonURLMax-len("https://e.test/"))) {
		t.Error("a link exactly at the limit is refused")
	}
	if miniappURLOK("https://e.test/" + strings.Repeat("a", tgButtonURLMax)) {
		t.Error("a link over the limit is accepted")
	}
}

// The new label exists with text in every language the bot speaks and is in
// the keys the locale test checks.
func TestMiniappKeys_InTheKeyList(t *testing.T) {
	found := false
	for _, k := range MessageKeys {
		if k == "bot.tf.edit_btn" {
			found = true
		}
	}
	if !found {
		t.Fatal("bot.tf.edit_btn is not in MessageKeys")
	}
}
