// billing_append_only_test.go keeps three billing rules true (spec
// 08_architecture/42 §3 п.9 and 41 §6.10), before the tables even exist:
//
//  1. billable_events, charges, charge_reversals, credit_ledger and
//     campaign_grants are INSERT-only: no UPDATE, DELETE FROM or TRUNCATE of
//     them anywhere in apps/backend/{cmd,internal,tests}, except _test.go
//     fixture clean-up, testdata and the Down section of a migration. A
//     correction is a compensating row, even inside internal/platform/billing.
//  2. A billing table (those five plus charge_allocations and credit_lots) is
//     INSERTed into only by the billing package: internal/platform/billing, its
//     query files (queries/billing_*.sql) and its generated wrappers
//     (gen/billing_*.sql.go), plus migrations. No other non-test Go file may
//     call a gen wrapper named Insert|Append<BillableEvent|Charge|...>.
//  3. Money is int64 minor units with its currency beside it: no float field,
//     parameter or result, every …Minor/Amount/Total/Price/Face/Remaining/Rate
//     field is int64 or *int64, and its struct has a string Currency.
//
// The scanners live in billing_guard_scan_test.go and are proven on the
// fixtures in testdata/billing_guard (good has none, each bad_* has exactly
// the listed ones). To allow a new place on purpose, extend the allowlist in
// that file and say why in the commit.
package staticanalysis

import (
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func loadBilling(t *testing.T, base string) []billingSource {
	t.Helper()
	srcs, err := walkBillingSources(base)
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	return srcs
}

func fixtureBase(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "apps", "backend", "tests", "staticanalysis", "testdata", "billing_guard", name)
}

func lines(vs []billingViolation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.File+":"+strconv.Itoa(v.Line))
	}
	sort.Strings(out)
	return out
}

func report(vs []billingViolation) string {
	var sb strings.Builder
	for _, v := range vs {
		sb.WriteString("\n  " + v.String())
	}
	return sb.String()
}

// TestBillingGuards_ProvedOnFixtures: the scanners are silent on the good
// fixture and catch exactly the planted violations of each bad_* one.
func TestBillingGuards_ProvedOnFixtures(t *testing.T) {
	type scan func([]billingSource) []billingViolation
	all := func(s []billingSource) []billingViolation {
		var out []billingViolation
		out = append(out, appendOnlyViolations(s)...)
		out = append(out, insertOriginViolations(s)...)
		out = append(out, moneyViolations(s)...)
		return out
	}
	cases := []struct {
		dir  string
		scan scan
		want []string
	}{
		{"good", all, nil},
		{"bad_update", all, []string{
			"internal/adapters/postgres/queries/billing_x.sql:2",
			"internal/migrations/sql/0998_backfill.sql:2",
			"internal/platform/billing/y.go:4",
			"internal/platform/httpserver/x.go:3",
			"internal/platform/httpserver/x.go:5",
		}},
		{"bad_delete", all, []string{
			"internal/adapters/postgres/queries/d.sql:2",
			"internal/platform/httpserver/d.go:3",
		}},
		{"bad_insert_outside", all, []string{
			"internal/adapters/postgres/gen/other_gen.sql.go:3",
			"internal/adapters/postgres/queries/other.sql:2",
			"internal/platform/httpserver/z.go:3",
			"internal/platform/httpserver/z.go:6",
		}},
		{"bad_float", all, []string{
			"internal/adapters/postgres/gen/billing_x.sql.go:5",
			"internal/adapters/postgres/gen/billing_x.sql.go:6",
			"internal/adapters/postgres/gen/billing_x.sql.go:4",
			"internal/adapters/postgres/gen/billing_x.sql.go:12",
			"internal/adapters/postgres/gen/billing_x.sql.go:16",
			"internal/adapters/postgres/gen/billing_x.sql.go:16",
			"internal/platform/billing/m.go:3",
		}},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			srcs := loadBilling(t, fixtureBase(t, c.dir))
			if len(srcs) == 0 {
				t.Fatal("fixture directory has no files: the proof would be vacuous")
			}
			vs := c.scan(srcs)
			got := lines(vs)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("violations differ\n got: %v\nwant: %v\ndetails:%s", got, want, report(vs))
			}
		})
	}
}

// TestBillingGuards_EachRuleCatchesItsFixture pins which rule fires where, so
// one rule cannot silently cover for another.
func TestBillingGuards_EachRuleCatchesItsFixture(t *testing.T) {
	check := func(dir string, f func([]billingSource) []billingViolation, n int, rule string) {
		t.Helper()
		vs := f(loadBilling(t, fixtureBase(t, dir)))
		if len(vs) != n {
			t.Errorf("%s: want %d violations of %s, got %d:%s", dir, n, rule, len(vs), report(vs))
		}
		for _, v := range vs {
			if v.Rule != rule {
				t.Errorf("%s: unexpected rule on %s", dir, v)
			}
		}
	}
	check("bad_update", appendOnlyViolations, 5, ruleAppendOnly)
	check("bad_delete", appendOnlyViolations, 2, ruleAppendOnly)
	check("bad_insert_outside", insertOriginViolations, 4, ruleInsertOrigin)
	check("bad_float", moneyViolations, 7, ruleMoneyInt)
	for _, dir := range []string{"bad_insert_outside", "bad_float", "good"} {
		if vs := appendOnlyViolations(loadBilling(t, fixtureBase(t, dir))); len(vs) != 0 {
			t.Errorf("%s: append-only rule fired by mistake:%s", dir, report(vs))
		}
	}
}

var (
	repoBillingOnce sync.Once
	repoBillingSrcs []billingSource
	repoBillingErr  error
)

// repoBackend scans the real tree once for the three guards.
func repoBackend(t *testing.T) []billingSource {
	t.Helper()
	repoBillingOnce.Do(func() {
		repoBillingSrcs, repoBillingErr = walkBillingSources(filepath.Join(repoRoot(t), "apps", "backend"))
	})
	if repoBillingErr != nil {
		t.Fatalf("walk repository: %v", repoBillingErr)
	}
	srcs := repoBillingSrcs
	// Sanity: the walk really saw the tree (and not the fixtures).
	var seenGo, seenSQL bool
	for _, s := range srcs {
		if strings.HasPrefix(s.rel, "tests/staticanalysis/testdata/") {
			t.Fatalf("testdata leaked into the repository scan: %s", s.rel)
		}
		seenGo = seenGo || s.ext == ".go"
		seenSQL = seenSQL || s.migrate
	}
	if !seenGo || !seenSQL {
		t.Fatal("the repository scan found no Go or migration files; did the walk break?")
	}
	return srcs
}

func TestBillingGuard_TablesAreAppendOnly(t *testing.T) {
	if vs := appendOnlyViolations(repoBackend(t)); len(vs) > 0 {
		t.Errorf("billable_events, charges, charge_reversals, credit_ledger and campaign_grants are INSERT-only (spec 42 §3 п.9):%s", report(vs))
	}
}

func TestBillingGuard_InsertsComeOnlyFromTheBillingPackage(t *testing.T) {
	if vs := insertOriginViolations(repoBackend(t)); len(vs) > 0 {
		t.Errorf("billing tables are written only from internal/platform/billing (spec 42 §3 п.9):%s", report(vs))
	}
}

func TestBillingGuard_MoneyIsBigint(t *testing.T) {
	if vs := moneyViolations(repoBackend(t)); len(vs) > 0 {
		t.Errorf("billing money is int64 minor units with its currency (spec 41 §6.10):%s", report(vs))
	}
}
