package himports

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

func importErr(t *testing.T, err error) *importError {
	t.Helper()
	var ie *importError
	if !errors.As(err, &ie) {
		t.Fatalf("want an importError, got %v", err)
	}
	return ie
}

func TestImportDescription_TextAndArt(t *testing.T) {
	got, err := importDescription(bil24compat.ImportSessionAction{Description: "**Big** night\n\n- one\n- two"})
	if err != nil || got == nil {
		t.Fatalf("text: %v %v", got, err)
	}
	if want := "<p><strong>Big</strong> night</p><ul><li>one</li><li>two</li></ul>"; *got != want {
		t.Fatalf("text: %q want %q", *got, want)
	}

	got, err = importDescription(bil24compat.ImportSessionAction{
		Description: `<h1>T</h1><p>Hi <b>x</b></p><img src=x onerror=y()>`, DescriptionFormat: "art"})
	if err != nil || got == nil {
		t.Fatalf("art: %v %v", got, err)
	}
	if want := "<h3>T</h3><p>Hi <strong>x</strong></p>"; *got != want {
		t.Fatalf("art: %q want %q", *got, want)
	}
}

func TestImportDescription_AbsentKeepsTheStoredText(t *testing.T) {
	for _, d := range []string{"", "   ", "\n"} {
		got, err := importDescription(bil24compat.ImportSessionAction{Description: d})
		if err != nil || got != nil {
			t.Errorf("%q: %v %v", d, got, err)
		}
	}
	// A tag-only text normalizes to nothing: nothing to store either.
	if got, err := importDescription(bil24compat.ImportSessionAction{Description: "<script>x</script>", DescriptionFormat: "art"}); err != nil || got != nil {
		t.Errorf("script only: %v %v", got, err)
	}
}

func TestImportDescription_Refusals(t *testing.T) {
	for name, a := range map[string]bil24compat.ImportSessionAction{
		"too long":       {Description: strings.Repeat("x", 4001)},
		"control":        {Description: "bad\x00byte"},
		"unknown format": {Description: "x", DescriptionFormat: "html"},
		"format alone":   {DescriptionFormat: "markdown"},
	} {
		_, err := importDescription(a)
		ie := importErr(t, err)
		if ie.status != http.StatusUnprocessableEntity || ie.code != "import.invalid_description" {
			t.Errorf("%s: %d %s", name, ie.status, ie.code)
		}
	}
	_, err := importDescription(bil24compat.ImportSessionAction{Description: strings.Repeat("x", 4001)})
	if ie := importErr(t, err); ie.field != "/action/description" || !strings.Contains(ie.message, "too_long") {
		t.Errorf("field %q message %q", ie.field, ie.message)
	}
}

func TestImportEmailNote_FormatAndReason(t *testing.T) {
	note := "Come early\n\nSee https://example.com/x"
	got, set, err := importEmailNote(bil24compat.ImportSessionAction{EmailNote: &note})
	if err != nil || !set {
		t.Fatalf("%v %v", set, err)
	}
	if want := `<p>Come early</p><p>See <a href="https://example.com/x">https://example.com/x</a></p>`; got != want {
		t.Fatalf("text: %q", got)
	}
	art := `<p>Hi <i>x</i></p>`
	got, set, err = importEmailNote(bil24compat.ImportSessionAction{EmailNote: &art, EmailNoteFormat: "art"})
	if err != nil || !set || got != "<p>Hi <em>x</em></p>" {
		t.Fatalf("art: %q %v %v", got, set, err)
	}
	if _, set, err := importEmailNote(bil24compat.ImportSessionAction{}); set || err != nil {
		t.Fatal("absent must keep")
	}
	empty := ""
	if got, set, err := importEmailNote(bil24compat.ImportSessionAction{EmailNote: &empty}); got != "" || !set || err != nil {
		t.Fatal(`"" must clear`)
	}

	bad := "go javascript:alert(1)"
	_, _, err = importEmailNote(bil24compat.ImportSessionAction{EmailNote: &bad})
	ie := importErr(t, err)
	if ie.code != "import.invalid_email_note" || ie.field != "/action/emailNote" || !strings.Contains(ie.message, "scheme_not_allowed") {
		t.Fatalf("%s %s %s", ie.code, ie.field, ie.message)
	}
	_, _, err = importEmailNote(bil24compat.ImportSessionAction{EmailNote: &note, EmailNoteFormat: "html"})
	if ie := importErr(t, err); ie.code != "import.invalid_email_note" || ie.field != "/action/emailNoteFormat" {
		t.Fatalf("%s %s", ie.code, ie.field)
	}
}

func TestImportAction_FormatFieldsDecodeCamelCase(t *testing.T) {
	var a bil24compat.ImportSessionAction
	raw := `{"actionName":"n","description":"d","descriptionFormat":"art","emailNote":"e","emailNoteFormat":"art"}`
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	if a.DescriptionFormat != "art" || a.EmailNoteFormat != "art" || a.EmailNote == nil {
		t.Fatalf("%+v", a)
	}
}
