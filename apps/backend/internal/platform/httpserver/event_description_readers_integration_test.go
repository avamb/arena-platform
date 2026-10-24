//go:build integration

package httpserver

// event_description_readers_integration_test.go - spec 44, RT-03a: the REST
// event reads carry description_html, description_text and description_legacy
// next to the raw description, and email_note_format next to the note, for
// rows stored as ART and rows still stored as old text, and for a translated
// locale (the flag of the event covers its translations). Through the REAL
// router.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// readEvent returns the event object of GET /v1/events/{id}.
func (e *enEnv) readEvent(t *testing.T, tok string, ev uuid.UUID, query string) map[string]any {
	t.Helper()
	return decodeEvent(t, e.expect(t, http.StatusOK, http.MethodGet, "/v1/events/"+ev.String()+query, tok, "", ""))
}

func strField(t *testing.T, m map[string]any, key string) *string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("field %q is missing from %v", key, m)
	}
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("field %q = %v (%T), want a string", key, v, v)
	}
	return &s
}

func wantStr(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	got := strField(t, m, key)
	if got == nil {
		t.Errorf("%s = null, want %q", key, want)
	} else if *got != want {
		t.Errorf("%s = %q, want %q", key, *got, want)
	}
}

func TestEventRead_ArtRowCarriesHTMLTextAndLegacy(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	art := `<h3>Head</h3><p>Hello <strong>there</strong> <a href="https://example.com/a">site</a></p><ul><li>one</li><li>two</li></ul>`
	out := e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"Read art `+uuid.NewString()[:6]+`","description_format":"art","description":`+jsonString(art)+`,
		  "translations":{"ru":{"description":`+jsonString("<p>Привет <strong>мир</strong></p><ul><li>раз</li><li>два</li></ul>")+`}}}`)
	ev := e.created(t, out)

	// The create answer is hydrated like every other read.
	created := decodeEvent(t, out)
	wantStr(t, created, "description_format", "art")
	wantStr(t, created, "description_text", "Head\n\nHello there site (https://example.com/a)\n\n• one\n• two")

	got := e.readEvent(t, tok, ev, "")
	// description stays the raw stored value
	wantStr(t, got, "description", art)
	wantStr(t, got, "description_format", "art")
	// HTML: Render, links get rel and target
	wantStr(t, got, "description_html",
		`<h3>Head</h3><p>Hello <strong>there</strong> <a href="https://example.com/a" rel="noopener noreferrer" target="_blank">site</a></p><ul><li>one</li><li>two</li></ul>`)
	wantStr(t, got, "description_text", "Head\n\nHello there site (https://example.com/a)\n\n• one\n• two")
	wantStr(t, got, "description_legacy", "Head\n\nHello **there** site (https://example.com/a)\n\n- one\n- two")

	// A translated locale reads the translation with the event's flag.
	ru := e.readEvent(t, tok, ev, "?lang=ru")
	wantStr(t, ru, "description", "<p>Привет <strong>мир</strong></p><ul><li>раз</li><li>два</li></ul>")
	wantStr(t, ru, "description_html", "<p>Привет <strong>мир</strong></p><ul><li>раз</li><li>два</li></ul>")
	wantStr(t, ru, "description_text", "Привет мир\n\n• раз\n• два")
	wantStr(t, ru, "description_legacy", "Привет **мир**\n\n- раз\n- два")

	// The list of the organization carries the same fields.
	var list struct {
		Events []map[string]any `json:"events"`
	}
	raw := e.expect(t, http.StatusOK, http.MethodGet, base+"?lang=ru", tok, "", "")
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.Events {
		if item["id"] == ev.String() {
			found = true
			wantStr(t, item, "description_html", "<p>Привет <strong>мир</strong></p><ul><li>раз</li><li>два</li></ul>")
			wantStr(t, item, "description_legacy", "Привет **мир**\n\n- раз\n- два")
		}
	}
	if !found {
		t.Fatalf("event %s is not in the list %s", ev, raw)
	}
}

func TestEventRead_TextRowIsConvertedOnTheFlyAndKeepsItsLegacyText(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org) // format 'text'
	ctx := context.Background()
	old := "**Bold** night <b>x</b>\n\n- one\n- two"
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE events SET description = $2 WHERE id = $1`, ev, old); err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpsertEventI18nDescription(ctx, ev.String(), "ru", "**Жирный** текст"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM i18n_text WHERE key = $1`, ev.String()) })

	got := e.readEvent(t, tok, ev, "")
	wantStr(t, got, "description", old) // raw, as stored
	wantStr(t, got, "description_format", "text")
	// "**Bold**" becomes <strong>, a literal <b> stays text
	wantStr(t, got, "description_html", "<p><strong>Bold</strong> night &lt;b&gt;x&lt;/b&gt;</p><ul><li>one</li><li>two</li></ul>")
	wantStr(t, got, "description_text", "Bold night <b>x</b>\n\n• one\n• two")
	wantStr(t, got, "description_legacy", old) // a text row: the stored text itself

	ru := e.readEvent(t, tok, ev, "?lang=ru")
	wantStr(t, ru, "description", "**Жирный** текст")
	wantStr(t, ru, "description_html", "<p><strong>Жирный</strong> текст</p>")
	wantStr(t, ru, "description_legacy", "**Жирный** текст")
}

func TestEventRead_NoDescriptionAndNoteFormat(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org)

	got := e.readEvent(t, tok, ev, "")
	for _, k := range []string{"description", "description_html", "description_text", "description_legacy", "email_note", "email_note_format"} {
		if strField(t, got, k) != nil {
			t.Errorf("%s should be null for an event with no description and no note, got %v", k, got[k])
		}
	}

	// The note comes back with its format.
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note_format":"art","email_note":`+jsonString(`<p>Come <strong>early</strong></p>`)+`}`)
	got = e.readEvent(t, tok, ev, "")
	wantStr(t, got, "email_note", "<p>Come <strong>early</strong></p>")
	wantStr(t, got, "email_note_format", "art")

	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note":"Plain **note**"}`)
	got = e.readEvent(t, tok, ev, "")
	// a text note is stored as ART by the write path (RT-02), so the flag says art
	wantStr(t, got, "email_note_format", "art")
	// the old text row of the note, written before the migration, says text
	if _, err := e.srv.pgxPool.Exec(context.Background(), `UPDATE event_email_notes SET note = 'Old **note**', note_format = 'text' WHERE event_id = $1`, ev); err != nil {
		t.Fatal(err)
	}
	got = e.readEvent(t, tok, ev, "")
	wantStr(t, got, "email_note", "Old **note**")
	wantStr(t, got, "email_note_format", "text")
}
