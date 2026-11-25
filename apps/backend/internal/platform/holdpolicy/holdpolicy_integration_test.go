//go:build integration

package holdpolicy

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestHoldPolicy_SeededProfilesAndEventOverride reads both profiles seeded
// by migration 0225 and checks that an event's hold_profile picks the
// profile its sessions read, NULL meaning standard.
func TestHoldPolicy_SeededProfilesAndEventOverride(t *testing.T) {
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
	q := gen.New(pool)

	rows, err := q.ListHoldPolicyProfiles(ctx)
	if err != nil {
		t.Fatalf("ListHoldPolicyProfiles: %v", err)
	}
	byName := map[string]Policy{}
	for _, r := range rows {
		byName[r.Name] = FromRow(r)
	}
	std, ok := byName[ProfileStandard]
	if !ok || std != Default() {
		t.Fatalf("standard = %+v; want Default() %+v", std, Default())
	}
	hd, ok := byName[ProfileHighDemand]
	if !ok {
		t.Fatal("high_demand not seeded")
	}
	want := Default()
	want.Profile = ProfileHighDemand
	want.Select = 420 * time.Second
	want.Payment = 600 * time.Second
	want.PollInterval = 1500 * time.Millisecond
	if hd != want {
		t.Fatalf("high_demand = %+v; want %+v", hd, want)
	}
	if one, err := q.GetHoldPolicyProfile(ctx, ProfileHighDemand); err != nil || FromRow(one) != want {
		t.Fatalf("GetHoldPolicyProfile = %+v, %v", one, err)
	}

	nonce := uuid.NewString()[:8]
	orgID, venueID, eventID, sessID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id = $1`, sessID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "HP Org "+nonce, "hp-org-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "HP Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, eventID, orgID, "HP Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'ILS', 'override')`,
		sessID, eventID, venueID)

	if name, err := q.GetEventHoldProfile(ctx, eventID); err != nil || name != nil {
		t.Fatalf("GetEventHoldProfile new event = %v, %v; want nil", name, err)
	}
	if p, err := ForSession(ctx, q, sessID); err != nil || p != Default() {
		t.Fatalf("ForSession without override = %+v, %v", p, err)
	}

	hdName := ProfileHighDemand
	if n, err := q.SetEventHoldProfile(ctx, eventID, &hdName); err != nil || n != 1 {
		t.Fatalf("SetEventHoldProfile = %d, %v", n, err)
	}
	if name, err := q.GetEventHoldProfile(ctx, eventID); err != nil || name == nil || *name != ProfileHighDemand {
		t.Fatalf("GetEventHoldProfile = %v, %v", name, err)
	}
	if p, err := ForSession(ctx, q, sessID); err != nil || p != want {
		t.Fatalf("ForSession with high_demand = %+v, %v", p, err)
	}

	bogus := "no_such_profile"
	if _, err := q.SetEventHoldProfile(ctx, eventID, &bogus); err == nil {
		t.Fatal("SetEventHoldProfile with an unknown profile: want a foreign-key error")
	}
	if n, err := q.SetEventHoldProfile(ctx, eventID, nil); err != nil || n != 1 {
		t.Fatalf("SetEventHoldProfile nil = %d, %v", n, err)
	}
	if p, err := ForSession(ctx, q, sessID); err != nil || p != Default() {
		t.Fatalf("ForSession after reset = %+v, %v", p, err)
	}
	if _, err := ForSession(ctx, q, uuid.New()); err == nil {
		t.Fatal("ForSession of a missing session: want an error")
	}
}
