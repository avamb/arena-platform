// Package usage projects the billing events onto the legacy usage_records
// table (billing wave B-A, task B2-6, spec 41 section 6.11): the old route
// GET /v1/organizations/{org_id}/billing/usage keeps its shape and now shows
// numbers that come from billable_events instead of from usage hooks nobody
// calls.
//
// The projection is a SET, never an increment: every pass recounts the
// current and the previous calendar month (UTC, the legacy billing_period) and
// writes the counts, so it is idempotent, a late event is picked up on the
// next pass, and nothing here can drift from the events. A row is touched only
// when a number changed.
//
// usage_records is not an append-only billing fact (it is the legacy counter
// table), so this package writes it freely; the billing facts themselves are
// only read.
package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configures a Projector.
type Options struct {
	// Now overrides the clock (tests).
	Now func() time.Time
	// OrgFilter, when non-empty, limits the projection to these organizations
	// (tests that share a database with other packages).
	OrgFilter []uuid.UUID
}

// Projector recounts usage_records from billable_events.
type Projector struct {
	pool *pgxpool.Pool
	opts Options
}

// New builds a Projector over a pool.
func New(pool *pgxpool.Pool, opts Options) *Projector {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Projector{pool: pool, opts: opts}
}

const projectSQL = `
INSERT INTO usage_records (org_id, billing_period, tickets_sold, complimentary_issued, events_published, tickets_refunded)
SELECT be.org_id,
       to_char(be.occurred_at AT TIME ZONE 'UTC', 'YYYY-MM'),
       (count(*) FILTER (WHERE be.kind = 'ticket_sold'))::bigint,
       (count(*) FILTER (WHERE be.kind = 'ticket_complimentary'))::bigint,
       (count(*) FILTER (WHERE be.kind = 'event_published'))::bigint,
       (count(*) FILTER (WHERE be.kind = 'ticket_refunded'))::bigint
FROM   billable_events be
WHERE  be.occurred_at >= $1 AND be.occurred_at < $2
  AND  ($3::uuid[] IS NULL OR be.org_id = ANY ($3))
GROUP  BY 1, 2
ON CONFLICT (org_id, billing_period) DO UPDATE SET
       tickets_sold         = EXCLUDED.tickets_sold,
       complimentary_issued = EXCLUDED.complimentary_issued,
       events_published     = EXCLUDED.events_published,
       tickets_refunded     = EXCLUDED.tickets_refunded,
       updated_at           = now()
WHERE (usage_records.tickets_sold, usage_records.complimentary_issued,
       usage_records.events_published, usage_records.tickets_refunded)
      IS DISTINCT FROM
      (EXCLUDED.tickets_sold, EXCLUDED.complimentary_issued,
       EXCLUDED.events_published, EXCLUDED.tickets_refunded)`

// Run recounts the previous and the current month and returns the number of
// usage_records rows it inserted or changed.
func (p *Projector) Run(ctx context.Context) (int64, error) {
	now := p.opts.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	from, to := thisMonth.AddDate(0, -1, 0), thisMonth.AddDate(0, 1, 0)
	var orgs []uuid.UUID
	if len(p.opts.OrgFilter) > 0 {
		orgs = p.opts.OrgFilter
	}
	tag, err := p.pool.Exec(ctx, projectSQL, from, to, orgs)
	if err != nil {
		return 0, fmt.Errorf("billing usage projection: %w", err)
	}
	return tag.RowsAffected(), nil
}
