//go:build integration

// usage_integration_test.go - the usage_records projection against a live
// database (task B2-6). Connects to DATABASE_URL and skips when it is unset;
// run it on a FRESH migrated database. The projector is limited to the
// fixture's organization.
package usage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/usage"
)

type counts struct {
	sold, comp, published, refunded int64
}

func TestProjection_CountsEventsPerMonthAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Usage Org "+suffix, "usage-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM usage_records WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM billable_events WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM billing_account_terms WHERE account_id IN (SELECT id FROM billing_accounts WHERE org_id = $1)`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM billing_accounts WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	acc, err := billing.EnsureAccount(ctx, q, orgID, "CZ")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	add := func(kind string, at time.Time) {
		t.Helper()
		_, ok, err := q.InsertBillableEvent(ctx, gen.InsertBillableEventParams{
			Kind: kind, SubjectID: uuid.New(), OccurredAt: at, OrgID: orgID, AccountID: acc.ID, Source: "collect",
		})
		if err != nil || !ok {
			t.Fatalf("event %s: ok=%v err=%v", kind, ok, err)
		}
	}
	read := func(period string) (counts, bool) {
		var c counts
		err := pool.QueryRow(ctx, `SELECT tickets_sold, complimentary_issued, events_published, tickets_refunded
		        FROM usage_records WHERE org_id = $1 AND billing_period = $2`, orgID, period).Scan(&c.sold, &c.comp, &c.published, &c.refunded)
		return c, err == nil
	}

	for i := 0; i < 3; i++ {
		add("ticket_sold", now.Add(-time.Hour*time.Duration(i+1)))
	}
	add("ticket_complimentary", now.Add(-time.Hour))
	add("event_published", now.Add(-2*time.Hour))
	add("ticket_refunded", now.Add(-3*time.Hour))
	add("ticket_sold", time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)) // previous month
	add("ticket_sold", time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC)) // too old: not recounted

	p := usage.New(pool, usage.Options{Now: func() time.Time { return now }, OrgFilter: []uuid.UUID{orgID}})
	n, err := p.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("first pass changed %d rows, want the two months", n)
	}
	if c, ok := read("2026-10"); !ok || c != (counts{3, 1, 1, 1}) {
		t.Errorf("2026-10 = %+v (ok=%v), want 3 sold, 1 complimentary, 1 published, 1 refunded", c, ok)
	}
	if c, ok := read("2026-09"); !ok || c != (counts{1, 0, 0, 0}) {
		t.Errorf("2026-09 = %+v (ok=%v), want 1 sold", c, ok)
	}
	if _, ok := read("2026-08"); ok {
		t.Error("a month before the previous one was projected")
	}

	// Nothing changed: nothing is written.
	if n, err = p.Run(ctx); err != nil || n != 0 {
		t.Errorf("second pass = %d rows, err %v, want 0", n, err)
	}
	// A late event moves one month only.
	add("ticket_sold", now.Add(-30*time.Minute))
	if n, err = p.Run(ctx); err != nil || n != 1 {
		t.Errorf("pass after one more sale = %d rows, err %v, want 1", n, err)
	}
	if c, _ := read("2026-10"); c.sold != 4 {
		t.Errorf("2026-10 sold = %d, want 4", c.sold)
	}
}
