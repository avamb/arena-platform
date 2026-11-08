//go:build integration

// wp_letters_import_integration_test.go — Vino&Co cutover: the ticket-letter
// history of the WordPress era (cmd/arena-wp-letters-import, package
// wpletters) against a real order. A dry run writes nothing, an apply files
// the letters, a second run changes nothing, a later run only moves a letter
// forward, a foreign organization matches nothing, and the order screen's
// letter list shows the filed letter.
package httpserver

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/wpletters"
)

func TestWPLettersImport_DryRunIdempotentAndForward(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, nil)
	ctx := context.Background()
	org := uuid.New()
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		org, "WPL "+org.String()[:8], "wpl-"+org.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM email_letters WHERE org_id = $1`, org)
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, org)
	})
	f := newPay03Order(t, srv.pgxPool, org, "stripe")

	var sysID int64
	if err := srv.pgxPool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, f.order).Scan(&sysID); err != nil {
		t.Fatal(err)
	}
	mid := "<" + uuid.NewString() + "@smtp-relay.example.test>"
	rec := func(system int64, state string, times wpletters.Times) wpletters.Record {
		return wpletters.Record{
			WCOrderID: "9001", PlatformOrderSystemID: wpletters.FlexString(fmt.Sprint(system)),
			Recipient: "wp.buyer@example.test", SentAt: "2026-10-01T10:00:05Z", MessageID: mid,
			Status: state, Times: times,
		}
	}
	count := func() (n int) {
		_ = srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM email_letters WHERE org_id = $1`, org).Scan(&n)
		return n
	}
	first := []wpletters.Record{
		rec(sysID, "delivered", wpletters.Times{Sent: 1790848805, Delivered: 1790848810}),
		rec(sysID+987654, "delivered", wpletters.Times{}),                                  // no such order
		{WCOrderID: "9009", Recipient: "old@example.test", SentAt: "2025-01-01T00:00:00Z"}, // no platform number
	}

	rep, err := wpletters.Import(ctx, srv.pgxPool, org, first, true)
	if err != nil || rep.Inserted != 1 || rep.Unmatched != 1 || rep.NoOrderNumber != 1 || count() != 0 {
		t.Fatalf("dry run: %+v %v rows=%d", rep, err, count())
	}
	rep, err = wpletters.Import(ctx, srv.pgxPool, org, first, false)
	if err != nil || rep.Inserted != 1 || count() != 1 {
		t.Fatalf("apply: %+v %v rows=%d", rep, err, count())
	}
	rep, err = wpletters.Import(ctx, srv.pgxPool, org, first, false)
	if err != nil || rep.Inserted != 0 || rep.AlreadyThere != 1 || count() != 1 {
		t.Fatalf("re-run: %+v %v rows=%d", rep, err, count())
	}

	var kind, provider, status string
	var numbers []string
	var tmpl *string
	if err := srv.pgxPool.QueryRow(ctx, `SELECT kind, provider, status, ticket_numbers, template_version FROM email_letters WHERE org_id = $1`, org).
		Scan(&kind, &provider, &status, &numbers, &tmpl); err != nil {
		t.Fatal(err)
	}
	if kind != "ticket" || provider != wpletters.Provider || status != "delivered" || len(numbers) != len(f.tickets) {
		t.Fatalf("row = %s %s %s %v", kind, provider, status, numbers)
	}

	// The order card lists it although no delivery job points at it.
	views, err := emailstatus.OrderLetters(ctx, srv.pgxPool, f.order)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range views {
		if v.Recipient == "wp.buyer@example.test" && v.Kind == "ticket" && v.Status == "delivered" {
			found = true
		}
	}
	if !found {
		t.Fatalf("order letters = %+v", views)
	}

	// The later run (after the switch) knows more: the status moves forward, a stale copy never moves it back.
	later := []wpletters.Record{rec(sysID, "unique_opened", wpletters.Times{Sent: 1790848805, Delivered: 1790848810, Opened: 1790850900})}
	rep, err = wpletters.Import(ctx, srv.pgxPool, org, later, false)
	if err != nil || rep.Updated != 1 || rep.Inserted != 0 || count() != 1 {
		t.Fatalf("forward: %+v %v", rep, err)
	}
	var openedAt *string
	_ = srv.pgxPool.QueryRow(ctx, `SELECT status, opened_at::text FROM email_letters WHERE org_id = $1`, org).Scan(&status, &openedAt)
	if status != "opened" || openedAt == nil {
		t.Fatalf("after forward: %s %v", status, openedAt)
	}
	rep, err = wpletters.Import(ctx, srv.pgxPool, org, first, false)
	if err != nil || rep.Updated != 0 || rep.AlreadyThere != 1 {
		t.Fatalf("stale copy: %+v %v", rep, err)
	}
	_ = srv.pgxPool.QueryRow(ctx, `SELECT status FROM email_letters WHERE org_id = $1`, org).Scan(&status)
	if status != "opened" {
		t.Fatalf("status went back to %s", status)
	}

	// Another organization does not see this order.
	other := uuid.New()
	if _, err := srv.pgxPool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		other, "WPL2 "+other.String()[:8], "wpl2-"+other.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = srv.pgxPool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, other)
	})
	rep, err = wpletters.Import(ctx, srv.pgxPool, other, later, true)
	if err != nil || rep.Matched != 0 || rep.Unmatched != 1 {
		t.Fatalf("foreign org: %+v %v", rep, err)
	}
}
