//go:build integration

// org_modules_0198_integration_test.go — migration 0198 (spec 61, M1): the
// backfill of existing shops, the rewrite of the sales-notification kinds and
// the Down section. The test runs the Down section of 0198 inside a
// transaction (the database is already at 0198), inserts rows of the OLD shape,
// runs the Up section again and checks what it made. Everything is rolled back.
package migrations_test

import (
	"context"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// section0198 returns the Up or the Down text of 0198 (it has no
// StatementBegin blocks, so the text runs as one multi-statement exec).
func section0198(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0198_org_modules.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(body), "-- +goose Down")
	if !ok {
		t.Fatal("no Down section")
	}
	_, up, ok = strings.Cut(up, "-- +goose Up")
	if !ok {
		t.Fatal("no Up section")
	}
	if which == "up" {
		return up
	}
	return down
}

func kindsOf(t *testing.T, ctx context.Context, tx pgx.Tx, chat string) []string {
	t.Helper()
	var k []string
	if err := tx.QueryRow(ctx, `SELECT kinds FROM sales_notification_subscriptions WHERE chat_id = $1`, chat).Scan(&k); err != nil {
		t.Fatalf("kinds of %s: %v", chat, err)
	}
	sort.Strings(k)
	return k
}

func TestMigration0198_BackfillKindsAndDown(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, section0198(t, "down")); err != nil {
		t.Fatalf("Down of 0198: %v", err)
	}
	var reg *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('org_module_periods')::text`).Scan(&reg); err != nil || reg != nil {
		t.Fatalf("Down left org_module_periods behind: %v %v", reg, err)
	}

	// Fixtures of the old shape (random names: slugs are unique).
	mkOrg := func(tag string) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id::text`,
			"mig0198 "+tag, "mig0198-"+tag+"-"+time.Now().Format("150405.000000000")+"-"+tag).Scan(&id); err != nil {
			t.Fatalf("org %s: %v", tag, err)
		}
		return id
	}
	orgPlain := mkOrg("plain")   // shop_settings only
	orgOld := mkOrg("old")       // an older product and order, plus a billing account in USD
	orgNoShop := mkOrg("noshop") // no shop at all
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	mustExec(`INSERT INTO shop_settings (org_id, currency, updated_at) VALUES ($1, 'CZK', '2026-10-20 10:00:00+00')`, orgPlain)
	mustExec(`INSERT INTO shop_settings (org_id, currency, updated_at) VALUES ($1, 'EUR', '2026-10-25 23:30:00+00')`, orgOld)
	mustExec(`INSERT INTO shop_products (org_id, slug, title, created_at) VALUES ($1, 'p', 'P', '2026-10-01 23:30:00+00')`, orgOld)
	mustExec(`INSERT INTO shop_orders (org_id, order_token, currency, subtotal, total, created_at)
	          VALUES ($1, repeat('a', 64), 'EUR', 100, 100, '2026-09-28 08:00:00+00')`, orgOld)
	mustExec(`INSERT INTO billing_accounts (org_id, currency, issuer_id) VALUES ($1, 'USD', (SELECT id FROM billing_issuers LIMIT 1))`, orgOld)

	chat := func(tag string) string { return "mig0198-" + tag + "-" + time.Now().Format("150405.000000000") }
	cT, cS, cB, cOpT, cOpS := chat("t"), chat("s"), chat("b"), chat("opt"), chat("ops")
	mustExec(`INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds) VALUES
	    ($1, 'tickets', $2, '{tickets}'), ($1, 'shop', $3, '{shop}'), ($1, 'both', $4, '{tickets,shop}'),
	    (NULL, 'op tickets', $5, '{tickets}'), (NULL, 'op shop', $6, '{shop}')`, orgPlain, cT, cS, cB, cOpT, cOpS)

	if _, err := tx.Exec(ctx, section0198(t, "up")); err != nil {
		t.Fatalf("Up of 0198: %v", err)
	}

	// Backfill: one open shop period per shop_settings row, price 0.
	type per struct {
		startsOn string
		endsOn   *string
		price    *int64
		currency *string
		reason   string
	}
	get := func(org string) []per {
		rows, err := tx.Query(ctx, `SELECT to_char(starts_on,'YYYY-MM-DD'), to_char(ends_on,'YYYY-MM-DD'), monthly_price_minor, currency, reason
		                            FROM org_module_periods WHERE org_id = $1 AND module = 'shop' ORDER BY starts_on`, org)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []per
		for rows.Next() {
			var p per
			if err := rows.Scan(&p.startsOn, &p.endsOn, &p.price, &p.currency, &p.reason); err != nil {
				t.Fatal(err)
			}
			out = append(out, p)
		}
		return out
	}
	const why = "migration 0198: shop existed before modules"
	zero := int64(0)
	if got := get(orgPlain); len(got) != 1 || got[0].startsOn != "2026-10-20" || got[0].endsOn != nil ||
		got[0].price == nil || *got[0].price != zero || got[0].currency == nil || *got[0].currency != "CZK" || got[0].reason != why {
		t.Errorf("plain shop period = %+v", got)
	}
	// The earliest trace wins (the order of 09-28), and the billing account's
	// currency beats the shop's own.
	if got := get(orgOld); len(got) != 1 || got[0].startsOn != "2026-09-28" || got[0].currency == nil || *got[0].currency != "USD" {
		t.Errorf("old shop period = %+v", got)
	}
	if got := get(orgNoShop); len(got) != 0 {
		t.Errorf("an organization without a shop got a period: %+v", got)
	}
	var createdBy *string
	if err := tx.QueryRow(ctx, `SELECT created_by::text FROM org_module_periods WHERE org_id = $1`, orgPlain).Scan(&createdBy); err != nil || createdBy != nil {
		t.Errorf("created_by = %v (%v), want NULL", createdBy, err)
	}

	// Kinds: shop -> shop_delivery, tickets also hear shop_sale, operator row too.
	for _, c := range []struct {
		chat string
		want []string
	}{
		{cT, []string{"shop_sale", "tickets"}},
		{cS, []string{"shop_delivery"}},
		{cB, []string{"shop_delivery", "shop_sale", "tickets"}},
		{cOpT, []string{"shop_sale", "tickets"}},
		{cOpS, []string{"shop_delivery"}},
	} {
		if got := kindsOf(t, ctx, tx, c.chat); !reflect.DeepEqual(got, c.want) {
			t.Errorf("kinds of %s = %v, want %v", c.chat, got, c.want)
		}
	}
	// The new CHECK refuses the old value and an empty list.
	for _, bad := range []string{"{shop}", "{}", "{tickets,other}"} {
		if _, err := tx.Exec(ctx, `SAVEPOINT bad`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_notification_subscriptions SET kinds = $2::text[] WHERE chat_id = $1`, cT, bad); err == nil {
			t.Errorf("kinds %s accepted", bad)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT bad`); err != nil {
			t.Fatal(err)
		}
	}

	// Price list: a zero subscription rule on every standard-2026 version,
	// one each, no duplicates on a re-run of the INSERT.
	var lists, rules int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM price_lists WHERE code = 'standard-2026'`).Scan(&lists); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM price_list_rules r JOIN price_lists pl ON pl.id = r.price_list_id
	    WHERE pl.code = 'standard-2026' AND r.rule_type = 'subscription' AND r.event_kind = 'shop_module_month'
	      AND r.line = 'standard' AND (r.params->>'monthly_minor')::bigint = 0`).Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if lists == 0 || rules != lists {
		t.Errorf("price rules = %d for %d price lists", rules, lists)
	}

	// The billable_events kind is allowed now, the table's CHECKs hold.
	mustExec(`SAVEPOINT cks`)
	if _, err := tx.Exec(ctx, `INSERT INTO org_module_periods (org_id, module, starts_on, reason) VALUES ($1, 'wine', '2026-11-01', 'x')`, orgNoShop); err == nil {
		t.Error("unknown module accepted")
	}
	mustExec(`ROLLBACK TO SAVEPOINT cks`)
	mustExec(`SAVEPOINT cks2`)
	if _, err := tx.Exec(ctx, `INSERT INTO org_module_periods (org_id, module, starts_on, monthly_price_minor, reason) VALUES ($1, 'shop', '2026-11-01', 5, 'x')`, orgNoShop); err == nil {
		t.Error("a price without a currency accepted")
	}
	mustExec(`ROLLBACK TO SAVEPOINT cks2`)

	// Down again: the old shape comes back, nothing is lost for old rows.
	if _, err := tx.Exec(ctx, section0198(t, "down")); err != nil {
		t.Fatalf("second Down of 0198: %v", err)
	}
	for _, c := range []struct {
		chat string
		want []string
	}{
		{cT, []string{"tickets"}},
		{cS, []string{"shop"}},
		{cB, []string{"shop", "tickets"}},
		{cOpT, []string{"tickets"}},
		{cOpS, []string{"shop"}},
	} {
		if got := kindsOf(t, ctx, tx, c.chat); !reflect.DeepEqual(got, c.want) {
			t.Errorf("after Down: kinds of %s = %v, want %v", c.chat, got, c.want)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM price_list_rules WHERE event_kind = 'shop_module_month'`).Scan(&rules); err != nil || rules != 0 {
		t.Errorf("Down left %d shop_module_month rules (%v)", rules, err)
	}
}

// TestMigration0198_DownRefusesOnceAModuleMonthIsCharged: a billing fact is
// never deleted, so the restored CHECK must fail while one exists.
func TestMigration0198_DownRefusesOnceAModuleMonthIsCharged(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var org, account string
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ('mig0198 charged', $1) RETURNING id::text`,
		"mig0198-charged-"+time.Now().Format("150405.000000000")).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO billing_accounts (org_id, currency, issuer_id)
	    VALUES ($1, 'EUR', (SELECT id FROM billing_issuers LIMIT 1)) RETURNING id::text`, org).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billable_events (kind, subject_id, occurred_at, org_id, account_id)
	    VALUES ('shop_module_month', uuidv7(), now(), $1, $2)`, org, account); err != nil {
		t.Fatalf("the new kind is refused: %v", err)
	}
	if _, err := tx.Exec(ctx, section0198(t, "down")); err == nil {
		t.Fatal("Down succeeded although a shop_module_month event exists")
	}
}
