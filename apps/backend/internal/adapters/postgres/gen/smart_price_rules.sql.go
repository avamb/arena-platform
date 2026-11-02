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

const listActiveSmartPriceRulesForSession = `-- name: ListActiveSmartPriceRulesForSession :many
SELECT ` + smartPriceRuleColumns + `
FROM   smart_price_rules
WHERE  session_id = $1 AND status = 'active'`

// ListActiveSmartPriceRulesForSession is the buyer feed's read: which chains
// of the session a preset drives (one card per chain, the head's name).
func (q *Queries) ListActiveSmartPriceRulesForSession(ctx context.Context, sessionID uuid.UUID) ([]SmartPriceRuleRow, error) {
	rows, err := q.db.Query(ctx, listActiveSmartPriceRulesForSession, sessionID)
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

const overrideSmartPriceRulesForTier = `-- name: OverrideSmartPriceRulesForTier :execrows
UPDATE smart_price_rules
SET    status = 'overridden', stopped_at = now(), updated_at = now()
WHERE  status = 'active' AND (tier_id = $1 OR $1 = ANY(step_tier_ids))`

// OverrideSmartPriceRulesForTier ends the preset a category belongs to after
// its price was edited by hand (owner decision В-5). Answers how many presets
// it ended (0 when the category follows none).
func (q *Queries) OverrideSmartPriceRulesForTier(ctx context.Context, tierID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, overrideSmartPriceRulesForTier, tierID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
