package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func sp(s string) *string { return &s }

// The letter text of an event is shown as ART whatever format it is stored in:
// a text in the old notation is read the way the letter reads it.
func TestEmailNoteART_ReadsBothFormats(t *testing.T) {
	cases := []struct {
		name string
		ev   openapi.EventItem
		want string
	}{
		{"none", openapi.EventItem{}, ""},
		{"empty", openapi.EventItem{EmailNote: sp("")}, ""},
		{"art", openapi.EventItem{EmailNote: sp("<p>Hi <strong>you</strong></p>"), EmailNoteFormat: sp("art")}, "<p>Hi <strong>you</strong></p>"},
		{"old notation", openapi.EventItem{EmailNote: sp("Hi **you**"), EmailNoteFormat: sp("text")}, "<p>Hi <strong>you</strong></p>"},
		{"old notation, format unknown", openapi.EventItem{EmailNote: sp("Hi **you**")}, "<p>Hi <strong>you</strong></p>"},
	}
	for _, tc := range cases {
		got := emailNoteART(tc.ev)
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if got != "" {
			// What the screen prints is Telegram HTML of valid ART.
			if tg := richtext.ToTelegramHTML(got); !strings.Contains(tg, "you") {
				t.Errorf("%s: telegram html %q", tc.name, tg)
			}
		}
	}
}

// The server's refusal of a letter text is answered in the bot's words by the
// reason the server names.
func TestTfRefusalForReason_NamesTheLimitsOfTheLetterProfile(t *testing.T) {
	cases := map[string]tfRefusal{
		"too_long":           {Key: "bot.tf.err_too_long", Limit: 2000},
		"html_too_long":      {Key: "bot.tf.err_too_long", Limit: 2000},
		"too_many_links":     {Key: "bot.tf.err_links", Links: 5},
		"scheme_not_allowed": {Key: "bot.tf.err_scheme"},
		"control_characters": {Key: "bot.tf.err_chars"},
		"bad_encoding":       {Key: "bot.tf.err_chars"},
		"":                   {Key: "bot.tf.err_generic"},
		"something_new":      {Key: "bot.tf.err_generic"},
	}
	for reason, want := range cases {
		if got := tfRefusalForReason(reason, tfKindEmailNote); got != want {
			t.Errorf("%q: %+v, want %+v", reason, got, want)
		}
	}
	if got := tfRefusalForReason("too_long", tfKindDescription); got.Limit != 4000 {
		t.Errorf("description limit = %d", got.Limit)
	}
}

// Saving sends ART with its format, clearing sends null, and the test letter
// goes to the route of the event with the organizer's language.
func TestArenaClient_EmailNoteCalls(t *testing.T) {
	org, ev := uuid.New(), uuid.New()
	type seen struct {
		method, path string
		body         map[string]any
	}
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		calls = append(calls, seen{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/email-note/test") {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"queued":true,"to":"me@example.com"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	ctx := context.Background()

	if err := c.SetEmailNote(ctx, "jwt", org, ev, "<p>Hi</p>"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEmailNote(ctx, "jwt", org, ev, ""); err != nil {
		t.Fatal(err)
	}
	ans, err := c.SendEmailNoteTest(ctx, "jwt", org, ev, "ru")
	if err != nil || !ans.Queued || ans.To != "me@example.com" {
		t.Fatalf("test letter = %+v %v", ans, err)
	}
	base := "/v1/organizations/" + org.String() + "/events/" + ev.String()
	if len(calls) != 3 {
		t.Fatalf("%d calls", len(calls))
	}
	if calls[0].method != http.MethodPatch || calls[0].path != base || calls[0].body["email_note"] != "<p>Hi</p>" || calls[0].body["email_note_format"] != "art" {
		t.Errorf("save = %+v", calls[0])
	}
	if v, present := calls[1].body["email_note"]; calls[1].method != http.MethodPatch || !present || v != nil || len(calls[1].body) != 1 {
		t.Errorf("clear = %+v", calls[1])
	}
	if calls[2].method != http.MethodPost || calls[2].path != base+"/email-note/test" || calls[2].body["locale"] != "ru" {
		t.Errorf("test letter call = %+v", calls[2])
	}
}
