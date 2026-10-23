//go:build integration

// credits_integration_test.go — credit lots, the settler and the expiry job
// against a live database (tasks B3-1, B3-2). Connects to DATABASE_URL and
// skips when it is unset; run it on a FRESH migrated database. The rater
// lists only the fixture's own organization.
package credits_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/credits"
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

type fixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *gen.Queries
	orgID uuid.UUID
	acc   gen.BillingAccountRow
	now   time.Time
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
	f := &fixture{t: t, pool: pool, q: gen.New(pool), orgID: uuid.New(), now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	t.Cleanup(f.cleanup)
	suffix := f.orgID.String()[:8]
	f.must(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.orgID, "Credits Org "+suffix, "credits-"+suffix)
	if f.acc, err = billing.EnsureAccount(ctx, f.q, f.orgID, "CZ"); err != nil {
		t.Fatalf("account: %v", err)
	}
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM credit_ledger WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
		`DELETE FROM charge_allocations WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1))`,
		`DELETE FROM credit_lots WHERE org_id = $1`,
		`DELETE FROM charge_reversals WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1))`,
		`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
		`DELETE FROM billable_events WHERE org_id = $1`,
		`DELETE FROM invoices WHERE org_id = $1`,
		`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
		`DELETE FROM billing_accounts WHERE org_id = $1`,
		`DELETE FROM org_rate_overrides WHERE org_id = $1`,
		`DELETE FROM organizations WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.orgID); err != nil {
			f.t.Logf("cleanup (%s): %v", sql, err)
		}
	}
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
		f.t.Fatal(err)
	}
	return n
}

// pack returns the standard-2026 EUR pack of a tier.
func (f *fixture) pack(tier int16) gen.PriceListPackRow {
	f.t.Helper()
	pl, err := f.q.GetPriceListVersionAt(context.Background(), "standard-2026", "EUR", f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	packs, err := f.q.ListPriceListPacks(context.Background(), pl.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, p := range packs {
		if p.Tier == tier {
			return p
		}
	}
	f.t.Fatalf("no tier %d", tier)
	return gen.PriceListPackRow{}
}

// buy writes a paid pack at `at` as a purchase transaction would.
func (f *fixture) buy(p gen.PriceListPackRow, at time.Time) gen.CreditLotRow {
	f.t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	invoice := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO invoices (id, org_id, billing_period, state, total_amount_minor, currency)
	        VALUES ($1, $2, $3, 'paid', $4, 'EUR')`, invoice, f.orgID, "pack-"+invoice.String()[:8], p.FaceMinor); err != nil {
		f.t.Fatal(err)
	}
	lot, created, err := credits.Purchase(ctx, gen.New(tx), credits.PurchaseInput{
		AccountID: f.acc.ID, OrgID: f.orgID, Currency: f.acc.Currency, Pack: p,
		InvoiceID: invoice, PaidAt: at,
	})
	if err != nil || !created {
		f.t.Fatalf("purchase: created=%v err=%v", created, err)
	}
	if err := tx.Commit(ctx); err != nil {
		f.t.Fatal(err)
	}
	return lot
}

type sale struct {
	kind    string
	at      time.Time
	session *uuid.UUID
	event   *uuid.UUID
	seated  bool
}

func (f *fixture) event(s sale) gen.BillableEventRow {
	f.t.Helper()
	cur, unit, disc, class := "EUR", int64(1500), int64(0), "standard"
	p := gen.InsertBillableEventParams{
		Kind: s.kind, SubjectID: uuid.New(), OccurredAt: s.at, OrgID: f.orgID, AccountID: f.acc.ID,
		SessionID: s.session, EventID: s.event, Seated: &s.seated, Currency: &cur, UnitPriceMinor: &unit,
		DiscountMinor: &disc, PricingClass: &class,
	}
	if _, ok, err := f.q.InsertBillableEvent(context.Background(), p); err != nil || !ok {
		f.t.Fatalf("event: ok=%v err=%v", ok, err)
	}
	row, err := f.q.GetBillableEvent(context.Background(), s.kind, p.SubjectID)
	if err != nil {
		f.t.Fatal(err)
	}
	return row
}

func (f *fixture) rate() {
	f.t.Helper()
	r := rate.New(f.pool, rate.Options{
		Since:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Settler: credits.NewSettler(credits.Options{Now: func() time.Time { return f.now }}),
		Lister:  scopedLister{f.q, f.orgID},
	})
	if _, err := r.Run(context.Background()); err != nil {
		f.t.Fatalf("rate pass: %v", err)
	}
}

func (f *fixture) allocations(ev gen.BillableEventRow) map[string]int64 {
	f.t.Helper()
	ch, err := f.q.GetChargeByEvent(context.Background(), ev.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.q.ListChargeAllocations(context.Background(), ch.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, a := range rows {
		out[a.Kind] += a.AmountMinor
	}
	return out
}

func (f *fixture) lot(id uuid.UUID) gen.CreditLotRow {
	f.t.Helper()
	l, err := f.q.GetCreditLot(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return l
}

// journalMatches is the reconcile rule: every lot equals its journal and its
// consume entries equal the allocations naming it.
func (f *fixture) journalMatches() {
	f.t.Helper()
	ctx := context.Background()
	if m, err := f.q.ListCreditLotMismatches(ctx, []uuid.UUID{f.orgID}, 100); err != nil || len(m) != 0 {
		f.t.Fatalf("lot vs journal: %+v %v", m, err)
	}
	if m, err := f.q.ListConsumeAllocationMismatches(ctx, []uuid.UUID{f.orgID}, 100); err != nil || len(m) != 0 {
		f.t.Fatalf("consume vs allocations: %+v %v", m, err)
	}
}

// A pack bought before a sale pays it at its tier (0.43 of 0.59, 0.16 pack
// discount, nothing to invoice); a seated ticket adds the pack surcharge from
// the same balance; a sale that happened BEFORE the purchase spends nothing,
// however late it is rated; a repeated purchase writes nothing.
func TestCredits_PackPaysSalesAfterItsPurchaseOnly(t *testing.T) {
	f := newFixture(t)
	bought := f.now.Add(-time.Hour)
	lot := f.buy(f.pack(5), bought)
	if lot.FaceMinor != 43000 || lot.RemainingMinor != 43000 || lot.UnseatedRateMinor == nil || *lot.UnseatedRateMinor != 43 {
		t.Fatalf("lot: %+v", lot)
	}

	early := f.event(sale{kind: "ticket_sold", at: bought.Add(-time.Minute)})
	unseated := f.event(sale{kind: "ticket_sold", at: bought.Add(time.Minute)})
	seated := f.event(sale{kind: "ticket_sold", at: bought.Add(2 * time.Minute), seated: true})
	f.rate()

	if got := f.allocations(early); len(got) != 0 {
		t.Errorf("a sale before the purchase spent credits: %v", got)
	}
	if got := f.allocations(unseated); got[credits.AllocPurchased] != 43 || got[credits.AllocPackDiscount] != 16 {
		t.Errorf("unseated: %v, want 43 from the lot and 16 pack discount", got)
	}
	if got := f.allocations(seated); got[credits.AllocPurchased] != 78 || got[credits.AllocPackDiscount] != 31 {
		t.Errorf("seated: %v, want 78 (43 + 35) from the lot and 31 pack discount", got)
	}
	if l := f.lot(lot.ID); l.RemainingMinor != 43000-43-78 {
		t.Errorf("remaining %d", l.RemainingMinor)
	}
	f.journalMatches()

	// The same invoice again: no second lot.
	ctx := context.Background()
	tx, _ := f.pool.Begin(ctx)
	invoice := *lot.SourceRef
	again, created, err := credits.Purchase(ctx, gen.New(tx), credits.PurchaseInput{
		AccountID: f.acc.ID, OrgID: f.orgID, Currency: "EUR", Pack: f.pack(5), InvoiceID: invoice, PaidAt: bought,
	})
	_ = tx.Commit(ctx)
	if err != nil || created || again.ID != lot.ID {
		t.Fatalf("repeat purchase: created=%v id=%s err=%v", created, again.ID, err)
	}
	if n := f.count(`SELECT count(*) FROM credit_lots WHERE org_id = $1`, f.orgID); n != 1 {
		t.Errorf("%d lots after a repeated purchase", n)
	}
}

// A cancelled session turns what the pack paid for its tickets into ONE bonus
// lot (B-6, revised): never back into the purchased lot. The bonus then pays
// at most 30 % of a ticket of ANOTHER event, and nothing of the event it
// came from.
func TestCredits_CancelledSessionBecomesBonusForOtherEvents(t *testing.T) {
	f := newFixture(t)
	bought := f.now.Add(-48 * time.Hour)
	lot := f.buy(f.pack(5), bought)
	show, other := uuid.New(), uuid.New()
	session := uuid.New()

	s1 := f.event(sale{kind: "ticket_sold", at: bought.Add(time.Hour), session: &session, event: &show})
	s2 := f.event(sale{kind: "ticket_sold", at: bought.Add(2 * time.Hour), session: &session, event: &show, seated: true})
	f.rate()
	cancel := f.event(sale{kind: "session_cancelled", at: bought.Add(3 * time.Hour), session: &session, event: &show})
	f.rate()

	_ = s1
	_ = s2
	bonus, err := f.q.GetCreditLotBySource(context.Background(), f.acc.ID, credits.SourceCancellation, cancel.ID)
	if err != nil {
		t.Fatalf("no cancellation bonus lot: %v", err)
	}
	if bonus.Kind != credits.KindBonus || bonus.FaceMinor != 43+78 || bonus.RemainingMinor != 43+78 {
		t.Fatalf("bonus lot %+v, want 121 = what the pack paid", bonus)
	}
	if bonus.ExpiresAt != nil {
		t.Errorf("bonus expiry %v: the owner has not named the term yet (ST-2)", bonus.ExpiresAt)
	}
	if l := f.lot(lot.ID); l.RemainingMinor != 43000-121 {
		t.Errorf("the purchased lot got money back: %d", l.RemainingMinor)
	}

	sameShow := f.event(sale{kind: "ticket_sold", at: bought.Add(4 * time.Hour), event: &show})
	nextShow := f.event(sale{kind: "ticket_sold", at: bought.Add(5 * time.Hour), event: &other})
	f.rate()
	if got := f.allocations(sameShow); got[credits.AllocBonus] != 0 {
		t.Errorf("the bonus paid for the event it came from: %v", got)
	}
	if got := f.allocations(nextShow); got[credits.AllocBonus] != 12 || got[credits.AllocPurchased] != 31 || got[credits.AllocPackDiscount] != 16 {
		t.Errorf("next event: %v, want 12 bonus (30 %% of 0.43), 31 from the pack, 16 discount", got)
	}
	f.journalMatches()
}

// Without a pack the account still uses a cancellation bonus for 30 % and
// leaves the rest for the invoice; a reversal of a ticket the bonus paid
// returns the money to that bonus lot.
func TestCredits_BonusWithoutPackAndItsReturn(t *testing.T) {
	f := newFixture(t)
	start := f.now.Add(-72 * time.Hour)
	show, next := uuid.New(), uuid.New()
	session := uuid.New()
	// A bonus lot from an old cancellation, granted by hand for the test.
	ctx := context.Background()
	src := uuid.New()
	bonus, _, err := f.q.InsertCreditLot(ctx, gen.InsertCreditLotParams{
		AccountID: f.acc.ID, OrgID: f.orgID, Currency: "EUR", Kind: credits.KindBonus,
		Source: credits.SourceAdjustment, SourceRef: &src, PurchasedAt: start, FaceMinor: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.must(`INSERT INTO credit_ledger (account_id, lot_id, entry_type, amount_minor, currency, remaining_after, reason)
	        VALUES ($1, $2, 'adjustment_credit', 1000, 'EUR', 1000, 'test grant')`, f.acc.ID, bonus.ID)

	sold := f.event(sale{kind: "ticket_sold", at: start.Add(time.Hour), session: &session, event: &next})
	f.rate()
	if got := f.allocations(sold); got[credits.AllocBonus] != 17 || len(got) != 1 {
		t.Errorf("FLEX sale: %v, want 17 bonus only (42 stays for the invoice)", got)
	}
	f.event(sale{kind: "session_cancelled", at: start.Add(2 * time.Hour), session: &session, event: &next})
	f.rate()
	if l := f.lot(bonus.ID); l.RemainingMinor != 1000 {
		t.Errorf("bonus lot %d after the reversal, want 1000", l.RemainingMinor)
	}
	if n := f.count(`SELECT count(*) FROM credit_lots WHERE org_id = $1 AND source = 'cancellation'`, f.orgID); n != 0 {
		t.Errorf("a cancellation bonus was granted for money that was never paid: %d lots", n)
	}
	_ = show
	f.journalMatches()
}

// An expired bonus lot is emptied by the expiry job with a journal entry; a
// reversal into it afterwards is journaled as dropped and gives nothing.
func TestCredits_ExpiryAndDroppedReversal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	start := f.now.Add(-10 * 24 * time.Hour)
	expires := f.now.Add(-time.Hour)
	src := uuid.New()
	bonus, _, err := f.q.InsertCreditLot(ctx, gen.InsertCreditLotParams{
		AccountID: f.acc.ID, OrgID: f.orgID, Currency: "EUR", Kind: credits.KindBonus,
		Source: credits.SourceAdjustment, SourceRef: &src, PurchasedAt: start, FaceMinor: 500, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.must(`INSERT INTO credit_ledger (account_id, lot_id, entry_type, amount_minor, currency, remaining_after, reason)
	        VALUES ($1, $2, 'adjustment_credit', 500, 'EUR', 500, 'test grant')`, f.acc.ID, bonus.ID)
	session, show := uuid.New(), uuid.New()
	f.event(sale{kind: "ticket_sold", at: start.Add(time.Hour), session: &session, event: &show})
	f.rate()

	n, sums, err := credits.NewExpirer(f.pool, func() time.Time { return f.now }, nil).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 || sums["EUR"] < 500-17 {
		t.Errorf("expired %d lots, %v", n, sums)
	}
	if l := f.lot(bonus.ID); l.RemainingMinor != 0 {
		t.Fatalf("expired lot still holds %d", l.RemainingMinor)
	}
	f.event(sale{kind: "session_cancelled", at: start.Add(2 * time.Hour), session: &session, event: &show})
	f.rate()
	if l := f.lot(bonus.ID); l.RemainingMinor != 0 {
		t.Errorf("an expired bonus was revived: %d", l.RemainingMinor)
	}
	if n := f.count(`SELECT count(*) FROM credit_ledger WHERE lot_id = $1 AND entry_type = 'reversal_dropped'`, bonus.ID); n != 1 {
		t.Errorf("%d reversal_dropped entries, want 1", n)
	}
	f.journalMatches()
}

// Fifty charges settled at once against one small lot never take it below
// zero and never spend a credit twice (spec 42, B3-2 acceptance).
func TestCredits_FiftyParallelConsumes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lot := f.buy(f.pack(1), f.now.Add(-time.Hour)) // 16.50 at 0.55
	settler := credits.NewSettler(credits.Options{Now: func() time.Time { return f.now }})

	terms, err := billing.TermsAt(ctx, f.q, f.acc.ID, f.now)
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	events := make([]gen.BillableEventRow, n)
	charges := make([]gen.ChargeRow, n)
	for i := range events {
		events[i] = f.event(sale{kind: "ticket_sold", at: f.now.Add(time.Duration(i) * time.Second)})
	}
	for i, ev := range events {
		t2, err := billing.TermsAt(ctx, f.q, f.acc.ID, ev.OccurredAt)
		if err != nil {
			t.Fatal(err)
		}
		c, err := billing.Rate(t2, billing.Event{Kind: billing.KindTicketSold, OccurredAt: ev.OccurredAt,
			Currency: "EUR", UnitPriceMinor: 1500, PricingClass: billing.PricingClassStandard})
		if err != nil {
			t.Fatal(err)
		}
		plID, ver := terms.PriceList.ID, terms.PriceList.Version
		inputs := []byte(`{"unseated_minor":59,"seated_minor":109,"seated":0}`)
		row, err := f.q.InsertCharge(ctx, gen.InsertChargeParams{
			BillableEventID: ev.ID, AccountID: f.acc.ID, Currency: "EUR", AmountMinor: c.AmountMinor,
			RuleType: string(c.RuleType), RuleID: c.RuleID, PriceListID: &plID, PriceListVersion: &ver, Inputs: inputs,
		})
		if err != nil {
			t.Fatal(err)
		}
		charges[i] = row
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := settler.Settle(ctx, gen.New(tx), events[i], charges[i]); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
	}
	l := f.lot(lot.ID)
	spent := f.count(`SELECT COALESCE(SUM(amount_minor), 0) FROM charge_allocations WHERE lot_id = $1`, lot.ID)
	if l.RemainingMinor < 0 || spent+l.RemainingMinor != l.FaceMinor {
		t.Fatalf("lot face %d, spent %d, remaining %d", l.FaceMinor, spent, l.RemainingMinor)
	}
	// 1650 at 55 a ticket: exactly 30 tickets fully paid, the lot empty.
	if l.RemainingMinor != 0 || spent != 1650 {
		t.Errorf("remaining %d, spent %d; want 0 and 1650", l.RemainingMinor, spent)
	}
	full := f.count(`SELECT count(*) FROM charge_allocations a JOIN charges c ON c.id = a.charge_id
	                 WHERE c.account_id = $1 AND a.kind = 'purchased_lot' AND a.amount_minor = 55`, f.acc.ID)
	if full != 30 {
		t.Errorf("%d tickets fully paid from the pack, want 30", full)
	}
	f.journalMatches()
}
