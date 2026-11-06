// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: org_modules.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrgModulePeriodRow is one org_module_periods row (migration 0198).
// StartsOn and EndsOn are UTC calendar days, both inclusive; a nil EndsOn has
// no end. MonthlyPriceMinor is the individual monthly price (nil = the price
// list, 0 = free) in Currency (both nil or both set).
type OrgModulePeriodRow struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	Module            string     `json:"module"`
	StartsOn          time.Time  `json:"starts_on"`
	EndsOn            *time.Time `json:"ends_on"`
	MonthlyPriceMinor *int64     `json:"monthly_price_minor"`
	Currency          *string    `json:"currency"`
	Reason            string     `json:"reason"`
	CreatedBy         *uuid.UUID `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	EndedBy           *uuid.UUID `json:"ended_by"`
	EndedAt           *time.Time `json:"ended_at"`
}

const orgModulePeriodColumns = `m.id, m.org_id, m.module, m.starts_on, m.ends_on, m.monthly_price_minor,
       m.currency, m.reason, m.created_by, m.created_at, m.ended_by, m.ended_at`

func scanOrgModulePeriod(row interface{ Scan(...any) error }) (OrgModulePeriodRow, error) {
	var r OrgModulePeriodRow
	err := row.Scan(&r.ID, &r.OrgID, &r.Module, &r.StartsOn, &r.EndsOn, &r.MonthlyPriceMinor,
		&r.Currency, &r.Reason, &r.CreatedBy, &r.CreatedAt, &r.EndedBy, &r.EndedAt)
	return r, err
}

// openShopOrderPredicate is THE definition of an OPEN shop order, over the
// alias `so` of shop_orders: awaiting payment or in manual review, or paid
// (also partially refunded) and not yet shipped. A delivery_kind of 'none'
// (digital goods, services) has nothing to ship, so such an order is closed
// once paid. Migration 0198's partial index shop_orders_open_idx repeats this
// text; keep the two identical.
const openShopOrderPredicate = `so.status IN ('pending_payment', 'manual_review')
       OR (so.status IN ('paid', 'partially_refunded') AND so.shipped_at IS NULL AND so.delivery_kind <> 'none')`

const lockOrgModule = `-- name: LockOrgModule :exec
SELECT pg_advisory_xact_lock(hashtextextended('org_module:' || $1::text || ':' || $2::text, 0))`

// LockOrgModule serializes the writes of one (organization, module)'s periods
// for the rest of the transaction, so the overlap check and the insert cannot
// race.
func (q *Queries) LockOrgModule(ctx context.Context, orgID uuid.UUID, module string) error {
	_, err := q.db.Exec(ctx, lockOrgModule, orgID, module)
	return err
}

const listOrgModulePeriodsOverlappingForUpdate = `-- name: ListOrgModulePeriodsOverlappingForUpdate :many
SELECT ` + orgModulePeriodColumns + `
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  m.module = $2
  AND  (m.ends_on IS NULL OR m.ends_on >= m.starts_on)
  AND  m.starts_on <= COALESCE($4::date, 'infinity'::date)
  AND  COALESCE(m.ends_on, 'infinity'::date) >= $3::date
  AND  ($5::uuid IS NULL OR m.id <> $5)
ORDER  BY m.starts_on, m.id
FOR UPDATE`

// ListOrgModulePeriodsOverlappingForUpdate returns, row-locked, the non-empty
// periods of (org, module) that share a day with [startsOn, endsOn] (nil
// endsOn: no end); exceptID leaves one period out (a period being replaced).
func (q *Queries) ListOrgModulePeriodsOverlappingForUpdate(ctx context.Context, orgID uuid.UUID, module string, startsOn time.Time, endsOn *time.Time, exceptID *uuid.UUID) ([]OrgModulePeriodRow, error) {
	rows, err := q.db.Query(ctx, listOrgModulePeriodsOverlappingForUpdate, orgID, module, startsOn, endsOn, exceptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgModulePeriodRow
	for rows.Next() {
		r, err := scanOrgModulePeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const insertOrgModulePeriod = `-- name: InsertOrgModulePeriod :one
INSERT INTO org_module_periods AS m (org_id, module, starts_on, ends_on, monthly_price_minor, currency, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ` + orgModulePeriodColumns

// InsertOrgModulePeriodParams are the columns of a new period.
type InsertOrgModulePeriodParams struct {
	OrgID             uuid.UUID
	Module            string
	StartsOn          time.Time
	EndsOn            *time.Time
	MonthlyPriceMinor *int64
	Currency          *string
	Reason            string
	CreatedBy         *uuid.UUID
}

// InsertOrgModulePeriod writes a new period.
func (q *Queries) InsertOrgModulePeriod(ctx context.Context, p InsertOrgModulePeriodParams) (OrgModulePeriodRow, error) {
	return scanOrgModulePeriod(q.db.QueryRow(ctx, insertOrgModulePeriod,
		p.OrgID, p.Module, p.StartsOn, p.EndsOn, p.MonthlyPriceMinor, p.Currency, p.Reason, p.CreatedBy))
}

const getOrgModulePeriod = `-- name: GetOrgModulePeriod :one
SELECT ` + orgModulePeriodColumns + `
FROM   org_module_periods m
WHERE  m.id = $1`

// GetOrgModulePeriod returns one period, pgx.ErrNoRows when there is none.
func (q *Queries) GetOrgModulePeriod(ctx context.Context, id uuid.UUID) (OrgModulePeriodRow, error) {
	return scanOrgModulePeriod(q.db.QueryRow(ctx, getOrgModulePeriod, id))
}

const getOrgModulePeriodForUpdate = `-- name: GetOrgModulePeriodForUpdate :one
SELECT ` + orgModulePeriodColumns + `
FROM   org_module_periods m
WHERE  m.id = $1
FOR UPDATE`

// GetOrgModulePeriodForUpdate is GetOrgModulePeriod with a row lock.
func (q *Queries) GetOrgModulePeriodForUpdate(ctx context.Context, id uuid.UUID) (OrgModulePeriodRow, error) {
	return scanOrgModulePeriod(q.db.QueryRow(ctx, getOrgModulePeriodForUpdate, id))
}

const endOrgModulePeriod = `-- name: EndOrgModulePeriod :one
UPDATE org_module_periods AS m
SET    ends_on = $2, ended_by = $3, ended_at = now()
WHERE  m.id = $1
  AND  (m.ends_on IS NULL OR m.ends_on > $2::date)
RETURNING ` + orgModulePeriodColumns

// EndOrgModulePeriod moves the end of a period EARLIER (or sets one);
// pgx.ErrNoRows when the period is unknown or already ends on or before
// endsOn. A period's end is never pushed later.
func (q *Queries) EndOrgModulePeriod(ctx context.Context, id uuid.UUID, endsOn time.Time, endedBy *uuid.UUID) (OrgModulePeriodRow, error) {
	return scanOrgModulePeriod(q.db.QueryRow(ctx, endOrgModulePeriod, id, endsOn, endedBy))
}

const listOrgModulePeriods = `-- name: ListOrgModulePeriods :many
SELECT ` + orgModulePeriodColumns + `
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  ($2::text IS NULL OR m.module = $2)
ORDER  BY m.module, m.starts_on, m.id`

// ListOrgModulePeriods lists the periods of one organization, optionally of
// one module (nil: all), oldest first per module.
func (q *Queries) ListOrgModulePeriods(ctx context.Context, orgID uuid.UUID, module *string) ([]OrgModulePeriodRow, error) {
	rows, err := q.db.Query(ctx, listOrgModulePeriods, orgID, module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgModulePeriodRow
	for rows.Next() {
		r, err := scanOrgModulePeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const orgModuleActive = `-- name: OrgModuleActive :one
SELECT EXISTS (
    SELECT 1 FROM org_module_periods m
    WHERE  m.org_id = $1
      AND  m.module = $2
      AND  m.starts_on <= $3::date
      AND  (m.ends_on IS NULL OR m.ends_on >= $3::date))`

// OrgModuleActive reports whether a period of (org, module) covers day.
func (q *Queries) OrgModuleActive(ctx context.Context, orgID uuid.UUID, module string, day time.Time) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, orgModuleActive, orgID, module, day).Scan(&ok)
	return ok, err
}

const activeOrgModules = `-- name: ActiveOrgModules :many
SELECT DISTINCT m.module
FROM   org_module_periods m
WHERE  m.org_id = $1
  AND  m.starts_on <= $2::date
  AND  (m.ends_on IS NULL OR m.ends_on >= $2::date)
ORDER  BY m.module`

// ActiveOrgModules lists, sorted, the modules an organization has on day.
func (q *Queries) ActiveOrgModules(ctx context.Context, orgID uuid.UUID, day time.Time) ([]string, error) {
	rows, err := q.db.Query(ctx, activeOrgModules, orgID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OrgModuleStateRow is what OrgModuleStates answers for one organization.
type OrgModuleStateRow struct {
	OrgID          uuid.UUID `json:"org_id"`
	Modules        []string  `json:"modules"`
	OpenShopOrders bool      `json:"open_shop_orders"`
	// PaidShopOrders: the organization has at least one shop order that was
	// ever paid (paid_at set), however closed it is now. Served by the partial
	// index shop_orders_paid_idx (migration 0198).
	PaidShopOrders bool `json:"paid_shop_orders"`
}

const orgModuleStates = `-- name: OrgModuleStates :many
SELECT o.id,
       COALESCE(array_agg(DISTINCT m.module ORDER BY m.module) FILTER (WHERE m.module IS NOT NULL), '{}')::text[],
       EXISTS (SELECT 1 FROM shop_orders so WHERE so.org_id = o.id AND (` + openShopOrderPredicate + `)),
       EXISTS (SELECT 1 FROM shop_orders so WHERE so.org_id = o.id AND so.paid_at IS NOT NULL)
FROM   unnest($1::uuid[]) AS o(id)
LEFT   JOIN org_module_periods m
       ON  m.org_id = o.id AND m.starts_on <= $2::date AND (m.ends_on IS NULL OR m.ends_on >= $2::date)
GROUP  BY o.id`

// OrgModuleStates answers, in ONE round trip, for each organization in
// orgIDs: its modules active on day, whether it has an open shop order and
// whether it ever had a paid one. An organization id that does not exist still
// gets a row (no modules, no orders). The /v1/me handler uses it for every membership at once.
func (q *Queries) OrgModuleStates(ctx context.Context, orgIDs []uuid.UUID, day time.Time) ([]OrgModuleStateRow, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, orgModuleStates, orgIDs, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgModuleStateRow
	for rows.Next() {
		var r OrgModuleStateRow
		if err := rows.Scan(&r.OrgID, &r.Modules, &r.OpenShopOrders, &r.PaidShopOrders); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const countOpenShopOrders = `-- name: CountOpenShopOrders :one
SELECT count(*) FROM shop_orders so WHERE so.org_id = $1 AND (` + openShopOrderPredicate + `)`

// CountOpenShopOrders counts the organization's OPEN shop orders (see
// openShopOrderPredicate).
func (q *Queries) CountOpenShopOrders(ctx context.Context, orgID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOpenShopOrders, orgID).Scan(&n)
	return n, err
}
