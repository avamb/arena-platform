//go:build integration

// wp_orders_import_integration_test.go — П-14: the WordPress-era order
// correspondence import (cmd/arena-wp-orders-import, package wporders) against
// a real order and its tickets: a dry run writes nothing, a mismatch stops the
// valid rows from being mixed with bad ones only when asked, the run is
// idempotent and a changed row is an update.
package httpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/wporders"
)

func TestWPOrdersImport_DryRunIdempotentAndMismatches(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, nil)
	ctx := context.Background()
	org := uuid.New()
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		org, "WPO "+org.String()[:8], "wpo-"+org.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org) })
	f := newPay03Order(t, srv.pgxPool, org, "stripe")
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM wp_order_links WHERE org_id = $1`, org)
	})

	var sysID int64
	if err := srv.pgxPool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, f.order).Scan(&sysID); err != nil {
		t.Fatal(err)
	}
	var tk [2]int64
	for i := range tk {
		if err := srv.pgxPool.QueryRow(ctx, `SELECT system_ticket_id FROM tickets WHERE id = $1`, f.tickets[i]).Scan(&tk[i]); err != nil {
			t.Fatal(err)
		}
	}
	var other int64
	if err := srv.pgxPool.QueryRow(ctx, `SELECT COALESCE(max(system_ticket_id), 0) + 1000 FROM tickets`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	csv := "wc_order_id,allpay_order_id,platform_order_system_id,ticket_system_ids\n" +
		fmt.Sprintf("W-1,AP-1,%d,%d;%d\n", sysID, tk[0], tk[1]) +
		fmt.Sprintf("W-2,AP-2,%d,%d\n", sysID+987654, tk[0]) + // unknown order
		fmt.Sprintf("W-3,AP-3,%d,%d\n", sysID, other) // unknown ticket
	rows, bad, err := wporders.Parse(strings.NewReader(csv))
	if err != nil || len(bad) != 0 || len(rows) != 3 {
		t.Fatalf("parse: %v %v %d", err, bad, len(rows))
	}
	count := func() (n int) {
		_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM wp_order_links WHERE org_id = $1`, org).Scan(&n)
		return n
	}

	rep, err := wporders.Import(ctx, srv.pgxPool, org, rows, bad, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 || len(rep.Mismatches) != 2 || count() != 0 {
		t.Fatalf("dry run: %+v rows=%d", rep, count())
	}
	codes := rep.Mismatches[0].Code + "," + rep.Mismatches[1].Code
	if codes != wporders.CodeUnknownOrder+","+wporders.CodeUnknownTicket {
		t.Fatalf("mismatch codes = %s", codes)
	}

	rep, err = wporders.Import(ctx, srv.pgxPool, org, rows, bad, false)
	if err != nil || rep.Inserted != 1 || count() != 1 {
		t.Fatalf("apply: %+v %v rows=%d", rep, err, count())
	}
	rep, err = wporders.Import(ctx, srv.pgxPool, org, rows, bad, false)
	if err != nil || rep.Inserted != 0 || rep.Updated != 0 || rep.Unchanged != 1 {
		t.Fatalf("re-run: %+v %v; want everything unchanged", rep, err)
	}

	// A corrected AllPay number is an update; a ticket of the order passed alone is fine.
	rows2, _, _ := wporders.Parse(strings.NewReader("wc_order_id;allpay_order_id;platform_order_system_id;ticket_system_ids\n" +
		fmt.Sprintf("W-1;AP-1X;%d;%d\n", sysID, tk[0])))
	rep, err = wporders.Import(ctx, srv.pgxPool, org, rows2, nil, false)
	if err != nil || rep.Updated != 1 {
		t.Fatalf("update: %+v %v", rep, err)
	}
	var allpay string
	var ids []int64
	_ = srv.pgxPool.QueryRow(ctx, `SELECT allpay_order_id, ticket_system_ids FROM wp_order_links WHERE org_id = $1 AND wc_order_id = 'W-1'`, org).Scan(&allpay, &ids)
	if allpay != "AP-1X" || len(ids) != 1 {
		t.Fatalf("row = %s %v", allpay, ids)
	}
}
