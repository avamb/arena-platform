// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: tier_chain.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const upsertTierChain = `-- name: UpsertTierChain :exec
INSERT INTO ticket_tier_chain (tier_id, next_tier_id)
VALUES ($1, $2)
ON CONFLICT (tier_id) DO UPDATE
SET    next_tier_id   = EXCLUDED.next_tier_id,
       handed_over_at = CASE WHEN ticket_tier_chain.next_tier_id = EXCLUDED.next_tier_id
                             THEN ticket_tier_chain.handed_over_at END,
       updated_at     = now()`

// UpsertTierChain sets (or replaces) the category a tier hands its free
// places to when its sale window closes. Changing the target resets
// handed_over_at, so the new target is opened on its first hand-over.
func (q *Queries) UpsertTierChain(ctx context.Context, tierID, nextTierID uuid.UUID) error {
	_, err := q.db.Exec(ctx, upsertTierChain, tierID, nextTierID)
	return err
}

const deleteTierChain = `-- name: DeleteTierChain :exec
DELETE FROM ticket_tier_chain WHERE tier_id = $1`

// DeleteTierChain removes a tier's chain link, if any.
func (q *Queries) DeleteTierChain(ctx context.Context, tierID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteTierChain, tierID)
	return err
}

// TierChainRow is one chain link.
type TierChainRow struct {
	SessionID    uuid.UUID  `json:"session_id"`
	TierID       uuid.UUID  `json:"tier_id"`
	NextTierID   uuid.UUID  `json:"next_tier_id"`
	HandedOverAt *time.Time `json:"handed_over_at"`
}

const listTierChainForSession = `-- name: ListTierChainForSession :many
SELECT c.tier_id, c.next_tier_id, c.handed_over_at
FROM   ticket_tier_chain c
JOIN   ticket_tiers t ON t.id = c.tier_id
WHERE  t.session_id = $1
  AND  t.deleted_at IS NULL`

// ListTierChainForSession returns every chain link of one session's live
// categories. SessionID is filled from the argument.
func (q *Queries) ListTierChainForSession(ctx context.Context, sessionID uuid.UUID) ([]TierChainRow, error) {
	rows, err := q.db.Query(ctx, listTierChainForSession, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TierChainRow
	for rows.Next() {
		r := TierChainRow{SessionID: sessionID}
		if err := rows.Scan(&r.TierID, &r.NextTierID, &r.HandedOverAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listDueTierHandOvers = `-- name: ListDueTierHandOvers :many
SELECT t.session_id, c.tier_id, c.next_tier_id, c.handed_over_at
FROM   ticket_tier_chain c
JOIN   ticket_tiers t  ON t.id  = c.tier_id      AND t.deleted_at  IS NULL
JOIN   ticket_tiers nt ON nt.id = c.next_tier_id AND nt.deleted_at IS NULL
                      AND nt.session_id = t.session_id
WHERE  t.sale_window_end IS NOT NULL
  AND  t.sale_window_end <= now()
  AND  EXISTS (
         SELECT 1 FROM session_seats ss
         WHERE  ss.session_id = t.session_id
           AND  ss.tier_id    = t.id
           AND  ss.kind       = 'ga_unit'
           AND  ss.status     = 'available'
           AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs WHERE rs.session_seat_id = ss.id))
ORDER  BY t.sale_window_end, t.sort_order
LIMIT  $1`

// ListDueTierHandOvers returns the links whose source category's sale window
// has closed and that still own a movable free GA place, oldest window first
// so a chain of several expired windows cascades within one sweep.
func (q *Queries) ListDueTierHandOvers(ctx context.Context, limit int32) ([]TierChainRow, error) {
	rows, err := q.db.Query(ctx, listDueTierHandOvers, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TierChainRow
	for rows.Next() {
		var r TierChainRow
		if err := rows.Scan(&r.SessionID, &r.TierID, &r.NextTierID, &r.HandedOverAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const moveFreeGAUnitsToTier = `-- name: MoveFreeGAUnitsToTier :execrows
WITH free AS (
    SELECT ss.id, ss.seat_key
    FROM   session_seats ss
    WHERE  ss.session_id = $1
      AND  ss.tier_id    = $2
      AND  ss.kind       = 'ga_unit'
      AND  ss.status     = 'available'
      AND  NOT EXISTS (SELECT 1 FROM reservation_seats rs WHERE rs.session_seat_id = ss.id)
    FOR UPDATE
), moving AS (
    SELECT id, row_number() OVER (ORDER BY seat_key) AS rn FROM free
)
UPDATE session_seats ss
SET    tier_id        = $3,
       seat_key       = $4 || '|' || lpad((m.rn + $5)::text, 6, '0'),
       status_version = $6,
       updated_at     = now()
FROM   moving m
WHERE  ss.id = m.id`

// MoveFreeGAUnitsToTier re-assigns every free GA place of fromTier to toTier,
// re-keyed '<keyPrefix>|<n>' with n counting on from startIndex, stamped with
// the caller's freshly bumped seat_status_version. Rows are kept rather than
// deleted and re-minted, so a free place an expired order's order_items still
// points at moves too. A place referenced by reservation_seats never moves.
// The caller MUST hold the sessions row lock.
func (q *Queries) MoveFreeGAUnitsToTier(
	ctx context.Context,
	sessionID, fromTier, toTier uuid.UUID,
	keyPrefix string,
	startIndex int32,
	statusVersion int64,
) (int64, error) {
	tag, err := q.db.Exec(ctx, moveFreeGAUnitsToTier,
		sessionID, fromTier, toTier, keyPrefix, startIndex, statusVersion)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const syncTierCapacityToPlaces = `-- name: SyncTierCapacityToPlaces :exec
UPDATE ticket_tiers t
SET    capacity   = NULLIF((SELECT COUNT(*) FROM session_seats ss
                             WHERE ss.session_id = t.session_id AND ss.tier_id = t.id
                               AND ss.kind IN ('seat', 'ga_unit')), 0),
       updated_at = now()
WHERE  t.id = $1 AND t.session_id = $2`

// SyncTierCapacityToPlaces writes the number of places a category owns into
// ticket_tiers.capacity (NULL when it owns none — the CHECK refuses 0).
func (q *Queries) SyncTierCapacityToPlaces(ctx context.Context, tierID, sessionID uuid.UUID) error {
	_, err := q.db.Exec(ctx, syncTierCapacityToPlaces, tierID, sessionID)
	return err
}

const markTierChainHandedOver = `-- name: MarkTierChainHandedOver :exec
UPDATE ticket_tier_chain
SET    handed_over_at = now(), updated_at = now()
WHERE  tier_id = $1 AND handed_over_at IS NULL`

// MarkTierChainHandedOver stamps the first hand-over of a link.
func (q *Queries) MarkTierChainHandedOver(ctx context.Context, tierID uuid.UUID) error {
	_, err := q.db.Exec(ctx, markTierChainHandedOver, tierID)
	return err
}
