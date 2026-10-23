//go:build integration

// rate_review_integration_test.go — the findings of the independent review of
// task B2-3, one test each (same fixtures as rate_integration_test.go).
package rate_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
)

// A sale made AFTER its session was cancelled is neither waited for nor
// reversed, in whatever order the events are rated.
func TestRate_SaleAfterTheCancellationIsNeitherWaitedForNorReversed(t *testing.T) {
	f := newFixture(t)
	s := uuid.New()
	before := f.event(sale{kind: "ticket_sold", at: t0, session: &s, unit: 1500, class: "standard"})
	cancel := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Hour), session: &s})
	// After the cancellation, and impossible to rate (low-price in a currency
	// with no rule): it must not hold the cancellation back.
	stuck := f.event(sale{kind: "ticket_sold", at: t0.Add(2 * time.Hour), session: &s, unit: 500, class: "low_price", currency: "CZK"})
	after := f.event(sale{kind: "ticket_sold", at: t0.Add(3 * time.Hour), session: &s, unit: 1500, class: "standard"})

	r := f.rater(nil)
	res := run(t, r)
	if res.Reversed != 1 {
		t.Fatalf("pass = %+v, want exactly the sale before the cancellation reversed", res)
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE reversal_of IS NOT NULL AND billable_event_id = $1`, before.ID); n != 1 {
		t.Errorf("the sale before the cancellation has %d reversals, want 1", n)
	}
	for _, e := range []gen.BillableEventRow{after, stuck, cancel} {
		if n := f.count(`SELECT count(*) FROM charges WHERE reversal_of IS NOT NULL AND billable_event_id = $1`, e.ID); n != 0 {
			t.Errorf("event %s %s was reversed (%d)", e.Kind, e.ID, n)
		}
	}
	if c := f.charge(after); c.AmountMinor != 59 {
		t.Errorf("the sale after the cancellation costs %d, want its own 59", c.AmountMinor)
	}
}

// Terms that contradict themselves (a price list in another currency than the
// account) are a data defect of that account only: its events stay unrated,
// every other account is rated, and a cancellation of that account still
// writes its journal charge.
func TestRate_BrokenTermsOfOneAccountDoNotStopTheOthers(t *testing.T) {
	f := newFixture(t)
	bad := f.event(sale{kind: "ticket_sold", at: t0, unit: 1500, class: "standard"})
	// Point this account's terms at a CZK price list although it bills in EUR.
	code := "b23-bad-" + uuid.NewString()[:8]
	f.listCodes = append(f.listCodes, code)
	f.must(`INSERT INTO price_lists (code, version, currency, effective_from, locked, created_by, reason, bonus_share_cap, bonus_covers_surcharge)
	        VALUES ($1, 1, 'CZK', '2026-01-01T00:00:00Z', false, 'test', 'b23 test', 0.3, false)`, code)
	f.must(`INSERT INTO billing_account_terms (account_id, effective_from, price_list_code, plan_kind, billing_cycle, shortfall_policy, issuer_id, created_by, reason)
	        VALUES ($1, '2026-10-01T00:00:00Z', $2, 'standard', 'monthly', 'invoice_postpaid', $3, 'test', 'b23 test')`, f.acc.ID, code, f.acc.IssuerID)
	s := uuid.New()
	cancel := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Minute), session: &s})

	// Another organization with healthy terms, rated in the same pass.
	other := newFixture(t)
	good := other.event(sale{kind: "ticket_sold", at: t0, unit: 1500, class: "standard"})
	both := rate.New(f.pool, rate.Options{
		Since:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Lister: pairLister{f.q, f.orgID, other.orgID},
	})
	res, err := both.Run(context.Background())
	if err != nil {
		t.Fatalf("a data defect of one account stopped the pass: %v", err)
	}
	if res.Skipped != 1 {
		t.Errorf("pass = %+v, want the broken account's sale skipped", res)
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id = $1`, bad.ID); n != 0 {
		t.Errorf("the broken account's sale got a charge (%d)", n)
	}
	if c := other.charge(good); c.AmountMinor != 59 {
		t.Errorf("the healthy account's sale costs %d, want 59", c.AmountMinor)
	}
	if c := f.charge(cancel); c.AmountMinor != 0 || c.Reason == nil || *c.Reason != billing.ReasonJournalOnly {
		t.Errorf("the cancellation under broken terms = %+v, want a journal-only zero", c)
	}
}

type pairLister struct {
	*gen.Queries
	a, b uuid.UUID
}

func (p pairLister) ListUnratedBillableEventsAfter(ctx context.Context, at time.Time, id uuid.UUID, limit int32) ([]gen.BillableEventRow, error) {
	rows, err := p.Queries.ListUnratedBillableEventsAfter(ctx, at, id, 100000)
	var out []gen.BillableEventRow
	for _, r := range rows {
		if (r.OrgID == p.a || r.OrgID == p.b) && int32(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, err
}

// A failing Settler rolls the charge back with it: no charge and no reversal
// are left, and the pass reports the failure; once it recovers the event is
// rated.
func TestRate_SettlerFailureRollsBackTheCharge(t *testing.T) {
	f := newFixture(t)
	s := uuid.New()
	sold := f.event(sale{kind: "ticket_sold", at: t0, session: &s, unit: 1500, class: "standard"})
	settler := &countingSettler{fail: true}
	r := f.rater(settler)
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("a settler failure must fail the pass")
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id = $1`, sold.ID); n != 0 {
		t.Fatalf("a charge survived the settler failure (%d)", n)
	}
	settler.mu.Lock()
	settler.fail = false
	settler.mu.Unlock()
	if res := run(t, r); res.Rated != 1 {
		t.Fatalf("after the recovery: %+v, want the sale rated", res)
	}

	// The same for a reversal: the cancellation and its reversal commit together.
	cancel := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Hour), session: &s})
	settler.mu.Lock()
	settler.fail = true
	settler.mu.Unlock()
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("a settler failure on a reversal must fail the pass")
	}
	if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id IN ($1, $2) AND (reversal_of IS NOT NULL OR billable_event_id = $2)`, sold.ID, cancel.ID); n != 0 {
		t.Fatalf("a reversal or the cancellation's own charge survived (%d)", n)
	}
}

// Many skipped events at the head of the queue do not hide the ones behind
// them, also when they share one instant and exceed the page size; an event
// without a session and a second cancellation of the same session are fine.
func TestRate_SkippedEventsDoNotShadowTheRest(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ { // five unrateable sales at one instant
		f.event(sale{kind: "ticket_sold", at: t0, unit: 500, class: "low_price", currency: "CZK"})
	}
	ok := f.event(sale{kind: "ticket_sold", at: t0.Add(time.Minute), unit: 1500, class: "standard"})
	noSession := f.event(sale{kind: "session_cancelled", at: t0.Add(2 * time.Minute)})
	s := uuid.New()
	sold := f.event(sale{kind: "ticket_sold", at: t0.Add(3 * time.Minute), session: &s, unit: 1500, class: "standard"})
	cancel1 := f.event(sale{kind: "session_cancelled", at: t0.Add(time.Hour), session: &s})
	cancel2 := f.event(sale{kind: "session_cancelled", at: t0.Add(2 * time.Hour), session: &s})

	r := rate.New(f.pool, rate.Options{
		Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), BatchSize: 2,
		Lister: scopedLister{f.q, f.orgID},
	})
	res := run(t, r)
	if res.Skipped != 6 {
		t.Errorf("pass = %+v, want the five unrateable sales and the session-less cancellation skipped", res)
	}
	if c := f.charge(ok); c.AmountMinor != 59 {
		t.Errorf("the sale behind the skipped ones costs %d, want 59", c.AmountMinor)
	}
	_ = noSession
	if n := f.count(`SELECT count(*) FROM charges WHERE billable_event_id = $1 AND reversal_of IS NOT NULL`, sold.ID); n != 1 {
		t.Errorf("the sale of the cancelled session has %d reversals, want exactly 1 although it was cancelled twice", n)
	}
	for _, e := range []gen.BillableEventRow{cancel1, cancel2} {
		if c := f.charge(e); c.AmountMinor != 0 {
			t.Errorf("cancellation %s costs %d, want 0", e.ID, c.AmountMinor)
		}
	}
}
