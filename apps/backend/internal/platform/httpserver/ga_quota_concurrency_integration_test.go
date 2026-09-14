//go:build integration

// ga_quota_concurrency_integration_test.go — the GA category quota
// mechanism (internal/platform/httpserver/gaquota, plan
// 08_architecture/23 step 3) contending with real GA holds on the SAME
// session.
//
// A quota change touches exactly the rows a hold touches — the sessions row
// (seat_status_version), the session-level inventory_ledger row and the GA
// place rows — so it must take them in the platform-wide hold-mutation lock
// order:
//
//	sessions → inventory_ledger → GA places
//
// and never touch a kind='seat' row after the ledger (the seated hold path
// locks sessions → seats FOR UPDATE → ledger, so the reverse order
// deadlocks). Getting that wrong is exactly the defect that made
// CreateGAHold deadlock against ExtendHold/ShrinkHold before it was
// reordered, so this test hammers both sides at once on a HYBRID session
// (plan seats plus a GA category) and fails if any 40P01 / 40001 survives
// the bounded retry on either side.
//
// It lives in httpserver rather than in gaquota because it needs both
// gaquota and hcheckout, and gaquota must never import a handler package.
package httpserver

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func TestGAQuota_SetQuantityVsGAHold_NoDeadlock(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	q := gen.New(pool)
	f := newQuotaRaceFixture(t, ctx, pool)
	defer f.cleanup()

	// The GA category starts with 40 places next to the 4 plan seats.
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.CreateCategory(ctx, txq, f.sessionID, f.tierID, 40)
	}); err != nil {
		t.Fatalf("create the GA category: %v", err)
	}

	const (
		buyers  = 8
		editors = 4
		rounds  = 6
	)

	var (
		wg        sync.WaitGroup
		holds     atomic.Int64
		resizes   atomic.Int64
		deadlocks atomic.Int64
	)

	// Buyers: real GA holds through hcheckout, which bumps the session
	// version, reserves ledger capacity and claims the category's places.
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				_, err := hcheckout.CreateGAHold(ctx, pool, q, hcheckout.GAHoldInput{
					OrgID:     f.orgID,
					ChannelID: f.channelID,
					SessionID: f.sessionID,
					Items: []hcheckout.GAHoldItem{
						{TierID: f.tierID, Quantity: 2, UnitPrice: 1000},
					},
					ExpiresAt: time.Now().Add(20 * time.Minute),
				})
				switch {
				case err == nil:
					holds.Add(1)
				case gaquota.IsSerializationFailure(err):
					deadlocks.Add(1)
					t.Errorf("GA hold died on a serialization failure that survived the retry: %v", err)
				default:
					// Over-capacity is a legitimate outcome while an editor
					// is shrinking the category underneath the buyer.
					var capErr *hcheckout.CapacityError
					if !errors.As(err, &capErr) {
						t.Errorf("unexpected GA hold error: %v", err)
					}
				}
			}
		}()
	}

	// Editors: quota changes on the same session, in both directions.
	for i := 0; i < editors; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				want := int32(40 + (idx+r)%3*10) //nolint:gosec // small test values
				err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
					_, serr := gaquota.SetQuantity(ctx, txq, f.sessionID, f.tierID, want)
					return serr
				})
				switch {
				case err == nil:
					resizes.Add(1)
				case gaquota.IsSerializationFailure(err):
					deadlocks.Add(1)
					t.Errorf("SetQuantity died on a serialization failure that survived the retry: %v", err)
				default:
					// Shrinking below what buyers already hold is legitimate.
					var below *gaquota.BelowUsedError
					if !errors.As(err, &below) {
						t.Errorf("unexpected SetQuantity error: %v", err)
					}
				}
			}
		}(i)
	}
	wg.Wait()

	if deadlocks.Load() != 0 {
		t.Fatalf("%d deadlock/serialization failures survived the retry", deadlocks.Load())
	}
	if holds.Load() == 0 {
		t.Fatal("no GA hold succeeded — the race never actually happened")
	}
	if resizes.Load() == 0 {
		t.Fatal("no quota change succeeded — the race never actually happened")
	}

	// The invariant survives the storm: the session capacity is the number
	// of places (plan seats plus GA places), the stated quantity equals the
	// places the category owns, and the ledger agrees.
	var seats, gaUnits, capacity, tierCap, ledger int32
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM session_seats WHERE session_id=$1 AND kind='seat'),
		       (SELECT count(*) FROM session_seats WHERE session_id=$1 AND kind='ga_unit'),
		       (SELECT capacity_total FROM sessions WHERE id=$1),
		       (SELECT capacity FROM ticket_tiers WHERE id=$2),
		       (SELECT capacity_total FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL)`,
		f.sessionID, f.tierID).Scan(&seats, &gaUnits, &capacity, &tierCap, &ledger); err != nil {
		t.Fatalf("read final state: %v", err)
	}
	if capacity != seats+gaUnits {
		t.Errorf("capacity_total = %d, want %d (%d seats + %d GA places)",
			capacity, seats+gaUnits, seats, gaUnits)
	}
	if tierCap != gaUnits {
		t.Errorf("category quantity = %d, want %d (the places it owns)", tierCap, gaUnits)
	}
	if ledger != capacity {
		t.Errorf("ledger capacity_total = %d, want %d", ledger, capacity)
	}
	t.Logf("quota vs GA hold race: %d holds, %d quota changes, final %d seats + %d GA places",
		holds.Load(), resizes.Load(), seats, gaUnits)
}

// ─── fixture ─────────────────────────────────────────────────────────────────

type quotaRaceFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
	tierID    uuid.UUID
	planID    uuid.UUID
	planVerID uuid.UUID
}

// newQuotaRaceFixture builds a hybrid session: a bound seating plan with 4
// seats plus a GA category whose places the quota mechanism owns. Hybrid is
// what makes the lock-order requirement real — a plan-less GA session has
// no kind='seat' row to order against.
func newQuotaRaceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *quotaRaceFixture {
	t.Helper()
	f := &quotaRaceFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
		planID:    uuid.New(),
		planVerID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	mustExec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			f.cleanup()
			t.Fatalf("quota race fixture: %v (sql %.60s)", err, sql)
		}
	}
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		f.orgID, "Quota Race Org "+suffix, "quota-race-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
		f.venueID, f.orgID, "Quota Race Venue "+suffix)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
	          VALUES ($1, $2, $3, 'published', 'public')`,
		f.eventID, f.orgID, "Quota Race Event "+suffix)
	mustExec(`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
	          VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
		f.channelID, f.orgID, "Quota Race Channel "+suffix)
	mustExec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
	          VALUES ($1, $2, $3, $4, 'mixed', 'active')`,
		f.planID, f.venueID, f.orgID, "Quota Race Plan "+suffix)
	mustExec(`INSERT INTO seating_plan_versions
	            (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
	          VALUES ($1, $2, 1, '{"sections":[]}'::jsonb, $3, 4)`,
		f.planVerID, f.planID, "quota-race-"+suffix)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
	            capacity_total, status, admission_mode, currency, currency_source,
	            seating_plan_version_id)
	          VALUES ($1, $2, $3, now() + interval '30 days',
	            now() + interval '30 days 2 hours', 4, 'scheduled', 'hybrid',
	            'EUR', 'override', $4)`,
		f.sessionID, f.eventID, f.venueID, f.planVerID)
	mustExec(`INSERT INTO session_seats
	            (session_id, seat_key, sector_name, row_name, seat_number,
	             tier_id, status, kind)
	          SELECT $1, 'A|1|' || gs::text, 'A', '1', gs::text, NULL, 'available', 'seat'
	          FROM generate_series(1, 4) gs`, f.sessionID)
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
	          VALUES ($1, NULL, 4)`, f.sessionID)
	mustExec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode,
	            price_amount, currency, sort_order)
	          VALUES ($1, $2, 'Standing', 'fixed', 1000, 'EUR', 0)`,
		f.tierID, f.sessionID)
	return f
}

func (f *quotaRaceFixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM reservation_ga_items WHERE reservation_id IN
		   (SELECT id FROM reservations WHERE session_id = $1)`,
		`DELETE FROM reservation_seats WHERE reservation_id IN
		   (SELECT id FROM reservations WHERE session_id = $1)`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM ticket_tiers WHERE session_id = $1`,
		`DELETE FROM sessions WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.sessionID); err != nil {
			f.t.Logf("quota race cleanup: %v (sql %.40s)", err, sql)
		}
	}
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.planVerID},
		{`DELETE FROM seating_plans WHERE id = $1`, f.planID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("quota race cleanup: %v", err)
		}
	}
}
