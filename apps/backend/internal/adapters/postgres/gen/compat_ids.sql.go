// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: compat_ids.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// W1-A2a (feature #475): compatibility_id_map queries — Bil24-compat integer
// identity registry for arena catalog entities.
// ─────────────────────────────────────────────────────────────────────────────

// CompatibilityIDRow is the shared result type for every compatibility_id_map
// query. Field names mirror the column names in migration 0090.
type CompatibilityIDRow struct {
	Kind       string    `json:"kind"`
	SystemID   int64     `json:"system_id"`
	PlatformID uuid.UUID `json:"platform_id"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

// scanCompatibilityIDRow scans a single compatibility_id_map row in the
// canonical column order used by every query in this file.
func scanCompatibilityIDRow(row interface {
	Scan(dest ...any) error
}) (CompatibilityIDRow, error) {
	var r CompatibilityIDRow
	err := row.Scan(&r.Kind, &r.SystemID, &r.PlatformID, &r.Source, &r.CreatedAt)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// EnsureCompatibilityID
// ─────────────────────────────────────────────────────────────────────────────

const ensureCompatibilityID = `-- name: EnsureCompatibilityID :one
INSERT INTO compatibility_id_map (kind, platform_id, system_id, source)
VALUES ($1, $2, nextval('compatibility_system_id_seq'), 'arena')
ON CONFLICT (kind, platform_id) DO NOTHING
RETURNING kind, system_id, platform_id, source, created_at`

// EnsureCompatibilityID performs the INSERT ... ON CONFLICT DO NOTHING branch
// of the lazy-mint pattern. Returns the freshly inserted row when the
// (kind, platform_id) pair was new, or pgx.ErrNoRows when the row already
// existed (in which case the caller MUST follow up with
// GetCompatibilityIDByPlatformID on the same connection/tx).
//
// system_id is drawn from compatibility_system_id_seq and is therefore always
// >= 1e9 (spec §4).
func (q *Queries) EnsureCompatibilityID(ctx context.Context, kind string, platformID uuid.UUID) (CompatibilityIDRow, error) {
	row := q.db.QueryRow(ctx, ensureCompatibilityID, kind, platformID)
	return scanCompatibilityIDRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetCompatibilityIDByPlatformID
// ─────────────────────────────────────────────────────────────────────────────

const getCompatibilityIDByPlatformID = `-- name: GetCompatibilityIDByPlatformID :one
SELECT kind, system_id, platform_id, source, created_at
FROM   compatibility_id_map
WHERE  kind = $1
  AND  platform_id = $2`

// GetCompatibilityIDByPlatformID resolves a (kind, platform_id) pair to its
// compat row. Returns pgx.ErrNoRows when absent.
func (q *Queries) GetCompatibilityIDByPlatformID(ctx context.Context, kind string, platformID uuid.UUID) (CompatibilityIDRow, error) {
	row := q.db.QueryRow(ctx, getCompatibilityIDByPlatformID, kind, platformID)
	return scanCompatibilityIDRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetCompatibilityIDBySystemID
// ─────────────────────────────────────────────────────────────────────────────

const getCompatibilityIDBySystemID = `-- name: GetCompatibilityIDBySystemID :one
SELECT kind, system_id, platform_id, source, created_at
FROM   compatibility_id_map
WHERE  kind = $1
  AND  system_id = $2`

// GetCompatibilityIDBySystemID reverse-resolves (kind, system_id) → row.
// Returns pgx.ErrNoRows when absent (translated by callers to gateway
// result codes -3 / 101 per spec §6).
func (q *Queries) GetCompatibilityIDBySystemID(ctx context.Context, kind string, systemID int64) (CompatibilityIDRow, error) {
	row := q.db.QueryRow(ctx, getCompatibilityIDBySystemID, kind, systemID)
	return scanCompatibilityIDRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// RegisterExternalCompatibilityID
// ─────────────────────────────────────────────────────────────────────────────

const registerExternalCompatibilityID = `-- name: RegisterExternalCompatibilityID :one
INSERT INTO compatibility_id_map (kind, platform_id, system_id, source)
VALUES ($1, $2, $3, 'bil24')
ON CONFLICT DO NOTHING
RETURNING kind, system_id, platform_id, source, created_at`

// RegisterExternalCompatibilityID records a Bil24-supplied system_id for a
// platform entity. Callers MUST verify system_id < 1e9 before invoking; a
// value >= 1e9 would collide with locally-minted arena ids and is rejected
// upstream by compatids.RegisterExternal with compat.external_id_out_of_range.
//
// Returns pgx.ErrNoRows on any conflict (kind+platform_id OR kind+system_id
// duplicate) so the caller can differentiate idempotent re-registration
// from a genuine collision by comparing existing rows.
func (q *Queries) RegisterExternalCompatibilityID(ctx context.Context, kind string, platformID uuid.UUID, systemID int64) (CompatibilityIDRow, error) {
	row := q.db.QueryRow(ctx, registerExternalCompatibilityID, kind, platformID, systemID)
	return scanCompatibilityIDRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// BulkInsertCompatibilityIDs / ListCompatibilityIDsByPlatformIDs — feature
// #498 (W1-B3b): the genuinely-batched pair behind compatids.EnsureMany,
// two round trips regardless of N. See doc comment on EnsureMany.
// ─────────────────────────────────────────────────────────────────────────────

const bulkInsertCompatibilityIDs = `-- name: BulkInsertCompatibilityIDs :exec
INSERT INTO compatibility_id_map (kind, platform_id, system_id, source)
SELECT $1, pid, nextval('compatibility_system_id_seq'), 'arena'
FROM   unnest($2::uuid[]) AS pid
ON CONFLICT (kind, platform_id) DO NOTHING`

// BulkInsertCompatibilityIDs mints an arena-owned system_id for every
// platformID of kind that does not already have one, in a single round
// trip. Rows that already exist are silently skipped by ON CONFLICT DO
// NOTHING — callers MUST follow up with ListCompatibilityIDsByPlatformIDs to
// read back the full (pre-existing + freshly minted) id set.
func (q *Queries) BulkInsertCompatibilityIDs(ctx context.Context, kind string, platformIDs []uuid.UUID) error {
	_, err := q.db.Exec(ctx, bulkInsertCompatibilityIDs, kind, platformIDs)
	return err
}

const listCompatibilityIDsByPlatformIDs = `-- name: ListCompatibilityIDsByPlatformIDs :many
SELECT kind, system_id, platform_id, source, created_at
FROM   compatibility_id_map
WHERE  kind = $1
  AND  platform_id = ANY($2::uuid[])`

// ListCompatibilityIDsByPlatformIDs resolves every (kind, platform_id) pair
// in platformIDs to its compat row in a single round trip. Platform ids with
// no matching row are simply absent from the result (never an error) —
// callers that require every input to resolve check the returned slice
// length against len(platformIDs) themselves.
func (q *Queries) ListCompatibilityIDsByPlatformIDs(ctx context.Context, kind string, platformIDs []uuid.UUID) ([]CompatibilityIDRow, error) {
	rows, err := q.db.Query(ctx, listCompatibilityIDsByPlatformIDs, kind, platformIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CompatibilityIDRow
	for rows.Next() {
		r, err := scanCompatibilityIDRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
