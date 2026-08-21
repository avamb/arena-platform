// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_media.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// SessionMediaItemRow — shared result type for session_media_items queries
// ─────────────────────────────────────────────────────────────────────────────

// SessionMediaItemRow mirrors the session_media_items table row (AB-47b,
// migration 0085). Exactly one of MediaID / VideoURL is non-nil per row,
// enforced at the DB layer by the kind_payload CHECK constraint.
type SessionMediaItemRow struct {
	ID        uuid.UUID  `json:"id"`
	SessionID uuid.UUID  `json:"session_id"`
	Kind      string     `json:"kind"`
	MediaID   *uuid.UUID `json:"media_id"`
	VideoURL  *string    `json:"video_url"`
	Position  int16      `json:"position"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// scanSessionMediaItemRow scans a single session_media_items row.
func scanSessionMediaItemRow(row interface {
	Scan(dest ...any) error
}) (SessionMediaItemRow, error) {
	var r SessionMediaItemRow
	err := row.Scan(
		&r.ID,
		&r.SessionID,
		&r.Kind,
		&r.MediaID,
		&r.VideoURL,
		&r.Position,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	return r, err
}

const sessionMediaColumns = `id, session_id, kind, media_id, video_url, position, created_at, updated_at`

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionMediaItems
// ─────────────────────────────────────────────────────────────────────────────

const listSessionMediaItems = `-- name: ListSessionMediaItems :many
SELECT ` + sessionMediaColumns + `
FROM   session_media_items
WHERE  session_id = $1
ORDER BY position ASC, id ASC`

// ListSessionMediaItems returns every session_media_items row for the
// session ordered by position ASC.
func (q *Queries) ListSessionMediaItems(ctx context.Context, sessionID uuid.UUID) ([]SessionMediaItemRow, error) {
	rows, err := q.db.Query(ctx, listSessionMediaItems, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionMediaItemRow
	for rows.Next() {
		item, err := scanSessionMediaItemRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteSessionMediaItems
// ─────────────────────────────────────────────────────────────────────────────

const deleteSessionMediaItems = `-- name: DeleteSessionMediaItems :exec
DELETE FROM session_media_items
WHERE  session_id = $1`

// DeleteSessionMediaItems wipes every session_media_items row for the
// session. Called at the top of a replace transaction.
func (q *Queries) DeleteSessionMediaItems(ctx context.Context, sessionID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteSessionMediaItems, sessionID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSessionMediaItem
// ─────────────────────────────────────────────────────────────────────────────

const insertSessionMediaItem = `-- name: InsertSessionMediaItem :one
INSERT INTO session_media_items (session_id, kind, media_id, video_url, position)
VALUES ($1, $2, $3, $4, $5)
RETURNING ` + sessionMediaColumns

// InsertSessionMediaItem inserts a single gallery row. The kind_payload
// CHECK constraint on session_media_items refuses inconsistent kind /
// media_id / video_url combinations, so this method returns a driver error
// (23514) if the handler validation is bypassed.
func (q *Queries) InsertSessionMediaItem(
	ctx context.Context,
	sessionID uuid.UUID,
	kind string,
	mediaID *uuid.UUID,
	videoURL *string,
	position int16,
) (SessionMediaItemRow, error) {
	row := q.db.QueryRow(ctx, insertSessionMediaItem, sessionID, kind, mediaID, videoURL, position)
	return scanSessionMediaItemRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// SessionExistsActive
// ─────────────────────────────────────────────────────────────────────────────

const sessionExistsActive = `-- name: SessionExistsActive :one
SELECT EXISTS (
    SELECT 1 FROM sessions
    WHERE  id = $1
      AND  deleted_at IS NULL
) AS exists`

// SessionExistsActive returns true when the session is present and not
// soft-deleted. Used by the session-media gallery handler.
func (q *Queries) SessionExistsActive(ctx context.Context, id uuid.UUID) (bool, error) {
	row := q.db.QueryRow(ctx, sessionExistsActive, id)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetMediaObjectOwnerType
// ─────────────────────────────────────────────────────────────────────────────

const getMediaObjectOwnerType = `-- name: GetMediaObjectOwnerType :one
SELECT owner_type
FROM   media_objects
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetMediaObjectOwnerType returns the owner_type of an ACTIVE media
// object. Returns pgx.ErrNoRows when the row does not exist or has been
// soft-deleted. Used by the session-media gallery handler to enforce
// poster items reference an owner_type='session_poster' media object.
func (q *Queries) GetMediaObjectOwnerType(ctx context.Context, id uuid.UUID) (string, error) {
	row := q.db.QueryRow(ctx, getMediaObjectOwnerType, id)
	var ownerType string
	err := row.Scan(&ownerType)
	return ownerType, err
}
