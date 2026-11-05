// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: add-on categories (migration 0197, ticket_tiers.is_addon)
//
// The flag is read through these narrow queries instead of TicketTierRow, so
// the shared tier scanner and every query feeding it stay as they are.

package gen

import (
	"context"

	"github.com/google/uuid"
)

const setTicketTierAddon = `-- name: SetTicketTierAddon :exec
UPDATE ticket_tiers SET is_addon = $2, updated_at = now()
WHERE  id = $1 AND deleted_at IS NULL AND is_addon <> $2`

// SetTicketTierAddon marks a category as an add-on (sold only together with
// a main category of the same session) or clears the mark. Setting the value
// it already has writes nothing.
func (q *Queries) SetTicketTierAddon(ctx context.Context, tierID uuid.UUID, isAddon bool) error {
	_, err := q.db.Exec(ctx, setTicketTierAddon, tierID, isAddon)
	return err
}

const listAddonTierIDs = `-- name: ListAddonTierIDs :many
SELECT id FROM ticket_tiers
WHERE  id = ANY($1::uuid[]) AND is_addon AND deleted_at IS NULL`

// ListAddonTierIDs returns which of the given categories are add-ons.
func (q *Queries) ListAddonTierIDs(ctx context.Context, tierIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(tierIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	return q.collectAddonIDs(ctx, listAddonTierIDs, tierIDs)
}

const listAddonTierIDsBySessions = `-- name: ListAddonTierIDsBySessions :many
SELECT id FROM ticket_tiers
WHERE  session_id = ANY($1::uuid[]) AND is_addon AND deleted_at IS NULL`

// ListAddonTierIDsBySessions returns the add-on categories of the given
// sessions.
func (q *Queries) ListAddonTierIDsBySessions(ctx context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(sessionIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	return q.collectAddonIDs(ctx, listAddonTierIDsBySessions, sessionIDs)
}

const listAddonTierIDsByOrg = `-- name: ListAddonTierIDsByOrg :many
SELECT tt.id
FROM   ticket_tiers tt
JOIN   sessions s ON s.id = tt.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  e.org_id = $1 AND tt.is_addon AND tt.deleted_at IS NULL AND s.deleted_at IS NULL`

// ListAddonTierIDsByOrg returns every add-on category of the organization
// (the gateway catalog reads it once per GET_ALL_ACTIONS).
func (q *Queries) ListAddonTierIDsByOrg(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]bool, error) {
	return q.collectAddonIDs(ctx, listAddonTierIDsByOrg, orgID)
}

func (q *Queries) collectAddonIDs(ctx context.Context, sql string, arg any) (map[uuid.UUID]bool, error) {
	rows, err := q.db.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
