// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: smart price presets (migration 0190, package smartprice)

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

const smartChainHandedOver = `-- name: SmartChainHandedOver :many
SELECT tier_id FROM ticket_tier_chain
WHERE  tier_id = ANY($1::uuid[]) AND handed_over_at IS NOT NULL`

const smartTierPrice = `-- name: SmartTierPrice :one
SELECT price_amount FROM ticket_tiers WHERE id = $1`

const endSmartStepPrices = `-- name: EndSmartStepPrices :exec
UPDATE ticket_tiers SET price_amount = $2, updated_at = now()
WHERE  id = ANY($1::uuid[]) AND deleted_at IS NULL`

const listSmartWindows = `-- name: ListSmartWindows :many
SELECT id, valid_from, valid_to, price_amount,
       valid_from > now() AS future,
       valid_from <= now() AND (valid_to IS NULL OR valid_to > now()) AS current
FROM   ticket_tier_prices WHERE tier_id = $1 ORDER BY valid_from`

const deleteSmartWindows = `-- name: DeleteSmartWindows :exec
DELETE FROM ticket_tier_prices WHERE id = ANY($1::uuid[])`

const setSmartWindowEnd = `-- name: SetSmartWindowEnd :exec
UPDATE ticket_tier_prices SET valid_to = $2, updated_at = now() WHERE id = $1`

// SmartOverride says what a hand edit of a category's price changed.
type SmartOverride struct {
	// TierID is the category whose price or schedule was set by hand.
	TierID uuid.UUID
	// ScheduleTyped is set when the edit wrote the category's schedule itself
	// (price-schedule PUT, pricing-bulk, an import). Only the future windows
	// that equal one of Before are then the preset's and are removed; a
	// window the person typed stays. Unset (a base price edit) removes every
	// future window of a schedule preset.
	ScheduleTyped bool
	// Before are the category's windows before the edit.
	Before []TicketTierPriceRow
	// PricesBefore, when set, are the chain steps' prices before an edit
	// that wrote every step itself (an import): a step whose price the edit
	// changed keeps it, only an untouched step takes the frozen price.
	PricesBefore map[uuid.UUID]int64
}

// OverrideSmartPriceRulesForTier ends the preset a category belongs to after
// its price was edited by hand (owner decision В-5: the price stops rising).
//
//   - A chain by tickets sold: the steps after the edited one, or after the
//     step selling now when the edited one already handed its places on,
//     take the price of that step. The step selling now never changes unless
//     it is the one edited, and no place moves.
//   - A schedule: the preset's future windows are removed (see
//     SmartOverride.ScheduleTyped) and the window in force now runs on to the
//     next window left, or open-ended, so nothing rises later.
//
// Answers how many presets it ended (0 when the category follows none).
func (q *Queries) OverrideSmartPriceRulesForTier(ctx context.Context, o SmartOverride) (int64, error) {
	type ended struct {
		kind  string
		head  uuid.UUID
		steps []uuid.UUID
	}
	rows, err := q.db.Query(ctx, overrideSmartPriceRulesForTier, o.TierID)
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
			if err := q.freezeSmartChain(ctx, append([]uuid.UUID{e.head}, e.steps...), o); err != nil {
				return 0, err
			}
			continue
		}
		if err := q.endSmartWindows(ctx, e.head, o); err != nil {
			return 0, err
		}
	}
	return int64(len(out)), nil
}

// freezeSmartChain gives the steps after max(edited, selling) the price of
// that step.
func (q *Queries) freezeSmartChain(ctx context.Context, chain []uuid.UUID, o SmartOverride) error {
	idx := -1
	for i, id := range chain {
		if id == o.TierID {
			idx = i
		}
	}
	rows, err := q.db.Query(ctx, smartChainHandedOver, chain)
	if err != nil {
		return err
	}
	handed := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		handed[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sel := len(chain) - 1
	for i, id := range chain {
		if !handed[id] {
			sel = i
			break
		}
	}
	from := max(idx, sel)
	if from < 0 || from+1 >= len(chain) {
		return nil
	}
	var price int64
	if err := q.db.QueryRow(ctx, smartTierPrice, chain[from]).Scan(&price); err != nil {
		return err
	}
	later := chain[from+1:]
	if o.PricesBefore != nil {
		var keep []uuid.UUID
		for _, id := range later {
			var now int64
			if err := q.db.QueryRow(ctx, smartTierPrice, id).Scan(&now); err != nil {
				return err
			}
			if before, ok := o.PricesBefore[id]; ok && before == now {
				keep = append(keep, id)
			}
		}
		later = keep
	}
	if len(later) == 0 {
		return nil
	}
	_, err = q.db.Exec(ctx, endSmartStepPrices, later, price)
	return err
}

// endSmartWindows removes a schedule preset's future windows and lets the
// window in force now run on to the next window left.
func (q *Queries) endSmartWindows(ctx context.Context, tierID uuid.UUID, o SmartOverride) error {
	type win struct {
		id              uuid.UUID
		from            time.Time
		to              *time.Time
		price           int64
		future, current bool
	}
	rows, err := q.db.Query(ctx, listSmartWindows, tierID)
	if err != nil {
		return err
	}
	var all []win
	for rows.Next() {
		var w win
		if err := rows.Scan(&w.id, &w.from, &w.to, &w.price, &w.future, &w.current); err != nil {
			rows.Close()
			return err
		}
		all = append(all, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	preset := func(w win) bool {
		if !o.ScheduleTyped {
			return true
		}
		for _, b := range o.Before {
			sameEnd := (b.ValidTo == nil && w.to == nil) || (b.ValidTo != nil && w.to != nil && b.ValidTo.Equal(*w.to))
			if b.ValidFrom.Equal(w.from) && sameEnd && b.PriceAmount == w.price {
				return true
			}
		}
		return false
	}
	var drop []uuid.UUID
	var cur *win
	var left []win
	for i := range all {
		w := all[i]
		switch {
		case w.future && preset(w):
			drop = append(drop, w.id)
		default:
			if w.current {
				cur = &all[i]
			}
			left = append(left, w)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	if _, err := q.db.Exec(ctx, deleteSmartWindows, drop); err != nil {
		return err
	}
	if cur == nil || cur.to == nil {
		return nil
	}
	var next *time.Time
	for _, w := range left {
		if !w.from.Before(*cur.to) && (next == nil || w.from.Before(*next)) {
			from := w.from
			next = &from
		}
	}
	if next != nil && next.Equal(*cur.to) {
		return nil
	}
	_, err = q.db.Exec(ctx, setSmartWindowEnd, cur.id, next)
	return err
}

const lockSmartSession = `-- name: LockSmartSession :exec
SELECT id FROM sessions WHERE id = $1 FOR UPDATE`

const lockSmartTiers = `-- name: LockSmartTiers :exec
SELECT id FROM ticket_tiers WHERE session_id = $1 ORDER BY id FOR UPDATE`

// LockSmartSession locks the session row first (the repository's lock
// order), then its categories in id order, so nothing a smart price import
// re-posts can change until the transaction ends.
func (q *Queries) LockSmartSession(ctx context.Context, sessionID uuid.UUID) error {
	if _, err := q.db.Exec(ctx, lockSmartSession, sessionID); err != nil {
		return err
	}
	_, err := q.db.Exec(ctx, lockSmartTiers, sessionID)
	return err
}

const smartSessionFingerprint = `-- name: SmartSessionFingerprint :one
SELECT md5(coalesce(string_agg(x, '|' ORDER BY x), '')) FROM (
    SELECT concat_ws(':', 's', s.event_id, s.venue_id, s.start_at, s.end_at, s.sales_end_at,
                     s.doors_open_at, s.status, s.deleted_at, s.capacity_total, s.capacity_override) AS x
    FROM   sessions s WHERE s.id = $1
    UNION ALL
    SELECT concat_ws(':', 'e', e.updated_at)
    FROM   events e JOIN sessions s ON s.event_id = e.id WHERE s.id = $1
    UNION ALL
    SELECT concat_ws(':', 't', t.id, t.updated_at, t.deleted_at, t.price_amount, t.capacity, t.is_open)
    FROM   ticket_tiers t WHERE t.session_id = $1
    UNION ALL
    SELECT concat_ws(':', 'p', p.id, p.updated_at)
    FROM   ticket_tier_prices p JOIN ticket_tiers t ON t.id = p.tier_id WHERE t.session_id = $1
    UNION ALL
    SELECT concat_ws(':', 'c', c.tier_id, c.next_tier_id, c.sell_limit, c.handed_over_at, c.updated_at)
    FROM   ticket_tier_chain c JOIN ticket_tiers t ON t.id = c.tier_id WHERE t.session_id = $1
    UNION ALL
    SELECT concat_ws(':', 'q', u.tier_id, count(*))
    FROM   session_seats u WHERE u.session_id = $1 AND u.kind = 'ga_unit' GROUP BY u.tier_id
    UNION ALL
    SELECT concat_ws(':', 'r', r.id, r.status, r.updated_at)
    FROM   smart_price_rules r WHERE r.session_id = $1
) f`

// SmartSessionFingerprint digests what a smart price import reads and
// re-posts: the session's times and venue, the event, every category with
// its windows, chain links and place count, and the presets. Sales and holds
// do not change it (they move no place between categories), so a busy
// session does not keep failing the check.
func (q *Queries) SmartSessionFingerprint(ctx context.Context, sessionID uuid.UUID) (string, error) {
	var out string
	err := q.db.QueryRow(ctx, smartSessionFingerprint, sessionID).Scan(&out)
	return out, err
}
