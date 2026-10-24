//go:build integration

package main

// backfill_safety_integration_test.go - the review fixes of the backfill
// (spec 44, RT-02): HTML the sites wrote is left alone unless asked for, the
// cursor moves past rows that are not converted, a row changed under the run
// is skipped and the run goes on, the report holds no texts by default,
// rollback honours --org, and old backup rows can be purged.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBackfill_LimitMovesOnPastRowsThatAreNotConverted(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	// Ids are time ordered: the order of creation is the order of the run.
	html1 := f.event("<p>from a site</p>", nil)
	n1 := f.event("**one** "+uuid.NewString()[:6], nil)
	html2 := f.event("<p>again <b>x</b></p>", nil)
	n2 := f.event("**two** "+uuid.NewString()[:6], nil)
	n3 := f.event("**three** "+uuid.NewString()[:6], nil)
	emptyEv := f.event("", nil) // nothing to convert: not a candidate

	var after uuid.UUID
	pages, converted, skipped := 0, 0, 0
	for {
		pages++
		if pages > 10 {
			t.Fatal("the run never reaches the end: skipped rows block the cursor")
		}
		rep, err := runBackfill(ctx, f.pool, options{Apply: true, OrgID: f.org, Limit: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		converted += len(rep.Changed)
		skipped += len(rep.Skipped)
		if rep.NextAfter == uuid.Nil {
			break
		}
		if after != uuid.Nil && rep.NextAfter.String() <= after.String() {
			t.Fatalf("the cursor did not move: %s after %s", rep.NextAfter, after)
		}
		after = rep.NextAfter
	}
	if converted != 3 || skipped != 2 {
		t.Fatalf("converted %d, skipped %d over %d pages", converted, skipped, pages)
	}
	for _, id := range []uuid.UUID{n1, n2, n3} {
		if _, format := f.desc(id); format != "art" {
			t.Errorf("event %s not converted", id)
		}
	}
	for _, id := range []uuid.UUID{html1, html2} {
		if d, format := f.desc(id); format != "text" || !strings.HasPrefix(d, "<p>") {
			t.Errorf("a site's HTML was changed: %q / %q", d, format)
		}
	}
	if _, format := f.desc(emptyEv); format != "text" {
		t.Fatalf("an event without a text was converted: %q", format)
	}

	// The first page alone, without the cursor, looks at the same two events
	// again: that is what the cursor is for.
	rep, err := runBackfill(ctx, f.pool, options{OrgID: f.org, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EventsScanned != 2 || len(rep.Skipped) == 0 || len(rep.Changed) != 0 {
		t.Fatalf("a dry page: scanned %d changed %d skipped %d", rep.EventsScanned, len(rep.Changed), len(rep.Skipped))
	}
}

func TestBackfill_HTMLTextsAreSkippedByDefaultAndConvertedAsARTOnRequest(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	const html = `<p>Concert <b>tonight</b></p><ul><li>one</li></ul>`
	ev := f.event(html, map[string]string{"ru": "Обычный **текст**"})
	f.note(ev, `<p>Doors at <a href="https://example.com/d">seven</a></p>`)

	rep, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changed) != 0 || f.backups(rep.RunID) != 0 {
		t.Fatalf("HTML was converted by default: %+v", rep.Changed)
	}
	reasons := map[string]bool{}
	for _, s := range rep.Skipped {
		reasons[s.Entity+"/"+s.Locale+"/"+s.Reason] = true
	}
	if !reasons[entityDescription+"//"+reasonLiteralTags] || !reasons[entityDescriptionI18n+"/ru/"+reasonSibling] || !reasons[entityEmailNote+"//"+reasonLiteralTags] {
		t.Fatalf("skipped: %+v", rep.Skipped)
	}
	if d, format := f.desc(ev); d != html || format != "text" {
		t.Fatalf("event changed: %q / %q", d, format)
	}
	if n, format := f.noteOf(ev); !strings.HasPrefix(n, "<p>Doors") || format != "text" {
		t.Fatalf("note changed: %q / %q", n, format)
	}

	rep, err = runBackfill(ctx, f.pool, options{Apply: true, OrgID: f.org, HTMLAsART: true, ShowText: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, format := f.desc(ev); d != `<p>Concert <strong>tonight</strong></p><ul><li>one</li></ul>` || format != "art" {
		t.Fatalf("--html-as-art stored %q / %q", d, format)
	}
	// The translation without tags is still the old text.
	if v := f.i18n(ev, "ru"); v != "<p>Обычный <strong>текст</strong></p>" {
		t.Fatalf("ru = %q", v)
	}
	if n, format := f.noteOf(ev); n != `<p>Doors at <a href="https://example.com/d">seven</a></p>` || format != "art" {
		t.Fatalf("note = %q / %q", n, format)
	}
	if n := f.backups(rep.RunID); n != 3 {
		t.Fatalf("backup rows = %d", n)
	}
	// And the run can be rolled back.
	if _, err := rollbackRun(ctx, f.pool, rep.RunID, true, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if d, format := f.desc(ev); d != html || format != "text" {
		t.Fatalf("after rollback: %q / %q", d, format)
	}
}

// A text that changed between the run reading it and writing it is a skipped
// row with its reason; nothing of that event is written and the run goes on.
func TestBackfill_ConcurrentChangeIsSkippedAndTheRunContinues(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	evA := f.event("**first** "+uuid.NewString()[:6], map[string]string{"ru": "**первый**"})
	evB := f.event("**second** "+uuid.NewString()[:6], nil)
	f.note(evB, "note **b**")

	old := beforeWrite
	defer func() { beforeWrite = old }()
	beforeWrite = func(id uuid.UUID) {
		if id != evA {
			return
		}
		// Somebody else edits A's translation after the run has read it.
		if _, err := f.pool.Exec(ctx, `UPDATE i18n_text SET value = '<p>edited meanwhile</p>' WHERE namespace = 'event.description' AND key = $1 AND locale = 'ru'`, evA.String()); err != nil {
			t.Error(err)
		}
	}
	rep, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatalf("a concurrent change aborted the run: %v", err)
	}
	var concurrent bool
	for _, s := range rep.Skipped {
		if s.ID == evA.String() && s.Reason == reasonConcurrent {
			concurrent = true
		}
	}
	if !concurrent {
		t.Fatalf("the concurrent change is not in the skipped rows: %+v", rep.Skipped)
	}
	// Event A: nothing written, neither the base nor the backup.
	if _, format := f.desc(evA); format != "text" {
		t.Fatal("event A was converted although a text of it changed")
	}
	if d, _ := f.desc(evA); !strings.HasPrefix(d, "**first**") {
		t.Fatalf("event A base was written: %q", d)
	}
	if f.i18n(evA, "ru") != "<p>edited meanwhile</p>" {
		t.Fatal("the concurrent edit was overwritten")
	}
	var backupsA int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM richtext_backfill_backup WHERE run_id = $1 AND row_id = $2`, rep.RunID, evA.String()).Scan(&backupsA); err != nil || backupsA != 0 {
		t.Fatalf("backup rows of A: %d %v", backupsA, err)
	}
	// The run went on: B and its note are converted.
	if _, format := f.desc(evB); format != "art" {
		t.Fatal("the run stopped at event A")
	}
	if _, format := f.noteOf(evB); format != "art" {
		t.Fatal("the note of the next event was not converted")
	}
	if len(rep.Changed) != 2 {
		t.Fatalf("changed %d, want 2 (B and its note)", len(rep.Changed))
	}
}

// By default the report holds ids, locales, lengths and reasons, never the
// text of a note.
func TestBackfill_ReportHasNoTextsByDefault(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	secret := "Call 777 123 456 or mail boss@example.com"
	ev := f.event("- "+secret, nil)
	f.note(ev, secret+"\n\n- lone line with 555 000 111")

	rep, err := runBackfill(ctx, f.pool, options{Apply: false, OrgID: f.org})
	if err != nil {
		t.Fatal(err)
	}
	md := rep.Markdown()
	for _, bad := range []string{"777 123 456", "boss@example.com", "555 000 111"} {
		if strings.Contains(md, bad) {
			t.Fatalf("the default report holds %q:\n%s", bad, md)
		}
	}
	if !strings.Contains(md, ev.String()) || !strings.Contains(md, "characters") {
		t.Fatalf("the default report lacks ids and lengths:\n%s", md)
	}
	shown, err := runBackfill(ctx, f.pool, options{Apply: false, OrgID: f.org, ShowText: true})
	if err != nil {
		t.Fatal(err)
	}
	if md := shown.Markdown(); !strings.Contains(md, "777 123 456") {
		t.Fatalf("--show-text report lacks the text:\n%s", md)
	}
}

func TestBackfill_RollbackHonoursTheOrganization(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	ev := f.event("**old** text", nil)
	f.note(ev, "note **n**")
	rep, err := runBackfill(ctx, f.pool, f.opts(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changed) != 2 {
		t.Fatalf("changed %d", len(rep.Changed))
	}
	converted, _ := f.desc(ev)

	// Another organization's id: nothing of this run belongs to it.
	if _, err := rollbackRun(ctx, f.pool, rep.RunID, true, uuid.New()); err == nil {
		t.Fatal("a rollback scoped to another organization must find nothing")
	}
	if d, format := f.desc(ev); d != converted || format != "art" {
		t.Fatalf("a rollback of another organization restored the text: %q / %q", d, format)
	}
	// Its own organization restores it.
	rb, err := rollbackRun(ctx, f.pool, rep.RunID, true, f.org)
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.Changed) != 2 {
		t.Fatalf("restored %d", len(rb.Changed))
	}
	if d, format := f.desc(ev); d != "**old** text" || format != "text" {
		t.Fatalf("after rollback: %q / %q", d, format)
	}
}

func TestBackfill_PurgeDeletesOldBackupRowsOnly(t *testing.T) {
	f := newBFFixture(t)
	ctx := context.Background()
	ev := f.event("x", nil)
	run := uuid.New()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM richtext_backfill_backup WHERE run_id = $1`, run)
	})
	insert := func(locale string, age time.Duration) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `INSERT INTO richtext_backfill_backup (run_id, entity, row_id, locale, old_value, old_format, new_value, created_at)
			VALUES ($1, $2, $3, $4, 'old', 'text', 'new', now() - $5::interval)`,
			run, entityDescriptionI18n, ev.String(), locale, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
			t.Fatal(err)
		}
	}
	insert("a", 90*24*time.Hour)
	insert("b", 45*24*time.Hour)
	insert("c", 2*24*time.Hour)

	if _, err := purgeBackups(ctx, f.pool, 0, true); err == nil {
		t.Fatal("0 days must be refused")
	}
	dry, err := purgeBackups(ctx, f.pool, 30, false)
	if err != nil {
		t.Fatal(err)
	}
	// The scratch database may hold other runs' old rows: at least ours.
	if dry.Purge.Rows < 2 || f.backups(run) != 3 {
		t.Fatalf("dry run: would delete %d, rows left %d", dry.Purge.Rows, f.backups(run))
	}
	done, err := purgeBackups(ctx, f.pool, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Purge.Rows < 2 {
		t.Fatalf("deleted %d", done.Purge.Rows)
	}
	if n := f.backups(run); n != 1 {
		t.Fatalf("rows of the run left = %d, want only the recent one", n)
	}
	var locale string
	if err := f.pool.QueryRow(ctx, `SELECT locale FROM richtext_backfill_backup WHERE run_id = $1`, run).Scan(&locale); err != nil || locale != "c" {
		t.Fatalf("the wrong row was kept: %q %v", locale, err)
	}
}
