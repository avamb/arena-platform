package hcatalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) (code string, details map[string]any) {
	t.Helper()
	var out struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out.Error.Code, out.Error.Details
}

func TestParseTextFormat(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	for in, want := range map[string]string{"": "text", "text": "text", " art ": "art", "art": "art"} {
		rec := httptest.NewRecorder()
		got, ok := parseTextFormat(rec, r, in, "description_format", "event.invalid_description_format")
		if !ok || got != want {
			t.Errorf("parseTextFormat(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("parseTextFormat(%q) answered %s", in, rec.Body.String())
		}
	}
	for _, in := range []string{"html", "ART", "markdown", "1"} {
		rec := httptest.NewRecorder()
		if _, ok := parseTextFormat(rec, r, in, "description_format", "event.invalid_description_format"); ok {
			t.Errorf("parseTextFormat(%q) accepted", in)
		}
		code, details := decodeErr(t, rec)
		if rec.Code != http.StatusBadRequest || code != "event.invalid_description_format" || details["field"] != "description_format" {
			t.Errorf("parseTextFormat(%q): %d %q %v", in, rec.Code, code, details)
		}
	}
}

func TestRequests_NewFormatFieldsDecode(t *testing.T) {
	var c createEventRequest
	if err := json.Unmarshal([]byte(`{"name":"n","description":"d","description_format":"art","translations":{"ru":{"name":"н","description":"о"}}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.DescriptionFormat != "art" || c.Translations["ru"].Description != "о" {
		t.Fatalf("create: %+v", c)
	}
	var u updateEventRequest
	if err := json.Unmarshal([]byte(`{"description":"d","description_format":"art","email_note":"n","email_note_format":"text","translations":{"he":{"description":"ד"}}}`), &u); err != nil {
		t.Fatal(err)
	}
	if u.DescriptionFormat != "art" || u.EmailNoteFormat != "text" || !u.EmailNote.Present || u.Translations["he"].Description != "ד" {
		t.Fatalf("update: %+v", u)
	}
	// Absent flags are empty, which means "text".
	var bare updateEventRequest
	if err := json.Unmarshal([]byte(`{"description":"d"}`), &bare); err != nil {
		t.Fatal(err)
	}
	if bare.DescriptionFormat != "" || bare.EmailNoteFormat != "" {
		t.Fatalf("defaults: %+v", bare)
	}
}

func TestNormalizeTranslationDescriptions(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	rec := httptest.NewRecorder()
	out, ok := normalizeTranslationDescriptions(rec, r, map[string]eventTranslation{
		"ru":  {Name: "n", Description: "**Привет**\n\n- раз\n- два"},
		"en":  {Description: "   "}, // blank: left out as before
		" he": {Description: "שלום\u200f"},
		"":    {Description: "no locale"},
	}, "text")
	if !ok {
		t.Fatalf("refused: %s", rec.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if out["ru"] != "<p><strong>Привет</strong></p><ul><li>раз</li><li>два</li></ul>" {
		t.Fatalf("ru = %q", out["ru"])
	}
	if !strings.Contains(out["he"], "\u200f") {
		t.Fatalf("he lost its direction mark: %q", out["he"])
	}

	// The same locale as ART is sanitized, not converted.
	rec = httptest.NewRecorder()
	out, ok = normalizeTranslationDescriptions(rec, r, map[string]eventTranslation{
		"en": {Description: `<p>Hi <b>there</b><script>x()</script></p>`},
	}, "art")
	if !ok || out["en"] != "<p>Hi <strong>there</strong></p>" {
		t.Fatalf("art: %v %v %s", out, ok, rec.Body.String())
	}
}

func TestNormalizeTranslationDescriptions_RefusalNamesTheLocale(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	rec := httptest.NewRecorder()
	_, ok := normalizeTranslationDescriptions(rec, r, map[string]eventTranslation{
		"cs": {Description: strings.Repeat("x", 4001)},
	}, "text")
	if ok {
		t.Fatal("an over-long translation was accepted")
	}
	code, details := decodeErr(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || code != "event.invalid_description" ||
		details["reason"] != "too_long" || details["locale"] != "cs" || details["field"] != "translations.cs.description" {
		t.Fatalf("%d %q %v", rec.Code, code, details)
	}
}

func TestWriteInvalidDescription_BaseHasNoLocale(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	writeInvalidDescription(rec, r, "description", "", richtext.ErrBadEncoding)
	code, details := decodeErr(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || code != "event.invalid_description" || details["reason"] != "bad_encoding" {
		t.Fatalf("%d %q %v", rec.Code, code, details)
	}
	if _, has := details["locale"]; has {
		t.Fatalf("base description answer names a locale: %v", details)
	}
}

func TestParseEmailNote_FormatSelectsTheReader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	note := "Come early\n\nSee https://example.com/x"
	opt := optionalString{Present: true, Value: &note}

	rec := httptest.NewRecorder()
	got, set, ok := parseEmailNote(rec, r, opt, "text")
	want := `<p>Come early</p><p>See <a href="https://example.com/x">https://example.com/x</a></p>`
	if !ok || !set || got != want {
		t.Fatalf("text: %q %v %v %s", got, set, ok, rec.Body.String())
	}

	art := `<p>Hi <i>there</i> <a href="https://example.com/">site</a></p>`
	rec = httptest.NewRecorder()
	got, set, ok = parseEmailNote(rec, r, optionalString{Present: true, Value: &art}, "art")
	if !ok || !set || got != `<p>Hi <em>there</em> <a href="https://example.com/">site</a></p>` {
		t.Fatalf("art: %q %v %v %s", got, set, ok, rec.Body.String())
	}

	// Absent keeps, null and blank clear.
	if _, set, ok := parseEmailNote(httptest.NewRecorder(), r, optionalString{}, "text"); set || !ok {
		t.Fatal("absent must keep")
	}
	if got, set, ok := parseEmailNote(httptest.NewRecorder(), r, optionalString{Present: true}, "text"); got != "" || !set || !ok {
		t.Fatal("null must clear")
	}
	blank := "  \n "
	if got, set, ok := parseEmailNote(httptest.NewRecorder(), r, optionalString{Present: true, Value: &blank}, "text"); got != "" || !set || !ok {
		t.Fatalf("blank must clear, got %q %v %v", got, set, ok)
	}
}

func TestParseEmailNote_RefusalIsA400WithTheRichtextReason(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	for in, reason := range map[string]string{
		"go javascript:alert(1)":  "scheme_not_allowed",
		strings.Repeat("x", 2001): "too_long",
		"bad\x00byte":             "control_characters",
	} {
		rec := httptest.NewRecorder()
		v := in
		if _, _, ok := parseEmailNote(rec, r, optionalString{Present: true, Value: &v}, "text"); ok {
			t.Errorf("%.20q accepted", in)
			continue
		}
		code, details := decodeErr(t, rec)
		if rec.Code != http.StatusUnprocessableEntity || code != "event.invalid_email_note" || details["reason"] != reason || details["field"] != "email_note" {
			t.Errorf("%.20q: %d %q %v", in, rec.Code, code, details)
		}
	}
}
