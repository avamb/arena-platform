//go:build integration

package main

// backfill_integration_test.go - spec 43, RT-02: the backfill command on seeded
// rows. Dry run writes nothing, apply converts text rows (the base description,
// every translation, the letter note) and keeps a backup, a row that cannot be
// converted is left alone with its event, a rerun changes nothing, rollback
// puts the old values back. Every run is scoped to the test's own
// organization, so it never touches rows of anybody else.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type bfFixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *gen.Queries
	org  uuid.UUID
}

func newBFFixture(t *testing.T) *bfFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	suffix := uuid.NewString()
	org, err := q.InsertOrganization(ctx, "Backfill Org "+suffix, "backfill-"+suffix, "HU", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	f := &bfFixture{t: t, pool: pool, q: q, org: org.ID}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM richtext_backfill_backup WHERE row_id IN (SELECT id::text FROM events WHERE org_id = $1)`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM i18n_text WHERE namespace = 'event.description' AND key IN (SELECT id::text FROM events WHERE org_id = $1)`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	return f
}

// event seeds an event whose description is the raw text, flag 'text'.
func (f *bfFixture) event(desc string, translations map[string]string) uuid.UUID {
	f.t.Helper()
	ctx := context.Background()
	ev, err := f.q.InsertEvent(ctx, f.org, "Backfill Event "+uuid.NewString()[:8], &desc, "draft", "public", nil, "text")
	if err != nil {
		f.t.Fatalf("InsertEvent: %v", err)
	}
	for locale, v := range translations {
		if err := f.q.UpsertEventI18nDescription(ctx, ev.ID.String(), locale, v); err != nil {
			f.t.Fatalf("UpsertEventI18nDescription: %v", err)
		}
	}
	return ev.ID
}

func (f *bfFixture) note(ev uuid.UUID, text string) {
	f.t.Helper()
	if err := f.q.UpsertEventEmailNote(context.Background(), ev, f.org, text, "text"); err != nil {
		f.t.Fatalf("UpsertEventEmailNote: %v", err)
	}
}

func (f *bfFixture) desc(ev uuid.UUID) (string, string) {
	f.t.Helper()
	var d *string
	var format string
	if err := f.pool.QueryRow(context.Background(), `SELECT description, description_format FROM events WHERE id = $1`, ev).Scan(&d, &format); err != nil {
		f.t.Fatal(err)
	}
	if d == nil {
		return "", format
	}
	return *d, format
}

func (f *bfFixture) i18n(ev uuid.UUID, locale string) string {
	f.t.Helper()
	var v string
	if err := f.pool.QueryRow(context.Background(), `SELECT value FROM i18n_text WHERE namespace = 'event.description' AND key = $1 AND locale = $2`,
		ev.String(), locale).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *bfFixture) noteOf(ev uuid.UUID) (string, string) {
	f.t.Helper()
	var n, format string
	if err := f.pool.QueryRow(context.Background(), `SELECT note, note_format FROM event_email_notes WHERE event_id = $1`, ev).Scan(&n, &format); err != nil {
		f.t.Fatal(err)
	}
	return n, format
}

func (f *bfFixture) backups(run uuid.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM richtext_backfill_backup WHERE run_id = $1`, run).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *bfFixture) opts(apply bool) options {
	return options{Apply: apply, OrgID: f.org}
}

func TestBackfill_DryRunApplyRerunAndRollback(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()

	hebrew := "שלום‏ עולם **מודגש**" // holds an RLM (U+200F)
	if !strings.Contains(hebrew, "‏") {
		t.Fatal("the test text lost its RLM")
	}
	baseA := "**Big** night <i>x</i>\n\n- one\n- two"
	evA := f.event(baseA, map[string]string{"ru": "Привет\n\nВторой абзац", "he": hebrew})
	f.note(evA, "Come early.\n\nMap: https://example.com/map")

	// An over-limit translation keeps its whole event as it is.
	baseB := "short base"
	evB := f.event(baseB, map[string]string{"cs": strings.Repeat("w", 4500)})
	// An over-limit note is left alone.
	evBNote := f.event("", nil)
	f.note(evBNote, strings.Repeat("n", 2500))

	// A lone "- " line and a literal tag.
	evC := f.event("- Hello!\n\nUse <b>this</b>", nil)

	// Already ART: never touched.
	evD := f.event("<p>already art</p>", nil)
	if _, err := f.pool.Exec(ctx, `UPDATE events SET description_format = 'art' WHERE id = $1`, evD); err != nil {
		t.Fatal(err)
	}

	// ---- dry run: reports, writes nothing.
	dry, err := runBackfill(ctx, f.pool, f.opts(false))
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Apply || f.backups(dry.RunID) != 0 {
		t.Fatal("a dry run wrote a backup")
	}
	if d, format := f.desc(evA); d != baseA || format != "text" {
		t.Fatalf("a dry run changed event A: %q / %q", d, format)
	}
	if len(dry.Changed) == 0 {
		t.Fatal("a dry run reported no change")
	}

	// ---- apply.
	rep, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	wantA := "<p><strong>Big</strong> night &lt;i&gt;x&lt;/i&gt;</p><ul><li>one</li><li>two</li></ul>"
	if d, format := f.desc(evA); d != wantA || format != "art" {
		t.Fatalf("event A base: %q / %q, want %q", d, format, wantA)
	}
	if v := f.i18n(evA, "ru"); v != "<p>Привет</p><p>Второй абзац</p>" {
		t.Fatalf("ru = %q", v)
	}
	he := f.i18n(evA, "he")
	if !strings.Contains(he, "‏") || !strings.Contains(he, "<strong>מודגש</strong>") {
		t.Fatalf("he lost its mark or its bold: %q", he)
	}
	if n, format := f.noteOf(evA); format != "art" || !strings.Contains(n, `<a href="https://example.com/map">`) {
		t.Fatalf("note of A: %q / %q", n, format)
	}
	// Event B: nothing changed, every text of it is listed.
	if d, format := f.desc(evB); d != baseB || format != "text" {
		t.Fatalf("event B changed: %q / %q", d, format)
	}
	if v := f.i18n(evB, "cs"); v != strings.Repeat("w", 4500) {
		t.Fatal("the over-limit translation was changed")
	}
	// The over-limit note: unchanged and listed.
	if n, format := f.noteOf(evBNote); n != strings.Repeat("n", 2500) || format != "text" {
		t.Fatalf("over-limit note changed: format %q", format)
	}
	var skipB, skipSibling, skipNote bool
	for _, s := range rep.Skipped {
		switch {
		case s.ID == evB.String() && s.Locale == "cs" && s.Reason == "too_long":
			skipB = true
		case s.ID == evB.String() && s.Locale == "" && s.Reason == reasonSibling:
			skipSibling = true
		case s.ID == evBNote.String() && s.Entity == entityEmailNote && s.Reason == "too_long":
			skipNote = true
		}
	}
	if !skipB || !skipSibling || !skipNote {
		t.Fatalf("skipped list is incomplete (cs %v, base %v, note %v): %+v", skipB, skipSibling, skipNote, rep.Skipped)
	}
	// Event C is converted, and shows up in both watch lists.
	if d, _ := f.desc(evC); !strings.HasPrefix(d, "<p>- Hello!</p>") || !strings.Contains(d, "&lt;b&gt;this&lt;/b&gt;") {
		t.Fatalf("event C: %q", d)
	}
	var single, literal bool
	for _, n := range rep.SingleMarkers {
		if n.ID == evC.String() && n.Detail == "- Hello!" {
			single = true
		}
	}
	for _, n := range rep.LiteralTags {
		if n.ID == evC.String() && strings.Contains(n.Detail, "<b>") {
			literal = true
		}
	}
	if !single || !literal {
		t.Fatalf("watch lists: single marker %v, literal tag %v", single, literal)
	}
	// Event D is untouched; the backup holds what was replaced.
	if d, format := f.desc(evD); d != "<p>already art</p>" || format != "art" {
		t.Fatalf("event D: %q / %q", d, format)
	}
	// A: base + ru + he + note, C: base. Nothing else.
	if n := f.backups(rep.RunID); n != 5 {
		t.Fatalf("backup rows = %d, want 5", n)
	}
	var oldValue, oldFormat, newValue string
	if err := f.pool.QueryRow(ctx, `SELECT old_value, old_format, new_value FROM richtext_backfill_backup
		WHERE run_id = $1 AND entity = $2 AND row_id = $3 AND locale = 'he'`, rep.RunID, entityDescriptionI18n, evA.String()).
		Scan(&oldValue, &oldFormat, &newValue); err != nil {
		t.Fatal(err)
	}
	if oldValue != hebrew || oldFormat != "text" || newValue != he {
		t.Fatalf("backup of he: %q / %q / %q", oldValue, oldFormat, newValue)
	}
	md := rep.Markdown()
	for _, want := range []string{evA.String(), "locale=he", "too_long", "- Hello!", "<b>"} {
		if !strings.Contains(md, want) {
			t.Errorf("the report lacks %q", want)
		}
	}

	// ---- rerun: only 'text' rows are looked at, nothing is changed.
	again, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if len(again.Changed) != 0 || f.backups(again.RunID) != 0 {
		t.Fatalf("a rerun changed %d texts", len(again.Changed))
	}
	if d, _ := f.desc(evA); d != wantA {
		t.Fatalf("a rerun changed A again: %q", d)
	}

	// ---- rollback: a dry run writes nothing, apply restores.
	if _, err := rollbackRun(ctx, f.pool, rep.RunID, false); err != nil {
		t.Fatalf("rollback dry run: %v", err)
	}
	if d, _ := f.desc(evA); d != wantA {
		t.Fatal("a rollback dry run restored a text")
	}
	rb, err := rollbackRun(ctx, f.pool, rep.RunID, true)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if d, format := f.desc(evA); d != baseA || format != "text" {
		t.Fatalf("A base after rollback: %q / %q", d, format)
	}
	if f.i18n(evA, "he") != hebrew || f.i18n(evA, "ru") != "Привет\n\nВторой абзац" {
		t.Fatal("translations were not restored")
	}
	if n, format := f.noteOf(evA); n != "Come early.\n\nMap: https://example.com/map" || format != "text" {
		t.Fatalf("note after rollback: %q / %q", n, format)
	}
	if d, format := f.desc(evC); d != "- Hello!\n\nUse <b>this</b>" || format != "text" {
		t.Fatalf("C after rollback: %q / %q", d, format)
	}
	if d, format := f.desc(evD); d != "<p>already art</p>" || format != "art" {
		t.Fatalf("the rollback touched D: %q / %q", d, format)
	}
	if len(rb.Changed) != 5 || len(rb.Skipped) != 0 {
		t.Fatalf("rollback report: %d changed, %+v skipped", len(rb.Changed), rb.Skipped)
	}
	// Rolling back twice restores nothing more.
	rb2, err := rollbackRun(ctx, f.pool, rep.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rb2.Changed) != 0 || len(rb2.Skipped) == 0 || rb2.Skipped[0].Reason != reasonAlreadyText {
		t.Fatalf("second rollback: %+v %+v", rb2.Changed, rb2.Skipped)
	}
	if _, err := rollbackRun(ctx, f.pool, uuid.New(), true); err == nil {
		t.Fatal("an unknown run id must be an error")
	}
}

func TestBackfill_RollbackLeavesTextsWrittenSinceTheRun(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	evA := f.event("**Old** base", map[string]string{"ru": "**Старый**"})
	evB := f.event("another **base**", nil)
	f.note(evB, "note text")

	rep, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changed) != 4 {
		t.Fatalf("changed %d", len(rep.Changed))
	}
	// Somebody edits A's translation and B's note after the run.
	if _, err := f.pool.Exec(ctx, `UPDATE i18n_text SET value = '<p>edited</p>' WHERE namespace = 'event.description' AND key = $1 AND locale = 'ru'`, evA.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE event_email_notes SET note = '<p>edited note</p>' WHERE event_id = $1`, evB); err != nil {
		t.Fatal(err)
	}
	rb, err := rollbackRun(ctx, f.pool, rep.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	// A is left whole (the flag covers its translations): base stays ART too.
	if d, format := f.desc(evA); d != "<p><strong>Old</strong> base</p>" || format != "art" {
		t.Fatalf("A: %q / %q", d, format)
	}
	if f.i18n(evA, "ru") != "<p>edited</p>" {
		t.Fatal("an edited translation was overwritten")
	}
	if n, format := f.noteOf(evB); n != "<p>edited note</p>" || format != "art" {
		t.Fatalf("an edited note was overwritten: %q / %q", n, format)
	}
	// B's base was not edited and goes back.
	if d, format := f.desc(evB); d != "another **base**" || format != "text" {
		t.Fatalf("B: %q / %q", d, format)
	}
	reasons := map[string]bool{}
	for _, s := range rb.Skipped {
		reasons[s.ID+"/"+s.Reason] = true
	}
	if !reasons[evA.String()+"/"+reasonChangedSince] || !reasons[evB.String()+"/"+reasonChangedSince] {
		t.Fatalf("skipped: %+v", rb.Skipped)
	}
}

func TestBackfill_LimitAndEmptyRows(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		f.event("**row** "+uuid.NewString()[:6], nil)
	}
	emptyEv := f.event("", nil) // nothing to convert: not a candidate

	rep, err := runBackfill(ctx, f.pool, options{Apply: true, OrgID: f.org, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsScanned != 2 || len(rep.Changed) != 2 {
		t.Fatalf("limit 2: scanned %d, changed %d", rep.EventsScanned, len(rep.Changed))
	}
	rep, err = runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changed) != 1 {
		t.Fatalf("the rest: changed %d", len(rep.Changed))
	}
	if _, format := f.desc(emptyEv); format != "text" {
		t.Fatalf("an event without a text was converted: %q", format)
	}
}
