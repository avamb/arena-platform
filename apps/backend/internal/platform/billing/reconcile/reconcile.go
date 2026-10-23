// Package reconcile is the daily integrity check of the billing pipeline
// (billing wave B-A, task B2-5, spec 41 section 6.10): it compares the sales
// tables with the billing facts and reports every gap. It is READ-ONLY on
// every table; the only things it writes are metrics and ops alerts (through
// the ops-watchdog alert engine).
//
// What it checks, all from the date X (Options.Since) up to a short horizon
// before now, so a fact that is still on its way is not a finding:
//
//   - order_items_without_event: a ticket line of a paid order has no
//     ticket_sold / ticket_complimentary event;
//   - tickets_without_event: an active ticket has no such event (this catches
//     a ticket whose order line the first check cannot see, and invitations);
//   - events_without_charge: an event collected long ago still has no direct
//     charge (rate could not price it: no terms, a rule not implemented, a
//     broken price list);
//   - reversal_mismatch: a reversal charge that is not the exact negative of
//     the charge it reverses (amount, currency or event differ);
//   - collect_lag / rate_lag: the pipeline is behind.
//
// Nothing in a report or an alert carries personal data: counts, organization
// ids, and the ids of billing facts only.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

// Check names. They are the metric label values and the alert fingerprints.
const (
	CheckOrderItemsWithoutEvent = "order_items_without_event"
	CheckTicketsWithoutEvent    = "tickets_without_event"
	CheckEventsWithoutCharge    = "events_without_charge"
	CheckReversalMismatch       = "reversal_mismatch"
	CheckCollectLag             = "collect_lag"
	CheckRateLag                = "rate_lag"
	CheckCreditJournal          = "credit_journal_mismatch"
	CheckCreditAllocations      = "credit_allocation_mismatch"
)

// Thresholds. Plain constants: they do not differ per deployment.
const (
	// DefaultGrace is how long a sales fact may take to reach the billing
	// tables before its absence is a finding. The collect cursor itself stays
	// 10 minutes behind the present, so an hour leaves a wide margin.
	DefaultGrace = time.Hour
	// UnratedAfter is how long an event may stay without a direct charge,
	// counted from the moment it was collected, before it is a defect rather
	// than lag.
	UnratedAfter = 6 * time.Hour
	// CollectLagAlert is the age of the oldest collect cursor that raises
	// collect_lag. A healthy cursor trails the present by about 10 minutes
	// plus the collect interval.
	CollectLagAlert = 45 * time.Minute
	// RateLagAlert is the age (since collection) of the oldest event without a
	// charge that raises rate_lag.
	RateLagAlert = 30 * time.Minute
	// DefaultInterval is the gap between daily runs.
	DefaultInterval = 24 * time.Hour

	topOrgs    = 10
	sampleIDs  = 3
	fpPrefix   = "billing_reconcile"
	collectKey = "collect."
)

// Finding is one check that found something.
type Finding struct {
	Check string `json:"check"`
	// Count is the number of facts out of order (or, for a lag, the number of
	// stages/cursors behind).
	Count int64 `json:"count"`
	// PerOrg is the top organizations by count ("org id": count).
	PerOrg map[uuid.UUID]int64 `json:"per_org,omitempty"`
	// Sample is a few ids of the facts concerned (order lines, tickets,
	// events, charges - never a person).
	Sample []uuid.UUID `json:"sample,omitempty"`
	// LagSeconds is set by the lag checks.
	LagSeconds int64 `json:"lag_seconds,omitempty"`
	// Note is a fixed-vocabulary hint for the operator.
	Note string `json:"note,omitempty"`
}

// Report is the result of one run. Findings is empty when everything agrees.
type Report struct {
	CheckedAt time.Time `json:"checked_at"`
	Since     time.Time `json:"since"`
	Horizon   time.Time `json:"horizon"`
	// Counts holds the number of facts examined per check, so "no findings"
	// can be told from "nothing to look at".
	Counts   map[string]int64 `json:"counts"`
	Findings []Finding        `json:"findings"`
}

// Metrics receives the numbers of a run. *observability.Metrics satisfies it.
type Metrics interface {
	SetBillingFindings(check string, n int64)
	SetBillingLag(stage string, seconds int64)
}

// Alerter raises and resolves ops alerts. *opswatchdog.AlertEngine satisfies
// it.
type Alerter interface {
	Sync(ctx context.Context, fingerprintPrefix string, current []opswatchdog.AlertInput, titleForResolve func(fingerprint string) string) error
}

// Options configures a Reconciler.
type Options struct {
	// Since is the date X. Required: without it billing is off and there is
	// nothing to reconcile.
	Since time.Time
	// Grace overrides DefaultGrace.
	Grace time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
	// Metrics and Alerter are optional; a nil one is skipped.
	Metrics Metrics
	Alerter Alerter
	// OrgFilter, when non-empty, limits every check to these organizations
	// (tests that share a database with other packages).
	OrgFilter []uuid.UUID
	// CursorPrefix is the prefix of the collect cursors; "" uses "collect."
	// (tests that run a collector under another prefix).
	CursorPrefix string
	Logger       *slog.Logger
}

// Reconciler runs the checks.
type Reconciler struct {
	pool *pgxpool.Pool
	opts Options
}

// New builds a Reconciler over a pool.
func New(pool *pgxpool.Pool, opts Options) *Reconciler {
	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.CursorPrefix == "" {
		opts.CursorPrefix = collectKey
	}
	return &Reconciler{pool: pool, opts: opts}
}

// ErrNoSince means the date X is unset.
var ErrNoSince = errors.New("billing reconcile: the date X (BILLING_COLLECT_SINCE) is not set")

// Run performs every check, publishes metrics and syncs alerts, and returns
// the report. A check that cannot run (a database error) fails the run; the
// checks already done are still in the report returned beside the error.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	rep := Report{Counts: map[string]int64{}}
	if r.opts.Since.IsZero() {
		return rep, ErrNoSince
	}
	now := r.opts.Now()
	rep.CheckedAt = now
	rep.Since = r.opts.Since
	rep.Horizon = now.Add(-r.opts.Grace)

	steps := []struct {
		name string
		fn   func(context.Context, *Report) error
	}{
		{CheckOrderItemsWithoutEvent, r.orderItemsWithoutEvent},
		{CheckTicketsWithoutEvent, r.ticketsWithoutEvent},
		{CheckEventsWithoutCharge, r.eventsWithoutCharge},
		{CheckReversalMismatch, r.reversalMismatch},
		{CheckCollectLag, r.collectLag},
		{CheckRateLag, r.rateLag},
		{CheckCreditJournal, r.creditJournal},
		{CheckCreditAllocations, r.creditAllocations},
	}
	var firstErr error
	for _, st := range steps {
		if err := st.fn(ctx, &rep); err != nil {
			r.opts.Logger.Error("billing reconcile check failed", "check", st.name, "error", err.Error())
			if firstErr == nil {
				firstErr = fmt.Errorf("billing reconcile %s: %w", st.name, err)
			}
		}
	}
	if firstErr != nil {
		return rep, firstErr
	}
	r.publish(ctx, rep)
	return rep, r.syncAlerts(ctx, rep)
}

func (r *Reconciler) orgArg() []uuid.UUID {
	if len(r.opts.OrgFilter) == 0 {
		return nil
	}
	return r.opts.OrgFilter
}

// grouped runs a query that returns, per organization, (org_id, n, total,
// sample ids) - the shape every gap check shares - and turns it into a
// Finding (nil when total is 0). $1 since, $2 horizon, $3 org filter (NULL =
// all) are the arguments of every such query.
func (r *Reconciler) grouped(ctx context.Context, check, query, note string, rep *Report) error {
	rows, err := r.pool.Query(ctx, query, r.opts.Since, rep.Horizon, r.orgArg())
	if err != nil {
		return err
	}
	defer rows.Close()
	f := Finding{Check: check, Note: note, PerOrg: map[uuid.UUID]int64{}}
	for rows.Next() {
		var org uuid.UUID
		var n, total int64
		var sample []uuid.UUID
		if err := rows.Scan(&org, &n, &total, &sample); err != nil {
			return err
		}
		f.Count = total
		f.PerOrg[org] = n
		for _, id := range sample {
			if len(f.Sample) < sampleIDs {
				f.Sample = append(f.Sample, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if f.Count > 0 {
		rep.Findings = append(rep.Findings, f)
	}
	return nil
}

func (r *Reconciler) examined(ctx context.Context, check, query string, rep *Report) error {
	var n int64
	if err := r.pool.QueryRow(ctx, query, r.opts.Since, rep.Horizon, r.orgArg()).Scan(&n); err != nil {
		return err
	}
	rep.Counts[check] = n
	return nil
}

const paidStatuses = `('paid', 'partially_refunded', 'refunded')`

// 1. Every ticket line of an order paid after X has an event.
func (r *Reconciler) orderItemsWithoutEvent(ctx context.Context, rep *Report) error {
	const base = `
FROM   orders o
JOIN   order_items oi ON oi.order_id = o.id AND oi.kind = 'ticket'
WHERE  o.paid_at IS NOT NULL AND o.status IN ` + paidStatuses + `
  AND  o.paid_at >= $1 AND o.paid_at < $2
  AND  ($3::uuid[] IS NULL OR o.org_id = ANY ($3))`
	if err := r.examined(ctx, CheckOrderItemsWithoutEvent, `SELECT count(*)::bigint `+base, rep); err != nil {
		return err
	}
	return r.grouped(ctx, CheckOrderItemsWithoutEvent, `
SELECT org_id, n, (sum(n) OVER ())::bigint, sample FROM (
  SELECT o.org_id, count(*)::bigint AS n, (array_agg(oi.id ORDER BY oi.id))[1:3] AS sample `+base+`
    AND NOT EXISTS (SELECT 1 FROM billable_events be
                    WHERE be.kind IN ('ticket_sold', 'ticket_complimentary') AND be.subject_id = oi.id)
  GROUP BY o.org_id) g
ORDER BY n DESC, org_id LIMIT `+fmt.Sprint(topOrgs),
		"collect has not written the event of a paid ticket line", rep)
}

// 2. Every active ticket issued after X, whose origin is a sale or an
// invitation, has an event.
func (r *Reconciler) ticketsWithoutEvent(ctx context.Context, rep *Report) error {
	const base = `
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events e ON e.id = s.event_id
WHERE  t.status = 'active'
  AND  t.issued_at >= $1 AND t.issued_at < $2
  AND  ($3::uuid[] IS NULL OR e.org_id = ANY ($3))
  AND  ((t.order_id IS NULL AND t.complimentary_issuance_id IS NOT NULL)
        OR EXISTS (SELECT 1 FROM orders o
                   WHERE o.id = t.order_id AND o.paid_at IS NOT NULL
                     AND o.status IN ` + paidStatuses + ` AND o.paid_at >= $1))`
	if err := r.examined(ctx, CheckTicketsWithoutEvent, `SELECT count(*)::bigint `+base, rep); err != nil {
		return err
	}
	return r.grouped(ctx, CheckTicketsWithoutEvent, `
SELECT org_id, n, (sum(n) OVER ())::bigint, sample FROM (
  SELECT e.org_id, count(*)::bigint AS n, (array_agg(t.id ORDER BY t.id))[1:3] AS sample `+base+`
    AND NOT EXISTS (SELECT 1 FROM billable_events be
                    WHERE be.kind IN ('ticket_sold', 'ticket_complimentary')
                      AND (be.ticket_id = t.id
                           OR (t.order_id IS NOT NULL AND be.order_id = t.order_id AND be.ticket_id IS NULL)))
  GROUP BY e.org_id) g
ORDER BY n DESC, org_id LIMIT `+fmt.Sprint(topOrgs),
		"an active ticket after the date X has no sale or invitation event", rep)
}

// 3. Every event collected more than UnratedAfter ago has a direct charge.
func (r *Reconciler) eventsWithoutCharge(ctx context.Context, rep *Report) error {
	cut := rep.CheckedAt.Add(-UnratedAfter)
	rows, err := r.pool.Query(ctx, `
SELECT org_id, n, (sum(n) OVER ())::bigint, sample FROM (
  SELECT be.org_id, count(*)::bigint AS n, (array_agg(be.id ORDER BY be.id))[1:3] AS sample
  FROM   billable_events be
  WHERE  be.occurred_at >= $1 AND be.collected_at < $2
    AND  ($3::uuid[] IS NULL OR be.org_id = ANY ($3))
    AND  NOT EXISTS (SELECT 1 FROM charges c WHERE c.billable_event_id = be.id AND c.reversal_of IS NULL)
  GROUP BY be.org_id) g
ORDER BY n DESC, org_id LIMIT `+fmt.Sprint(topOrgs), r.opts.Since, cut, r.orgArg())
	if err != nil {
		return err
	}
	defer rows.Close()
	f := Finding{Check: CheckEventsWithoutCharge, PerOrg: map[uuid.UUID]int64{},
		Note: "events with no direct charge for more than 6 hours: terms, price list or rule missing"}
	for rows.Next() {
		var org uuid.UUID
		var n, total int64
		var sample []uuid.UUID
		if err := rows.Scan(&org, &n, &total, &sample); err != nil {
			return err
		}
		f.Count = total
		f.PerOrg[org] = n
		for _, id := range sample {
			if len(f.Sample) < sampleIDs {
				f.Sample = append(f.Sample, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var seen int64
	if err := r.pool.QueryRow(ctx, `
SELECT count(*)::bigint FROM billable_events be
WHERE  be.occurred_at >= $1 AND be.collected_at < $2 AND ($3::uuid[] IS NULL OR be.org_id = ANY ($3))`,
		r.opts.Since, cut, r.orgArg()).Scan(&seen); err != nil {
		return err
	}
	rep.Counts[CheckEventsWithoutCharge] = seen
	if f.Count > 0 {
		rep.Findings = append(rep.Findings, f)
	}
	return nil
}

// 4. A reversal is exactly the negative of what it reverses.
func (r *Reconciler) reversalMismatch(ctx context.Context, rep *Report) error {
	var seen int64
	if err := r.pool.QueryRow(ctx, `
SELECT count(*)::bigint FROM charges rv JOIN billable_events be ON be.id = rv.billable_event_id
WHERE  rv.reversal_of IS NOT NULL AND be.occurred_at >= $1 AND ($2::uuid[] IS NULL OR be.org_id = ANY ($2))`,
		r.opts.Since, r.orgArg()).Scan(&seen); err != nil {
		return err
	}
	rep.Counts[CheckReversalMismatch] = seen
	rows, err := r.pool.Query(ctx, `
SELECT be.org_id, rv.id
FROM   charges rv
JOIN   charges o ON o.id = rv.reversal_of
JOIN   billable_events be ON be.id = o.billable_event_id
WHERE  be.occurred_at >= $1 AND ($2::uuid[] IS NULL OR be.org_id = ANY ($2))
  AND  (rv.amount_minor <> -o.amount_minor OR rv.currency <> o.currency
        OR rv.billable_event_id <> o.billable_event_id OR rv.account_id <> o.account_id)
ORDER BY rv.id LIMIT 1000`, r.opts.Since, r.orgArg())
	if err != nil {
		return err
	}
	defer rows.Close()
	f := Finding{Check: CheckReversalMismatch, PerOrg: map[uuid.UUID]int64{},
		Note: "a reversal charge is not the exact negative of the charge it reverses"}
	for rows.Next() {
		var org, id uuid.UUID
		if err := rows.Scan(&org, &id); err != nil {
			return err
		}
		f.Count++
		f.PerOrg[org]++
		if len(f.Sample) < sampleIDs {
			f.Sample = append(f.Sample, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if f.Count > 0 {
		rep.Findings = append(rep.Findings, f)
	}
	return nil
}

// 4a. Credits: every lot's remaining_minor is the signed sum of its journal
// and the remaining_after of its last entry (41 section 6.10). Lots have no
// event date, so the check is not bounded by the date X.
func (r *Reconciler) creditJournal(ctx context.Context, rep *Report) error {
	rows, err := gen.New(r.pool).ListCreditLotMismatches(ctx, r.orgArg(), 1000)
	if err != nil {
		return err
	}
	f := Finding{Check: CheckCreditJournal, PerOrg: map[uuid.UUID]int64{},
		Note: "a credit lot's balance differs from its journal"}
	for _, m := range rows {
		f.add(m.OrgID, m.LotID)
	}
	rep.Counts[CheckCreditJournal] = int64(len(rows))
	if f.Count > 0 {
		rep.Findings = append(rep.Findings, f)
	}
	return nil
}

// 4b. Credits: what the journal consumed from a lot equals the charge
// allocations naming it (41 section 6.10).
func (r *Reconciler) creditAllocations(ctx context.Context, rep *Report) error {
	rows, err := gen.New(r.pool).ListConsumeAllocationMismatches(ctx, r.orgArg(), 1000)
	if err != nil {
		return err
	}
	f := Finding{Check: CheckCreditAllocations, PerOrg: map[uuid.UUID]int64{},
		Note: "credits consumed from a lot differ from the charge allocations naming it"}
	for _, m := range rows {
		f.add(m.OrgID, m.LotID)
	}
	rep.Counts[CheckCreditAllocations] = int64(len(rows))
	if f.Count > 0 {
		rep.Findings = append(rep.Findings, f)
	}
	return nil
}

func (f *Finding) add(org, id uuid.UUID) {
	f.Count++
	f.PerOrg[org]++
	if len(f.Sample) < sampleIDs {
		f.Sample = append(f.Sample, id)
	}
}

// 5. Collect lag: the oldest source cursor against now. A pipeline that never
// ran (no cursor at all) for longer than the lag threshold is behind too.
func (r *Reconciler) collectLag(ctx context.Context, rep *Report) error {
	var oldest *time.Time
	var n int64
	if err := r.pool.QueryRow(ctx,
		`SELECT min(cursor_at), count(*)::bigint FROM billing_job_cursors WHERE key LIKE $1`,
		r.opts.CursorPrefix+"%").Scan(&oldest, &n); err != nil {
		return err
	}
	rep.Counts[CheckCollectLag] = n
	var lag time.Duration
	note := "the oldest collect cursor is far behind the present"
	switch {
	case oldest != nil:
		lag = rep.CheckedAt.Sub(*oldest)
	case rep.CheckedAt.Sub(r.opts.Since) > CollectLagAlert:
		lag = rep.CheckedAt.Sub(r.opts.Since)
		note = "billing.collect has never saved a cursor"
	}
	if lag > CollectLagAlert {
		rep.Findings = append(rep.Findings, Finding{Check: CheckCollectLag, Count: 1, LagSeconds: int64(lag.Seconds()), Note: note})
	}
	rep.Counts["collect_lag_seconds"] = int64(lag.Seconds())
	return nil
}

// 6. Rate lag: how long the oldest event without a direct charge has been
// waiting since it was collected.
func (r *Reconciler) rateLag(ctx context.Context, rep *Report) error {
	var oldest *time.Time
	var waiting int64
	if err := r.pool.QueryRow(ctx, `
SELECT min(be.collected_at), count(*)::bigint
FROM   billable_events be
WHERE  be.occurred_at >= $1 AND ($2::uuid[] IS NULL OR be.org_id = ANY ($2))
  AND  NOT EXISTS (SELECT 1 FROM charges c WHERE c.billable_event_id = be.id AND c.reversal_of IS NULL)`,
		r.opts.Since, r.orgArg()).Scan(&oldest, &waiting); err != nil {
		return err
	}
	rep.Counts[CheckRateLag] = waiting
	if oldest == nil {
		return nil
	}
	lag := rep.CheckedAt.Sub(*oldest)
	rep.Counts["rate_lag_seconds"] = int64(lag.Seconds())
	if lag > RateLagAlert {
		rep.Findings = append(rep.Findings, Finding{Check: CheckRateLag, Count: waiting, LagSeconds: int64(lag.Seconds()),
			Note: "events are waiting for their charge"})
	}
	return nil
}

// publish writes the metrics: every check's count (0 when clean, so a gauge
// falls back after a repair) and the two lags.
func (r *Reconciler) publish(_ context.Context, rep Report) {
	if r.opts.Metrics == nil {
		return
	}
	got := map[string]int64{}
	for _, f := range rep.Findings {
		got[f.Check] = f.Count
	}
	for _, c := range allChecks {
		r.opts.Metrics.SetBillingFindings(c, got[c])
	}
	r.opts.Metrics.SetBillingLag("collect", rep.Counts["collect_lag_seconds"])
	r.opts.Metrics.SetBillingLag("rate", rep.Counts["rate_lag_seconds"])
}

var allChecks = []string{
	CheckOrderItemsWithoutEvent, CheckTicketsWithoutEvent, CheckEventsWithoutCharge,
	CheckReversalMismatch, CheckCollectLag, CheckRateLag,
	CheckCreditJournal, CheckCreditAllocations,
}

// alertInput renders a finding as an alert without personal data.
func alertInput(f Finding, since time.Time) opswatchdog.AlertInput {
	severity := "high"
	if f.Check == CheckCollectLag || f.Check == CheckRateLag {
		severity = "warn"
	}
	details := map[string]any{"count": f.Count, "since": since.Format(time.RFC3339), "note": f.Note}
	if f.LagSeconds > 0 {
		details["lag_minutes"] = f.LagSeconds / 60
	}
	if len(f.PerOrg) > 0 {
		type kv struct {
			org uuid.UUID
			n   int64
		}
		list := make([]kv, 0, len(f.PerOrg))
		for o, n := range f.PerOrg {
			list = append(list, kv{o, n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].n != list[j].n {
				return list[i].n > list[j].n
			}
			return list[i].org.String() < list[j].org.String()
		})
		parts := make([]string, 0, len(list))
		for _, e := range list {
			parts = append(parts, fmt.Sprintf("%s=%d", e.org, e.n))
		}
		details["orgs"] = strings.Join(parts, ", ")
	}
	if len(f.Sample) > 0 {
		ids := make([]string, len(f.Sample))
		for i, id := range f.Sample {
			ids[i] = id.String()
		}
		details["sample"] = strings.Join(ids, ", ")
	}
	return opswatchdog.AlertInput{
		Fingerprint: fpPrefix + ":" + f.Check,
		Severity:    severity,
		Title:       "billing reconcile: " + f.Check,
		Details:     details,
	}
}

func (r *Reconciler) syncAlerts(ctx context.Context, rep Report) error {
	if r.opts.Alerter == nil {
		return nil
	}
	current := make([]opswatchdog.AlertInput, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		current = append(current, alertInput(f, rep.Since))
	}
	return r.opts.Alerter.Sync(ctx, fpPrefix, current, func(fp string) string {
		return "billing reconcile: " + strings.TrimPrefix(fp, fpPrefix+":")
	})
}
