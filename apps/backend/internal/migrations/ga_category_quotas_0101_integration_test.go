//go:build integration

// ga_category_quotas_0101_integration_test.go — the data conversion of
// migration 0101 (GA category quotas, plan 08_architecture/23 step 2).
//
// Once 0101 is applied, the pre-0101 pool shape can no longer be produced
// by a migration run, so the conversion is tested the other way round: the
// fixture writes rows in the OLD shape ('ga|pool|<n>' places with tier_id
// NULL on a plan-less GA session) into an already-migrated database, then
// runs the migration's own DO block — read verbatim out of the embedded
// migration file, so the test can never drift from what ships — and checks
// what it did.
package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// conversionBlock returns migration 0101's data-conversion DO block,
// extracted from the embedded migration file between the goose statement
// markers so the test always runs exactly the shipped SQL.
func conversionBlock(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/0101_ga_category_quotas.sql")
	if err != nil {
		t.Fatalf("read migration 0101: %v", err)
	}
	body := string(raw)
	const (
		begin = "-- +goose StatementBegin"
		end   = "-- +goose StatementEnd"
	)
	i := strings.Index(body, begin)
	j := strings.Index(body, end)
	if i < 0 || j < 0 || j <= i {
		t.Fatal("migration 0101 no longer has a single goose statement block")
	}
	block := strings.TrimSpace(body[i+len(begin) : j])
	if !strings.HasPrefix(block, "DO $$") {
		t.Fatalf("extracted block is not the conversion DO block: %.40s", block)
	}
	return block
}

// TestMigration0101_ConvertsPoolToCategoryQuotas is the shape the step-1
// data report found on the stand: a plan-less GA session with a shared pool
// and two categories with stated quantities, a few places already sold, and
// one FREE place still referenced by a cascade-less reservation_seats row
// left behind by a converted reservation. That last one is the trap — a
// DELETE of it fails with 23503.
func TestMigration0101_ConvertsPoolToCategoryQuotas(t *testing.T) {
	ctx, pool := convDB(t)
	block := conversionBlock(t)

	f := newConversionFixture(t, ctx, pool, 60)
	defer f.cleanup()
	std := f.addTier(ctx, "Standard", 0, ptrInt32(50))
	vip := f.addTier(ctx, "VIP", 1, ptrInt32(10))
	f.sellPoolUnits(ctx, std, 2)
	f.sellPoolUnits(ctx, vip, 2)
	pinned := f.referenceAvailablePoolUnit(ctx)

	if _, err := pool.Exec(ctx, block); err != nil {
		t.Fatalf("conversion block failed: %v", err)
	}

	f.wantTierUnits(ctx, std, 50)
	f.wantTierUnits(ctx, vip, 10)
	f.wantTierCapacity(ctx, std, 50)
	f.wantTierCapacity(ctx, vip, 10)
	f.wantTotals(ctx, 60)
	f.wantNoUnstampedPlaces(ctx)

	if !f.placeExists(ctx, pinned) {
		t.Fatal("the reservation_seats-referenced place was deleted — that DELETE cannot succeed (23503)")
	}

	// Re-running the conversion is a no-op.
	if _, err := pool.Exec(ctx, block); err != nil {
		t.Fatalf("second conversion run failed: %v", err)
	}
	f.wantTierUnits(ctx, std, 50)
	f.wantTierUnits(ctx, vip, 10)
	f.wantTotals(ctx, 60)
}

// TestMigration0101_MintsAndRemovesPlacesToMatchQuantities: the pool rarely
// happens to equal the sum of the stated quantities. A deficit is minted
// under the category's own 'ga|t<unit_seq>' prefix; a surplus of free,
// unreferenced places is removed.
func TestMigration0101_MintsAndRemovesPlacesToMatchQuantities(t *testing.T) {
	block := conversionBlock(t)

	t.Run("deficit is minted under the category prefix", func(t *testing.T) {
		ctx, pool := convDB(t)
		f := newConversionFixture(t, ctx, pool, 30)
		defer f.cleanup()
		std := f.addTier(ctx, "Standard", 0, ptrInt32(50))
		vip := f.addTier(ctx, "VIP", 1, ptrInt32(10))

		if _, err := pool.Exec(ctx, block); err != nil {
			t.Fatalf("conversion block failed: %v", err)
		}
		f.wantTierUnits(ctx, std, 50)
		f.wantTierUnits(ctx, vip, 10)
		f.wantTotals(ctx, 60)
		// Minted places carry the new prefix; the old pool keys are never
		// renamed (tickets.seat_key references them).
		f.wantMintedPrefixCount(ctx, std, "ga|t1|", 20)
		f.wantPoolKeysKept(ctx, 30)
	})

	t.Run("surplus free places are removed", func(t *testing.T) {
		ctx, pool := convDB(t)
		f := newConversionFixture(t, ctx, pool, 80)
		defer f.cleanup()
		std := f.addTier(ctx, "Standard", 0, ptrInt32(50))
		vip := f.addTier(ctx, "VIP", 1, ptrInt32(10))

		if _, err := pool.Exec(ctx, block); err != nil {
			t.Fatalf("conversion block failed: %v", err)
		}
		f.wantTierUnits(ctx, std, 50)
		f.wantTierUnits(ctx, vip, 10)
		f.wantTotals(ctx, 60)
	})
}

// TestMigration0101_SplitsThePoolAmongCategoriesWithoutAQuantity: the three
// sessions in the dev database whose categories have capacity NULL. The
// free places are shared evenly, the remainder going to the earlier
// category, and the computed quantity is written back.
func TestMigration0101_SplitsThePoolAmongCategoriesWithoutAQuantity(t *testing.T) {
	ctx, pool := convDB(t)
	block := conversionBlock(t)

	f := newConversionFixture(t, ctx, pool, 51)
	defer f.cleanup()
	early := f.addTier(ctx, "Early Bird", 0, nil)
	std := f.addTier(ctx, "Standard", 1, nil)

	if _, err := pool.Exec(ctx, block); err != nil {
		t.Fatalf("conversion block failed: %v", err)
	}

	f.wantTierCapacity(ctx, early, 26) // the remainder goes to the earlier one
	f.wantTierCapacity(ctx, std, 25)
	f.wantTierUnits(ctx, early, 26)
	f.wantTierUnits(ctx, std, 25)
	f.wantTotals(ctx, 51)
}

// TestMigration0101_LeavesASessionWithoutCategoriesAlone: 119 of the 183
// plan-less GA sessions with a pool have no active category at all. Nothing
// can be sold there, so the conversion must not touch their places or their
// capacity — the step-1 report lists them for a human instead.
func TestMigration0101_LeavesASessionWithoutCategoriesAlone(t *testing.T) {
	ctx, pool := convDB(t)
	block := conversionBlock(t)

	f := newConversionFixture(t, ctx, pool, 25)
	defer f.cleanup()

	if _, err := pool.Exec(ctx, block); err != nil {
		t.Fatalf("conversion block failed: %v", err)
	}

	var units, capacity int32
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM session_seats WHERE session_id=$1 AND kind='ga_unit'),
		       (SELECT capacity_total FROM sessions WHERE id=$1)`,
		f.sessionID).Scan(&units, &capacity); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if units != 25 {
		t.Errorf("places = %d, want the original 25", units)
	}
	if capacity != 25 {
		t.Errorf("capacity_total = %d, want the original 25", capacity)
	}
	f.wantNoLedgerRow(ctx)
}

// ─── fixture ─────────────────────────────────────────────────────────────────

func convDB(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func ptrInt32(v int32) *int32 { return &v }

type conversionFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
}

// newConversionFixture writes the PRE-0101 shape: a plan-less GA session
// with poolSize 'ga|pool|<n>' places, all tier_id NULL, and NO
// inventory_ledger row (130 general_admission sessions in the dev database
// have none, so the conversion must create one).
func newConversionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, poolSize int) *conversionFixture {
	t.Helper()
	f := &conversionFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	mustExec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			f.cleanup()
			t.Fatalf("conversion fixture: %v (sql %.60s)", err, sql)
		}
	}
	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		f.orgID, "Conv Org "+suffix, "conv-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
		f.venueID, f.orgID, "Conv Venue "+suffix)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
	          VALUES ($1, $2, $3, 'draft', 'private')`,
		f.eventID, f.orgID, "Conv Event "+suffix)
	mustExec(`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
	          VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
		f.channelID, f.orgID, "Conv Channel "+suffix)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
	            capacity_total, status, admission_mode, currency, currency_source)
	          VALUES ($1, $2, $3, now() + interval '30 days',
	            now() + interval '30 days 2 hours', $4, 'scheduled',
	            'general_admission', 'EUR', 'override')`,
		f.sessionID, f.eventID, f.venueID, poolSize)
	mustExec(`INSERT INTO session_seats
	            (session_id, seat_key, sector_name, row_name, seat_number,
	             tier_id, status, kind)
	          SELECT $1, 'ga|pool|' || lpad(gs::text, 6, '0'), '', '', '',
	                 NULL, 'available', 'ga_unit'
	          FROM generate_series(1, $2::int) gs`, f.sessionID, poolSize)
	return f
}

func (f *conversionFixture) cleanup() {
	ctx := context.Background()
	for _, sql := range []string{
		`DELETE FROM reservation_seats WHERE session_seat_id IN
		   (SELECT id FROM session_seats WHERE session_id = $1)`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM ticket_tiers WHERE session_id = $1`,
		`DELETE FROM sessions WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.sessionID); err != nil {
			f.t.Logf("conversion cleanup: %v (sql %.40s)", err, sql)
		}
	}
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("conversion cleanup: %v", err)
		}
	}
}

func (f *conversionFixture) addTier(ctx context.Context, name string, sortOrder int32, capacity *int32) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount,
		   currency, sort_order, capacity)
		 VALUES ($1, $2, 'fixed', 1000, 'EUR', $3, $4) RETURNING id`,
		f.sessionID, name, sortOrder, capacity).Scan(&id); err != nil {
		f.t.Fatalf("insert tier %q: %v", name, err)
	}
	return id
}

// sellPoolUnits reproduces what a pre-0101 sale left behind: the place is
// sold and carries the line's category stamp.
func (f *conversionFixture) sellPoolUnits(ctx context.Context, tierID uuid.UUID, n int) {
	f.t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE session_seats SET status='sold', tier_id=$2 WHERE id IN (
		   SELECT id FROM session_seats
		   WHERE session_id=$1 AND kind='ga_unit' AND status='available' AND tier_id IS NULL
		   ORDER BY seat_key LIMIT $3)`,
		f.sessionID, tierID, n); err != nil {
		f.t.Fatalf("sell %d pool places: %v", n, err)
	}
}

// referenceAvailablePoolUnit pins one FREE place behind a cascade-less
// reservation_seats row — what a converted reservation leaves behind once
// its ticket is cancelled (query I of the step-1 report).
func (f *conversionFixture) referenceAvailablePoolUnit(ctx context.Context) uuid.UUID {
	f.t.Helper()
	resID := uuid.New()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		 VALUES ($1, $2, $3, $4, 1, 'converted', now() - interval '1 hour')`,
		resID, f.orgID, f.channelID, f.sessionID); err != nil {
		f.t.Fatalf("insert reservation: %v", err)
	}
	var seatID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT id FROM session_seats
		 WHERE session_id=$1 AND kind='ga_unit' AND status='available'
		 ORDER BY seat_key DESC LIMIT 1`, f.sessionID).Scan(&seatID); err != nil {
		f.t.Fatalf("pick a free place: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO reservation_seats (reservation_id, session_seat_id) VALUES ($1, $2)`,
		resID, seatID); err != nil {
		f.t.Fatalf("insert reservation_seats: %v", err)
	}
	return seatID
}

func (f *conversionFixture) placeExists(ctx context.Context, id uuid.UUID) bool {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE id=$1`, id).Scan(&n); err != nil {
		f.t.Fatalf("count place: %v", err)
	}
	return n == 1
}

func (f *conversionFixture) wantTierUnits(ctx context.Context, tierID uuid.UUID, want int) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit'`,
		f.sessionID, tierID).Scan(&n); err != nil {
		f.t.Fatalf("count category places: %v", err)
	}
	if n != want {
		f.t.Errorf("category %s owns %d places, want %d", tierID, n, want)
	}
}

func (f *conversionFixture) wantTierCapacity(ctx context.Context, tierID uuid.UUID, want int32) {
	f.t.Helper()
	var got *int32
	if err := f.pool.QueryRow(ctx,
		`SELECT capacity FROM ticket_tiers WHERE id=$1`, tierID).Scan(&got); err != nil {
		f.t.Fatalf("read category quantity: %v", err)
	}
	if got == nil || *got != want {
		f.t.Errorf("category %s quantity = %v, want %d", tierID, got, want)
	}
}

// wantTotals asserts the whole point of the conversion: the sum of the
// category quantities, the number of places, sessions.capacity_total and
// the session-level ledger row all agree.
func (f *conversionFixture) wantTotals(ctx context.Context, want int32) {
	f.t.Helper()
	var units, capacity, ledger, sumCap int32
	if err := f.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM session_seats WHERE session_id=$1 AND kind='ga_unit'),
		       (SELECT capacity_total FROM sessions WHERE id=$1),
		       COALESCE((SELECT capacity_total FROM inventory_ledger
		                  WHERE session_id=$1 AND tier_id IS NULL), -1),
		       COALESCE((SELECT sum(capacity)::int FROM ticket_tiers
		                  WHERE session_id=$1 AND deleted_at IS NULL), -1)`,
		f.sessionID).Scan(&units, &capacity, &ledger, &sumCap); err != nil {
		f.t.Fatalf("read totals: %v", err)
	}
	for _, c := range []struct {
		name string
		got  int32
	}{
		{"GA places", units},
		{"sessions.capacity_total", capacity},
		{"inventory_ledger.capacity_total", ledger},
		{"sum of category quantities", sumCap},
	} {
		if c.got != want {
			f.t.Errorf("%s = %d, want %d", c.name, c.got, want)
		}
	}
}

func (f *conversionFixture) wantNoUnstampedPlaces(ctx context.Context) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND kind='ga_unit' AND tier_id IS NULL`,
		f.sessionID).Scan(&n); err != nil {
		f.t.Fatalf("count unstamped places: %v", err)
	}
	if n != 0 {
		f.t.Errorf("%d places still carry no category — the shared pool must be gone", n)
	}
}

func (f *conversionFixture) wantMintedPrefixCount(ctx context.Context, tierID uuid.UUID, prefix string, want int) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit' AND seat_key LIKE $3 || '%'`,
		f.sessionID, tierID, prefix).Scan(&n); err != nil {
		f.t.Fatalf("count minted places: %v", err)
	}
	if n != want {
		f.t.Errorf("%d places under %q, want %d", n, prefix, want)
	}
}

func (f *conversionFixture) wantPoolKeysKept(ctx context.Context, want int) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND kind='ga_unit' AND seat_key LIKE 'ga|pool|%'`,
		f.sessionID).Scan(&n); err != nil {
		f.t.Fatalf("count kept pool keys: %v", err)
	}
	if n != want {
		f.t.Errorf("%d original pool keys survive, want %d — keys are never renamed", n, want)
	}
}

func (f *conversionFixture) wantNoLedgerRow(ctx context.Context) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inventory_ledger WHERE session_id=$1`, f.sessionID).Scan(&n); err != nil {
		f.t.Fatalf("count ledger rows: %v", err)
	}
	if n != 0 {
		f.t.Errorf("%d ledger rows created for an untouched session, want 0", n)
	}
}
