//go:build integration

// rate_integration_test.go — the billing.rate job against a live database
// (task B2-3). Connects to DATABASE_URL and skips when it is unset; run it on
// a FRESH migrated database, never the shared dev stand.
//
// The shared database also holds other packages' billable events, so the
// rater here only lists the fixture's own organization (scopedLister).
package rate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
)

type scopedLister struct {
	*gen.Queries
	org uuid.UUID
}

func (s scopedLister) ListUnratedBillableEventsAfter(ctx context.Context, at time.Time, id uuid.UUID, limit int32) ([]gen.BillableEventRow, error) {
	rows, err := s.Queries.ListUnratedBillableEventsAfter(ctx, at, id, 100000)
	var out []gen.BillableEventRow
	for _, r := range rows {
		if r.OrgID == s.org && int32(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, err
}

type countingSettler struct {
	mu        sync.Mutex
	settled   int
	reversals int
	fail      bool
}

func (c *countingSettler) counts() (settled, reversals int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settled, c.reversals
}

func (c *countingSettler) Settle(context.Context, *gen.Queries, gen.BillableEventRow, gen.ChargeRow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("settler down")
	}
	c.settled++
	return nil
}

func (c *countingSettler) SettleReversal(context.Context, *gen.Queries, gen.BillableEventRow, gen.ChargeRow, gen.ChargeRow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return errors.New("settler down")
	}
	c.reversals++
	return nil
}

type fixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	q         *gen.Queries
	orgID     uuid.UUID
	acc       gen.BillingAccountRow
	listCodes []string
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
	f := &fixture{t: t, pool: pool, q: gen.New(pool), orgID: uuid.New()}
	t.Cleanup(f.cleanup)
	suffix := f.orgID.String()[:8]
	f.must(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.orgID, "Rate Org "+suffix, "rate-"+suffix)
	f.acc, err = billing.EnsureAccount(ctx, f.q, f.orgID, "CZ")
	if err != nil {
		t.Fatalf("account: %v", err)
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
	exec(`DELETE FROM charge_reversals WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1))`, f.orgID)
	exec(`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billable_events WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, f.orgID)
	exec(`DELETE FROM billing_accounts WHERE org_id = $1`, f.orgID)
	exec(`DELETE FROM org_rate_overrides WHERE org_id = $1`, f.orgID)
	for _, code := range f.listCodes {
		exec(`DELETE FROM price_list_rules WHERE price_list_id IN (SELECT id FROM price_lists WHERE code = $1)`, code)
		exec(`DELETE FROM price_list_packs WHERE price_list_id IN (SELECT id FROM price_lists WHERE code = $1)`, code)
		exec(`DELETE FROM price_lists WHERE code = $1`, code)
	}
	exec(`DELETE FROM organizations WHERE id = $1`, f.orgID)
}

func (f *fixture) must(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

type sale struct {
	kind     string
	at       time.Time
	session  *uuid.UUID
	seated   bool
	currency string
	unit     int64
	discount int64
	class    string
}

func (f *fixture) event(e sale) gen.BillableEventRow {
	f.t.Helper()
	cur, unit, disc, seated := e.currency, e.unit, e.discount, e.seated
	if cur == "" {
		cur = "EUR"
	}
	class := e.class
	p := gen.InsertBillableEventParams{
		Kind: e.kind, SubjectID: uuid.New(), OccurredAt: e.at, OrgID: f.orgID, AccountID: f.acc.ID,
		SessionID: e.session, Seated: &seated, Currency: &cur, UnitPriceMinor: &unit, DiscountMinor: &disc,
	}
	if class != "" {
		p.PricingClass = &class
	}
	id, ok, err := f.q.InsertBillableEvent(context.Background(), p)
	if err != nil || !ok {
		f.t.Fatalf("event: ok=%v err=%v", ok, err)
	}
	row, err := f.q.GetBillableEvent(context.Background(), e.kind, p.SubjectID)
	if err != nil || row.ID != id {
		f.t.Fatalf("read event back: %v", err)
	}
	return row
}

func (f *fixture) rater(settler rate.Settler) *rate.Rater {
	return rate.New(f.pool, rate.Options{
		Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Settler: settler,
		Lister: scopedLister{f.q, f.orgID},
	})
}

func run(t *testing.T, r *rate.Rater) rate.Result {
	t.Helper()
	res, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	return res
}

func (f *fixture) charge(ev gen.BillableEventRow) gen.ChargeRow {
	f.t.Helper()
	c, err := f.q.GetChargeByEvent(context.Background(), ev.ID)
	if err != nil {
		f.t.Fatalf("charge of %s %s: %v", ev.Kind, ev.ID, err)
	}
	return c
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// A sale is charged by the engine at its own date; every kind of event gets
// exactly one direct charge, free and journal-only ones with zero; a second
// pass writes nothing; two passes at once write one charge per event.
func TestRate_ChargesEveryKindOnceAtTheEventDate(t *testing.T) {
	f := newFixture(t)
	low := f.event(sale{kind: "ticket_sold", at: t0, unit: 500, class: "low_price"})
	std := f.event(sale{kind: "ticket_sold", at: t0.Add(time.Minute), unit: 1500, class: "standard"})
	seat := f.event(sale{kind: "ticket_sold", at: t0.Add(2 * time.Minute), unit: 1500, class: "standard", seated: true})
	free := f.event(sale{kind: "ticket_sold", at: t0.Add(3 * time.Minute), unit: 1500, discount: 1500, class: "standard"})
	refund := f.event(sale{kind: "ticket_refunded", at: t0.Add(4 * time.Minute)})
	cancel := f.event(sale{kind: "ticket_cancelled", at: t0.Add(5 * time.Minute)})
	comp := f.event(sale{kind: "ticket_complimentary", at: t0.Add(6 * time.Minute)})

	settler := &countingSettler{}
	r := f.rater(settler)
	if res := run(t, r); res.Rated != 7 || res.Reversed != 0 {
		t.Fatalf("first pass = %+v, want 7 rated", res)
	}
	want := map[uuid.UUID]int64{low.ID: 20, std.ID: 59, seat.ID: 109, free.ID: 0, refund.ID: 0, cancel.ID: 0, comp.ID: 0}
	for id, amount := range want {
		var c gen.ChargeRow
		c, err := f.q.GetChargeByEvent(context.Background(), id)
		if err != nil {
			t.Fatalf("no charge for %s: %v", id, err)
		}
		if c.AmountMinor != amount || c.Currency != "EUR" || c.PriceListVersion == nil || *c.PriceListVersion != 1 || c.ReversalOf != nil {
			t.Errorf("charge of %s = %+v, want %d EUR v1", id, c, amount)
		}
	}
	if r := f.charge(refund); r.Reason == nil || *r.Reason != billing.ReasonJournalOnly {
		t.Errorf("refund reason = %v, want journal_only", r.Reason)
	}
	if r := f.charge(free); r.Reason == nil || *r.Reason != billing.ReasonFree {
		t.Errorf("free reason = %v, want free", r.Reason)
	}
	if settled, _ := settler.counts(); settled != 3 {
		t.Errorf("Settle called %d times, want once per positive charge (3)", settled)
	}

	if res := run(t, r); res.Rated != 0 {
		t.Fatalf("second pass rated %d, want 0", res.Rated)
	}
	// Two passes racing for new events: one charge each.
	var evs []gen.BillableEventRow
	for i := 0; i < 5; i++ {
		evs = append(evs, f.event(sale{kind: "ticket_sold", at: t0.Add(time.Duration(10+i) * time.Minute), unit: 1500, class: "standard"}))
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.rater(&countingSettler{}).Run(context.Background()); err != nil {
				t.Errorf("concurrent pass: %v", err)
			}
		}()
	}
	wg.Wait()
	for _, e := range evs {
		if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id = $1`, e.ID); n != 1 {
			t.Errorf("event %s has %d charges, want 1", e.ID, n)
		}
	}
}

// The version of the price list is the one in force at the event's date, not
// at the time of the pass; the individual rate replaces the list.
func TestRate_VersionByOccurredAtAndIndividualRate(t *testing.T) {
	f := newFixture(t)
	code := "b23-" + uuid.NewString()[:8]
	f.listCodes = append(f.listCodes, code)
	boundary := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		version int
		from    time.Time
		un, se  int
	}{{1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 59, 109}, {2, boundary, 70, 120}} {
		var id uuid.UUID
		if err := f.pool.QueryRow(context.Background(), `
			INSERT INTO price_lists (code, version, currency, effective_from, locked, free_tickets_annual_limit,
			       floor_per_ticket_minor, bonus_share_cap, bonus_covers_surcharge, created_by, reason)
			SELECT $1, $2, currency, $3, locked, free_tickets_annual_limit, floor_per_ticket_minor,
			       bonus_share_cap, bonus_covers_surcharge, 'test', 'b23 test'
			FROM   price_lists WHERE code = 'standard-2026' AND currency = 'EUR' AND version = 1
			RETURNING id`, code, v.version, v.from).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.must(`INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
		        SELECT $1, rule_type, line, event_kind, sale_currency,
		               CASE WHEN rule_type = 'fixed_per_ticket' THEN jsonb_build_object('unseated_minor', $2::int, 'seated_minor', $3::int) ELSE params END,
		               sort_order
		        FROM price_list_rules WHERE price_list_id = (SELECT id FROM price_lists WHERE code = 'standard-2026' AND currency = 'EUR' AND version = 1)`,
			id, v.un, v.se)
	}
	f.must(`INSERT INTO billing_account_terms (account_id, effective_from, price_list_code, plan_kind, billing_cycle, shortfall_policy, issuer_id, created_by, reason)
	        VALUES ($1, '2026-01-02T00:00:00Z', $2, 'standard', 'monthly', 'invoice_postpaid', $3, 'test', 'b23 test')`, f.acc.ID, code, f.acc.IssuerID)

	before := f.event(sale{kind: "ticket_sold", at: boundary.Add(-time.Second), unit: 1500, class: "standard"})
	after := f.event(sale{kind: "ticket_sold", at: boundary, unit: 1500, class: "standard"})
	afterSeated := f.event(sale{kind: "ticket_sold", at: boundary.Add(time.Hour), unit: 1500, class: "standard", seated: true})
	run(t, f.rater(nil))
	for _, c := range []struct {
		ev      gen.BillableEventRow
		amount  int64
		version int32
	}{{before, 59, 1}, {after, 70, 2}, {afterSeated, 120, 2}} {
		got := f.charge(c.ev)
		if got.AmountMinor != c.amount || got.PriceListVersion == nil || *got.PriceListVersion != c.version {
			t.Errorf("sale at %s: charge %d v%v, want %d v%d", c.ev.OccurredAt.Format(time.RFC3339), got.AmountMinor, got.PriceListVersion, c.amount, c.version)
		}
		var in map[string]int64
		if err := json.Unmarshal(got.Inputs, &in); err != nil || in["unit_price_minor"] != 1500 {
			t.Errorf("inputs = %s", got.Inputs)
		}
	}

	// An individual rate from the 20th: events before it keep the list.
	f.must(`INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, note, created_by)
	        VALUES ($1, 'EUR', 50, 100, '2026-10-20T00:00:00Z', 'b23 test', 'test')`, f.orgID)
	onList := f.event(sale{kind: "ticket_sold", at: time.Date(2026, 10, 19, 23, 59, 0, 0, time.UTC), unit: 1500, class: "standard"})
	onRate := f.event(sale{kind: "ticket_sold", at: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), unit: 800, class: "low_price"})
	run(t, f.rater(nil))
	if got := f.charge(onList); got.AmountMinor != 70 || got.OverrideID != nil {
		t.Errorf("before the individual rate: %+v, want the list's 70", got)
	}
	if got := f.charge(onRate); got.AmountMinor != 50 || got.OverrideID == nil {
		t.Errorf("under the individual rate: %+v, want 50 with the override recorded", got)
	}
}

// A cancelled session reverses exactly the charges of its ticket_sold events,
// as paired rows, once; free tickets and other sessions are untouched.
func TestRate_SessionCancellationReversesExactly(t *testing.T) {
	f := newFixture(t)
	s, other := uuid.New(), uuid.New()
	a := f.event(sale{kind: "ticket_sold", at: t0, session: &s, unit: 500, class: "low_price"})
	b := f.event(sale{kind: "ticket_sold", at: t0.Add(time.Minute), session: &s, unit: 1500, class: "standard", seated: true})
	free := f.event(sale{kind: "ticket_sold", at: t0.Add(2 * time.Minute), session: &s, unit: 1500, discount: 1500, class: "standard"})
	keep := f.event(sale{kind: "ticket_sold", at: t0.Add(3 * time.Minute), session: &other, unit: 1500, class: "standard"})
	cancel := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Hour), session: &s})

	settler := &countingSettler{}
	r := f.rater(settler)
	res := run(t, r)
	if res.Rated != 5 || res.Reversed != 2 {
		t.Fatalf("pass = %+v, want 5 rated and 2 reversed", res)
	}
	if _, reversals := settler.counts(); reversals != 2 {
		t.Errorf("SettleReversal called %d times, want 2", reversals)
	}
	var net int64
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(sum(c.amount_minor), 0) FROM charges c JOIN billable_events e ON e.id = c.billable_event_id
		WHERE e.session_id = $1 AND e.kind = 'ticket_sold'`, s).Scan(&net); err != nil {
		t.Fatal(err)
	}
	if net != 0 {
		t.Errorf("net of the cancelled session = %d, want 0", net)
	}
	for _, e := range []gen.BillableEventRow{a, b} {
		orig := f.charge(e)
		rev, err := f.q.GetChargeReversalByCharge(context.Background(), orig.ID)
		if err != nil {
			t.Fatalf("no reversal journal for %s: %v", e.ID, err)
		}
		if rev.CauseEventID != cancel.ID {
			t.Errorf("cause = %s, want the cancellation", rev.CauseEventID)
		}
		var amount int64
		var of *uuid.UUID
		if err := f.pool.QueryRow(context.Background(), `SELECT amount_minor, reversal_of FROM charges WHERE id = $1`, rev.ReversalChargeID).Scan(&amount, &of); err != nil {
			t.Fatal(err)
		}
		if amount != -orig.AmountMinor || of == nil || *of != orig.ID {
			t.Errorf("reversal = %d of %v, want %d of %s", amount, of, -orig.AmountMinor, orig.ID)
		}
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE reversal_of IS NOT NULL AND billable_event_id IN ($1, $2)`, free.ID, keep.ID); n != 0 {
		t.Errorf("a free ticket or another session was reversed (%d)", n)
	}
	if c := f.charge(cancel); c.AmountMinor != 0 {
		t.Errorf("the cancellation itself costs %d, want 0", c.AmountMinor)
	}
	if res := run(t, r); res.Rated != 0 || res.Reversed != 0 {
		t.Fatalf("second pass = %+v, want nothing", res)
	}
}

// The cancellation waits for a sale of its session that cannot be rated yet,
// and a sale that is rated after the cancellation was processed is reversed
// at once.
func TestRate_CancellationWaitsAndLateSalesAreReversed(t *testing.T) {
	f := newFixture(t)
	s := uuid.New()
	ok := f.event(sale{kind: "ticket_sold", at: t0, session: &s, unit: 1500, class: "standard"})
	// A low-price class in a currency the price list has no rule for cannot be
	// rated: the engine refuses it.
	stuck := f.event(sale{kind: "ticket_sold", at: t0.Add(time.Minute), session: &s, unit: 500, class: "low_price", currency: "CZK"})
	cancel := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Hour), session: &s})

	r := f.rater(nil)
	res := run(t, r)
	if res.Rated != 1 || res.Skipped != 2 || res.Reversed != 0 {
		t.Fatalf("pass = %+v, want 1 rated and the stuck sale and the cancellation skipped", res)
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id IN ($1, $2)`, stuck.ID, cancel.ID); n != 0 {
		t.Fatalf("the cancellation or the stuck sale got a charge (%d)", n)
	}
	_ = ok

	// Another cancelled session whose cancellation is processed first, then a
	// sale paid before it arrives late.
	s2 := uuid.New()
	cancel2 := f.event(sale{kind: "session_cancelled", at: t0.Add(2 * time.Hour), session: &s2})
	if res := run(t, r); res.Rated != 1 {
		t.Fatalf("pass = %+v, want the second cancellation rated", res)
	}
	late := f.event(sale{kind: "ticket_sold", at: t0.Add(90 * time.Minute), session: &s2, unit: 1500, class: "standard"})
	res = run(t, r)
	if res.Rated != 1 || res.Reversed != 1 {
		t.Fatalf("pass = %+v, want the late sale rated and reversed", res)
	}
	orig := f.charge(late)
	rev, err := f.q.GetChargeReversalByCharge(context.Background(), orig.ID)
	if err != nil || rev.CauseEventID != cancel2.ID {
		t.Fatalf("late sale reversal journal = %+v, %v", rev, err)
	}
}
