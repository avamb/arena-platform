//go:build integration

// charge_integration_test.go: the shop module charge against a live database,
// from a period row to a rated charge and an invoice line. Connects to
// DATABASE_URL and skips when it is unset; run it on a FRESH migrated
// database, never the shared dev stand.
package orgmodulecharge_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/invoicing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/orgmodulecharge"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/rate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/whitelabelcharge"
)

func utc(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

type itFixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *gen.Queries
	orgID uuid.UUID
}

type orgScopedLister struct {
	*gen.Queries
	org uuid.UUID
}

func (l orgScopedLister) ListUnratedBillableEventsAfter(ctx context.Context, at time.Time, id uuid.UUID, limit int32) ([]gen.BillableEventRow, error) {
	rows, err := l.Queries.ListUnratedBillableEventsAfter(ctx, at, id, 100000)
	var out []gen.BillableEventRow
	for _, r := range rows {
		if r.OrgID == l.org && int32(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, err
}

func newITFixture(t *testing.T) *itFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &itFixture{t: t, pool: pool, q: gen.New(pool), orgID: uuid.New()}
	t.Cleanup(f.cleanup)
	sfx := f.orgID.String()[:8]
	f.exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.orgID, "Module Org "+sfx, "mod-"+sfx)
	return f
}

func (f *itFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *itFixture) cleanup() {
	ctx := context.Background()
	exec := func(sql string) {
		if _, err := f.pool.Exec(ctx, sql, f.orgID); err != nil {
			f.t.Logf("cleanup (%s): %v", sql, err)
		}
	}
	acc := `(SELECT id FROM billing_accounts WHERE org_id = $1)`
	exec(`DELETE FROM charge_allocations WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN ` + acc + `)`)
	exec(`DELETE FROM invoices WHERE org_id = $1`)
	exec(`DELETE FROM charge_reversals WHERE charge_id IN (SELECT id FROM charges WHERE account_id IN ` + acc + `)`)
	exec(`DELETE FROM charges WHERE account_id IN ` + acc)
	exec(`DELETE FROM billable_events WHERE org_id = $1`)
	exec(`DELETE FROM billing_account_terms WHERE account_id IN ` + acc)
	exec(`DELETE FROM billing_accounts WHERE org_id = $1`)
	exec(`DELETE FROM org_module_periods WHERE org_id = $1`)
	exec(`DELETE FROM white_label_periods WHERE org_id = $1`)
	exec(`DELETE FROM organizations WHERE id = $1`)
}

func (f *itFixture) shopPeriod(starts time.Time, ends *time.Time, price *int64, currency *string) uuid.UUID {
	f.t.Helper()
	p, err := f.q.InsertOrgModulePeriod(context.Background(), gen.InsertOrgModulePeriodParams{
		OrgID: f.orgID, Module: billing.ModuleShop, StartsOn: starts, EndsOn: ends,
		MonthlyPriceMinor: price, Currency: currency, Reason: "integration test",
	})
	if err != nil {
		f.t.Fatalf("period: %v", err)
	}
	return p.ID
}

func (f *itFixture) charger(since, now time.Time) *orgmodulecharge.Charger {
	return orgmodulecharge.New(f.q, orgmodulecharge.Options{
		Since: since, OrgID: &f.orgID, Now: func() time.Time { return now },
	})
}

func (f *itFixture) rate() {
	f.t.Helper()
	r := rate.New(f.pool, rate.Options{Since: utc(2026, 1, 1), Lister: orgScopedLister{f.q, f.orgID}})
	if _, err := r.Run(context.Background()); err != nil {
		f.t.Fatalf("rate: %v", err)
	}
}

type charged struct {
	amount int64
	cur    string
	rule   string
	reason string
	rated  bool
}

// charges maps the subject id of each shop_module_month event of the
// organization to its charge.
func (f *itFixture) charges() map[uuid.UUID]charged {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
SELECT be.subject_id, COALESCE(c.amount_minor, 0), COALESCE(c.currency, ''), COALESCE(c.rule_type, ''),
       COALESCE(c.reason, ''), c.id IS NOT NULL
FROM   billable_events be LEFT JOIN charges c ON c.billable_event_id = be.id
WHERE  be.org_id = $1 AND be.kind = 'shop_module_month'`, f.orgID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]charged{}
	for rows.Next() {
		var s uuid.UUID
		var c charged
		if err := rows.Scan(&s, &c.amount, &c.cur, &c.rule, &c.reason, &c.rated); err != nil {
			f.t.Fatal(err)
		}
		out[s] = c
	}
	return out
}

func i64(v int64) *int64 { return &v }
func str(v string) *string {
	return &v
}

// A period becomes one event per completed month, the rate job turns each into
// exactly one charge prorated by days, and a re-run adds nothing.
func TestShopModule_PeriodToOneProratedChargePerMonth(t *testing.T) {
	f := newITFixture(t)
	ctx := context.Background()
	price := i64(3100)
	period := f.shopPeriod(utc(2026, 10, 16), nil, price, str("EUR"))
	c := f.charger(utc(2026, 9, 1), time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))

	n, err := c.Run(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first pass: %d %v, want 1 event (October)", n, err)
	}
	if n, err := c.Run(ctx); err != nil || n != 0 {
		t.Fatalf("second pass: %d %v, want nothing new", n, err)
	}
	f.rate()
	f.rate() // a second rating pass writes no second charge
	got := f.charges()
	oct := got[orgmodulecharge.SubjectID(period, utc(2026, 10, 1))]
	// 31.00 x 16 / 31 days of October.
	if len(got) != 1 || !oct.rated || oct.amount != 1600 || oct.cur != "EUR" || oct.rule != "subscription" ||
		oct.reason != billing.ReasonIndividualPrice {
		t.Fatalf("charges %+v", got)
	}
	// November closes: one more event and one more charge.
	if n, err := f.charger(utc(2026, 9, 1), time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)).Run(ctx); err != nil || n != 1 {
		t.Fatalf("november: %d %v", n, err)
	}
	f.rate()
	if nov := f.charges()[orgmodulecharge.SubjectID(period, utc(2026, 11, 1))]; nov.amount != 3100 {
		t.Errorf("a whole November: %+v, want 3100", nov)
	}
}

// The price list says 0 (migration 0198): the month is a zero charge with
// reason free, and a period priced 0 by hand is a zero charge with reason
// individual_price. Neither stays unrated.
func TestShopModule_FreePeriodsProduceZeroCharges(t *testing.T) {
	f := newITFixture(t)
	listed := f.shopPeriod(utc(2026, 9, 1), ptrTime(utc(2026, 9, 30)), nil, nil)
	own := f.shopPeriod(utc(2026, 10, 1), nil, i64(0), str("EUR"))
	if n, err := f.charger(utc(2026, 9, 1), time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)).Run(context.Background()); err != nil || n != 2 {
		t.Fatalf("pass: %d %v, want September and October", n, err)
	}
	f.rate()
	got := f.charges()
	sep := got[orgmodulecharge.SubjectID(listed, utc(2026, 9, 1))]
	oct := got[orgmodulecharge.SubjectID(own, utc(2026, 10, 1))]
	if !sep.rated || sep.amount != 0 || sep.reason != billing.ReasonFree || sep.rule != "subscription" {
		t.Errorf("price list zero: %+v", sep)
	}
	if !oct.rated || oct.amount != 0 || oct.reason != billing.ReasonIndividualPrice || oct.rule != "subscription" {
		t.Errorf("individual zero: %+v", oct)
	}
}

// An ended period is charged for its days of the last month and never again.
func TestShopModule_EndedPeriodStopsCharging(t *testing.T) {
	f := newITFixture(t)
	ctx := context.Background()
	period := f.shopPeriod(utc(2026, 10, 1), ptrTime(utc(2026, 11, 10)), i64(3000), str("EUR"))
	c := f.charger(utc(2026, 10, 1), time.Date(2027, 2, 1, 9, 0, 0, 0, time.UTC))
	if n, err := c.Run(ctx); err != nil || n != 2 {
		t.Fatalf("pass: %d %v, want October and November only", n, err)
	}
	f.rate()
	got := f.charges()
	// 30.00 x 10 / 30 days of November.
	if nov := got[orgmodulecharge.SubjectID(period, utc(2026, 11, 1))]; nov.amount != 1000 {
		t.Errorf("November: %+v, want 1000", nov)
	}
	if oct := got[orgmodulecharge.SubjectID(period, utc(2026, 10, 1))]; oct.amount != 3000 {
		t.Errorf("October: %+v, want 3000", oct)
	}
	// Ending it earlier (the admin action) changes what a LATER pass writes only.
	if _, err := f.q.EndOrgModulePeriod(ctx, period, utc(2026, 10, 20), nil); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Run(ctx); err != nil || n != 0 {
		t.Errorf("after ending earlier: %d %v, want nothing new", n, err)
	}
}

// A non-zero individual price typed in another currency than the account's is
// skipped with an error, and a period that fits is still charged.
func TestShopModule_CurrencyMismatchIsSkippedAndReported(t *testing.T) {
	f := newITFixture(t)
	other := f.addOrg("Other")
	bad := f.shopPeriod(utc(2026, 10, 1), nil, i64(2900), str("USD")) // the account is EUR
	good := f.shopPeriod2(other, utc(2026, 10, 1), i64(2900), str("EUR"))
	// One pass per fixture organization keeps the shared database out of it.
	_, errBad := orgmodulecharge.New(f.q, orgmodulecharge.Options{Since: utc(2026, 10, 1), OrgID: &f.orgID,
		Now: func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) }}).Run(context.Background())
	if !errors.Is(errBad, orgmodulecharge.ErrCurrencyChanged) {
		t.Fatalf("error %v, want ErrCurrencyChanged", errBad)
	}
	if _, ok := f.charges()[orgmodulecharge.SubjectID(bad, utc(2026, 10, 1))]; ok {
		t.Error("the mismatching month was written")
	}
	if n, err := orgmodulecharge.New(f.q, orgmodulecharge.Options{Since: utc(2026, 10, 1), OrgID: &other,
		Now: func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) }}).Run(context.Background()); err != nil || n != 1 {
		t.Errorf("the fitting period: %d %v", n, err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM billable_events WHERE org_id = $1 AND subject_id = $2`,
		other, orgmodulecharge.SubjectID(good, utc(2026, 10, 1))).Scan(&count); err != nil || count != 1 {
		t.Errorf("event of the fitting period: %d %v", count, err)
	}
}

// An organization deleted before the month starts pays nothing for it, the
// month of the deletion is still charged, and a pass without the date X does
// nothing at all.
func TestShopModule_DeletedOrganizationAndNoDateX(t *testing.T) {
	f := newITFixture(t)
	ctx := context.Background()
	period := f.shopPeriod(utc(2026, 8, 1), nil, i64(3000), str("EUR"))
	f.exec(`UPDATE organizations SET deleted_at = $2 WHERE id = $1`, f.orgID, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if n, err := f.charger(time.Time{}, time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)).Run(ctx); err != nil || n != 0 {
		t.Fatalf("without the date X: %d %v", n, err)
	}
	if n, err := f.charger(utc(2026, 8, 1), time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)).Run(ctx); err != nil || n != 3 {
		t.Fatalf("pass: %d %v, want August, September, October", n, err)
	}
	if _, ok := f.charges()[orgmodulecharge.SubjectID(period, utc(2026, 11, 1))]; ok {
		t.Error("November was charged after the organization was deleted")
	}
}

// The monthly invoice gets a line of its own for the shop module, apart from
// the white label line, and a free month adds no line.
func TestShopModule_InvoiceLineIsSeparateFromWhiteLabel(t *testing.T) {
	f := newITFixture(t)
	ctx := context.Background()
	f.shopPeriod(utc(2026, 10, 1), nil, i64(2000), str("EUR"))
	f.exec(`INSERT INTO white_label_periods (org_id, starts_on, reason) VALUES ($1, $2, 'integration test')`, f.orgID, utc(2026, 10, 1))
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	if n, err := f.charger(utc(2026, 10, 1), now).Run(ctx); err != nil || n != 1 {
		t.Fatalf("shop pass: %d %v", n, err)
	}
	wl := whitelabelcharge.New(f.q, whitelabelcharge.Options{Since: utc(2026, 10, 1), OrgID: &f.orgID, Now: func() time.Time { return now }})
	if n, err := wl.Run(ctx); err != nil || n != 1 {
		t.Fatalf("white label pass: %d %v", n, err)
	}
	f.rate()
	acc, err := f.q.GetBillingAccount(ctx, f.orgID, "EUR", billing.AccountKindOrganizer)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, err := invoicing.GenerateUsage(ctx, f.q.WithTx(tx), acc.ID, "2026-10", "test", now)
	if err != nil || inv == nil {
		t.Fatalf("invoice: %v %v", inv, err)
	}
	lines := map[string]int64{}
	for _, l := range inv.Lines {
		lines[l.Description] = l.TotalAmountMinor
	}
	if len(inv.Lines) != 2 || lines["Shop module: 1 months"] != 2000 || lines["White label: 1 brand-months"] != 4500 {
		t.Errorf("lines %v", lines)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func (f *itFixture) addOrg(name string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	sfx := id.String()[:8]
	f.exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, id, name+" "+sfx, "mod2-"+sfx)
	f.t.Cleanup(func() {
		ctx := context.Background()
		for _, sql := range []string{
			`DELETE FROM charges WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
			`DELETE FROM billable_events WHERE org_id = $1`,
			`DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`,
			`DELETE FROM billing_accounts WHERE org_id = $1`,
			`DELETE FROM org_module_periods WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := f.pool.Exec(ctx, sql, id); err != nil {
				f.t.Logf("cleanup (%s): %v", sql, err)
			}
		}
	})
	return id
}

func (f *itFixture) shopPeriod2(org uuid.UUID, starts time.Time, price *int64, currency *string) uuid.UUID {
	f.t.Helper()
	p, err := f.q.InsertOrgModulePeriod(context.Background(), gen.InsertOrgModulePeriodParams{
		OrgID: org, Module: billing.ModuleShop, StartsOn: starts, MonthlyPriceMinor: price, Currency: currency,
		Reason: "integration test",
	})
	if err != nil {
		f.t.Fatalf("period: %v", err)
	}
	return p.ID
}
