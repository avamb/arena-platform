// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: smart price presets (migration 0186, package smartprice)

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SmartPriceRuleRow is one preset applied to a category.
type SmartPriceRuleRow struct {
	ID          uuid.UUID       `json:"id"`
	OrgID       uuid.UUID       `json:"org_id"`
	SessionID   uuid.UUID       `json:"session_id"`
	TierID      uuid.UUID       `json:"tier_id"`
	Kind        string          `json:"kind"`
	Params      json.RawMessage `json:"params"`
	BasePrice   int64           `json:"base_price"`
	StepTierIDs []uuid.UUID     `json:"step_tier_ids"`
	Status      string          `json:"status"`
	CreatedBy   *uuid.UUID      `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	StoppedAt   *time.Time      `json:"stopped_at"`
}

const smartPriceRuleColumns = `id, org_id, session_id, tier_id, kind, params, base_price,
       step_tier_ids, status, created_by, created_at, updated_at, stopped_at`

func scanSmartPriceRule(row interface{ Scan(...any) error }) (SmartPriceRuleRow, error) {
	var r SmartPriceRuleRow
	err := row.Scan(&r.ID, &r.OrgID, &r.SessionID, &r.TierID, &r.Kind, &r.Params, &r.BasePrice,
		&r.StepTierIDs, &r.Status, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.StoppedAt)
	return r, err
}

// InsertSmartPriceRuleParams is a new active preset.
type InsertSmartPriceRuleParams struct {
	OrgID       uuid.UUID
	SessionID   uuid.UUID
	TierID      uuid.UUID
	Kind        string
	Params      json.RawMessage
	BasePrice   int64
	StepTierIDs []uuid.UUID
	CreatedBy   *uuid.UUID
}

const insertSmartPriceRule = `-- name: InsertSmartPriceRule :one
INSERT INTO smart_price_rules (org_id, session_id, tier_id, kind, params, base_price, step_tier_ids, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ` + smartPriceRuleColumns

// InsertSmartPriceRule records a preset. A second active preset of the same
// category fails on smart_price_rules_active_tier_uq (23505).
func (q *Queries) InsertSmartPriceRule(ctx context.Context, p InsertSmartPriceRuleParams) (SmartPriceRuleRow, error) {
	steps := p.StepTierIDs
	if steps == nil {
		steps = []uuid.UUID{}
	}
	return scanSmartPriceRule(q.db.QueryRow(ctx, insertSmartPriceRule,
		p.OrgID, p.SessionID, p.TierID, p.Kind, p.Params, p.BasePrice, steps, p.CreatedBy))
}

const listSmartPriceRulesBySession = `-- name: ListSmartPriceRulesBySession :many
SELECT ` + smartPriceRuleColumns + `
FROM   smart_price_rules
WHERE  session_id = $1 AND org_id = $2
ORDER  BY created_at DESC, id`

// ListSmartPriceRulesBySession lists every preset of one session, newest first.
func (q *Queries) ListSmartPriceRulesBySession(ctx context.Context, sessionID, orgID uuid.UUID) ([]SmartPriceRuleRow, error) {
	rows, err := q.db.Query(ctx, listSmartPriceRulesBySession, sessionID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartPriceRuleRow
	for rows.Next() {
		r, err := scanSmartPriceRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listSmartPriceRulesForSession = `-- name: ListSmartPriceRulesForSession :many
SELECT ` + smartPriceRuleColumns + `
FROM   smart_price_rules
WHERE  session_id = $1`

// ListSmartPriceRulesForSession is the buyer feed's read: which chains of the
// session a preset made (one card per chain, the head's name), whatever the
// preset's status — a stopped preset's steps still carry generated names.
func (q *Queries) ListSmartPriceRulesForSession(ctx context.Context, sessionID uuid.UUID) ([]SmartPriceRuleRow, error) {
	rows, err := q.db.Query(ctx, listSmartPriceRulesForSession, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SmartPriceRuleRow
	for rows.Next() {
		r, err := scanSmartPriceRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getActiveSmartPriceRule = `-- name: GetActiveSmartPriceRule :one
SELECT ` + smartPriceRuleColumns + `
FROM   smart_price_rules
WHERE  id = $1 AND org_id = $2 AND status = 'active'
FOR UPDATE`

// GetActiveSmartPriceRule locks one active preset of the organization.
func (q *Queries) GetActiveSmartPriceRule(ctx context.Context, id, orgID uuid.UUID) (SmartPriceRuleRow, error) {
	return scanSmartPriceRule(q.db.QueryRow(ctx, getActiveSmartPriceRule, id, orgID))
}

const stopSmartPriceRule = `-- name: StopSmartPriceRule :exec
UPDATE smart_price_rules
SET    status = 'stopped', stopped_at = now(), updated_at = now()
WHERE  id = $1 AND status = 'active'`

// StopSmartPriceRule marks a preset stopped by the organizer.
func (q *Queries) StopSmartPriceRule(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, stopSmartPriceRule, id)
	return err
}

const overrideSmartPriceRulesForTier = `-- name: OverrideSmartPriceRulesForTier :many
UPDATE smart_price_rules
SET    status = 'overridden', stopped_at = now(), updated_at = now()
WHERE  status = 'active' AND (tier_id = $1 OR $1 = ANY(step_tier_ids))
RETURNING kind, tier_id, step_tier_ids`

// The price stops rising after a hand edit: the later steps of a chain take
// the edited price, and a schedule keeps the window in force now, open-ended.
const endSmartStepPrices = `-- name: EndSmartStepPrices :exec
UPDATE ticket_tiers SET price_amount = $2, updated_at = now()
WHERE  id = ANY($1::uuid[]) AND deleted_at IS NULL`

const endSmartFutureWindows = `-- name: EndSmartFutureWindows :exec
DELETE FROM ticket_tier_prices WHERE tier_id = $1 AND valid_from > now()`

const openSmartCurrentWindow = `-- name: OpenSmartCurrentWindow :exec
UPDATE ticket_tier_prices SET valid_to = NULL, updated_at = now()
WHERE  tier_id = $1 AND valid_from <= now() AND valid_to IS NOT NULL AND valid_to > now()`

// OverrideSmartPriceRulesForTier ends the preset a category belongs to after
// its price was edited by hand (owner decision В-5: the preset is off for the
// FUTURE steps). Steps by tickets sold after the edited one take newPrice (0
// leaves them); a schedule loses its future windows
// and the window in force now stays open-ended, so nothing rises later.
// Answers how many presets it ended (0 when the category follows none).
func (q *Queries) OverrideSmartPriceRulesForTier(ctx context.Context, tierID uuid.UUID, newPrice int64) (int64, error) {
	type ended struct {
		kind  string
		head  uuid.UUID
		steps []uuid.UUID
	}
	rows, err := q.db.Query(ctx, overrideSmartPriceRulesForTier, tierID)
	if err != nil {
		return 0, err
	}
	var out []ended
	for rows.Next() {
		var e ended
		if err := rows.Scan(&e.kind, &e.head, &e.steps); err != nil {
			rows.Close()
			return 0, err
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range out {
		if e.kind == "by_sales" {
			if newPrice <= 0 {
				continue
			}
			chain := append([]uuid.UUID{e.head}, e.steps...)
			for i, id := range chain {
				if id == tierID {
					if later := chain[i+1:]; len(later) > 0 {
						if _, err := q.db.Exec(ctx, endSmartStepPrices, later, newPrice); err != nil {
							return 0, err
						}
					}
				}
			}
			continue
		}
		if _, err := q.db.Exec(ctx, endSmartFutureWindows, e.head); err != nil {
			return 0, err
		}
		if _, err := q.db.Exec(ctx, openSmartCurrentWindow, e.head); err != nil {
			return 0, err
		}
	}
	return int64(len(out)), nil
}
