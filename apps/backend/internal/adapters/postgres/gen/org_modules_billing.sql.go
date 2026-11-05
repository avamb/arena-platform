// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: org_modules_billing.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listOrgModulePeriodsForMonth = `-- name: ListOrgModulePeriodsForMonth :many
SELECT ` + orgModulePeriodColumns + `
FROM   org_module_periods m
WHERE  m.module = $1
  AND  m.starts_on <= $3::date
  AND  (m.ends_on IS NULL OR m.ends_on >= $2::date)
  AND  (m.ends_on IS NULL OR m.ends_on >= m.starts_on)
  AND  ($4::uuid IS NULL OR m.org_id = $4)
  AND  NOT EXISTS (SELECT 1 FROM organizations o
                   WHERE o.id = m.org_id AND o.deleted_at < ($2::date)::timestamp AT TIME ZONE 'UTC')
ORDER  BY m.id`

// ListOrgModulePeriodsForMonth returns the non-empty periods of one module
// that cover at least one day of [firstDay, lastDay]; orgID narrows to one
// organization (tests), nil means all. A period of an organization deleted
// before the month starts is not charged for it; the month of the deletion
// still is, for the period's days (like white label).
func (q *Queries) ListOrgModulePeriodsForMonth(ctx context.Context, module string, firstDay, lastDay time.Time, orgID *uuid.UUID) ([]OrgModulePeriodRow, error) {
	rows, err := q.db.Query(ctx, listOrgModulePeriodsForMonth, module, firstDay, lastDay, orgID)
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
