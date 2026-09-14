// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: ga_quota.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ticket_tiers — category number, open flag, quantity
// ─────────────────────────────────────────────────────────────────────────────

const maxTicketTierUnitSeq = `-- name: MaxTicketTierUnitSeq :one
SELECT COALESCE(MAX(unit_seq), 0)::int AS max_unit_seq
FROM   ticket_tiers
WHERE  session_id = $1`

// MaxTicketTierUnitSeq returns the highest category number ever handed
// out on this session, INCLUDING soft-deleted categories, so a number is
// never reused. 0 when the session has no numbered category yet.
func (q *Queries) MaxTicketTierUnitSeq(ctx context.Context, sessionID uuid.UUID) (int32, error) {
	var n int32
	err := q.db.QueryRow(ctx, maxTicketTierUnitSeq, sessionID).Scan(&n)
	return n, err
}

const assignTicketTierUnitSeq = `-- name: AssignTicketTierUnitSeq :one
UPDATE ticket_tiers
SET    unit_seq   = $3::int,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  unit_seq IS NULL
RETURNING unit_seq`

// AssignTicketTierUnitSeq assigns the category number, but only when the
// row has none yet. Returns pgx.ErrNoRows when the category is already
// numbered (or does not belong to the session).
func (q *Queries) AssignTicketTierUnitSeq(ctx context.Context, id, sessionID uuid.UUID, unitSeq int32) (int32, error) {
	var n int32
	err := q.db.QueryRow(ctx, assignTicketTierUnitSeq, id, sessionID, unitSeq).Scan(&n)
	return n, err
}

const setTicketTierOpen = `-- name: SetTicketTierOpen :one
UPDATE ticket_tiers
SET    is_open    = $3::boolean,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING is_open`

// SetTicketTierOpen opens or closes an active category. Returns
// pgx.ErrNoRows when the category does not exist, belongs to another
// session, or has been soft-deleted.
func (q *Queries) SetTicketTierOpen(ctx context.Context, id, sessionID uuid.UUID, open bool) (bool, error) {
	var b bool
	err := q.db.QueryRow(ctx, setTicketTierOpen, id, sessionID, open).Scan(&b)
	return b, err
}

const setTicketTierCapacity = `-- name: SetTicketTierCapacity :one
UPDATE ticket_tiers
SET    capacity   = $3::int,
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING capacity`

// SetTicketTierCapacity writes the category quantity. Returns
// pgx.ErrNoRows when the category does not exist, belongs to another
// session, or has been soft-deleted.
func (q *Queries) SetTicketTierCapacity(ctx context.Context, id, sessionID uuid.UUID, capacity int32) (int32, error) {
	var n int32
	err := q.db.QueryRow(ctx, setTicketTierCapacity, id, sessionID, capacity).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GA places owned by a category
// ─────────────────────────────────────────────────────────────────────────────

const maxGAUnitIndexForTierPrefix = `-- name: MaxGAUnitIndexForTierPrefix :one
SELECT COALESCE(MAX(split_part(seat_key, '|', 3)::int), 0)::int AS max_index
FROM   session_seats
WHERE  session_id = $1
  AND  kind       = 'ga_unit'
  AND  seat_key LIKE $2 || '|%'`

// MaxGAUnitIndexForTierPrefix returns the highest index already used
// under a category's own key prefix ("ga|t3"), so newly minted places
// continue the sequence instead of colliding on the seat_key unique
// constraint. 0 when the prefix has no place yet.
func (q *Queries) MaxGAUnitIndexForTierPrefix(ctx context.Context, sessionID uuid.UUID, keyPrefix string) (int32, error) {
	var n int32
	err := q.db.QueryRow(ctx, maxGAUnitIndexForTierPrefix, sessionID, keyPrefix).Scan(&n)
	return n, err
}

const insertGAUnitsForTier = `-- name: InsertGAUnitsForTier :execrows
INSERT INTO session_seats
    (session_id, seat_key, sector_name, row_name, seat_number,
     tier_id, status, kind, status_version)
SELECT $1,
       $2 || '|' || lpad((gs + $3)::text, 6, '0'),
       '', '', '',
       $4::uuid,
       'available',
       'ga_unit',
       $6::bigint
FROM generate_series(1, $5::int) gs`

// InsertGAUnitsForTier materializes quantity places for one category
// under its own key prefix starting at startIndex+1, stamped with the
// caller's freshly bumped sessions.seat_status_version. Returns the
// inserted count.
func (q *Queries) InsertGAUnitsForTier(
	ctx context.Context,
	sessionID uuid.UUID,
	keyPrefix string,
	startIndex int32,
	tierID uuid.UUID,
	quantity int32,
	statusVersion int64,
) (int64, error) {
	tag, err := q.db.Exec(ctx, insertGAUnitsForTier,
		sessionID, keyPrefix, startIndex, tierID, quantity, statusVersion)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const countDeletableGAUnitsForTier = `-- name: CountDeletableGAUnitsForTier :one
SELECT COUNT(*)::bigint AS count
FROM   session_seats ss
WHERE  ss.session_id = $1
  AND  ss.kind       = 'ga_unit'
  AND  ss.tier_id    = $2
  AND  ss.status     = 'available'
  AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs
                    WHERE rs.session_seat_id = ss.id)
  AND  NOT EXISTS (SELECT 1 FROM order_items oi
                    WHERE oi.session_seat_id = ss.id)`

// CountDeletableGAUnitsForTier reports how many of a category's places
// can actually be removed: available, and referenced by neither
// reservation_seats nor order_items (both FKs are cascade-less, so
// deleting a referenced row fails with 23503).
func (q *Queries) CountDeletableGAUnitsForTier(ctx context.Context, sessionID, tierID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countDeletableGAUnitsForTier, sessionID, tierID).Scan(&n)
	return n, err
}

const deleteAvailableGAUnitsForTier = `-- name: DeleteAvailableGAUnitsForTier :execrows
DELETE FROM session_seats
WHERE id IN (
    SELECT ss.id
    FROM   session_seats ss
    WHERE  ss.session_id = $1
      AND  ss.kind       = 'ga_unit'
      AND  ss.tier_id    = $2
      AND  ss.status     = 'available'
      AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs
                        WHERE rs.session_seat_id = ss.id)
      AND  NOT EXISTS (SELECT 1 FROM order_items oi
                        WHERE oi.session_seat_id = ss.id)
    ORDER  BY ss.seat_key DESC
    LIMIT  $3
)`

// DeleteAvailableGAUnitsForTier removes up to limit of a category's
// highest-keyed available, unreferenced places. Returns the number
// actually removed; the caller MUST compare it with what it asked for.
func (q *Queries) DeleteAvailableGAUnitsForTier(
	ctx context.Context, sessionID, tierID uuid.UUID, limit int32,
) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteAvailableGAUnitsForTier, sessionID, tierID, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GAUnitStatsRow is one category's place counters on a session.
type GAUnitStatsRow struct {
	TierID    uuid.UUID `json:"tier_id"`
	Total     int64     `json:"total"`
	Held      int64     `json:"held"`
	Sold      int64     `json:"sold"`
	Available int64     `json:"available"`
}

const listGAUnitStatsBySession = `-- name: ListGAUnitStatsBySession :many
SELECT ss.tier_id,
       COUNT(*)::bigint                                              AS total,
       COUNT(*) FILTER (WHERE ss.status = 'held')::bigint            AS held,
       COUNT(*) FILTER (WHERE ss.status = 'sold')::bigint            AS sold,
       COUNT(*) FILTER (WHERE ss.status = 'available')::bigint       AS available
FROM   session_seats ss
WHERE  ss.session_id = $1
  AND  ss.kind       = 'ga_unit'
  AND  ss.tier_id IS NOT NULL
GROUP  BY ss.tier_id`

// ListGAUnitStatsBySession returns per-category place counters (quantity
// owned, held, sold, available) for one session.
func (q *Queries) ListGAUnitStatsBySession(ctx context.Context, sessionID uuid.UUID) ([]GAUnitStatsRow, error) {
	rows, err := q.db.Query(ctx, listGAUnitStatsBySession, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GAUnitStatsRow
	for rows.Next() {
		var r GAUnitStatsRow
		if err := rows.Scan(&r.TierID, &r.Total, &r.Held, &r.Sold, &r.Available); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const countSeatRowsForTier = `-- name: CountSeatRowsForTier :one
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  tier_id    = $2
  AND  kind       = 'seat'`

// CountSeatRowsForTier counts the coordinate-bearing seats carrying this
// category. Non-zero means a SEATED category, whose quantity comes from
// the plan geometry and may not be changed through the quota mechanism.
func (q *Queries) CountSeatRowsForTier(ctx context.Context, sessionID, tierID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSeatRowsForTier, sessionID, tierID).Scan(&n)
	return n, err
}

const countSessionPlaces = `-- name: CountSessionPlaces :one
SELECT COUNT(*)::bigint AS count
FROM   session_seats
WHERE  session_id = $1
  AND  kind IN ('seat', 'ga_unit')`

// CountSessionPlaces counts every sellable place of a session (plan seats
// plus GA places) — what sessions.capacity_total and the session-level
// inventory_ledger row are recomputed from.
func (q *Queries) CountSessionPlaces(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countSessionPlaces, sessionID).Scan(&n)
	return n, err
}

const countActiveTicketsForTier = `-- name: CountActiveTicketsForTier :one
SELECT COUNT(*)::bigint AS count
FROM   tickets
WHERE  session_id = $1
  AND  tier_id    = $2
  AND  status     = 'active'`

// CountActiveTicketsForTier counts active tickets issued against a
// category — the delete guard.
func (q *Queries) CountActiveTicketsForTier(ctx context.Context, sessionID, tierID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countActiveTicketsForTier, sessionID, tierID).Scan(&n)
	return n, err
}

// ─────────────────────────────────────────────────────────────────────────────
// sessions — derived capacity and the one legal admission-mode transition
// ─────────────────────────────────────────────────────────────────────────────

const setSessionCapacityTotal = `-- name: SetSessionCapacityTotal :exec
UPDATE sessions
SET    capacity_total = $2::int,
       updated_at     = now()
WHERE  id             = $1
  AND  deleted_at     IS NULL`

// SetSessionCapacityTotal writes the recomputed session capacity, keyed
// on the session alone.
func (q *Queries) SetSessionCapacityTotal(ctx context.Context, sessionID uuid.UUID, capacityTotal int32) error {
	_, err := q.db.Exec(ctx, setSessionCapacityTotal, sessionID, capacityTotal)
	return err
}

const setSessionAdmissionMode = `-- name: SetSessionAdmissionMode :exec
UPDATE sessions
SET    admission_mode = $2::text,
       updated_at     = now()
WHERE  id             = $1
  AND  deleted_at     IS NULL`

// SetSessionAdmissionMode flips a session's admission mode without
// touching its plan binding. The quota mechanism performs exactly one
// transition with it: assigned_seats -> hybrid when the first GA category
// is added to a seated session.
func (q *Queries) SetSessionAdmissionMode(ctx context.Context, sessionID uuid.UUID, mode string) error {
	_, err := q.db.Exec(ctx, setSessionAdmissionMode, sessionID, mode)
	return err
}
