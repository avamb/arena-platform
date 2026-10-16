//go:build integration

// foundation_integration_test.go — migration 0157 against a live database
// (task B1-1). Connects to DATABASE_URL and skips when it is unset; run it on
// a FRESH migrated database (AGENTS.md, "Reproduce the CI Integration job on
// a FRESH database"), never the shared dev stand.
package billing_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

func openPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// Every account the migration created matches the currencies of its
// organization's live venues (or the organization's own country when it has
// none), one account per currency, each with exactly one first-terms row.
func TestFoundation_MigrationAccountsFollowVenueCurrencies(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()

	// Organizations whose accounts the migration wrote, with the account
	// currencies they have and the ones the routing rule expects.
	const q = `
WITH migrated AS (
    SELECT DISTINCT a.org_id
    FROM   billing_accounts a
    JOIN   billing_account_terms t ON t.account_id = a.id AND t.created_by = 'migration'
    JOIN   organizations o ON o.id = a.org_id AND o.deleted_at IS NULL
),
org_countries AS (
    SELECT v.org_id,
           upper(btrim(COALESCE(NULLIF(btrim(v.country), ''), c.iso2::text, ''))) AS country
    FROM   venues v
    LEFT JOIN cities ci ON ci.id = v.city_id
    LEFT JOIN countries c ON c.id = ci.country_id
    WHERE  v.deleted_at IS NULL AND v.org_id IN (SELECT org_id FROM migrated)
    UNION ALL
    SELECT o.id, upper(btrim(COALESCE(NULLIF(btrim(o.legal_address_country), ''), NULLIF(btrim(o.country), ''), '')))
    FROM   organizations o
    WHERE  o.id IN (SELECT org_id FROM migrated)
      AND  NOT EXISTS (SELECT 1 FROM venues v WHERE v.org_id = o.id AND v.deleted_at IS NULL)
),
expected AS (
    SELECT DISTINCT oc.org_id, COALESCE(r.currency::text, 'EUR') AS currency
    FROM   org_countries oc LEFT JOIN billing_country_routes r ON r.country::text = oc.country
),
actual AS (
    SELECT org_id, currency::text AS currency FROM billing_accounts
    WHERE  account_kind = 'organizer' AND org_id IN (SELECT org_id FROM migrated)
)
SELECT (SELECT count(*) FROM migrated),
       (SELECT count(*) FROM ((SELECT * FROM expected EXCEPT SELECT * FROM actual)
                              UNION ALL
                              (SELECT * FROM actual EXCEPT SELECT * FROM expected)) d)`
	var migrated, diff int
	if err := pool.QueryRow(ctx, q).Scan(&migrated, &diff); err != nil {
		t.Fatal(err)
	}
	t.Logf("organizations with migration-created accounts: %d", migrated)
	if diff != 0 {
		t.Fatalf("%d (organization, currency) pairs differ between the accounts and the venue currencies", diff)
	}

	var badTerms int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM billing_accounts a
WHERE (SELECT count(*) FROM billing_account_terms t WHERE t.account_id = a.id) <> 1`).Scan(&badTerms); err != nil {
		t.Fatal(err)
	}
	if badTerms != 0 {
		t.Fatalf("%d accounts do not have exactly one terms row", badTerms)
	}
}

func TestFoundation_StandardPriceListIsFoundFrom2026(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	for _, cur := range []string{"EUR", "USD"} {
		for _, at := range []time.Time{
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		} {
			pl, err := q.GetPriceListVersionAt(ctx, billing.StandardPriceListCode, cur, at)
			if err != nil {
				t.Fatalf("%s at %s: %v", cur, at, err)
			}
			if pl.Version != 1 || pl.Currency != cur || pl.BonusShareCapBP != 3000 || pl.BonusCoversSurcharge {
				t.Fatalf("%s price list = %+v", cur, pl)
			}
			rules, err := q.ListPriceListRules(ctx, pl.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rules) != 4 {
				t.Fatalf("%s: %d rules, want 4", cur, len(rules))
			}
			for _, r := range rules {
				if r.RuleType == "low_price_formula" && (r.SaleCurrency == nil || *r.SaleCurrency != cur) {
					t.Fatalf("%s: low price rule sale currency = %v", cur, r.SaleCurrency)
				}
			}
			wantParams := map[string]string{
				"low_price_formula":         `{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}`,
				"fixed_per_ticket":          `{"unseated_minor":59,"seated_minor":109}`,
				"credit_pack":               `{"seated_surcharge_minor":35}`,
				"percent_of_price_with_cap": `{"pct_bp":200,"cap_minor":300}`,
			}
			for _, r := range rules {
				var got, want map[string]any
				if err := json.Unmarshal(r.Params, &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(wantParams[r.RuleType]), &want); err != nil {
					t.Fatalf("%s: unexpected rule type %q", cur, r.RuleType)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s %s params = %s, want %s", cur, r.RuleType, r.Params, wantParams[r.RuleType])
				}
			}
			packs, err := q.ListPriceListPacks(ctx, pl.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantPacks := [][2]int64{{1650, 55}, {5200, 52}, {14700, 49}, {23000, 46}, {43000, 43}, {100000, 40}, {190000, 38}}
			for i, w := range wantPacks {
				if i >= len(packs) || packs[i].FaceMinor != w[0] || packs[i].UnseatedRateMinor != w[1] || packs[i].SeatedSurchargeMinor != 35 {
					t.Fatalf("%s pack %d = %+v, want face %d rate %d surcharge 35", cur, i+1, packs, w[0], w[1])
				}
			}
			if len(packs) != 7 || packs[0].FaceMinor != 1650 || packs[0].UnseatedRateMinor != 55 ||
				packs[6].FaceMinor != 190000 || packs[6].UnseatedRateMinor != 38 || packs[6].SeatedSurchargeMinor != 35 {
				t.Fatalf("%s packs = %+v", cur, packs)
			}
		}
		_, err := q.GetPriceListVersionAt(ctx, billing.StandardPriceListCode, cur,
			time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC))
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s at 2025-12-31: err = %v, want no rows", cur, err)
		}
	}
}

// On a fresh database no organization carries a client name from policy
// section 10, so the migration wrote no individual rate.
func TestFoundation_RateOverridesOnlyForNamedClients(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	var matching, overrides int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM organizations
        WHERE deleted_at IS NULL
          AND (name ILIKE 'Lampyris%' OR name ILIKE 'Vino%Co%'
               OR name ILIKE '%Учебный театр%' OR name ILIKE '%Колыбель%')),
       (SELECT count(*) FROM org_rate_overrides)`).Scan(&matching, &overrides); err != nil {
		t.Fatal(err)
	}
	if matching == 0 && overrides != 0 {
		t.Fatalf("no named client exists but org_rate_overrides holds %d rows", overrides)
	}
	t.Logf("named clients: %d, overrides: %d", matching, overrides)
}

func TestFoundation_EnsureAccountRoutesAndNeverDuplicates(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	q := gen.New(pool)

	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Billing B1-1 "+suffix, "billing-b11-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM billing_accounts WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	cz, err := billing.EnsureAccount(ctx, q, orgID, "CZ")
	if err != nil {
		t.Fatal(err)
	}
	if cz.Currency != "EUR" {
		t.Fatalf("CZ account currency = %s", cz.Currency)
	}

	// Inside a transaction through WithTx, as the collect job will call it.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mx, err := billing.EnsureAccount(ctx, q.WithTx(tx), orgID, "MX")
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if mx.Currency != "USD" {
		t.Fatalf("MX account currency = %s", mx.Currency)
	}

	none, err := billing.EnsureAccount(ctx, q, orgID, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := billing.EnsureAccount(ctx, q, orgID, "cz")
	if err != nil {
		t.Fatal(err)
	}
	if none.ID != cz.ID || again.ID != cz.ID {
		t.Fatalf("EUR calls returned different accounts: %s %s %s", cz.ID, none.ID, again.ID)
	}

	accounts, err := q.ListBillingAccountsByOrg(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2 (EUR and USD)", len(accounts))
	}
	issuer, err := q.GetBillingIssuerByCode(ctx, billing.DefaultIssuerCode)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.Country != "EE" || issuer.Timezone != "Europe/Tallinn" {
		t.Fatalf("issuer = %+v", issuer)
	}
	for _, a := range accounts {
		if a.IssuerID != issuer.ID {
			t.Fatalf("account %s issuer = %s, want %s", a.ID, a.IssuerID, issuer.ID)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM billing_account_terms WHERE account_id = $1`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("account %s has %d terms rows", a.ID, n)
		}
		terms, err := q.GetBillingAccountTermsAt(ctx, a.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if terms.PriceListCode != billing.StandardPriceListCode || terms.BillingCycle != "monthly" ||
			terms.PlanKind != "standard" || terms.ShortfallPolicy != "invoice_postpaid" {
			t.Fatalf("terms = %+v", terms)
		}
		if _, err := q.GetBillingAccountTermsAt(ctx, a.ID, time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("terms before 2026: err = %v, want no rows", err)
		}
	}

	// A dated individual rate is found inside its window only.
	if _, err := pool.Exec(ctx, `
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, created_by)
VALUES ($1, 'EUR', 50, 100, '2026-02-01T00:00:00Z', '2026-03-01T00:00:00Z', 'test')`, orgID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM org_rate_overrides WHERE org_id = $1`, orgID)
	})
	ov, err := q.GetOrgRateOverrideAt(ctx, orgID, "EUR", time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ov.UnseatedMinor != 50 || ov.SeatedMinor != 100 {
		t.Fatalf("override = %+v", ov)
	}
	if _, err := q.GetOrgRateOverrideAt(ctx, orgID, "EUR", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("override after effective_to: err = %v, want no rows", err)
	}
	if _, err := q.GetOrgRateOverrideAt(ctx, orgID, "USD", time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("override in USD: err = %v, want no rows", err)
	}

	routes, err := q.ListBillingCountryRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 52 {
		t.Fatalf("routes = %d, want 52 (31 EUR + 21 USD: US and 20 Latin American countries)", len(routes))
	}
}
