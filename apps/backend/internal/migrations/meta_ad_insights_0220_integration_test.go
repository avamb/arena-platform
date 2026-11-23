//go:build integration

// meta_ad_insights_0220_integration_test.go — migration 0220 (spec 66 M2):
// Down, Up, Down and Up again inside one transaction (the database is
// already at 0220). Up gives 'ads' to every organization subscription with
// 'tickets' and never to an operator row; Down removes it and deletes a
// subscription that heard only 'ads'. Everything is rolled back.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func section0220(t *testing.T, which string) string {
	t.Helper()
	body, err := os.ReadFile("sql/0220_meta_ad_insights.sql")
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

func TestMigration0220_UpDownUp(t *testing.T) {
	ctx := context.Background()
	conn := connectDB(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tables := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_name IN ('ad_spend_daily', 'ad_campaigns', 'meta_insights_state', 'meta_graph_calls')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if tables() != 4 {
		t.Fatalf("the database is not at 0220")
	}
	if _, err := tx.Exec(ctx, section0220(t, "down")); err != nil {
		t.Fatalf("Down of 0220: %v", err)
	}
	if n := tables(); n != 0 {
		t.Fatalf("Down left %d tables", n)
	}

	var org string
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name, slug) VALUES ('mig0220', $1) RETURNING id::text`,
		"mig0220-"+time.Now().Format("150405.000000000")).Scan(&org); err != nil {
		t.Fatal(err)
	}
	sub := func(orgID any, kinds string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
			VALUES ($1, 'm', md5(random()::text), $2::text[]) RETURNING id::text`, orgID, kinds).Scan(&id); err != nil {
			t.Fatalf("subscription %s: %v", kinds, err)
		}
		return id
	}
	tickets := sub(org, "{tickets,shop_sale}")
	shopOnly := sub(org, "{shop_delivery}")
	operator := sub(nil, "{tickets}")
	refused := func(what, sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, "SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err == nil {
			t.Fatalf("%s was accepted", what)
		}
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
	}
	refused("'ads' before Up", `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
		VALUES ($1, 'x', 'x', '{ads}')`, org)

	for round := 0; round < 2; round++ {
		if _, err := tx.Exec(ctx, section0220(t, "up")); err != nil {
			t.Fatalf("Up of 0220 (round %d): %v", round, err)
		}
		if tables() != 4 {
			t.Fatalf("Up did not create the tables")
		}
		kinds := func(id string) string {
			var k string
			if err := tx.QueryRow(ctx, `SELECT array_to_string(kinds, ',') FROM sales_notification_subscriptions WHERE id = $1`, id).Scan(&k); err != nil {
				return "<gone>"
			}
			return k
		}
		if k := kinds(tickets); k != "tickets,shop_sale,ads" {
			t.Errorf("org tickets row = %s", k)
		}
		if k := kinds(shopOnly); k != "shop_delivery" {
			t.Errorf("shop-only row = %s", k)
		}
		if k := kinds(operator); k != "tickets" {
			t.Errorf("operator row = %s", k)
		}
		adsOnly := sub(org, "{ads}")
		if _, err := tx.Exec(ctx, `INSERT INTO meta_graph_calls (day, org_id, endpoint, calls) VALUES (current_date, $1, 'insights', 1)`, org); err != nil {
			t.Fatal(err)
		}
		refused("an unknown endpoint", `INSERT INTO meta_graph_calls (day, org_id, endpoint) VALUES (current_date, $1, 'other')`, org)
		if _, err := tx.Exec(ctx, `INSERT INTO sales_chat_connect_codes (code_hash, org_id, name, kinds, expires_at)
			VALUES (md5(random()::text), $1, 'c', '{ads}', now() + interval '30 minutes')`, org); err != nil {
			t.Fatalf("a connect code with 'ads': %v", err)
		}
		if _, err := tx.Exec(ctx, section0220(t, "down")); err != nil {
			t.Fatalf("Down (round %d): %v", round, err)
		}
		if k := kinds(adsOnly); k != "<gone>" {
			t.Errorf("Down kept an ads-only row: %s", k)
		}
		if k := kinds(tickets); k != "tickets,shop_sale" {
			t.Errorf("Down left tickets row = %s", k)
		}
	}
	if _, err := tx.Exec(ctx, section0220(t, "up")); err != nil {
		t.Fatalf("final Up: %v", err)
	}
}
