//go:build integration

// events_integration_test.go — migration 0158 and gen/billing_events.sql.go
// against a live database (task B2-1). Connects to DATABASE_URL and skips when
// it is unset; run it on a FRESH migrated database, never the shared dev
// stand. Every row the tests write is keyed to a fixture organization and
// removed in the cleanup (a test file may delete the append-only tables).
package billing_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

type eventsFixture struct {
	pool    *pgxpool.Pool
	q       *gen.Queries
	orgID   uuid.UUID
	account gen.BillingAccountRow
	base    time.Time
}

// newEventsFixture creates an organization with its EUR billing account.
// base is a unique far-future instant, so ListUnratedBillableEventsSince
// sees only this test's events even on a database other packages share.
func newEventsFixture(t *testing.T) *eventsFixture {
	t.Helper()
	pool := openPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Billing B2-1 "+suffix, "billing-b21-"+suffix); err != nil {
		t.Fatal(err)
	}
	acc, err := billing.EnsureAccount(ctx, q, orgID, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM charge_allocations WHERE charge_id IN (SELECT id FROM charges WHERE account_id = $1)`, acc.ID)
		_, _ = pool.Exec(bg, `DELETE FROM credit_lots WHERE account_id = $1`, acc.ID)
		_, _ = pool.Exec(bg, `DELETE FROM charge_reversals WHERE charge_id IN (SELECT id FROM charges WHERE account_id = $1)`, acc.ID)
		_, _ = pool.Exec(bg, `DELETE FROM charges WHERE account_id = $1`, acc.ID)
		_, _ = pool.Exec(bg, `DELETE FROM billable_events WHERE account_id = $1`, acc.ID)
		_, _ = pool.Exec(bg, `DELETE FROM billing_accounts WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	base := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(uuid.New().ID()%100000) * time.Minute)
	return &eventsFixture{pool: pool, q: q, orgID: orgID, account: acc, base: base}
}

func (f *eventsFixture) event(t *testing.T, kind string, at time.Time, session *uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	subject := uuid.New()
	cur := "EUR"
	price := int64(2500)
	id, inserted, err := f.q.InsertBillableEvent(context.Background(), gen.InsertBillableEventParams{
		Kind: kind, SubjectID: subject, OccurredAt: at, OrgID: f.orgID, AccountID: f.account.ID,
		SessionID: session, Currency: &cur, UnitPriceMinor: &price,
	})
	if err != nil || !inserted {
		t.Fatalf("insert %s event: id=%s inserted=%v err=%v", kind, id, inserted, err)
	}
	return id, subject
}

func (f *eventsFixture) charge(t *testing.T, eventID uuid.UUID, amount int64, reversalOf *uuid.UUID) (gen.ChargeRow, error) {
	t.Helper()
	return f.q.InsertCharge(context.Background(), gen.InsertChargeParams{
		BillableEventID: eventID, AccountID: f.account.ID, Currency: "EUR", AmountMinor: amount,
		RuleType: "fixed_per_ticket", ReversalOf: reversalOf,
	})
}

func pgCode(err error) (code, constraint string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code, pe.ConstraintName
	}
	return "", ""
}

func wantPgError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	gotCode, gotConstraint := pgCode(err)
	if gotCode != code || (constraint != "" && gotConstraint != constraint) {
		t.Fatalf("error = %v (code %q constraint %q), want code %s constraint %q", err, gotCode, gotConstraint, code, constraint)
	}
}

// The same (kind, subject) written twice leaves one row and the second call
// says so; a hundred concurrent writers of one key insert exactly once.
func TestBillingEvents_InsertIsIdempotentByKindAndSubject(t *testing.T) {
	f := newEventsFixture(t)
	ctx := context.Background()

	subject := uuid.New()
	p := gen.InsertBillableEventParams{
		Kind: "ticket_sold", SubjectID: subject, OccurredAt: f.base, OrgID: f.orgID, AccountID: f.account.ID,
	}
	id1, inserted1, err := f.q.InsertBillableEvent(ctx, p)
	if err != nil || !inserted1 || id1 == uuid.Nil {
		t.Fatalf("first insert: %v %v %v", id1, inserted1, err)
	}
	id2, inserted2, err := f.q.InsertBillableEvent(ctx, p)
	if err != nil || inserted2 || id2 != uuid.Nil {
		t.Fatalf("second insert: %v %v %v", id2, inserted2, err)
	}
	// Same subject under another kind is another fact.
	p.Kind = "ticket_refunded"
	if _, inserted, err := f.q.InsertBillableEvent(ctx, p); err != nil || !inserted {
		t.Fatalf("other kind: %v %v", inserted, err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM billable_events WHERE subject_id = $1`, subject).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d, err %v, want 2 (one per kind)", n, err)
	}

	got, err := f.q.GetBillableEvent(ctx, "ticket_sold", subject)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id1 || got.Source != "collect" || string(got.Attributes) != "{}" || got.Currency != nil {
		t.Fatalf("event = %+v", got)
	}
	if _, err := f.q.GetBillableEvent(ctx, "ticket_sold", uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing event err = %v", err)
	}

	race := uuid.New()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := f.q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
				Kind: "ticket_cancelled", SubjectID: race, OccurredAt: f.base, OrgID: f.orgID, AccountID: f.account.ID,
			})
			if err != nil {
				t.Errorf("race insert: %v", err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent inserts won %d times, want 1", wins.Load())
	}
}

// A bad kind, class or currency is refused by the table, not by the caller.
func TestBillingEvents_TableRefusesBadValues(t *testing.T) {
	f := newEventsFixture(t)
	ctx := context.Background()
	bad := "usd"
	class := "premium"
	for name, p := range map[string]gen.InsertBillableEventParams{
		"kind":     {Kind: "ticket_gifted"},
		"class":    {Kind: "ticket_sold", PricingClass: &class},
		"currency": {Kind: "ticket_sold", Currency: &bad},
		"source":   {Kind: "ticket_sold", Source: "magic"},
	} {
		p.SubjectID, p.OccurredAt, p.OrgID, p.AccountID = uuid.New(), f.base, f.orgID, f.account.ID
		_, _, err := f.q.InsertBillableEvent(ctx, p)
		if code, _ := pgCode(err); code != "23514" {
			t.Errorf("%s: err = %v, want a check violation", name, err)
		}
	}
}

// One direct charge per event: the second fails on the partial unique index.
// ListUnrated stops returning the event once it has a direct charge, and a
// zero-amount charge counts as rated.
func TestBillingEvents_OneDirectChargePerEventAndUnratedList(t *testing.T) {
	f := newEventsFixture(t)
	ctx := context.Background()

	sold, _ := f.event(t, "ticket_sold", f.base, nil)
	refunded, _ := f.event(t, "ticket_refunded", f.base.Add(time.Minute), nil)

	has := func(rows []gen.BillableEventRow, id uuid.UUID) bool {
		for _, r := range rows {
			if r.ID == id {
				return true
			}
		}
		return false
	}
	unrated, err := f.q.ListUnratedBillableEventsSince(ctx, f.base, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrated) != 2 || unrated[0].ID != sold || unrated[1].ID != refunded {
		t.Fatalf("unrated (oldest first) = %+v", unrated)
	}
	all, err := f.q.ListUnratedBillableEvents(ctx, 100000)
	if err != nil || !has(all, sold) || !has(all, refunded) {
		t.Fatalf("ListUnratedBillableEvents: %v has=%v/%v", err, has(all, sold), has(all, refunded))
	}
	limited, err := f.q.ListUnratedBillableEventsSince(ctx, f.base, 1)
	if err != nil || len(limited) != 1 || limited[0].ID != sold {
		t.Fatalf("limit 1: %+v err %v", limited, err)
	}

	if _, err := f.q.GetChargeByEvent(ctx, sold); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unrated event has a charge: %v", err)
	}
	c1, err := f.charge(t, sold, 40, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c1.AmountMinor != 40 || c1.Currency != "EUR" || string(c1.Inputs) != "{}" || c1.ReversalOf != nil {
		t.Fatalf("charge = %+v", c1)
	}
	_, err = f.charge(t, sold, 40, nil)
	wantPgError(t, err, "23505", "charges_one_direct_per_event_uq")

	// Journal-only event: a zero-amount direct charge marks it rated.
	if _, err := f.charge(t, refunded, 0, nil); err != nil {
		t.Fatal(err)
	}
	unrated, err = f.q.ListUnratedBillableEventsSince(ctx, f.base, 100)
	if err != nil || len(unrated) != 0 {
		t.Fatalf("after rating: %+v err %v", unrated, err)
	}
	all, err = f.q.ListUnratedBillableEvents(ctx, 100000)
	if err != nil || has(all, sold) || has(all, refunded) {
		t.Fatalf("ListUnratedBillableEvents still lists rated events: %v", err)
	}
	got, err := f.q.GetChargeByEvent(ctx, sold)
	if err != nil || got.ID != c1.ID {
		t.Fatalf("GetChargeByEvent = %+v err %v", got, err)
	}

	// The amount sign is tied to the kind of charge.
	other, _ := f.event(t, "ticket_sold", f.base.Add(2*time.Minute), nil)
	_, err = f.charge(t, other, -1, nil)
	wantPgError(t, err, "23514", "charges_direct_amount_check")
	_, err = f.charge(t, other, 5, &c1.ID)
	wantPgError(t, err, "23514", "charges_reversal_amount_check")
}

// A charge is reversed at most once, by a negative paired row; the journal
// refuses a second entry; the period sum nets to zero and the session query
// stops offering a reversed charge.
func TestBillingEvents_ReversalIsPairedAndOnce(t *testing.T) {
	f := newEventsFixture(t)
	ctx := context.Background()

	session := uuid.New()
	soldA, _ := f.event(t, "ticket_sold", f.base, &session)
	soldB, _ := f.event(t, "ticket_sold", f.base.Add(time.Minute), &session)
	cancelled, _ := f.event(t, "session_cancelled", f.base.Add(time.Hour), &session)

	chA, err := f.charge(t, soldA, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	chB, err := f.charge(t, soldB, 70, nil)
	if err != nil {
		t.Fatal(err)
	}

	n, err := f.q.CountUnratedBillableEventsForSession(ctx, session, "ticket_sold")
	if err != nil || n != 0 {
		t.Fatalf("unrated sold = %d err %v", n, err)
	}
	n, err = f.q.CountUnratedBillableEventsForSession(ctx, session, "session_cancelled")
	if err != nil || n != 1 {
		t.Fatalf("unrated cancelled = %d err %v", n, err)
	}

	rev, err := f.charge(t, soldA, -50, &chA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.ReversalOf == nil || *rev.ReversalOf != chA.ID {
		t.Fatalf("reversal = %+v", rev)
	}
	// The reversal does not count as the event's direct charge.
	direct, err := f.q.GetChargeByEvent(ctx, soldA)
	if err != nil || direct.ID != chA.ID {
		t.Fatalf("direct charge after reversal = %+v err %v", direct, err)
	}
	_, err = f.charge(t, soldA, -50, &chA.ID)
	wantPgError(t, err, "23505", "charges_one_reversal_per_charge_uq")

	j, err := f.q.InsertChargeReversal(ctx, gen.InsertChargeReversalParams{
		ChargeID: chA.ID, ReversalChargeID: rev.ID, CauseEventID: cancelled, Reason: "session cancelled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.ActorType != "system" || j.ActorID != "" || j.CauseEventID != cancelled {
		t.Fatalf("journal = %+v", j)
	}
	_, err = f.q.InsertChargeReversal(ctx, gen.InsertChargeReversalParams{
		ChargeID: chA.ID, ReversalChargeID: rev.ID, CauseEventID: cancelled, Reason: "again",
	})
	wantPgError(t, err, "23505", "")
	got, err := f.q.GetChargeReversalByCharge(ctx, chA.ID)
	if err != nil || got.ID != j.ID {
		t.Fatalf("GetChargeReversalByCharge = %+v err %v", got, err)
	}
	if _, err := f.q.GetChargeReversalByCharge(ctx, chB.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unreversed charge has a journal entry: %v", err)
	}

	reversible, err := f.q.ListReversibleChargesForSession(ctx, session)
	if err != nil || len(reversible) != 1 || reversible[0].ID != chB.ID {
		t.Fatalf("reversible = %+v err %v", reversible, err)
	}

	from, to := f.base.Add(-time.Hour), f.base.Add(24*time.Hour)
	rows, err := f.q.ListChargesByAccountPeriod(ctx, f.account.ID, from, to)
	if err != nil || len(rows) != 3 {
		t.Fatalf("period charges = %d err %v", len(rows), err)
	}
	sums, err := f.q.SumChargesByAccountPeriod(ctx, f.account.ID, from, to)
	if err != nil || len(sums) != 1 || sums[0].Currency != "EUR" || sums[0].AmountMinor != 70 || sums[0].ChargeCount != 3 {
		t.Fatalf("sums = %+v err %v (want EUR 70 over 3 rows: 50 + 70 - 50)", sums, err)
	}
	// An empty window sums to nothing.
	sums, err = f.q.SumChargesByAccountPeriod(ctx, f.account.ID, to, to.Add(time.Hour))
	if err != nil || len(sums) != 0 {
		t.Fatalf("empty window sums = %+v err %v", sums, err)
	}
}

// An allocation names what closed the charge: a lot needs lot_id, an invoice
// line needs its id, the amount is positive.
func TestBillingEvents_AllocationShapeIsEnforced(t *testing.T) {
	f := newEventsFixture(t)
	ctx := context.Background()

	ev, _ := f.event(t, "ticket_sold", f.base, nil)
	ch, err := f.charge(t, ev, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lot uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO credit_lots (account_id, org_id, currency, kind, source,
	        tier, unseated_rate_minor, seated_surcharge_minor, purchased_at, face_minor, remaining_minor)
	        VALUES ($1, $2, 'EUR', 'purchased', 'adjustment', 1, 55, 35, now(), 100, 60) RETURNING id`,
		f.account.ID, f.account.OrgID).Scan(&lot); err != nil {
		t.Fatal(err)
	}
	ok, err := f.q.InsertChargeAllocation(ctx, gen.InsertChargeAllocationParams{
		ChargeID: ch.ID, Kind: "purchased_lot", LotID: &lot, AmountMinor: 40, Currency: "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.InsertChargeAllocation(ctx, gen.InsertChargeAllocationParams{
		ChargeID: ch.ID, Kind: "agreement_discount", AmountMinor: 20, Currency: "EUR",
	}); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]gen.InsertChargeAllocationParams{
		"lot kind without lot":      {Kind: "bonus_lot", AmountMinor: 5},
		"invoice kind without line": {Kind: "invoice_line", AmountMinor: 5},
		"zero amount":               {Kind: "agreement_discount", AmountMinor: 0},
		"unknown kind":              {Kind: "cash", AmountMinor: 5},
	} {
		p.ChargeID, p.Currency = ch.ID, "EUR"
		_, err := f.q.InsertChargeAllocation(ctx, p)
		if code, _ := pgCode(err); code != "23514" {
			t.Errorf("%s: err = %v, want a check violation", name, err)
		}
	}
	list, err := f.q.ListChargeAllocations(ctx, ch.ID)
	if err != nil || len(list) != 2 || list[0].ID != ok.ID || list[0].AmountMinor != 40 {
		t.Fatalf("allocations = %+v err %v", list, err)
	}
}

// The cursor is created on first write and moved by later ones.
func TestBillingEvents_JobCursorUpserts(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	q := gen.New(pool)
	key := "test.b21." + uuid.NewString()
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM billing_job_cursors WHERE key = $1`, key) })

	if _, err := q.GetBillingJobCursor(ctx, key); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing cursor err = %v", err)
	}
	t1 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := q.UpsertBillingJobCursor(ctx, key, t1); err != nil {
		t.Fatal(err)
	}
	t2 := t1.Add(time.Hour)
	if err := q.UpsertBillingJobCursor(ctx, key, t2); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetBillingJobCursor(ctx, key)
	if err != nil || !got.CursorAt.Equal(t2) || got.Key != key {
		t.Fatalf("cursor = %+v err %v", got, err)
	}
}
