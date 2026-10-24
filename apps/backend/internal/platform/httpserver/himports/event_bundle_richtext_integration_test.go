//go:build integration

package himports

// event_bundle_richtext_integration_test.go - spec 43, RT-02: the event-bundle
// writes the description and the organizer's letter text through
// richtext.Normalize. action.descriptionFormat / action.emailNoteFormat are
// "text" (default) or "art"; the stored text is ART and the flag is 'art'.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f *bundle525Fixture) storedDescription(ctx context.Context, t *testing.T, eventID uuid.UUID) (desc, format string) {
	t.Helper()
	var d *string
	if err := f.pool.QueryRow(ctx, `SELECT description, description_format FROM events WHERE id = $1`, eventID).Scan(&d, &format); err != nil {
		t.Fatalf("read description: %v", err)
	}
	if d != nil {
		desc = *d
	}
	return desc, format
}

func (f *bundle525Fixture) storedNoteFormat(ctx context.Context, t *testing.T, eventID uuid.UUID) string {
	t.Helper()
	var format string
	if err := f.pool.QueryRow(ctx, `SELECT note_format FROM event_email_notes WHERE event_id = $1`, eventID).Scan(&format); err != nil {
		t.Fatalf("read note format: %v", err)
	}
	return format
}

func TestEventBundleDescription_TextAndArt(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	// The old text (the default format) becomes ART.
	body := f.payload()
	tag := uuid.NewString()[:8]
	body.Action.Description = "**Tasting " + tag + "**\n\n- red\n- white"
	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	desc, format := f.storedDescription(ctx, t, created.EventID)
	if want := "<p><strong>Tasting " + tag + "</strong></p><ul><li>red</li><li>white</li></ul>"; format != "art" || desc != want {
		t.Fatalf("text import stored %q / %q, want %q", desc, format, want)
	}

	// ART: sanitized.
	art := f.payload()
	art.Action.DescriptionFormat = "art"
	art.Action.Description = `<h2>Evening</h2><p onclick="x()">Hi <b>there</b> <a href="https://example.com/e">site</a></p><script>alert(1)</script>`
	if rec, _ := f.call(h, art); rec.Code != http.StatusOK {
		t.Fatalf("art: %d %s", rec.Code, rec.Body.String())
	}
	desc, format = f.storedDescription(ctx, t, created.EventID)
	if want := `<h3>Evening</h3><p>Hi <strong>there</strong> <a href="https://example.com/e">site</a></p>`; format != "art" || desc != want {
		t.Fatalf("art import stored %q / %q, want %q", desc, format, want)
	}

	// No description in the bundle keeps the stored one.
	keep := f.payload()
	keep.Action.Description = ""
	if rec, _ := f.call(h, keep); rec.Code != http.StatusOK {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body.String())
	}
	if again, _ := f.storedDescription(ctx, t, created.EventID); again != desc {
		t.Fatalf("an absent description changed it to %q", again)
	}
}

func TestEventBundleDescription_RefusalsAreFoundByTheDryRunAndWriteNothing(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	first := f.payload()
	rec, created := f.call(h, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	before, _ := f.storedDescription(ctx, t, created.EventID)

	for name, c := range map[string]struct {
		description, format, field string
	}{
		"too long":       {strings.Repeat("x", 4001), "", "/action/description"},
		"control":        {"bad\x00byte", "", "/action/description"},
		"unknown format": {"x", "html", "/action/descriptionFormat"},
	} {
		bad := f.payload()
		bad.Action.Description, bad.Action.DescriptionFormat = c.description, c.format
		rec, _ := f.call(h, bad)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_description") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		dry := dryRun(t, f, h, bad)
		found := false
		for _, e := range dry.Errors {
			if e.Code == "import.invalid_description" && e.Field == c.field {
				found = true
			}
		}
		if dry.Valid || !found {
			t.Errorf("%s: dry run missed it: %+v", name, dry.Errors)
		}
	}
	if after, _ := f.storedDescription(ctx, t, created.EventID); after != before {
		t.Fatalf("a refused import changed the description to %q", after)
	}
}

// The flag of an event covers its translations (rows of i18n_text): a bundle
// that stores ART into an event that still holds plain texts converts them.
func TestEventBundleDescription_ConvertsTheLegacyTranslations(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	// Put the event back into the state before RT-02: plain texts, flag 'text'.
	if _, err := pool.Exec(ctx, `UPDATE events SET description = '**Old** text', description_format = 'text' WHERE id = $1`, created.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO i18n_text (namespace, key, locale, value) VALUES ('event.description', $1, 'ru', $2)`,
		created.EventID.String(), "**Старый** текст"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM i18n_text WHERE key = $1`, created.EventID.String())
	}()

	body := f.payload()
	body.Action.DescriptionFormat = "art"
	body.Action.Description = "<p>Fresh</p>"
	if rec, _ := f.call(h, body); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	if desc, format := f.storedDescription(ctx, t, created.EventID); desc != "<p>Fresh</p>" || format != "art" {
		t.Fatalf("base %q / %q", desc, format)
	}
	var ru string
	if err := pool.QueryRow(ctx, `SELECT value FROM i18n_text WHERE namespace = 'event.description' AND key = $1 AND locale = 'ru'`,
		created.EventID.String()).Scan(&ru); err != nil {
		t.Fatal(err)
	}
	if ru != "<p><strong>Старый</strong> текст</p>" {
		t.Fatalf("the translation was not converted: %q", ru)
	}
}

func TestEventBundleEmailNote_FormatArtAndText(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	body := f.payload()
	art := `<h3>Head</h3><p>Come <b>early</b> <a href="https://example.com/map">map</a><script>x()</script></p>`
	body.Action.EmailNote, body.Action.EmailNoteFormat = &art, "art"
	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	want := `<p><strong>Head</strong></p><p>Come <strong>early</strong> <a href="https://example.com/map">map</a></p>`
	if got, ok := f.storedNote(ctx, t, created.EventID); !ok || got != want {
		t.Fatalf("art note %q (%v), want %q", got, ok, want)
	}
	if format := f.storedNoteFormat(ctx, t, created.EventID); format != "art" {
		t.Fatalf("note_format %q", format)
	}

	// The old text, default format.
	again := f.payload()
	text := "Doors **close** at 19:00\n\n- coat check\n- map: https://example.com/m"
	again.Action.EmailNote = &text
	if rec, _ := f.call(h, again); rec.Code != http.StatusOK {
		t.Fatalf("text note: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := f.storedNote(ctx, t, created.EventID)
	if !strings.Contains(got, "<strong>close</strong>") || !strings.Contains(got, "<ul><li>coat check</li>") {
		t.Fatalf("text note stored as %q", got)
	}
	if format := f.storedNoteFormat(ctx, t, created.EventID); format != "art" {
		t.Fatalf("note_format %q", format)
	}

	// Refusals: 422 import.invalid_email_note, with the richtext reason, dry run too.
	for name, c := range map[string]struct{ note, format, field, in string }{
		"scheme":         {"x javascript:alert(1)", "", "/action/emailNote", "scheme_not_allowed"},
		"too long":       {strings.Repeat("x", 2001), "", "/action/emailNote", "too_long"},
		"unknown format": {"x", "rtf", "/action/emailNoteFormat", ""},
	} {
		bad := f.payload()
		n := c.note
		bad.Action.EmailNote, bad.Action.EmailNoteFormat = &n, c.format
		rec, _ := f.call(h, bad)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_email_note") ||
			(c.in != "" && !strings.Contains(rec.Body.String(), c.in)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		dry := dryRun(t, f, h, bad)
		found := false
		for _, e := range dry.Errors {
			if e.Code == "import.invalid_email_note" && e.Field == c.field {
				found = true
			}
		}
		if dry.Valid || !found {
			t.Errorf("%s: dry run missed it: %+v", name, dry.Errors)
		}
	}
	if got2, _ := f.storedNote(ctx, t, created.EventID); got2 != got {
		t.Fatalf("a refused import changed the note to %q", got2)
	}
}

// The export names the stored format next to the stored text, so what a client
// reads and posts back lands in the database byte for byte: text, format and
// note stay as they were, round after round (the old export gave ART without a
// format, and each round turned <p> into &lt;p&gt;).
func TestEventBundleRoundTrip_ArtTextAndFormatAreByteIdentical(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	body := f.payload()
	body.Action.DescriptionFormat = "art"
	body.Action.Description = `<h3>Evening</h3><p>Hi <strong>there</strong> &amp; <em>you</em> <a href="https://example.com/e">site</a></p><ul><li>one</li><li>two</li></ul>`
	note := `<p>Come <strong>early</strong>, bring &lt;ID&gt;.</p><p>Map: <a href="https://example.com/map">map</a></p>`
	body.Action.EmailNote, body.Action.EmailNoteFormat = &note, "art"
	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	wantDesc, wantFormat := f.storedDescription(ctx, t, created.EventID)
	wantNote, _ := f.storedNote(ctx, t, created.EventID)
	wantNoteFormat := f.storedNoteFormat(ctx, t, created.EventID)
	if wantFormat != "art" || wantNoteFormat != "art" || !strings.Contains(wantDesc, "<ul><li>one</li>") {
		t.Fatalf("setup stored %q / %q, note %q / %q", wantDesc, wantFormat, wantNote, wantNoteFormat)
	}

	for round := 1; round <= 3; round++ {
		out, raw := f.readBundles(h, created.EventID)
		a := out.Bundles[0].Action
		if a.Description != wantDesc || a.DescriptionFormat != "art" || a.EmailNote == nil || *a.EmailNote != wantNote || a.EmailNoteFormat != "art" {
			t.Fatalf("round %d: the export is not the stored text with its format: %s", round, raw)
		}
		if !strings.Contains(raw, `"descriptionFormat":"art"`) || !strings.Contains(raw, `"emailNoteFormat":"art"`) {
			t.Fatalf("round %d: the formats are not in the raw body: %s", round, raw)
		}
		if dry := dryRun(t, f, h, out.Bundles[0]); !dry.Valid {
			t.Fatalf("round %d: dry run refuses the export: %+v", round, dry.Errors)
		}
		f.reimport(h, out)
		gotDesc, gotFormat := f.storedDescription(ctx, t, created.EventID)
		gotNote, _ := f.storedNote(ctx, t, created.EventID)
		if gotDesc != wantDesc || gotFormat != wantFormat || gotNote != wantNote || f.storedNoteFormat(ctx, t, created.EventID) != wantNoteFormat {
			t.Fatalf("round %d changed the stored texts:\ndescription %q / %q\nnote %q", round, gotDesc, gotFormat, gotNote)
		}
	}

	// A legacy event (still format text) exports its text with format "text",
	// and posting it back converts it exactly as the first import would.
	legacy := "Old **text** <b>x</b>"
	if _, err := pool.Exec(ctx, `UPDATE events SET description = $2, description_format = 'text' WHERE id = $1`, created.EventID, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM i18n_text WHERE namespace = 'event.description' AND key = $1`, created.EventID.String()); err != nil {
		t.Fatal(err)
	}
	out, raw := f.readBundles(h, created.EventID)
	if a := out.Bundles[0].Action; a.Description != legacy || a.DescriptionFormat != "text" {
		t.Fatalf("legacy export: %s", raw)
	}
	f.reimport(h, out)
	got, format := f.storedDescription(ctx, t, created.EventID)
	if format != "art" || got != "<p>Old <strong>text</strong> &lt;b&gt;x&lt;/b&gt;</p>" {
		t.Fatalf("legacy re-import stored %q / %q", got, format)
	}
}
