// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: event_email_notes.sql
//
// The organizer's own text of an event for the buyer letters (migration 0165,
// EC-31). The text is filtered by richtext.Normalize (eventtext.NormalizeNote)
// before it gets here and stored as ART with note_format (migration 0168); a
// row exists only while the event has a note.

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getEventEmailNote = `-- name: GetEventEmailNote :one
SELECT note
FROM   event_email_notes
WHERE  event_id = $1
  AND  org_id   = $2`

// GetEventEmailNote returns the event's note. pgx.ErrNoRows means the event
// has none (or belongs to another organization).
func (q *Queries) GetEventEmailNote(ctx context.Context, eventID, orgID uuid.UUID) (string, error) {
	var note string
	err := q.db.QueryRow(ctx, getEventEmailNote, eventID, orgID).Scan(&note)
	return note, err
}

const listEventEmailNotes = `-- name: ListEventEmailNotes :many
SELECT event_id, note, note_format
FROM   event_email_notes
WHERE  event_id = ANY($1::uuid[])`

// EventEmailNoteRow is a note with the flag that says how it is stored.
type EventEmailNoteRow struct {
	Note string
	// Format is "art" or "text" (note_format).
	Format string
}

// ListEventEmailNotes returns the notes of the given events with their format
// flag, keyed by event id. Events without a note are absent from the map.
func (q *Queries) ListEventEmailNotes(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]EventEmailNoteRow, error) {
	out := make(map[uuid.UUID]EventEmailNoteRow, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listEventEmailNotes, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var n EventEmailNoteRow
		if err := rows.Scan(&id, &n.Note, &n.Format); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

const upsertEventEmailNote = `-- name: UpsertEventEmailNote :exec
INSERT INTO event_email_notes (event_id, org_id, note, note_format)
VALUES ($1, $2, $3, COALESCE(NULLIF($4, ''), 'text'))
ON CONFLICT (event_id) DO UPDATE
   SET note = EXCLUDED.note, note_format = EXCLUDED.note_format, updated_at = now()
 WHERE event_email_notes.org_id = EXCLUDED.org_id`

// UpsertEventEmailNote stores the (already normalized, non-empty) note of an
// event of the organization. The composite foreign key refuses an event of
// another organization. noteFormat is "text" or "art" (empty means "text"):
// a writer that has passed the note through richtext.Normalize passes "art".
func (q *Queries) UpsertEventEmailNote(ctx context.Context, eventID, orgID uuid.UUID, note, noteFormat string) error {
	_, err := q.db.Exec(ctx, upsertEventEmailNote, eventID, orgID, note, noteFormat)
	return err
}

const deleteEventEmailNote = `-- name: DeleteEventEmailNote :exec
DELETE FROM event_email_notes
WHERE  event_id = $1
  AND  org_id   = $2`

// DeleteEventEmailNote removes the event's note; a missing one is not an error.
func (q *Queries) DeleteEventEmailNote(ctx context.Context, eventID, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteEventEmailNote, eventID, orgID)
	return err
}

const getEventEmailNoteWithFormat = `-- name: GetEventEmailNoteWithFormat :one
SELECT note, note_format
FROM   event_email_notes
WHERE  event_id = $1
  AND  org_id   = $2`

// GetEventEmailNoteWithFormat returns the event's note together with the
// format it is stored in ("art" or "text"). pgx.ErrNoRows means the event has
// none (or belongs to another organization).
func (q *Queries) GetEventEmailNoteWithFormat(ctx context.Context, eventID, orgID uuid.UUID) (note, format string, err error) {
	err = q.db.QueryRow(ctx, getEventEmailNoteWithFormat, eventID, orgID).Scan(&note, &format)
	return note, format, err
}
