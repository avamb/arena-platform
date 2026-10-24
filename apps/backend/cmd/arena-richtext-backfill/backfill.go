package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
	// Limit bounds how many events and notes one run looks at; 0 means all.
	Limit int
	// OrgID, when set, restricts the run to the events of that organization.
	OrgID uuid.UUID
}

// changedRow is a text that was (or, in a dry run, would be) converted.
type changedRow struct {
	Entity, ID, Locale, Before, After string
}

// skippedRow is a text that was left as it is.
type skippedRow struct {
	Entity, ID, Locale, Reason, Detail string
}

// noticeRow is a text the owner should look at.
type noticeRow struct {
	Entity, ID, Locale, Detail string
}

// Report is what a run found and did.
type Report struct {
	RunID    uuid.UUID
	Apply    bool
	Rollback bool

	EventsScanned int
	NotesScanned  int

	Changed []changedRow
	Skipped []skippedRow
	// SingleMarkers lists the lines that begin with a lone "- " or "* ": a
	// block of one line is not a list, so the mark stays as text.
	SingleMarkers []noticeRow
	// LiteralTags lists the texts that hold a literal "<...>": it stays
	// visible text and is no longer a tag.
	LiteralTags []noticeRow
}

var (
	markerLineRE = regexp.MustCompile(`^\s*[-*•]\s+\S`)
	literalTagRE = regexp.MustCompile(`<[^<>\n]{1,200}>`)
)

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
	for _, l := range singleMarkerLines(it.old) {
		r.SingleMarkers = append(r.SingleMarkers, noticeRow{Entity: it.entity, ID: id, Locale: it.locale, Detail: l})
	}
	if tags := literalTags(it.old); len(tags) > 0 {
		r.LiteralTags = append(r.LiteralTags, noticeRow{Entity: it.entity, ID: id, Locale: it.locale, Detail: strings.Join(tags, " ")})
	}
}

func insertBackup(ctx context.Context, tx pgx.Tx, runID uuid.UUID, id string, it textItem) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO richtext_backfill_backup (run_id, entity, row_id, locale, old_value, old_format, new_value)
		VALUES ($1, $2, $3, $4, $5, 'text', $6)`,
		runID, it.entity, id, it.locale, it.old, it.new)
	return err
}

// runBackfill converts every text still marked 'text' (spec 43, RT-02). Each
// event (its base description and its translations together) and each note is
// one transaction; a dry run runs the same code and rolls back.
func runBackfill(ctx context.Context, pool *pgxpool.Pool, opt options) (*Report, error) {
	rep := &Report{RunID: uuid.New(), Apply: opt.Apply}
	remaining := opt.Limit

	eventIDs, err := candidateEvents(ctx, pool, opt.OrgID, opt.Limit)
	if err != nil {
		return nil, err
	}
	for _, id := range eventIDs {
		if err := inTx(ctx, pool, opt.Apply, func(tx pgx.Tx) error {
			return convertEvent(ctx, tx, rep, id, opt.Apply)
		}); err != nil {
			return rep, fmt.Errorf("event %s: %w", id, err)
		}
		rep.EventsScanned++
		remaining--
	}
	if opt.Limit > 0 && remaining <= 0 {
		return rep, nil
	}
	noteLimit := 0
	if opt.Limit > 0 {
		noteLimit = remaining
	}
	noteIDs, err := candidateNotes(ctx, pool, opt.OrgID, noteLimit)
	if err != nil {
		return rep, err
	}
	for _, id := range noteIDs {
		if err := inTx(ctx, pool, opt.Apply, func(tx pgx.Tx) error {
			return convertNote(ctx, tx, rep, id, opt.Apply)
		}); err != nil {
			return rep, fmt.Errorf("note of event %s: %w", id, err)
		}
		rep.NotesScanned++
	}
	return rep, nil
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

func candidateEvents(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, limit int) ([]uuid.UUID, error) {
	q := `SELECT e.id FROM events e
		WHERE e.description_format = 'text'
		  AND ($1::uuid IS NULL OR e.org_id = $1)
		  AND (COALESCE(e.description, '') <> ''
		       OR EXISTS (SELECT 1 FROM i18n_text t
		                  WHERE t.namespace = 'event.description' AND t.key = e.id::text AND t.value <> ''))
		ORDER BY e.id`
	return candidateIDs(ctx, pool, q, orgID, limit)
}

func candidateNotes(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, limit int) ([]uuid.UUID, error) {
	return candidateIDs(ctx, pool, `SELECT event_id FROM event_email_notes
		WHERE note_format = 'text' AND ($1::uuid IS NULL OR org_id = $1) ORDER BY event_id`, orgID, limit)
}

func candidateIDs(ctx context.Context, pool *pgxpool.Pool, q string, orgID uuid.UUID, limit int) ([]uuid.UUID, error) {
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	var org any
	if orgID != uuid.Nil {
		org = orgID
	}
	rows, err := pool.Query(ctx, q, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// convertEvent converts the base description and the translations of one
// event. The flag covers them all, so either every one of them is converted or
// none: a text that cannot be converted (too long, rejected characters) keeps
// the whole event as it is and is listed in the report.
func convertEvent(ctx context.Context, tx pgx.Tx, rep *Report, id uuid.UUID, apply bool) error {
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
		items[i].new, items[i].err = richtext.Normalize(items[i].old, richtext.FormatText, richtext.ProfileDescription)
		if items[i].err == nil && items[i].new == "" {
			items[i].err = errors.New(reasonEmptyAfter)
		}
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

	for _, it := range items {
		if apply {
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
				return fmt.Errorf("%s %s changed while the run was reading it", it.entity, idStr)
			}
		}
		rep.Changed = append(rep.Changed, changedRow{Entity: it.entity, ID: idStr, Locale: it.locale, Before: it.old, After: it.new})
	}
	if apply {
		if _, err := tx.Exec(ctx, `UPDATE events SET description_format = 'art' WHERE id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

// convertNote converts the organizer's letter text of one event.
func convertNote(ctx context.Context, tx pgx.Tx, rep *Report, id uuid.UUID, apply bool) error {
	var note string
	err := tx.QueryRow(ctx, `SELECT note FROM event_email_notes WHERE event_id = $1 AND note_format = 'text' FOR UPDATE`, id).Scan(&note)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	it := textItem{entity: entityEmailNote, old: note}
	it.new, it.err = richtext.Normalize(note, richtext.FormatText, richtext.ProfileEmailNote)
	if it.err == nil && it.new == "" {
		it.err = errors.New(reasonEmptyAfter)
	}
	idStr := id.String()
	rep.notices(idStr, it)
	if it.err != nil {
		rep.Skipped = append(rep.Skipped, skippedRow{Entity: it.entity, ID: idStr, Reason: reasonOf(it.err), Detail: it.err.Error()})
		return nil
	}
	if apply {
		if err := insertBackup(ctx, tx, rep.RunID, idStr, it); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE event_email_notes SET note = $2, note_format = 'art' WHERE event_id = $1`, id, it.new); err != nil {
			return err
		}
	}
	rep.Changed = append(rep.Changed, changedRow{Entity: it.entity, ID: idStr, Before: it.old, After: it.new})
	return nil
}

func reasonOf(err error) string {
	var re *richtext.Error
	if errors.As(err, &re) {
		return string(re.Reason)
	}
	return err.Error()
}

// backupRow is one row of richtext_backfill_backup.
type backupRow struct {
	entity, locale, oldValue, oldFormat, newValue string
}

// rollbackRun puts back what the run with that id replaced (spec 43, RT-02).
// A text that has been written since the run (it no longer equals what the run
// stored) is not touched, and neither is the rest of its event: the flag
// covers the translations, so an event is restored as a whole or not at all.
// Without apply nothing is written.
func rollbackRun(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, apply bool) (*Report, error) {
	rep := &Report{RunID: runID, Apply: apply, Rollback: true}
	eventIDs, err := backupRowIDs(ctx, pool, runID, entityDescription, entityDescriptionI18n)
	if err != nil {
		return nil, err
	}
	noteIDs, err := backupRowIDs(ctx, pool, runID, entityEmailNote)
	if err != nil {
		return nil, err
	}
	if len(eventIDs)+len(noteIDs) == 0 {
		return nil, fmt.Errorf("run %s has no backup rows", runID)
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

func backupRowIDs(ctx context.Context, pool *pgxpool.Pool, runID uuid.UUID, entities ...string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT row_id FROM richtext_backfill_backup
		WHERE run_id = $1 AND entity = ANY($2) ORDER BY row_id`, runID, entities)
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
		rep.Changed = append(rep.Changed, changedRow{Entity: entityDescription, ID: idStr, Before: baseBackup.newValue, After: baseBackup.oldValue})
	}
	for l, b := range i18nBackups {
		if apply {
			if _, err := tx.Exec(ctx, `UPDATE i18n_text SET value = $3
				WHERE namespace = 'event.description' AND key = $1 AND locale = $2`, idStr, l, b.oldValue); err != nil {
				return err
			}
		}
		rep.Changed = append(rep.Changed, changedRow{Entity: entityDescriptionI18n, ID: idStr, Locale: l, Before: b.newValue, After: b.oldValue})
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
	rep.Changed = append(rep.Changed, changedRow{Entity: entityEmailNote, ID: idStr, Before: b.newValue, After: b.oldValue})
	return nil
}
