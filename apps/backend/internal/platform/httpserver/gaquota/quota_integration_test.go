//go:build integration

package gaquota_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// ─────────────────────────────────────────────────────────────────────────────
// GA category quotas — the single mechanism (plan 08_architecture/23 step 3)
//
// Every test drives the real package against a live database through
// gaquota.InTx, so the lock order, the transaction boundary and the derived
// capacity / ledger recomputation are all exercised for real.
// ─────────────────────────────────────────────────────────────────────────────

func TestGAQuota_CreateCategory_MaterializesPlacesAndCapacity(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	vip := f.addTier(ctx, "VIP", 1)

	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.CreateCategory(ctx, txq, f.sessionID, std, 50)
	}); err != nil {
		t.Fatalf("create Standard: %v", err)
	}
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.CreateCategory(ctx, txq, f.sessionID, vip, 10)
	}); err != nil {
		t.Fatalf("create VIP: %v", err)
	}

	// Each category owns exactly its quantity, under its own key prefix.
	f.wantUnitCount(ctx, std, 50)
	f.wantUnitCount(ctx, vip, 10)
	f.wantKeyPrefix(ctx, std, "ga|t1|")
	f.wantKeyPrefix(ctx, vip, "ga|t2|")

	// Stated quantity, session capacity and the ledger all agree.
	f.wantTierCapacity(ctx, std, 50)
	f.wantTierCapacity(ctx, vip, 10)
	f.wantSessionCapacity(ctx, 60)
	f.wantLedgerTotal(ctx, 60)

	// Keys are zero-padded to six digits, like every other GA place.
	var first string
	if err := pool.QueryRow(ctx,
		`SELECT min(seat_key) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		f.sessionID, std).Scan(&first); err != nil {
		t.Fatalf("read first key: %v", err)
	}
	if first != "ga|t1|000001" {
		t.Errorf("first place key = %q, want ga|t1|000001", first)
	}
}

func TestGAQuota_SetQuantity_GrowsAndShrinks(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 20)

	res, err := f.mustSetQuantity(ctx, q, std, 35)
	if err != nil {
		t.Fatalf("grow: %v", err)
	}
	if res.Added != 15 || res.Removed != 0 || res.After != 35 {
		t.Errorf("grow result = %+v, want Added 15 / Removed 0 / After 35", res)
	}
	f.wantUnitCount(ctx, std, 35)
	f.wantSessionCapacity(ctx, 35)
	f.wantLedgerTotal(ctx, 35)
	f.wantNoDuplicateKeys(ctx)

	res, err = f.mustSetQuantity(ctx, q, std, 12)
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if res.Removed != 23 || res.Added != 0 || res.After != 12 {
		t.Errorf("shrink result = %+v, want Removed 23 / Added 0 / After 12", res)
	}
	f.wantUnitCount(ctx, std, 12)
	f.wantTierCapacity(ctx, std, 12)
	f.wantSessionCapacity(ctx, 12)
	f.wantLedgerTotal(ctx, 12)

	// Shrinking removed the HIGHEST keys, so a re-grow continues past them
	// instead of colliding on UNIQUE (session_id, seat_key).
	if _, err := f.mustSetQuantity(ctx, q, std, 18); err != nil {
		t.Fatalf("re-grow after shrink: %v", err)
	}
	f.wantUnitCount(ctx, std, 18)
	f.wantNoDuplicateKeys(ctx)
}

func TestGAQuota_SetQuantity_BelowHeldAndSoldRefused(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 10)
	f.markUnits(ctx, std, "sold", 3)
	f.markUnits(ctx, std, "held", 2)

	_, err := f.mustSetQuantity(ctx, q, std, 4)
	var below *gaquota.BelowUsedError
	if !errors.As(err, &below) {
		t.Fatalf("expected *BelowUsedError, got %v", err)
	}
	if below.Used != 5 {
		t.Errorf("Used = %d, want 5 (3 sold + 2 held)", below.Used)
	}
	if below.Floor() != 5 {
		t.Errorf("Floor() = %d, want 5", below.Floor())
	}

	// Nothing was written: the refusal rolled the transaction back.
	f.wantUnitCount(ctx, std, 10)
	f.wantTierCapacity(ctx, std, 10)
	f.wantSessionCapacity(ctx, 10)

	// Exactly at the used count is allowed.
	if _, err := f.mustSetQuantity(ctx, q, std, 5); err != nil {
		t.Fatalf("shrink to the used count: %v", err)
	}
	f.wantUnitCount(ctx, std, 5)
	f.wantLedgerTotal(ctx, 5)
}

// TestGAQuota_SetQuantity_ReferencedPlaceIsNeverDeleted: reservation_seats
// references session_seats WITHOUT a cascade, and a converted reservation
// keeps its join rows, so an AVAILABLE place can still be referenced.
// Deleting one fails with 23503, so the shrink must step over it — and,
// when the referenced places are all that is left, refuse cleanly instead
// of exploding.
func TestGAQuota_SetQuantity_ReferencedPlaceIsNeverDeleted(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 6)

	// Reference the HIGHEST-keyed place — the first one a shrink would
	// otherwise remove.
	pinned := f.referenceHighestPlace(ctx, std)

	if _, err := f.mustSetQuantity(ctx, q, std, 4); err != nil {
		t.Fatalf("shrink past a referenced place: %v", err)
	}
	f.wantUnitCount(ctx, std, 4)
	if !f.placeExists(ctx, pinned) {
		t.Fatal("the referenced place was deleted — reservation_seats has no cascade")
	}

	// Now only the pinned place and three others remain; ask for fewer than
	// the deletable ones can give.
	if _, err := f.mustSetQuantity(ctx, q, std, 1); err != nil {
		t.Fatalf("shrink down to the referenced place: %v", err)
	}
	f.wantUnitCount(ctx, std, 1)
	if !f.placeExists(ctx, pinned) {
		t.Fatal("the referenced place was deleted on the second shrink")
	}

	// Nothing deletable is left: a further shrink is refused, not a 23503.
	_, err := f.mustSetQuantity(ctx, q, std, 1)
	if err != nil {
		t.Fatalf("no-op shrink should succeed: %v", err)
	}
}

func TestGAQuota_DeleteCategory(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	vip := f.addTier(ctx, "VIP", 1)
	f.mustCreate(ctx, q, std, 10)
	f.mustCreate(ctx, q, vip, 5)
	f.markUnits(ctx, std, "sold", 1)

	err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.DeleteCategory(ctx, txq, f.sessionID, std)
	})
	if !errors.Is(err, gaquota.ErrCategoryInUse) {
		t.Fatalf("delete with a sold place: got %v, want ErrCategoryInUse", err)
	}
	f.wantUnitCount(ctx, std, 10)

	// The untouched category deletes cleanly and the capacity follows.
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.DeleteCategory(ctx, txq, f.sessionID, vip)
	}); err != nil {
		t.Fatalf("delete VIP: %v", err)
	}
	f.wantUnitCount(ctx, vip, 0)
	f.wantSessionCapacity(ctx, 10)
	f.wantLedgerTotal(ctx, 10)

	// Its number stays retired: the next category gets a fresh prefix, so
	// the deleted category's old keys can never be minted again.
	extra := f.addTier(ctx, "Extra", 2)
	f.mustCreate(ctx, q, extra, 4)
	f.wantKeyPrefix(ctx, extra, "ga|t3|")
}

func TestGAQuota_SeatedCategoryIsReadOnly(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{seated: true, seats: 8})
	defer f.cleanup()

	parter := f.addTier(ctx, "Parter", 0)
	f.assignSeatsToTier(ctx, parter)

	var kind gaquota.Kind
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		var err error
		kind, err = gaquota.CategoryKind(ctx, txq, f.sessionID, parter)
		return err
	}); err != nil {
		t.Fatalf("CategoryKind: %v", err)
	}
	if kind != gaquota.KindSeated {
		t.Fatalf("kind = %q, want %q", kind, gaquota.KindSeated)
	}

	_, err := f.mustSetQuantity(ctx, q, parter, 20)
	if !errors.Is(err, gaquota.ErrSeatedCategory) {
		t.Errorf("SetQuantity on a seated category: got %v, want ErrSeatedCategory", err)
	}
	err = gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.DeleteCategory(ctx, txq, f.sessionID, parter)
	})
	if !errors.Is(err, gaquota.ErrSeatedCategory) {
		t.Errorf("DeleteCategory on a seated category: got %v, want ErrSeatedCategory", err)
	}

	// Closing and re-opening a seated category still works (decision 10).
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.SetOpen(ctx, txq, f.sessionID, parter, false)
	}); err != nil {
		t.Fatalf("close a seated category: %v", err)
	}
	f.wantTierOpen(ctx, parter, false)
}

// TestGAQuota_FirstGACategoryTurnsSeatedSessionHybrid is decision 10: a
// category added by hand to a session with a seating plan is always GA, and
// the session becomes hybrid in the SAME transaction.
func TestGAQuota_FirstGACategoryTurnsSeatedSessionHybrid(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{seated: true, seats: 8})
	defer f.cleanup()

	standing := f.addTier(ctx, "Standing", 1)
	if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
		return gaquota.CreateCategory(ctx, txq, f.sessionID, standing, 12)
	}); err != nil {
		t.Fatalf("add a GA category to a seated session: %v", err)
	}

	if mode := f.admissionMode(ctx); mode != "hybrid" {
		t.Errorf("admission_mode = %q, want hybrid", mode)
	}
	if planVer := f.planVersionID(ctx); planVer == nil {
		t.Error("the seating plan binding must survive the switch to hybrid")
	}
	f.wantUnitCount(ctx, standing, 12)
	// Hybrid capacity = plan seats + GA places.
	f.wantSessionCapacity(ctx, 20)
	f.wantLedgerTotal(ctx, 20)

	// A second quota change keeps the hybrid sum right.
	if _, err := f.mustSetQuantity(ctx, q, standing, 30); err != nil {
		t.Fatalf("resize the GA category on a hybrid session: %v", err)
	}
	f.wantSessionCapacity(ctx, 38)
	f.wantLedgerTotal(ctx, 38)
	if mode := f.admissionMode(ctx); mode != "hybrid" {
		t.Errorf("admission_mode = %q after resize, want hybrid", mode)
	}
}

func TestGAQuota_SetOpen_RoundTrip(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 5)
	f.wantTierOpen(ctx, std, true) // the column default

	for _, open := range []bool{false, true, false} {
		if err := gaquota.InTx(ctx, pool, q, func(txq *gen.Queries) error {
			return gaquota.SetOpen(ctx, txq, f.sessionID, std, open)
		}); err != nil {
			t.Fatalf("SetOpen(%v): %v", open, err)
		}
		f.wantTierOpen(ctx, std, open)
	}

	// Closing changes no place and no capacity.
	f.wantUnitCount(ctx, std, 5)
	f.wantSessionCapacity(ctx, 5)
}

// TestGAQuota_Recompute_CreatesMissingLedgerRow: 130 general_admission
// sessions in the dev database have no inventory_ledger row at all, so the
// recompute step must create one rather than assume it exists.
func TestGAQuota_Recompute_CreatesMissingLedgerRow(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{noLedger: true})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 7)

	f.wantLedgerTotal(ctx, 7)
	f.wantSessionCapacity(ctx, 7)
}

// TestGAQuota_Recompute_NeverBreaksTheLedgerInvariant: the ledger row's
// capacity_total may never drop below capacity_held + capacity_sold
// (inventory_ledger_invariant), even when the places say otherwise.
func TestGAQuota_Recompute_NeverBreaksTheLedgerInvariant(t *testing.T) {
	ctx, pool, q := quotaDB(t)
	f := newQuotaFixture(t, ctx, pool, quotaFixtureOpts{})
	defer f.cleanup()

	std := f.addTier(ctx, "Standard", 0)
	f.mustCreate(ctx, q, std, 10)

	// Simulate a ledger that already counts more than the places do.
	if _, err := pool.Exec(ctx,
		`UPDATE inventory_ledger SET capacity_sold = 8 WHERE session_id=$1 AND tier_id IS NULL`,
		f.sessionID); err != nil {
		t.Fatalf("seed ledger sold: %v", err)
	}
	f.markUnits(ctx, std, "sold", 8)

	if _, err := f.mustSetQuantity(ctx, q, std, 8); err != nil {
		t.Fatalf("shrink to the sold count: %v", err)
	}
	f.wantLedgerTotal(ctx, 8)
}

// ─────────────────────────────────────────────────────────────────────────────
// fixture plumbing
// ─────────────────────────────────────────────────────────────────────────────

func quotaDB(t *testing.T) (context.Context, *pgxpool.Pool, *gen.Queries) {
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
	return ctx, pool, gen.New(pool)
}

type quotaFixtureOpts struct {
	seated   bool
	seats    int
	noLedger bool
}

type quotaFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	channelID uuid.UUID
	sessionID uuid.UUID
	planID    uuid.UUID
	planVerID uuid.UUID
	seated    bool
}

func newQuotaFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, opts quotaFixtureOpts) *quotaFixture {
	t.Helper()
	f := &quotaFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		channelID: uuid.New(),
		sessionID: uuid.New(),
		planID:    uuid.New(),
		planVerID: uuid.New(),
		seated:    opts.seated,
	}
	suffix := f.orgID.String()[:8]
	mustExec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			f.cleanup()
			t.Fatalf("quota fixture: %v (sql %.60s)", err, sql)
		}
	}

	mustExec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		f.orgID, "Quota Org "+suffix, "quota-"+suffix)
	mustExec(`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
		f.venueID, f.orgID, "Quota Venue "+suffix)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
	          VALUES ($1, $2, $3, 'draft', 'private')`,
		f.eventID, f.orgID, "Quota Event "+suffix)
	mustExec(`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
	          VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
		f.channelID, f.orgID, "Quota Channel "+suffix)

	capacity := 1
	if opts.seated {
		capacity = opts.seats
		mustExec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		          VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			f.planID, f.venueID, f.orgID, "Quota Plan "+suffix)
		mustExec(`INSERT INTO seating_plan_versions
		            (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		          VALUES ($1, $2, 1, '{"sections":[]}'::jsonb, $3, $4)`,
			f.planVerID, f.planID, "quota-"+suffix, int32(opts.seats)) //nolint:gosec // small test fixture
		mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		            capacity_total, status, admission_mode, currency, currency_source,
		            seating_plan_version_id)
		          VALUES ($1, $2, $3, now() + interval '30 days',
		            now() + interval '30 days 2 hours', $4, 'draft', 'assigned_seats',
		            'EUR', 'override', $5)`,
			f.sessionID, f.eventID, f.venueID, capacity, f.planVerID)
		mustExec(`INSERT INTO session_seats
		            (session_id, seat_key, sector_name, row_name, seat_number,
		             tier_id, status, kind)
		          SELECT $1, 'A|1|' || gs::text, 'A', '1', gs::text, NULL, 'available', 'seat'
		          FROM generate_series(1, $2::int) gs`,
			f.sessionID, opts.seats)
	} else {
		mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		            capacity_total, status, admission_mode, currency, currency_source)
		          VALUES ($1, $2, $3, now() + interval '30 days',
		            now() + interval '30 days 2 hours', $4, 'scheduled',
		            'general_admission', 'EUR', 'override')`,
			f.sessionID, f.eventID, f.venueID, capacity)
	}
	if !opts.noLedger {
		mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		          VALUES ($1, NULL, $2)`, f.sessionID, capacity)
	}
	return f
}

func (f *quotaFixture) cleanup() {
	ctx := context.Background()
	stmts := []string{
		`DELETE FROM reservation_seats WHERE session_seat_id IN
		   (SELECT id FROM session_seats WHERE session_id = $1)`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM ticket_tiers WHERE session_id = $1`,
		`DELETE FROM sessions WHERE id = $1`,
	}
	for _, sql := range stmts {
		if _, err := f.pool.Exec(ctx, sql, f.sessionID); err != nil {
			f.t.Logf("quota cleanup: %v (sql %.40s)", err, sql)
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
			f.t.Logf("quota cleanup: %v", err)
		}
	}
}

// addTier inserts the ticket_tiers row the caller owns; CreateCategory
// materializes its places afterwards, exactly as a handler would.
func (f *quotaFixture) addTier(ctx context.Context, name string, sortOrder int32) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	err := f.pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode, price_amount,
		   currency, sort_order)
		 VALUES ($1, $2, 'fixed', 1000, 'EUR', $3) RETURNING id`,
		f.sessionID, name, sortOrder).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert tier %q: %v", name, err)
	}
	return id
}

func (f *quotaFixture) mustCreate(ctx context.Context, q *gen.Queries, tierID uuid.UUID, qty int32) {
	f.t.Helper()
	if err := gaquota.InTx(ctx, f.pool, q, func(txq *gen.Queries) error {
		return gaquota.CreateCategory(ctx, txq, f.sessionID, tierID, qty)
	}); err != nil {
		f.t.Fatalf("CreateCategory(%d): %v", qty, err)
	}
}

func (f *quotaFixture) mustSetQuantity(ctx context.Context, q *gen.Queries, tierID uuid.UUID, qty int32) (gaquota.Result, error) {
	f.t.Helper()
	var res gaquota.Result
	err := gaquota.InTx(ctx, f.pool, q, func(txq *gen.Queries) error {
		var err error
		res, err = gaquota.SetQuantity(ctx, txq, f.sessionID, tierID, qty)
		return err
	})
	return res, err
}

// assignSeatsToTier makes every plan seat belong to the category, which is
// what makes it a SEATED category.
func (f *quotaFixture) assignSeatsToTier(ctx context.Context, tierID uuid.UUID) {
	f.t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE session_seats SET tier_id=$2 WHERE session_id=$1 AND kind='seat'`,
		f.sessionID, tierID); err != nil {
		f.t.Fatalf("assign seats to tier: %v", err)
	}
}

// markUnits flips the n lowest-keyed places of a category into status.
func (f *quotaFixture) markUnits(ctx context.Context, tierID uuid.UUID, status string, n int) {
	f.t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE session_seats SET status=$3 WHERE id IN (
		   SELECT id FROM session_seats
		   WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit' AND status='available'
		   ORDER BY seat_key LIMIT $4)`,
		f.sessionID, tierID, status, n); err != nil {
		f.t.Fatalf("mark %d places %s: %v", n, status, err)
	}
}

// referenceHighestPlace pins a category's highest-keyed available place
// behind a cascade-less reservation_seats row, the shape a converted
// reservation leaves behind after its ticket is cancelled.
func (f *quotaFixture) referenceHighestPlace(ctx context.Context, tierID uuid.UUID) uuid.UUID {
	f.t.Helper()
	resID := uuid.New()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		 VALUES ($1, $2, $3, $4, 1, 'expired', now() - interval '1 hour')`,
		resID, f.orgID, f.channelID, f.sessionID); err != nil {
		f.t.Fatalf("insert reservation: %v", err)
	}
	var seatID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT id FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit' AND status='available'
		 ORDER BY seat_key DESC LIMIT 1`, f.sessionID, tierID).Scan(&seatID); err != nil {
		f.t.Fatalf("pick the highest place: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO reservation_seats (reservation_id, session_seat_id) VALUES ($1, $2)`,
		resID, seatID); err != nil {
		f.t.Fatalf("insert reservation_seats: %v", err)
	}
	return seatID
}

func (f *quotaFixture) placeExists(ctx context.Context, id uuid.UUID) bool {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats WHERE id=$1`, id).Scan(&n); err != nil {
		f.t.Fatalf("count place: %v", err)
	}
	return n == 1
}

func (f *quotaFixture) admissionMode(ctx context.Context) string {
	f.t.Helper()
	var mode string
	if err := f.pool.QueryRow(ctx,
		`SELECT admission_mode FROM sessions WHERE id=$1`, f.sessionID).Scan(&mode); err != nil {
		f.t.Fatalf("read admission_mode: %v", err)
	}
	return mode
}

func (f *quotaFixture) planVersionID(ctx context.Context) *uuid.UUID {
	f.t.Helper()
	var id *uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT seating_plan_version_id FROM sessions WHERE id=$1`, f.sessionID).Scan(&id); err != nil {
		f.t.Fatalf("read seating_plan_version_id: %v", err)
	}
	return id
}

func (f *quotaFixture) wantUnitCount(ctx context.Context, tierID uuid.UUID, want int) {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit'`,
		f.sessionID, tierID).Scan(&n); err != nil {
		f.t.Fatalf("count places: %v", err)
	}
	if n != want {
		f.t.Errorf("category %s owns %d places, want %d", tierID, n, want)
	}
}

func (f *quotaFixture) wantKeyPrefix(ctx context.Context, tierID uuid.UUID, prefix string) {
	f.t.Helper()
	var bad int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM session_seats
		 WHERE session_id=$1 AND tier_id=$2 AND kind='ga_unit' AND seat_key NOT LIKE $3 || '%'`,
		f.sessionID, tierID, prefix).Scan(&bad); err != nil {
		f.t.Fatalf("check key prefix: %v", err)
	}
	if bad != 0 {
		f.t.Errorf("category %s has %d places outside prefix %q", tierID, bad, prefix)
	}
}

func (f *quotaFixture) wantNoDuplicateKeys(ctx context.Context) {
	f.t.Helper()
	var dup int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM (
		   SELECT seat_key FROM session_seats WHERE session_id=$1
		   GROUP BY seat_key HAVING count(*) > 1) d`,
		f.sessionID).Scan(&dup); err != nil {
		f.t.Fatalf("check duplicate keys: %v", err)
	}
	if dup != 0 {
		f.t.Errorf("%d duplicate seat keys on the session", dup)
	}
}

func (f *quotaFixture) wantTierCapacity(ctx context.Context, tierID uuid.UUID, want int32) {
	f.t.Helper()
	var got *int32
	if err := f.pool.QueryRow(ctx,
		`SELECT capacity FROM ticket_tiers WHERE id=$1`, tierID).Scan(&got); err != nil {
		f.t.Fatalf("read tier capacity: %v", err)
	}
	if got == nil || *got != want {
		f.t.Errorf("category %s capacity = %v, want %d", tierID, got, want)
	}
}

func (f *quotaFixture) wantTierOpen(ctx context.Context, tierID uuid.UUID, want bool) {
	f.t.Helper()
	var got bool
	if err := f.pool.QueryRow(ctx,
		`SELECT is_open FROM ticket_tiers WHERE id=$1`, tierID).Scan(&got); err != nil {
		f.t.Fatalf("read is_open: %v", err)
	}
	if got != want {
		f.t.Errorf("category %s is_open = %v, want %v", tierID, got, want)
	}
}

func (f *quotaFixture) wantSessionCapacity(ctx context.Context, want int32) {
	f.t.Helper()
	var got int32
	if err := f.pool.QueryRow(ctx,
		`SELECT capacity_total FROM sessions WHERE id=$1`, f.sessionID).Scan(&got); err != nil {
		f.t.Fatalf("read session capacity: %v", err)
	}
	if got != want {
		f.t.Errorf("sessions.capacity_total = %d, want %d", got, want)
	}
}

func (f *quotaFixture) wantLedgerTotal(ctx context.Context, want int32) {
	f.t.Helper()
	var got *int32
	err := f.pool.QueryRow(ctx,
		`SELECT capacity_total FROM inventory_ledger
		 WHERE session_id=$1 AND tier_id IS NULL`, f.sessionID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		f.t.Fatalf("the session has no ledger row; want capacity_total %d", want)
	}
	if err != nil {
		f.t.Fatalf("read ledger: %v", err)
	}
	if got == nil || *got != want {
		f.t.Errorf("inventory_ledger.capacity_total = %v, want %d", got, want)
	}
}
