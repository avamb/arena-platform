package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

// The entities of richtext_backfill_backup.
const (
	entityDescription     = "event_description"
	entityDescriptionI18n = "event_description_i18n"
	entityEmailNote       = "event_email_note"
)

// Why a row is left as it is.
const (
	reasonEmptyAfter   = "empty_after_conversion"
	reasonLiteralTags  = "literal_tags"
	reasonSibling      = "event_not_changed_because_another_text_of_it_cannot_be_converted"
	reasonConcurrent   = "changed_while_running"
	reasonChangedSince = "changed_since_the_run"
	reasonAlreadyText  = "already_text"
	reasonEventGone    = "event_no_longer_exists"
	reasonAddedSince   = "translation_added_since_the_run"
	reasonMissingNow   = "text_missing_now"
	reasonNoteGone     = "note_no_longer_exists"
	reasonBaseChanged  = "base_description_changed_since_the_run"
)

// options is what one run does.
type options struct {
	// Apply writes the changes; without it the run only reports them.
	Apply bool
	// Limit bounds how many events (each with its letter note) one run looks
	// at; 0 means all.
	Limit int
	// After is the cursor: only the events whose id is greater are looked at.
	// A run stopped by Limit prints the id to pass here next, so the next run
	// moves on past the events this one left as they are.
	After uuid.UUID
	// OrgID, when set, restricts the run to the events of that organization.
	OrgID uuid.UUID
	// HTMLAsART converts a text that holds HTML tags of the white list as ART
	// (Normalize with format art) instead of leaving it as it is.
	HTMLAsART bool
	// ShowText keeps the full before and after texts in the report. Without
	// it the report has ids, locales, lengths and reasons only.
	ShowText bool
}

// changedRow is a text that was (or, in a dry run, would be) converted.
// Before and After are kept only when the run was asked to show texts.
type changedRow struct {
	Entity, ID, Locale, Before, After string
	BeforeLen, AfterLen               int
}

// skippedRow is a text that was left as it is.
type skippedRow struct {
	Entity, ID, Locale, Reason, Detail string
}

// noticeRow is a text the owner should look at. Detail (a piece of the text)
// is kept only when the run was asked to show texts; Len is the length of the
// text it is about.
type noticeRow struct {
	Entity, ID, Locale, Detail string
	Len                        int
}

// Report is what a run found and did.
type Report struct {
	RunID    uuid.UUID
	Apply    bool
	Rollback bool
	// ShowText says that the rows carry their texts (see options.ShowText).
	ShowText bool

	EventsScanned int
	NotesScanned  int
	// NextAfter is the cursor to continue with when Limit stopped the run.
	NextAfter uuid.UUID

	Changed []changedRow
	Skipped []skippedRow
	// SingleMarkers lists the lines that begin with a lone "- " or "* ": a
	// block of one line is not a list, so the mark stays as text.
	SingleMarkers []noticeRow
	// LiteralTags lists the texts that hold a literal "<...>": it stays
	// visible text and is no longer a tag.
	LiteralTags []noticeRow

	// Purge is set by a --purge-backup-older-than run.
	Purge *purgeResult
}

// purgeResult is what a purge of old backup rows did (or, in a dry run, would do).
type purgeResult struct {
	Days int
	Rows int64
}

var (
	markerLineRE = regexp.MustCompile(`^\s*[-*•]\s+\S`)
	literalTagRE = regexp.MustCompile(`<[^<>\n]{1,200}>`)
)

// hasHTMLTags reports whether text holds a tag of the white list (and the
// block tags a site editor puts around them): a text that has one was written
// as HTML by a site, not as the old plain text with marks. It is the test the
// Bil24 gateway uses, so both read the same rows as HTML.
func hasHTMLTags(text string) bool { return richtext.LooksLikeHTML(text) }

// singleMarkerLines are the lines of text that start with "- ", "* " or "• "
// without a neighbour line that does too (two or more in a row are a list).
func singleMarkerLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if !markerLineRE.MatchString(lines[i]) {
			continue
		}
		j := i
		for j+1 < len(lines) && markerLineRE.MatchString(lines[j+1]) {
			j++
		}
		if j == i {
			out = append(out, strings.TrimSpace(lines[i]))
		}
		i = j
	}
	return out
}

// literalTags are the "<...>" pieces of text (at most 5).
func literalTags(text string) []string {
	m := literalTagRE.FindAllString(text, 5)
	return m
}

// textItem is one stored text of an event or a note.
type textItem struct {
	entity string
	locale string
	old    string
	new    string
	err    error
}

// notices records the texts of the report's two watch lists for one item.
func (r *Report) notices(id string, it textItem) {
	n := utf8.RuneCountInString(it.old)
	for _, l := range singleMarkerLines(it.old) {
		row := noticeRow{Entity: it.entity, ID: id, Locale: it.locale, Len: n}
		if r.ShowText {
			row.Detail = l
		}
		r.SingleMarkers = append(r.SingleMarkers, row)
	}
	if tags := literalTags(it.old); len(tags) > 0 {
		row := noticeRow{Entity: it.entity, ID: id, Locale: it.locale, Len: n}
		if r.ShowText {
			row.Detail = strings.Join(tags, " ")
		}
		r.LiteralTags = append(r.LiteralTags, row)
	}
}

// changed records a converted text; the texts themselves only when shown.
func (r *Report) changed(entity, id, locale, before, after string) {
	row := changedRow{Entity: entity, ID: id, Locale: locale,
		BeforeLen: utf8.RuneCountInString(before), AfterLen: utf8.RuneCountInString(after)}
	if r.ShowText {
		row.Before, row.After = before, after
	}
	r.Changed = append(r.Changed, row)
}

// merge adds the rows of one transaction's report (taken only after the
// transaction was fine) to the run's.
func (r *Report) merge(o *Report) {
	r.Changed = append(r.Changed, o.Changed...)
	r.Skipped = append(r.Skipped, o.Skipped...)
	r.SingleMarkers = append(r.SingleMarkers, o.SingleMarkers...)
	r.LiteralTags = append(r.LiteralTags, o.LiteralTags...)
}

// concurrentError says that a row changed between the run reading it and
// writing it. The transaction of that event is rolled back and the run goes on.
type concurrentError struct{ entity, locale string }

func (e *concurrentError) Error() string {
	return fmt.Sprintf("%s changed while the run was reading it", e.entity)
}

func insertBackup(ctx context.Context, tx pgx.Tx, runID uuid.UUID, id string, it textItem) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO richtext_backfill_backup (run_id, entity, row_id, locale, old_value, old_format, new_value)
		VALUES ($1, $2, $3, $4, $5, 'text', $6)`,
		runID, it.entity, id, it.locale, it.old, it.new)
	return err
}

// candidate is one event id with what there is to look at: its description
// (and translations) and/or its letter note.
type candidate struct {
	id         uuid.UUID
	desc, note bool
}

// runBackfill converts every text still marked 'text' (spec 43, RT-02). Each
// event (its base description and its translations together) and each note is
// one transaction; a dry run runs the same code and rolls back. A row that is
// not converted (a reason in the report) never stops the run, and the cursor
// (options.After, Report.NextAfter) moves past it.
func runBackfill(ctx context.Context, pool *pgxpool.Pool, opt options) (*Report, error) {
	rep := &Report{RunID: uuid.New(), Apply: opt.Apply, ShowText: opt.ShowText}

	cands, err := candidates(ctx, pool, opt)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		idStr := c.id.String()
		if c.desc {
			local := &Report{RunID: rep.RunID, ShowText: opt.ShowText}
			err := inTx(ctx, pool, opt.Apply, func(tx pgx.Tx) error { return convertEvent(ctx, tx, local, c.id, opt) })
			if err := absorb(rep, local, idStr, err); err != nil {
				return rep, fmt.Errorf("event %s: %w", c.id, err)
			}
			rep.EventsScanned++
		}
		if c.note {
			local := &Report{RunID: rep.RunID, ShowText: opt.ShowText}
			err := inTx(ctx, pool, opt.Apply, func(tx pgx.Tx) error { return convertNote(ctx, tx, local, c.id, opt) })
			if err := absorb(rep, local, idStr, err); err != nil {
				return rep, fmt.Errorf("note of event %s: %w", c.id, err)
			}
			rep.NotesScanned++
		}
	}
	if opt.Limit > 0 && len(cands) == opt.Limit {
		rep.NextAfter = cands[len(cands)-1].id
	}
	return rep, nil
}

// absorb folds the rows of one finished transaction into the run's report. A
// concurrent change is a skipped row, not a failure: nothing of that
// transaction was written. Any other error is returned.
func absorb(rep, local *Report, id string, err error) error {
	var ce *concurrentError
	switch {
	case err == nil:
		rep.merge(local)
		return nil
	case errors.As(err, &ce):
		rep.Skipped = append(rep.Skipped, skippedRow{Entity: ce.entity, ID: id, Locale: ce.locale, Reason: reasonConcurrent})
		return nil
	}
	return err
}

// inTx runs fn in a transaction that is committed when commit is true and
// rolled back otherwise (a dry run).
func inTx(ctx context.Context, pool *pgxpool.Pool, commit bool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if !commit {
		return nil
	}
	return tx.Commit(ctx)
}

// candidates lists the events that have a text still marked 'text' (a
// description or a note), in id order, after the cursor, at most Limit.
func candidates(ctx context.Context, pool *pgxpool.Pool, opt options) ([]candidate, error) {
	q := `SELECT c.id, bool_or(c.ev), bool_or(c.nt) FROM (
		SELECT e.id, true AS ev, false AS nt FROM events e
		 WHERE e.description_format = 'text'
		   AND ($1::uuid IS NULL OR e.org_id = $1)
		   AND (COALESCE(e.description, '') <> ''
		        OR EXISTS (SELECT 1 FROM i18n_text t
		                   WHERE t.namespace = 'event.description' AND t.key = e.id::text AND t.value <> ''))
		UNION ALL
		SELECT n.event_id, false, true FROM event_email_notes n
		 WHERE n.note_format = 'text' AND ($1::uuid IS NULL OR n.org_id = $1)
	) c
	WHERE $2::uuid IS NULL OR c.id > $2
	GROUP BY c.id ORDER BY c.id`
	if opt.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", opt.Limit)
	}
	var org, after any
	if opt.OrgID != uuid.Nil {
		org = opt.OrgID
	}
	if opt.After != uuid.Nil {
		after = opt.After
	}
	rows, err := pool.Query(ctx, q, org, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.desc, &c.note); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// beforeWrite is called once the texts of an event are read and converted and
// before the first is written. It does nothing; a test replaces it to change a
// row at exactly that moment.
var beforeWrite = func(uuid.UUID) {}

var errLiteralTags = errors.New(reasonLiteralTags)

// convertText is the conversion of one stored text. A text that holds HTML
// tags of the white list was written as HTML by a site (the flag says 'text'
// because the site never named a format): it is left as it is (literal_tags)
// unless the run was asked to read such texts as ART.
func convertText(old string, p richtext.Profile, htmlAsART bool) (string, error) {
	format := richtext.FormatText
	if hasHTMLTags(old) {
		if !htmlAsART {
			return "", errLiteralTags
		}
		format = richtext.FormatART
	}
	out, err := richtext.Normalize(old, format, p)
	if err == nil && out == "" {
		err = errors.New(reasonEmptyAfter)
	}
	return out, err
}

// convertEvent converts the base description and the translations of one
// event. The flag covers them all, so either every one of them is converted or
// none: a text that cannot be converted (too long, rejected characters, HTML
// the run was not asked to read) keeps the whole event as it is and is listed
// in the report.
func convertEvent(ctx context.Context, tx pgx.Tx, rep *Report, id uuid.UUID, opt options) error {
	var base *string
	err := tx.QueryRow(ctx, `SELECT description FROM events WHERE id = $1 AND description_format = 'text' FOR UPDATE`, id).Scan(&base)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // written by someone since the list was read
	}
	if err != nil {
		return err
	}
	var items []textItem
	if base != nil && *base != "" {
		items = append(items, textItem{entity: entityDescription, old: *base})
	}
	rows, err := tx.Query(ctx, `SELECT locale, value FROM i18n_text
		WHERE namespace = 'event.description' AND key = $1 AND value <> '' ORDER BY locale`, id.String())
	if err != nil {
		return err
	}
	for rows.Next() {
		it := textItem{entity: entityDescriptionI18n}
		if err := rows.Scan(&it.locale, &it.old); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	failed := false
	for i := range items {
		items[i].new, items[i].err = convertText(items[i].old, richtext.ProfileDescription, opt.HTMLAsART)
		if items[i].err != nil {
			failed = true
		}
	}
	idStr := id.String()
	for _, it := range items {
		rep.notices(idStr, it)
	}
	if failed {
		for _, it := range items {
			if it.err != nil {
				rep.Skipped = append(rep.Skipped, skippedRow{Entity: it.entity, ID: idStr, Locale: it.locale,
					Reason: reasonOf(it.err), Detail: it.err.Error()})
			} else {
				rep.Skipped = append(rep.Skipped, skippedRow{Entity: it.entity, ID: idStr, Locale: it.locale, Reason: reasonSibling})
			}
		}
		return nil
	}

	beforeWrite(id)
	for _, it := range items {
		if opt.Apply {
			if err := insertBackup(ctx, tx, rep.RunID, idStr, it); err != nil {
				return fmt.Errorf("backup: %w", err)
			}
			var tag int64
			if it.entity == entityDescription {
				ct, err := tx.Exec(ctx, `UPDATE events SET description = $2 WHERE id = $1 AND description = $3`, id, it.new, it.old)
				if err != nil {
					return err
				}
				tag = ct.RowsAffected()
			} else {
				ct, err := tx.Exec(ctx, `UPDATE i18n_text SET value = $4
					WHERE namespace = 'event.description' AND key = $1 AND locale = $2 AND value = $3`,
					idStr, it.locale, it.old, it.new)
				if err != nil {
					return err
				}
				tag = ct.RowsAffected()
			}
			if tag != 1 {
				return &concurrentError{entity: it.entity, locale: it.locale}
			}
		}
		rep.changed(it.entity, idStr, it.locale, it.old, it.new)
	}
	if opt.Apply {
		if _, err := tx.Exec(ctx, `UPDATE events SET description_format = 'art' WHERE id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

// convertNote converts the organizer's letter text of one event.
func convertNote(ctx context.Context, tx pgx.Tx, rep *Report, id uuid.UUID, opt options) error {
	var note string
	err := tx.QueryRow(ctx, `SELECT note FROM event_email_notes WHERE event_id = $1 AND note_format = 'text' FOR UPDATE`, id).Scan(&note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	it := textItem{entity: entityEmailNote, old: note}
	it.new, it.err = convertText(note, richtext.ProfileEmailNote, opt.HTMLAsART)
	idStr := id.String()
	rep.notices(idStr, it)
	if it.err != nil {
		rep.Skipped = append(rep.Skipped, skippedRow{Entity: it.entity, ID: idStr, Reason: reasonOf(it.err), Detail: it.err.Error()})
		return nil
	}
	if opt.Apply {
		if err := insertBackup(ctx, tx, rep.RunID, idStr, it); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		ct, err := tx.Exec(ctx, `UPDATE event_email_notes SET note = $2, note_format = 'art' WHERE event_id = $1 AND note = $3 AND note_format = 'text'`,
			id, it.new, it.old)
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 1 {
			return &concurrentError{entity: entityEmailNote}
		}
	}
	rep.changed(it.entity, idStr, "", it.old, it.new)
	return nil
}

func reasonOf(err error) string {
	var re *richtext.Error
	if errors.As(err, &re) {
		return string(re.Reason)
	}
	return err.Error()
}

// purgeBackups deletes the rows of richtext_backfill_backup older than days
// (by created_at). Without apply it only counts them. The backup holds the
// full old texts, which can carry personal data, so it is not kept for ever.
func purgeBackups(ctx context.Context, pool *pgxpool.Pool, days int, apply bool) (*Report, error) {
	if days <= 0 {
		return nil, fmt.Errorf("--purge-backup-older-than wants a number of days above 0")
	}
	rep := &Report{RunID: uuid.New(), Apply: apply, Purge: &purgeResult{Days: days}}
	interval := fmt.Sprintf("%d days", days)
	if apply {
		ct, err := pool.Exec(ctx, `DELETE FROM richtext_backfill_backup WHERE created_at < now() - $1::interval`, interval)
		if err != nil {
			return nil, err
		}
		rep.Purge.Rows = ct.RowsAffected()
		return rep, nil
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM richtext_backfill_backup WHERE created_at < now() - $1::interval`, interval).Scan(&rep.Purge.Rows); err != nil {
		return nil, err
	}
	return rep, nil
}

// backupRow is one row of richtext_backfill_backup.
type backupRow struct {
	entity, locale, oldValue, oldFormat, newValue string
}

// rollbackRun puts back what the run with that id replaced (spec 43, RT-02).
// A text that has been written since the run (it no longer equals what the run
// stored) is not touched, and neither is the rest of its event: the flag
// covers the translations, so an event is restored as a whole or not at all.
// orgID, when set, restores only the events (and notes) of that organization.
// Without apply nothing is written.
func rollbackRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, apply bool, orgID uuid.UUID) (*Report, error) {
	rep := &Report{RunID: runID, Apply: apply, Rollback: true}
	eventIDs, err := backupRowIDs(ctx, pool, runID, orgID, entityDescription, entityDescriptionI18n)
	if err != nil {
		return nil, err
	}
	noteIDs, err := backupRowIDs(ctx, pool, runID, orgID, entityEmailNote)
	if err != nil {
		return nil, err
	}
	if len(eventIDs)+len(noteIDs) == 0 {
		return nil, fmt.Errorf("run %s has no backup rows to restore%s", runID, orgSuffix(orgID))
	}
	for _, id := range eventIDs {
		if err := inTx(ctx, pool, apply, func(tx pgx.Tx) error { return rollbackEvent(ctx, tx, rep, id, apply) }); err != nil {
			return rep, fmt.Errorf("event %s: %w", id, err)
		}
		rep.EventsScanned++
	}
	for _, id := range noteIDs {
		if err := inTx(ctx, pool, apply, func(tx pgx.Tx) error { return rollbackNote(ctx, tx, rep, id, apply) }); err != nil {
			return rep, fmt.Errorf("note of event %s: %w", id, err)
		}
		rep.NotesScanned++
	}
	return rep, nil
}

func orgSuffix(orgID uuid.UUID) string {
	if orgID == uuid.Nil {
		return ""
	}
	return " in organization " + orgID.String()
}

// backupRowIDs lists the events the run backed up, of the organization when
// one is given. The event of a note is the note's row id too.
func backupRowIDs(ctx context.Context, pool *pgxpool.Pool, runID, orgID uuid.UUID, entities ...string) ([]string, error) {
	var org any
	if orgID != uuid.Nil {
		org = orgID
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT b.row_id FROM richtext_backfill_backup b
		WHERE b.run_id = $1 AND b.entity = ANY($2)
		  AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM events e WHERE e.id::text = b.row_id AND e.org_id = $3))
		ORDER BY b.row_id`, runID, entities, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadBackups(ctx context.Context, tx pgx.Tx, runID uuid.UUID, id string, entities ...string) ([]backupRow, error) {
	rows, err := tx.Query(ctx, `SELECT entity, locale, old_value, old_format, new_value FROM richtext_backfill_backup
		WHERE run_id = $1 AND row_id = $2 AND entity = ANY($3) ORDER BY entity, locale`, runID, id, entities)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backupRow
	for rows.Next() {
		var b backupRow
		if err := rows.Scan(&b.entity, &b.locale, &b.oldValue, &b.oldFormat, &b.newValue); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func rollbackEvent(ctx context.Context, tx pgx.Tx, rep *Report, idStr string, apply bool) error {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return err
	}
	skip := func(reason, detail string) {
		rep.Skipped = append(rep.Skipped, skippedRow{Entity: entityDescription, ID: idStr, Reason: reason, Detail: detail})
	}
	var base *string
	var format string
	err = tx.QueryRow(ctx, `SELECT description, description_format FROM events WHERE id = $1 FOR UPDATE`, id).Scan(&base, &format)
	if errors.Is(err, pgx.ErrNoRows) {
		skip(reasonEventGone, "")
		return nil
	}
	if err != nil {
		return err
	}
	if format != richtext.FormatART {
		skip(reasonAlreadyText, "")
		return nil
	}
	backups, err := loadBackups(ctx, tx, rep.RunID, idStr, entityDescription, entityDescriptionI18n)
	if err != nil {
		return err
	}
	var baseBackup *backupRow
	i18nBackups := map[string]backupRow{}
	for i := range backups {
		if backups[i].entity == entityDescription {
			baseBackup = &backups[i]
		} else {
			i18nBackups[backups[i].locale] = backups[i]
		}
	}
	// What the event holds now must be what the run stored.
	cur := ""
	if base != nil {
		cur = *base
	}
	if baseBackup != nil && cur != baseBackup.newValue {
		skip(reasonChangedSince, "base description")
		return nil
	}
	if baseBackup == nil && cur != "" {
		skip(reasonBaseChanged, "")
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT locale, value FROM i18n_text
		WHERE namespace = 'event.description' AND key = $1 AND value <> ''`, idStr)
	if err != nil {
		return err
	}
	now := map[string]string{}
	for rows.Next() {
		var l, v string
		if err := rows.Scan(&l, &v); err != nil {
			rows.Close()
			return err
		}
		now[l] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for l := range now {
		if _, ok := i18nBackups[l]; !ok {
			skip(reasonAddedSince, l)
			return nil
		}
	}
	for l, b := range i18nBackups {
		v, ok := now[l]
		if !ok {
			skip(reasonMissingNow, l)
			return nil
		}
		if v != b.newValue {
			skip(reasonChangedSince, l)
			return nil
		}
	}

	if baseBackup != nil {
		if apply {
			if _, err := tx.Exec(ctx, `UPDATE events SET description = $2 WHERE id = $1`, id, baseBackup.oldValue); err != nil {
				return err
			}
		}
		rep.changed(entityDescription, idStr, "", baseBackup.newValue, baseBackup.oldValue)
	}
	for l, b := range i18nBackups {
		if apply {
			if _, err := tx.Exec(ctx, `UPDATE i18n_text SET value = $3
				WHERE namespace = 'event.description' AND key = $1 AND locale = $2`, idStr, l, b.oldValue); err != nil {
				return err
			}
		}
		rep.changed(entityDescriptionI18n, idStr, l, b.newValue, b.oldValue)
	}
	if apply {
		if _, err := tx.Exec(ctx, `UPDATE events SET description_format = $2 WHERE id = $1`, id, richtext.FormatText); err != nil {
			return err
		}
	}
	return nil
}

func rollbackNote(ctx context.Context, tx pgx.Tx, rep *Report, idStr string, apply bool) error {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return err
	}
	skip := func(reason string) {
		rep.Skipped = append(rep.Skipped, skippedRow{Entity: entityEmailNote, ID: idStr, Reason: reason})
	}
	var note, format string
	err = tx.QueryRow(ctx, `SELECT note, note_format FROM event_email_notes WHERE event_id = $1 FOR UPDATE`, id).Scan(&note, &format)
	if errors.Is(err, pgx.ErrNoRows) {
		skip(reasonNoteGone)
		return nil
	}
	if err != nil {
		return err
	}
	backups, err := loadBackups(ctx, tx, rep.RunID, idStr, entityEmailNote)
	if err != nil {
		return err
	}
	if len(backups) != 1 {
		return fmt.Errorf("note of event %s: %d backup rows", idStr, len(backups))
	}
	b := backups[0]
	if format != richtext.FormatART {
		skip(reasonAlreadyText)
		return nil
	}
	if note != b.newValue {
		skip(reasonChangedSince)
		return nil
	}
	if apply {
		if _, err := tx.Exec(ctx, `UPDATE event_email_notes SET note = $2, note_format = $3 WHERE event_id = $1`, id, b.oldValue, b.oldFormat); err != nil {
			return err
		}
	}
	rep.changed(entityEmailNote, idStr, "", b.newValue, b.oldValue)
	return nil
}
