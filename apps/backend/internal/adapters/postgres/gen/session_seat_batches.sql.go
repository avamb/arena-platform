// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: session_seat_batches.sql (task S1c, migration 0162)
//
// These queries are the storage half of seatlayout.ApplyBatch. Every one
// that runs inside a batch transaction is called AFTER
// IncrementSessionSeatStatusVersion (the sessions row lock, the first
// statement of every seat mutation — AGENTS.md lock order).

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SessionSeatBatchRow is one row of session_seat_batches. Changes and
// Skipped are the raw jsonb arrays; seatlayout owns their shape.
type SessionSeatBatchRow struct {
	ID                uuid.UUID       `json:"id"`
	OrgID             uuid.UUID       `json:"org_id"`
	SessionID         uuid.UUID       `json:"session_id"`
	ActorID           *uuid.UUID      `json:"actor_id"`
	ActorLabel        string          `json:"actor_label"`
	Kind              string          `json:"kind"`
	SeatStatusVersion int64           `json:"seat_status_version"`
	AppliedAt         time.Time       `json:"applied_at"`
	RevertedAt        *time.Time      `json:"reverted_at"`
	RevertedByBatchID *uuid.UUID      `json:"reverted_by_batch_id"`
	RevertsBatchID    *uuid.UUID      `json:"reverts_batch_id"`
	Changes           json.RawMessage `json:"changes"`
	Skipped           json.RawMessage `json:"skipped"`
	CreatedAt         time.Time       `json:"created_at"`
}

const sessionSeatBatchColumns = `id, org_id, session_id, actor_id, actor_label, kind,
       seat_status_version, applied_at, reverted_at, reverted_by_batch_id,
       reverts_batch_id, changes, skipped, created_at`

func scanSessionSeatBatchRow(row interface {
	Scan(dest ...any) error
}) (SessionSeatBatchRow, error) {
	var b SessionSeatBatchRow
	var changes, skipped []byte
	err := row.Scan(
		&b.ID, &b.OrgID, &b.SessionID, &b.ActorID, &b.ActorLabel, &b.Kind,
		&b.SeatStatusVersion, &b.AppliedAt, &b.RevertedAt, &b.RevertedByBatchID,
		&b.RevertsBatchID, &changes, &skipped, &b.CreatedAt,
	)
	b.Changes = json.RawMessage(changes)
	b.Skipped = json.RawMessage(skipped)
	return b, err
}

// ─────────────────────────────────────────────────────────────────────────────
// LockSessionSeatsForBatch
// ─────────────────────────────────────────────────────────────────────────────

const lockSessionSeatsForBatch = `-- name: LockSessionSeatsForBatch :many
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id = $1
  AND  seat_key   = ANY($2::text[])
ORDER  BY seat_key ASC, id ASC
FOR UPDATE`

// LockSessionSeatsForBatch locks the named seats of a session in seat_key
// order, the same order CreateSeatedHold locks them in, and returns their
// state as of the lock. Keys that do not resolve are simply absent.
func (q *Queries) LockSessionSeatsForBatch(ctx context.Context, sessionID uuid.UUID, seatKeys []string) ([]SessionSeatRow, error) {
	rows, err := q.db.Query(ctx, lockSessionSeatsForBatch, sessionID, seatKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatRow
	for rows.Next() {
		s, err := scanSessionSeatRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListActiveSessionTierIDs
// ─────────────────────────────────────────────────────────────────────────────

const listActiveSessionTierIDs = `-- name: ListActiveSessionTierIDs :many
SELECT id
FROM   ticket_tiers
WHERE  session_id = $1
  AND  id         = ANY($2::uuid[])
  AND  deleted_at IS NULL`

// ListActiveSessionTierIDs returns which of tierIDs are live categories of
// the session. A tier of another session, a deleted one or an unknown id is
// absent from the answer.
func (q *Queries) ListActiveSessionTierIDs(ctx context.Context, sessionID uuid.UUID, tierIDs []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, listActiveSessionTierIDs, sessionID, tierIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ApplySessionSeatBatchUpdates
// ─────────────────────────────────────────────────────────────────────────────

const applySessionSeatBatchUpdates = `-- name: ApplySessionSeatBatchUpdates :execrows
UPDATE session_seats ss
SET    tier_id        = v.tier_id::uuid,
       status         = v.status,
       status_version = $2,
       updated_at     = now()
FROM   unnest($3::text[], $4::text[], $5::text[]) AS v(id, tier_id, status)
WHERE  ss.id         = v.id::uuid
  AND  ss.session_id = $1
  AND  (ss.kind = 'seat' OR ss.tier_id IS NOT DISTINCT FROM v.tier_id::uuid)
  AND  ss.status     IN ('available', 'unavailable')
  AND  v.status      IN ('available', 'unavailable')`

// ApplySessionSeatBatchUpdates writes the new tier and status of every seat
// in one statement and stamps them with the batch's seat_status_version.
// The WHERE clause repeats the batch rules (available or unavailable before
// AND after; a GA place only opens or closes, it never changes category), so
// even a caller that skipped the row lock can never re-price or reopen a
// sold or held seat. The rows are locked by
// LockSessionSeatsForBatch first, so the affected count equals len(ids).
// ids and tierIDs travel as text[] (UUID strings, a nil tier is NULL) and are
// cast per row, the InsertSessionSeats precedent.
func (q *Queries) ApplySessionSeatBatchUpdates(
	ctx context.Context,
	sessionID uuid.UUID,
	statusVersion int64,
	ids []string,
	tierIDs []*string,
	statuses []string,
) (int64, error) {
	tag, err := q.db.Exec(ctx, applySessionSeatBatchUpdates, sessionID, statusVersion, ids, tierIDs, statuses)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSessionSeatBatch
// ─────────────────────────────────────────────────────────────────────────────

const insertSessionSeatBatch = `-- name: InsertSessionSeatBatch :one
INSERT INTO session_seat_batches
    (org_id, session_id, actor_id, actor_label, kind, seat_status_version,
     reverts_batch_id, changes, skipped)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)
RETURNING ` + sessionSeatBatchColumns

// InsertSessionSeatBatchParams carries the columns of a new batch row.
type InsertSessionSeatBatchParams struct {
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	ActorID           *uuid.UUID
	ActorLabel        string
	Kind              string
	SeatStatusVersion int64
	RevertsBatchID    *uuid.UUID
	Changes           []byte
	Skipped           []byte
}

// InsertSessionSeatBatch records one batch.
func (q *Queries) InsertSessionSeatBatch(ctx context.Context, p InsertSessionSeatBatchParams) (SessionSeatBatchRow, error) {
	row := q.db.QueryRow(ctx, insertSessionSeatBatch,
		p.OrgID, p.SessionID, p.ActorID, p.ActorLabel, p.Kind, p.SeatStatusVersion,
		p.RevertsBatchID, string(p.Changes), string(p.Skipped))
	return scanSessionSeatBatchRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionSeatBatchForUpdate / MarkSessionSeatBatchReverted
// ─────────────────────────────────────────────────────────────────────────────

const getSessionSeatBatchForUpdate = `-- name: GetSessionSeatBatchForUpdate :one
SELECT ` + sessionSeatBatchColumns + `
FROM   session_seat_batches
WHERE  id         = $1
  AND  session_id = $2
  AND  org_id     = $3
FOR UPDATE`

// GetSessionSeatBatchForUpdate locks one batch of the session and
// organization. pgx.ErrNoRows for an unknown id or one of another session
// or organization.
func (q *Queries) GetSessionSeatBatchForUpdate(ctx context.Context, id, sessionID, orgID uuid.UUID) (SessionSeatBatchRow, error) {
	return scanSessionSeatBatchRow(q.db.QueryRow(ctx, getSessionSeatBatchForUpdate, id, sessionID, orgID))
}

const markSessionSeatBatchReverted = `-- name: MarkSessionSeatBatchReverted :execrows
UPDATE session_seat_batches
SET    reverted_at          = $3,
       reverted_by_batch_id = $2
WHERE  id          = $1
  AND  reverted_at IS NULL
  AND  kind        <> 'revert'`

// MarkSessionSeatBatchReverted links an original batch to the batch that
// reverted it. Zero rows means it was already reverted or is a revert.
func (q *Queries) MarkSessionSeatBatchReverted(ctx context.Context, id, revertBatchID uuid.UUID, at time.Time) (int64, error) {
	tag, err := q.db.Exec(ctx, markSessionSeatBatchReverted, id, revertBatchID, at)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSessionSeatBatchSummaries / GetSessionSeatBatch / CountSessionSeatBatches
// ─────────────────────────────────────────────────────────────────────────────

// SessionSeatBatchSummaryRow is a batch without its changes / skipped
// arrays, only their lengths — what the batch list shows.
type SessionSeatBatchSummaryRow struct {
	ID                uuid.UUID
	SessionID         uuid.UUID
	ActorID           *uuid.UUID
	ActorLabel        string
	Kind              string
	SeatStatusVersion int64
	AppliedAt         time.Time
	RevertedAt        *time.Time
	RevertedByBatchID *uuid.UUID
	RevertsBatchID    *uuid.UUID
	ChangedCount      int32
	SkippedCount      int32
}

const listSessionSeatBatchSummaries = `-- name: ListSessionSeatBatchSummaries :many
SELECT id, session_id, actor_id, actor_label, kind, seat_status_version,
       applied_at, reverted_at, reverted_by_batch_id, reverts_batch_id,
       jsonb_array_length(changes), jsonb_array_length(skipped)
FROM   session_seat_batches
WHERE  session_id = $1
  AND  org_id     = $2
ORDER  BY applied_at DESC, id DESC
LIMIT  $3 OFFSET $4`

// ListSessionSeatBatchSummaries pages the batches of one session, newest
// first, without reading their changes / skipped arrays out of the row.
func (q *Queries) ListSessionSeatBatchSummaries(ctx context.Context, sessionID, orgID uuid.UUID, limit, offset int32) ([]SessionSeatBatchSummaryRow, error) {
	rows, err := q.db.Query(ctx, listSessionSeatBatchSummaries, sessionID, orgID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSeatBatchSummaryRow
	for rows.Next() {
		var b SessionSeatBatchSummaryRow
		if err := rows.Scan(&b.ID, &b.SessionID, &b.ActorID, &b.ActorLabel, &b.Kind, &b.SeatStatusVersion,
			&b.AppliedAt, &b.RevertedAt, &b.RevertedByBatchID, &b.RevertsBatchID,
			&b.ChangedCount, &b.SkippedCount); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const getSessionSeatBatch = `-- name: GetSessionSeatBatch :one
SELECT ` + sessionSeatBatchColumns + `
FROM   session_seat_batches
WHERE  id         = $1
  AND  session_id = $2
  AND  org_id     = $3`

// GetSessionSeatBatch reads one batch of the session and organization;
// pgx.ErrNoRows for an unknown id or one of another session or organization.
func (q *Queries) GetSessionSeatBatch(ctx context.Context, id, sessionID, orgID uuid.UUID) (SessionSeatBatchRow, error) {
	return scanSessionSeatBatchRow(q.db.QueryRow(ctx, getSessionSeatBatch, id, sessionID, orgID))
}

const countSessionSeatBatches = `-- name: CountSessionSeatBatches :one
SELECT count(*) FROM session_seat_batches WHERE session_id = $1 AND org_id = $2`

// CountSessionSeatBatches counts the batches of one session.
func (q *Queries) CountSessionSeatBatches(ctx context.Context, sessionID, orgID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSessionSeatBatches, sessionID, orgID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSeatBatchHistory / CountSeatBatchHistory
// ─────────────────────────────────────────────────────────────────────────────

// The seat filter is a jsonb containment probe, `[{"seat_key": "<key>"}]`,
// built by the caller with encoding/json so a key with quotes or
// backslashes stays one value. changes is served by the GIN index; skipped
// attempts are scanned inside the (session_id, applied_at) range.
const listSeatBatchHistory = `-- name: ListSeatBatchHistory :many
SELECT ` + sessionSeatBatchColumns + `
FROM   session_seat_batches
WHERE  session_id = $1
  AND  org_id     = $2
  AND  (changes @> $3::jsonb OR skipped @> $3::jsonb)
ORDER  BY applied_at DESC, id DESC
LIMIT  $4 OFFSET $5`

// ListSeatBatchHistory pages the batches that changed or skipped one seat,
// newest first. probe is the JSON array `[{"seat_key": ...}]`.
func (q *Queries) ListSeatBatchHistory(ctx context.Context, sessionID, orgID uuid.UUID, probe []byte, limit, offset int32) ([]SessionSeatBatchRow, error) {
	rows, err := q.db.Query(ctx, listSeatBatchHistory, sessionID, orgID, string(probe), limit, offset)
	if err != nil {
		return nil, err
	}
	return collectSessionSeatBatches(rows)
}

const countSeatBatchHistory = `-- name: CountSeatBatchHistory :one
SELECT count(*)
FROM   session_seat_batches
WHERE  session_id = $1
  AND  org_id     = $2
  AND  (changes @> $3::jsonb OR skipped @> $3::jsonb)`

// CountSeatBatchHistory counts the batches ListSeatBatchHistory pages.
func (q *Queries) CountSeatBatchHistory(ctx context.Context, sessionID, orgID uuid.UUID, probe []byte) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSeatBatchHistory, sessionID, orgID, string(probe)).Scan(&n)
	return n, err
}

func collectSessionSeatBatches(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]SessionSeatBatchRow, error) {
	defer rows.Close()
	var out []SessionSeatBatchRow
	for rows.Next() {
		b, err := scanSessionSeatBatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
