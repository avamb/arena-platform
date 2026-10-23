-- event_email_notes.sql — EC-31 (task 7-1): the organizer's own text of an
-- event for the ticket and session-change letters. Hand-maintained wrappers in
-- gen/event_email_notes.sql.go. A row exists only while the event has a note.

-- name: GetEventEmailNote :one
SELECT note
FROM   event_email_notes
WHERE  event_id = $1
  AND  org_id   = $2;

-- name: ListEventEmailNotes :many
SELECT event_id, note
FROM   event_email_notes
WHERE  event_id = ANY($1::uuid[]);

-- name: UpsertEventEmailNote :exec
INSERT INTO event_email_notes (event_id, org_id, note)
VALUES ($1, $2, $3)
ON CONFLICT (event_id) DO UPDATE
   SET note = EXCLUDED.note, updated_at = now()
 WHERE event_email_notes.org_id = EXCLUDED.org_id;

-- name: DeleteEventEmailNote :exec
DELETE FROM event_email_notes
WHERE  event_id = $1
  AND  org_id   = $2;
