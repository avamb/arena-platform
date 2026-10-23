//go:build integration

// reconcile_integration_test.go — the daily billing reconcile against a live
// database (task B2-5). Connects to DATABASE_URL and skips when it is unset;
// run it on a FRESH migrated database. Every check is limited to the
// fixture's organization (Options.OrgFilter), the collector is scoped to it,
// and its cursors live under a private prefix.
package reconcile_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/collect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/reconcile"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/opswatchdog"
)

// scopedStore narrows every collect source to one organization.
type scopedStore struct {
	*gen.Queries
	org uuid.UUID
}

func (s scopedStore) keep(n int32, rows []gen.CollectCandidate, err error) ([]gen.CollectCandidate, error) {
	var out []gen.CollectCandidate
	for _, r := range rows {
		if r.OrgID == s.org && int32(len(out)) < n {
			out = append(out, r)
		}
	}
	return out, err
}

func (s scopedStore) ListCollectOrderItems(ctx context.Context, at time.Time, id uuid.UUID, n int32, comp bool) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectOrderItems(ctx, at, id, 100000, comp)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectRefunds(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectRefunds(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectCancelledTickets(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectCancelledTickets(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectSessionCancellations(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectSessionCancellations(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectEventPublications(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectEventPublications(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}
func (s scopedStore) ListCollectComplimentaryTickets(ctx context.Context, at time.Time, id uuid.UUID, n int32) ([]gen.CollectCandidate, error) {
	rows, err := s.Queries.ListCollectComplimentaryTickets(ctx, at, id, 100000)
	return s.keep(n, rows, err)
}

type fixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	prefix    string
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	channelID uuid.UUID
	tierID    uuid.UUID
	since     time.Time
	items     []uuid.UUID
	tickets   []uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &fixture{
		t: t, pool: pool, prefix: "reconcile-test-" + uuid.NewString()[:8] + ".",
		orgID: uuid.New(), venueID: uuid.New(), eventID: uuid.New(), sessionID: uuid.New(),
		channelID: uuid.New(), tierID: uuid.New(), since: time.Now().UTC().Add(-48 * time.Hour),
	}
	t.Cleanup(f.cleanup)
	suffix := f.orgID.String()[:8]
	for i, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, []any{f.orgID, "Reconcile Org " + suffix, "reconcile-" + suffix}},
		{`INSERT INTO venues (id, org_id, name, timezone, country) VALUES ($1, $2, $3, 'Europe/Prague', 'CZ')`, []any{f.venueID, f.orgID, "Reconcile Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{f.eventID, f.orgID, "Reconcile Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '20 days', now() + interval '20 days 2 hours', 100, 'scheduled', 'general_admission', 'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode) VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`, []any{f.channelID, f.orgID, "Reconcile Channel " + suffix}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, currency) VALUES ($1, $2, 'General', 'fixed', 'EUR')`, []any{f.tierID, f.sessionID}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("cleanup (%s): %v", sql, err)
		}
	}
	exec(`DELETE FROM ops_alerts WHERE fingerprint LIKE 'billing_reconcile:%'`)
	exec(`DELETE FROM billing_job_cursors WHERE key LIKE $1`, f.prefix+"%")
	exec(`DELETE FROM charge_reversals WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1))`, f.orgID)
	exec(`DELETE FROM charges WHERE reversal_of IS NOT NULL AND account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billable_events WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billing_accounts WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, f.orgID)
	exec(`UPDATE tickets SET order_id = NULL WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM tickets WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM orders WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM checkout_sessions WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM reservations WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID)
	exec(`DELETE FROM sales_channels WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM sessions WHERE id = $1`, f.sessionID)
	exec(`DELETE FROM events WHERE id = $1`, f.eventID)
	exec(`DELETE FROM venues WHERE id = $1`, f.venueID)
	exec(`DELETE FROM organizations WHERE id = $1`, f.orgID)
}

func (f *fixture) must(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixture) count(sql string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// paidOrder inserts a paid order of n GA lines (paid 3 hours ago) with active
// tickets issued at the same time; it returns the lines and the tickets.
func (f *fixture) paidOrder(n int) {
	f.t.Helper()
	ctx := context.Background()
	paidAt := time.Now().UTC().Add(-3 * time.Hour)
	res, err := gen.New(f.pool).InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, int32(n), time.Now().UTC().Add(time.Hour))
	if err != nil {
		f.t.Fatalf("reservation: %v", err)
	}
	csID, orderID := uuid.New(), uuid.New()
	f.must(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
		csID, f.orgID, f.channelID, res.ID)
	f.must(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id, source, status, currency,
	            subtotal, discount, charge, total, paid_at)
	        VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 0, 0, 0, 0, $8)`,
		orderID, f.orgID, f.channelID, f.eventID, f.sessionID, csID, res.ID, paidAt)
	for i := 0; i < n; i++ {
		ticketID, itemID := uuid.New(), uuid.New()
		f.must(`INSERT INTO tickets (id, checkout_session_id, session_id, status, order_id, ordinal, issued_at) VALUES ($1, $2, $3, 'active', $4, $5, $6)`,
			ticketID, csID, f.sessionID, orderID, i+1, paidAt)
		f.must(`INSERT INTO order_items (id, order_id, ordinal, tier_id, ticket_id, unit_price, discount, charge, total)
		        VALUES ($1, $2, $3, $4, $5, 1500, 0, 0, 1500)`, itemID, orderID, i+1, f.tierID, ticketID)
		f.items = append(f.items, itemID)
		f.tickets = append(f.tickets, ticketID)
	}
}

func (f *fixture) collectOnce() {
	f.t.Helper()
	c := collect.New(scopedStore{gen.New(f.pool), f.orgID}, collect.Options{Since: f.since, CursorPrefix: f.prefix})
	if _, err := c.Run(context.Background()); err != nil {
		f.t.Fatalf("collect: %v", err)
	}
}

// rateAll gives every event of the fixture a direct charge (as rate would).
func (f *fixture) rateAll() {
	f.t.Helper()
	f.must(`INSERT INTO charges (billable_event_id, account_id, currency, amount_minor, rule_type)
	        SELECT be.id, be.account_id, 'EUR', 59, 'per_ticket' FROM billable_events be
	        WHERE be.org_id = $1
	          AND NOT EXISTS (SELECT 1 FROM charges c WHERE c.billable_event_id = be.id AND c.reversal_of IS NULL)`, f.orgID)
}

type fakeMetrics struct {
	mu       sync.Mutex
	findings map[string]int64
	lag      map[string]int64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{findings: map[string]int64{}, lag: map[string]int64{}}
}
func (m *fakeMetrics) SetBillingFindings(check string, n int64) {
	m.mu.Lock()
	m.findings[check] = n
	m.mu.Unlock()
}
func (m *fakeMetrics) SetBillingLag(stage string, s int64) {
	m.mu.Lock()
	m.lag[stage] = s
	m.mu.Unlock()
}

// capture is a Notifier that keeps every message.
type capture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *capture) Send(_ context.Context, text string) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, text)
	c.mu.Unlock()
	return nil
}

func (c *capture) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.msgs, "\n---\n")
}

func (f *fixture) reconciler(m reconcile.Metrics, a reconcile.Alerter, now time.Time) *reconcile.Reconciler {
	return reconcile.New(f.pool, reconcile.Options{
		Since: f.since, OrgFilter: []uuid.UUID{f.orgID}, CursorPrefix: f.prefix,
		Metrics: m, Alerter: a, Now: func() time.Time { return now },
	})
}

func findingOf(rep reconcile.Report, check string) (reconcile.Finding, bool) {
	for _, f := range rep.Findings {
		if f.Check == check {
			return f, true
		}
	}
	return reconcile.Finding{}, false
}

// A pipeline that agrees with the sales tables has nothing to report.
func TestReconcile_CleanPipelineHasNoFindings(t *testing.T) {
	f := newFixture(t)
	f.paidOrder(2)
	f.collectOnce()
	f.rateAll()
	rep, err := f.reconciler(newFakeMetrics(), nil, time.Now().UTC()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", rep.Findings)
	}
	if rep.Counts[reconcile.CheckOrderItemsWithoutEvent] != 2 || rep.Counts[reconcile.CheckTicketsWithoutEvent] != 2 {
		t.Errorf("examined = %+v, want two lines and two tickets looked at", rep.Counts)
	}
}

// A deliberately deleted event is found by both sales-side checks, raises an
// alert that carries no personal data, and the alert is resolved once the
// event is back.
func TestReconcile_DeletedEventRaisesAndResolvesAnAlert(t *testing.T) {
	f := newFixture(t)
	f.paidOrder(2)
	f.collectOnce()
	f.rateAll()
	f.must(`DELETE FROM charges WHERE billable_event_id = (SELECT id FROM billable_events WHERE subject_id = $1)`, f.items[0])
	f.must(`DELETE FROM billable_events WHERE subject_id = $1`, f.items[0])

	notifier := &capture{}
	engine := &opswatchdog.AlertEngine{Store: opswatchdog.NewPGAlertStore(f.pool), Notifier: notifier, Clock: clock.New()}
	metrics := newFakeMetrics()
	r := f.reconciler(metrics, engine, time.Now().UTC())

	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := findingOf(rep, reconcile.CheckOrderItemsWithoutEvent)
	if !ok || items.Count != 1 || items.PerOrg[f.orgID] != 1 || len(items.Sample) != 1 || items.Sample[0] != f.items[0] {
		t.Fatalf("order_items_without_event = %+v (ok=%v), want exactly the deleted line", items, ok)
	}
	tix, ok := findingOf(rep, reconcile.CheckTicketsWithoutEvent)
	if !ok || tix.Count != 1 || tix.Sample[0] != f.tickets[0] {
		t.Fatalf("tickets_without_event = %+v (ok=%v), want exactly the deleted line's ticket", tix, ok)
	}
	if metrics.findings[reconcile.CheckOrderItemsWithoutEvent] != 1 || metrics.findings[reconcile.CheckReversalMismatch] != 0 {
		t.Errorf("metrics = %+v", metrics.findings)
	}
	msg := notifier.all()
	if !strings.Contains(msg, "billing reconcile: order_items_without_event") {
		t.Fatalf("no alert for the missing event:\n%s", msg)
	}
	// No personal data: the fixture's organization name and any address stay out.
	for _, bad := range []string{"@", "Reconcile Org", "Reconcile Event"} {
		if strings.Contains(msg, bad) {
			t.Errorf("alert text contains %q:\n%s", bad, msg)
		}
	}

	// The collect cursor is already past the deleted line, so a plain pass does
	// not bring it back and the reconcile keeps reporting it. The operator's
	// repair is to reset the cursors: the pass then reads from the date X
	// again (the events are idempotent), rate prices the new one, and the next
	// reconcile is clean and resolves the alerts.
	f.collectOnce()
	if rep, err = r.Run(context.Background()); err != nil || len(rep.Findings) == 0 {
		t.Fatalf("a deleted line must stay a finding until repaired: %+v %v", rep.Findings, err)
	}
	f.must(`DELETE FROM billing_job_cursors WHERE key LIKE $1`, f.prefix+"%")
	f.collectOnce()
	f.rateAll()
	before := len(notifier.msgs)
	rep, err = r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("after the repair: %+v", rep.Findings)
	}
	if len(notifier.msgs) <= before {
		t.Errorf("no resolved message after the repair")
	}
	var open int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ops_alerts WHERE fingerprint LIKE 'billing_reconcile:%' AND resolved_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d reconcile alerts still open", open)
	}
	if metrics.findings[reconcile.CheckOrderItemsWithoutEvent] != 0 {
		t.Errorf("gauge did not fall back to 0: %+v", metrics.findings)
	}
}

// An event collected long ago that rate never priced is a defect; a young one
// is only lag.
func TestReconcile_UnratedEventsAndRateLag(t *testing.T) {
	f := newFixture(t)
	f.paidOrder(2)
	f.collectOnce()
	// Nothing rated yet: young events are no defect, and not yet lag either.
	rep, err := f.reconciler(nil, nil, time.Now().UTC()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("young unrated events: %+v, want none", rep.Findings)
	}
	// 45 minutes later: rate lag only.
	rep, err = f.reconciler(nil, nil, time.Now().UTC().Add(45*time.Minute)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findingOf(rep, reconcile.CheckRateLag); !ok {
		t.Fatalf("no rate_lag after 45 minutes: %+v", rep.Findings)
	}
	if _, ok := findingOf(rep, reconcile.CheckEventsWithoutCharge); ok {
		t.Fatalf("events_without_charge fired too early: %+v", rep.Findings)
	}
	// 7 hours later: the events are unrated for good.
	rep, err = f.reconciler(nil, nil, time.Now().UTC().Add(7*time.Hour)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	un, ok := findingOf(rep, reconcile.CheckEventsWithoutCharge)
	if !ok || un.Count != 2 || un.PerOrg[f.orgID] != 2 {
		t.Fatalf("events_without_charge = %+v (ok=%v), want both events", un, ok)
	}
}

// A reversal that is not the exact negative of its charge is found.
func TestReconcile_ReversalMismatch(t *testing.T) {
	f := newFixture(t)
	f.paidOrder(1)
	f.collectOnce()
	f.rateAll()
	f.must(`INSERT INTO charges (billable_event_id, account_id, currency, amount_minor, rule_type, reversal_of)
	        SELECT c.billable_event_id, c.account_id, c.currency, -58, 'reversal', c.id FROM charges c
	        JOIN billable_events be ON be.id = c.billable_event_id WHERE be.org_id = $1 AND c.reversal_of IS NULL`, f.orgID)
	rep, err := f.reconciler(nil, nil, time.Now().UTC()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := findingOf(rep, reconcile.CheckReversalMismatch)
	if !ok || m.Count != 1 {
		t.Fatalf("reversal_mismatch = %+v (ok=%v), want the -58 against 59", m, ok)
	}
}

// A collect pipeline that stopped is lag; a missing cursor is lag too.
func TestReconcile_CollectLag(t *testing.T) {
	f := newFixture(t)
	f.paidOrder(1)
	f.collectOnce()
	f.rateAll()
	rep, err := f.reconciler(nil, nil, time.Now().UTC()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findingOf(rep, reconcile.CheckCollectLag); ok {
		t.Fatalf("a healthy collect cursor reported as lag: %+v", rep.Findings)
	}
	rep, err = f.reconciler(nil, nil, time.Now().UTC().Add(3*time.Hour)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lag, ok := findingOf(rep, reconcile.CheckCollectLag); !ok || lag.LagSeconds < 3*3600 {
		t.Fatalf("collect_lag = %+v (ok=%v), want about 3 hours", lag, ok)
	}
	f.must(`DELETE FROM billing_job_cursors WHERE key LIKE $1`, f.prefix+"%")
	rep, err = f.reconciler(nil, nil, time.Now().UTC()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lag, ok := findingOf(rep, reconcile.CheckCollectLag); !ok || !strings.Contains(lag.Note, "never") {
		t.Fatalf("no cursor at all: %+v (ok=%v), want 'never saved a cursor'", lag, ok)
	}
}

func TestReconcile_RefusesWithoutTheDateX(t *testing.T) {
	f := newFixture(t)
	r := reconcile.New(f.pool, reconcile.Options{})
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("a run without the date X must be refused")
	}
}
