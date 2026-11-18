// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: price categories of a plan-bound session that own no place yet

package gen

import (
	"context"

	"github.com/google/uuid"
)

const listPlacelessPlanTierIDsBySessions = `-- name: ListPlacelessPlanTierIDsBySessions :many
SELECT tt.id
FROM   ticket_tiers tt
JOIN   sessions s ON s.id = tt.session_id
WHERE  tt.session_id = ANY($1::uuid[])
  AND  tt.deleted_at IS NULL
  AND  s.seating_plan_version_id IS NOT NULL
  AND  EXISTS (SELECT 1 FROM session_seats any_place WHERE any_place.session_id = tt.session_id)
  AND  NOT EXISTS (SELECT 1 FROM session_seats own
                   WHERE  own.session_id = tt.session_id AND own.tier_id = tt.id)`

// ListPlacelessPlanTierIDsBySessions returns the price categories of the
// given sessions that are bound to a seating plan and own no place at all:
// no plan seat and no General Admission place carries their tier_id. Such a
// category exists before the organizer paints seats into it (an extra price
// category of the event-bundle import, planExtra, or one made in the seat
// manager); it has nothing a buyer could take, so no sales surface offers it
// and the hold gate refuses it (hcheckout.CheckGALinesSellable).
//
// A session without a plan, and a plan-bound session with no place at all
// (a ledger-only shape), never report a category here: their categories keep
// the fallbacks they always had.
func (q *Queries) ListPlacelessPlanTierIDsBySessions(ctx context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(sessionIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	return q.collectAddonIDs(ctx, listPlacelessPlanTierIDsBySessions, sessionIDs)
}
