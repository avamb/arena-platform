//go:build integration

package httpserver

// event_description_integration_test.go - spec 43, RT-02: the description of
// an event and the organizer's letter text have ONE write path,
// richtext.Normalize, and every writer stores ART with the format flag 'art'.
// REST create and patch with the old text and with art, the translations, the
// refusals, the rows that were stored as plain text before and the note.
// Everything goes through the REAL router.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (e *enEnv) descriptionOf(t *testing.T, ev uuid.UUID) (desc *string, format string) {
	t.Helper()
	err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT description, description_format FROM events WHERE id = $1`, ev).Scan(&desc, &format)
	if err != nil {
		t.Fatalf("read description of %s: %v", ev, err)
	}
	return desc, format
}

func (e *enEnv) i18nDescription(t *testing.T, ev uuid.UUID, locale string) (string, bool) {
	t.Helper()
	var v string
	err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT value FROM i18n_text WHERE namespace = 'event.description' AND key = $1 AND locale = $2`,
		ev.String(), locale).Scan(&v)
	return v, err == nil
}

func (e *enEnv) noteFormatOf(t *testing.T, ev uuid.UUID) string {
	t.Helper()
	var f string
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT note_format FROM event_email_notes WHERE event_id = $1`, ev).Scan(&f); err != nil {
		t.Fatalf("note format of %s: %v", ev, err)
	}
	return f
}

// created registers an event made through the API for cleanup and returns its id.
func (e *enEnv) created(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(decodeEvent(t, raw)["id"].(string))
	if err != nil {
		t.Fatalf("created event id: %v", err)
	}
	e.events = append(e.events, id)
	t.Cleanup(func() {
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM i18n_text WHERE key = $1`, id.String())
	})
	return id
}

func descEnv(t *testing.T) (e *enEnv, org uuid.UUID, tok string) {
	t.Helper()
	e = newENEnv(t)
	org = e.org(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, org, "org_admin")
	return e, org, e.token(t, owner)
}

func TestEventDescription_CreateTextAndArt(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	tag := uuid.NewString()[:8]

	// The old text (the default format): marks become ART.
	out := e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"Text `+tag+`","description":`+jsonString("**Big** night\n\n- one\n- two")+`,
		  "translations":{"ru":{"description":`+jsonString("Первый абзац\n\nВторой")+`},"he":{"description":`+jsonString("שלום‏ עולם")+`}}}`)
	ev := e.created(t, out)
	desc, format := e.descriptionOf(t, ev)
	if format != "art" || desc == nil || *desc != "<p><strong>Big</strong> night</p><ul><li>one</li><li>two</li></ul>" {
		t.Fatalf("stored %v / %q", desc, format)
	}
	if v, _ := e.i18nDescription(t, ev, "ru"); v != "<p>Первый абзац</p><p>Второй</p>" {
		t.Fatalf("ru = %q", v)
	}
	// The Hebrew text keeps its right-to-left mark.
	if v, ok := e.i18nDescription(t, ev, "he"); !ok || !strings.Contains(v, "‏") {
		t.Fatalf("he = %q (%v)", v, ok)
	}

	// ART is sanitized, not converted.
	out = e.expect(t, http.StatusCreated, http.MethodPost, base, tok, "",
		`{"name":"Art `+tag+`","description_format":"art","description":`+
			jsonString(`<h1>Title</h1><p onclick="x()">Hi <b>there</b> <a href="https://example.com/a">site</a></p><script>alert(1)</script>`)+
			`,"translations":{"en":{"description":`+jsonString(`<p>English <i>text</i></p>`)+`}}}`)
	ev2 := e.created(t, out)
	desc, format = e.descriptionOf(t, ev2)
	want := `<h3>Title</h3><p>Hi <strong>there</strong> <a href="https://example.com/a">site</a></p>`
	if format != "art" || desc == nil || *desc != want {
		t.Fatalf("art stored %v / %q, want %q", desc, format, want)
	}
	if v, _ := e.i18nDescription(t, ev2, "en"); v != "<p>English <em>text</em></p>" {
		t.Fatalf("en = %q", v)
	}
}

func TestEventDescription_CreateRefusals(t *testing.T) {
	e, org, tok := descEnv(t)
	base := "/v1/organizations/" + org.String() + "/events"
	tag := uuid.NewString()[:8]

	for name, c := range map[string]struct {
		body   string
		status int
		code   string
		reason string
	}{
		"too long":           {`{"name":"R1 ` + tag + `","description":` + jsonString(strings.Repeat("x", 4001)) + `}`, http.StatusUnprocessableEntity, "event.invalid_description", "too_long"},
		"control":            {`{"name":"R2 ` + tag + `","description":` + jsonString("bad\u0000byte") + `}`, http.StatusUnprocessableEntity, "event.invalid_description", "control_characters"},
		"bidi override":      {`{"name":"R3 ` + tag + `","description":` + jsonString("evil ‮ text") + `}`, http.StatusUnprocessableEntity, "event.invalid_description", "control_characters"},
		"translation":        {`{"name":"R4 ` + tag + `","description":"ok","translations":{"cs":{"description":` + jsonString(strings.Repeat("y", 4001)) + `}}}`, http.StatusUnprocessableEntity, "event.invalid_description", "too_long"},
		"unknown format":     {`{"name":"R5 ` + tag + `","description":"ok","description_format":"html"}`, http.StatusBadRequest, "event.invalid_description_format", ""},
		"art too many links": {`{"name":"R6 ` + tag + `","description_format":"art","description":` + jsonString(strings.Repeat(`<a href="https://a.test/">x</a> `, 21)) + `}`, http.StatusUnprocessableEntity, "event.invalid_description", "too_many_links"},
	} {
		raw := e.expect(t, c.status, http.MethodPost, base, tok, "", c.body)
		if sec1Code(raw) != c.code || errReason(raw) != c.reason {
			t.Errorf("%s: code %q reason %q, want %s / %q", name, sec1Code(raw), errReason(raw), c.code, c.reason)
		}
	}
	// Nothing was created by any of them.
	if n := e.count(t, `SELECT count(*) FROM events WHERE org_id = $1 AND name LIKE 'R_ '||$2`, org, tag); n != 0 {
		t.Fatalf("%d events were created by refused requests", n)
	}
}

func TestEventDescription_PatchTextAndArt(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org)
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()

	// The old text, the default format.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"description":`+jsonString("_Soft_ intro\n\n1. first\n2. second")+`}`)
	desc, format := e.descriptionOf(t, ev)
	if format != "art" || desc == nil || *desc != "<p><em>Soft</em> intro</p><ol><li>first</li><li>second</li></ol>" {
		t.Fatalf("text patch stored %v / %q", desc, format)
	}

	// ART, sanitized.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"description_format":"art","description":`+
		jsonString(`<blockquote><p>Quote</p></blockquote><p>x<iframe src="https://evil.test"></iframe></p>`)+`}`)
	desc, format = e.descriptionOf(t, ev)
	if format != "art" || desc == nil || *desc != "<blockquote><p>Quote</p></blockquote><p>x</p>" {
		t.Fatalf("art patch stored %v / %q", desc, format)
	}

	// The flag alone changes nothing: the format applies to a text in the request.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"name":"Renamed `+uuid.NewString()[:6]+`","description_format":"art"}`)
	if after, _ := e.descriptionOf(t, ev); after == nil || *after != "<blockquote><p>Quote</p></blockquote><p>x</p>" {
		t.Fatalf("a patch without description changed it to %v", after)
	}

	// A refusal is 422 with the reason and leaves the event as it was.
	raw := e.expect(t, http.StatusUnprocessableEntity, http.MethodPatch, path, tok, "",
		`{"name":"Never","description":`+jsonString(strings.Repeat("z", 4001))+`}`)
	if sec1Code(raw) != "event.invalid_description" || errReason(raw) != "too_long" {
		t.Fatalf("refusal: %q %q", sec1Code(raw), errReason(raw))
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id = $1 AND name = 'Never'`, ev); n != 0 {
		t.Fatal("a refused description renamed the event")
	}
	raw = e.expect(t, http.StatusBadRequest, http.MethodPatch, path, tok, "", `{"description":"x","description_format":"markdown"}`)
	if sec1Code(raw) != "event.invalid_description_format" {
		t.Fatalf("bad format: %q", sec1Code(raw))
	}

	// "" clears the text.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"description":""}`)
	if cleared, format := e.descriptionOf(t, ev); cleared == nil || *cleared != "" || format != "art" {
		t.Fatalf("cleared: %v / %q", cleared, format)
	}
}

// The flag of an event covers its translations: a patch that stores ART into an
// event that still holds plain texts converts the rest of its texts first.
func TestEventDescription_PatchConvertsTheLegacyTextsOfTheEvent(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org) // created by InsertEvent with the format 'text'
	ctx := context.Background()
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE events SET description = $2 WHERE id = $1`, ev, "**Old** base\n\n- a\n- b"); err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpsertEventI18nDescription(ctx, ev.String(), "ru", "**Старый** перевод"); err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpsertEventI18nDescription(ctx, ev.String(), "he", "שלום‏ עולם"); err != nil {
		t.Fatal(err)
	}
	e.events = append(e.events, ev)
	t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM i18n_text WHERE key = $1`, ev.String()) })
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()

	// Only the base is patched: the translations are converted with it.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"description_format":"art","description":`+jsonString("<p>New base</p>")+`}`)
	if desc, format := e.descriptionOf(t, ev); format != "art" || desc == nil || *desc != "<p>New base</p>" {
		t.Fatalf("base %v / %q", desc, format)
	}
	if v, _ := e.i18nDescription(t, ev, "ru"); v != "<p><strong>Старый</strong> перевод</p>" {
		t.Fatalf("ru was not converted: %q", v)
	}
	if v, _ := e.i18nDescription(t, ev, "he"); !strings.Contains(v, "<p>") || !strings.Contains(v, "‏") {
		t.Fatalf("he was not converted: %q", v)
	}

	// The other way round: only a translation is patched, the base is converted.
	ev2 := e.event(t, org)
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE events SET description = $2 WHERE id = $1`, ev2, "**Legacy** base"); err != nil {
		t.Fatal(err)
	}
	path2 := "/v1/organizations/" + org.String() + "/events/" + ev2.String()
	t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM i18n_text WHERE key = $1`, ev2.String()) })
	e.expect(t, http.StatusOK, http.MethodPatch, path2, tok, "", `{"translations":{"en":{"description":"**English** text"}}}`)
	if desc, format := e.descriptionOf(t, ev2); format != "art" || desc == nil || *desc != "<p><strong>Legacy</strong> base</p>" {
		t.Fatalf("base of ev2 %v / %q", desc, format)
	}
	if v, _ := e.i18nDescription(t, ev2, "en"); v != "<p><strong>English</strong> text</p>" {
		t.Fatalf("en = %q", v)
	}
}

// A stored text that cannot be converted keeps the whole PATCH from applying,
// so the event is never left with texts of two formats under one flag.
func TestEventDescription_PatchRefusedWhenAStoredTextCannotBeConverted(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org)
	ctx := context.Background()
	if err := e.q.UpsertEventI18nDescription(ctx, ev.String(), "cs", strings.Repeat("w", 5000)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(ctx, `DELETE FROM i18n_text WHERE key = $1`, ev.String()) })
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()

	raw := e.expect(t, http.StatusUnprocessableEntity, http.MethodPatch, path, tok, "",
		`{"name":"Half `+uuid.NewString()[:6]+`","description":"fresh"}`)
	if sec1Code(raw) != "event.invalid_description" || errReason(raw) != "too_long" || !strings.Contains(raw, `"locale":"cs"`) {
		t.Fatalf("answer: %s", raw)
	}
	desc, format := e.descriptionOf(t, ev)
	if format != "text" || (desc != nil && *desc == "fresh") {
		t.Fatalf("a refused patch changed the event: %v / %q", desc, format)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE id = $1 AND name LIKE 'Half %'`, ev); n != 0 {
		t.Fatal("the rename of a refused patch was applied")
	}
	// Overwriting the unconvertible row in the same request is fine.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "",
		`{"description":"fresh","translations":{"cs":{"description":"krátký text"}}}`)
	if v, _ := e.i18nDescription(t, ev, "cs"); v != "<p>krátký text</p>" {
		t.Fatalf("cs = %q", v)
	}
	if _, format := e.descriptionOf(t, ev); format != "art" {
		t.Fatalf("format %q", format)
	}
}

func TestEventEmailNote_WriteWithArtAndText(t *testing.T) {
	e, org, tok := descEnv(t)
	ev := e.event(t, org)
	path := "/v1/organizations/" + org.String() + "/events/" + ev.String()

	// ART: sanitized, flag 'art'.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "",
		`{"email_note_format":"art","email_note":`+jsonString(`<h3>Head</h3><p>Come <b>early</b> <a href="https://example.com/map">map</a><script>x()</script></p>`)+`}`)
	got, ok := e.noteOf(t, ev)
	want := `<p><strong>Head</strong></p><p>Come <strong>early</strong> <a href="https://example.com/map">map</a></p>`
	if !ok || got != want {
		t.Fatalf("art note %q (%v), want %q", got, ok, want)
	}
	if f := e.noteFormatOf(t, ev); f != "art" {
		t.Fatalf("note_format %q", f)
	}

	// The old text: converted, flag 'art'.
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note":`+jsonString("Doors **close** at 19:00\n\n- coat check\n- map: https://example.com/m")+`}`)
	got, _ = e.noteOf(t, ev)
	if !strings.Contains(got, "<strong>close</strong>") || !strings.Contains(got, "<ul><li>coat check</li>") ||
		!strings.Contains(got, `<a href="https://example.com/m">`) {
		t.Fatalf("text note stored as %q", got)
	}
	if f := e.noteFormatOf(t, ev); f != "art" {
		t.Fatalf("note_format %q", f)
	}

	// The room is 8000 characters of HTML, the visible limit 2000.
	long := strings.Repeat("<p>"+strings.Repeat("word ", 40)+"</p>", 9) // 1800 visible, over 1800 html
	e.expect(t, http.StatusOK, http.MethodPatch, path, tok, "", `{"email_note_format":"art","email_note":`+jsonString(long)+`}`)
	over := strings.Repeat("<p>"+strings.Repeat("word ", 100)+"</p>", 5) // 2500 visible
	raw := e.expect(t, http.StatusBadRequest, http.MethodPatch, path, tok, "", `{"email_note_format":"art","email_note":`+jsonString(over)+`}`)
	if sec1Code(raw) != "event.invalid_email_note" || errReason(raw) != "too_long" {
		t.Fatalf("over the visible limit: %q %q", sec1Code(raw), errReason(raw))
	}
	raw = e.expect(t, http.StatusBadRequest, http.MethodPatch, path, tok, "", `{"email_note_format":"rtf","email_note":"x"}`)
	if sec1Code(raw) != "event.invalid_email_note_format" {
		t.Fatalf("bad format: %q", sec1Code(raw))
	}
}
