//go:build integration

// seatlayout_integration_test.go — task S1c against a live PostgreSQL:
//
//   - a batch over a mix of sold / held / available / unavailable / unknown
//     seats applies the free ones, skips the frozen ones with a reason and
//     bumps seat_status_version exactly once;
//   - a Strict batch that meets a held seat writes nothing;
//   - revert is a new batch with the inverse changes, skips a seat a later
//     batch moved, links both batches and refuses a second revert and the
//     revert of a revert;
//   - a seat's history lists every batch that touched it;
//   - another organization's id is ErrSessionNotFound;
//   - a batch racing N buyers' holds on the same seats: every seat is
//     either held by its buyer (and skipped by the batch) or closed by the
//     batch (and refused to the buyer) — never both, never a deadlock.
//
// Requires DATABASE_URL against a migrated database.
package seatlayout_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

type layoutFixture struct {
	t                                    *testing.T
	pool                                 *pgxpool.Pool
	q                                    *gen.Queries
	org, venue, event, channel, session  uuid.UUID
	plan, version, tier1, tier2, foreign uuid.UUID
}

func seatKey(n int) string { return fmt.Sprintf("S|A|%02d", n) }

func newLayoutFixture(t *testing.T, seats int) *layoutFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &layoutFixture{t: t, pool: pool, q: gen.New(pool),
		org: uuid.New(), venue: uuid.New(), event: uuid.New(), channel: uuid.New(), session: uuid.New(),
		plan: uuid.New(), version: uuid.New(), tier1: uuid.New(), tier2: uuid.New(), foreign: uuid.New()}
	sfx := f.org.String()[:8]
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, []any{f.org, "S1c Org " + sfx, "s1c-" + sfx}},
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, []any{f.foreign, "S1c Foreign " + sfx, "s1c-f-" + sfx}},
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, []any{f.venue, f.org, "S1c Venue " + sfx}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{f.event, f.org, "S1c Event " + sfx}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode) VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`, []any{f.channel, f.org, "S1c Channel " + sfx}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`, []any{f.plan, f.venue, f.org, "S1c Plan " + sfx}},
		{`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, locked_at)
		  VALUES ($1, $2, 1, '{}'::jsonb, $3, $4, now())`, []any{f.version, f.plan, "s1c-" + sfx, seats}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode,
		    seating_plan_version_id, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 3 hours', $4, 'scheduled',
		    'assigned_seats', $5, 'EUR', 'override')`, []any{f.session, f.event, f.venue, seats, f.version}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'Parter', 'fixed', 1000, 'EUR', $3, 1, true)`, []any{f.tier1, f.session, seats}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
		  VALUES ($1, $2, 'Balcony', 'fixed', 2000, 'EUR', $3, 2, true)`, []any{f.tier2, f.session, seats}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, $2)`, []any{f.session, seats}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'S|A|' || lpad(gs::text, 2, '0'), 'S', 'A', gs::text, $3, 'available', 'seat'
		  FROM generate_series(1, $2::int) gs`, []any{f.session, seats, f.tier1}},
	}
	t.Cleanup(f.cleanup)
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}
	return f
}

func (f *layoutFixture) cleanup() {
	ctx := context.Background()
	for _, s := range []string{
		`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`,
		`DELETE FROM session_seat_batches WHERE session_id = $1`,
		`DELETE FROM session_seats WHERE session_id = $1`,
		`DELETE FROM reservations WHERE session_id = $1`,
		`DELETE FROM inventory_ledger WHERE session_id = $1`,
		`DELETE FROM ticket_tiers WHERE session_id = $1`,
		`DELETE FROM sessions WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, s, f.session); err != nil {
			f.t.Logf("cleanup %q: %v", s, err)
		}
	}
	for _, s := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.version},
		{`DELETE FROM seating_plans WHERE id = $1`, f.plan},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channel},
		{`DELETE FROM events WHERE id = $1`, f.event},
		{`DELETE FROM venues WHERE id = $1`, f.venue},
		{`DELETE FROM organizations WHERE id = $1`, f.org},
		{`DELETE FROM organizations WHERE id = $1`, f.foreign},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			f.t.Logf("cleanup %q: %v", s.sql, err)
		}
	}
}

func (f *layoutFixture) version_() int64 {
	f.t.Helper()
	var v int64
	if err := f.pool.QueryRow(context.Background(), `SELECT seat_status_version FROM sessions WHERE id = $1`, f.session).Scan(&v); err != nil {
		f.t.Fatalf("read version: %v", err)
	}
	return v
}

func (f *layoutFixture) seat(key string) (tier uuid.UUID, status string, version int64) {
	f.t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT tier_id, status, status_version FROM session_seats WHERE session_id = $1 AND seat_key = $2`,
		f.session, key).Scan(&tier, &status, &version); err != nil {
		f.t.Fatalf("read seat %s: %v", key, err)
	}
	return tier, status, version
}

func (f *layoutFixture) batchCount() int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM session_seat_batches WHERE session_id = $1`, f.session).Scan(&n); err != nil {
		f.t.Fatalf("count batches: %v", err)
	}
	return n
}

func (f *layoutFixture) hold(keys ...string) error {
	_, err := hcheckout.CreateSeatedHold(context.Background(), f.pool, f.q, hcheckout.SeatedHoldInput{
		OrgID: f.org, ChannelID: f.channel, SessionID: f.session, SeatKeys: keys,
		ExpiresAt: time.Now().Add(20 * time.Minute),
	})
	return err
}

func strp(s string) *string { return &s }

func skippedReasons(b seatlayout.Batch) map[string]string {
	out := map[string]string{}
	for _, s := range b.Skipped {
		out[s.SeatKey] = s.Reason
	}
	return out
}

func changedKeys(b seatlayout.Batch) map[string]seatlayout.AppliedChange {
	out := map[string]seatlayout.AppliedChange{}
	for _, c := range b.Changes {
		out[c.SeatKey] = c
	}
	return out
}

func TestApplyBatch_MixedSeatsRevertAndHistory_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 6)
	ctx := context.Background()

	// Seat 1 sold upstream, seat 2 held by a buyer, seat 4 closed; 3, 5, 6 free.
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, f.session, seatKey(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.hold(seatKey(2)); err != nil {
		t.Fatalf("hold seat 2: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET status = 'unavailable' WHERE session_id = $1 AND seat_key = $2`, f.session, seatKey(4)); err != nil {
		t.Fatal(err)
	}

	// A Strict batch meeting the held seat writes nothing at all.
	before, batchesBefore := f.version_(), f.batchCount()
	_, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, Strict: true,
		Changes: []seatlayout.Change{{SeatKey: seatKey(2), TierID: &f.tier2}, {SeatKey: seatKey(3), TierID: &f.tier2}},
	})
	var conflict *seatlayout.ConflictError
	if !errors.As(err, &conflict) || len(conflict.Skipped) != 1 || conflict.Skipped[0].Reason != seatlayout.ReasonHeld {
		t.Fatalf("strict batch: err = %v", err)
	}
	if f.version_() != before || f.batchCount() != batchesBefore {
		t.Fatal("a refused strict batch must leave no trace")
	}
	if tier, _, _ := f.seat(seatKey(3)); tier != f.tier1 {
		t.Fatal("seat 3 repainted by a refused batch")
	}

	// Paint 1..4 plus an unknown key into tier 2.
	paint, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		OrgID: f.org, SessionID: f.session, Kind: seatlayout.KindPaint,
		Actor: seatlayout.Actor{Label: "user"},
		Changes: []seatlayout.Change{
			{SeatKey: seatKey(1), TierID: &f.tier2}, {SeatKey: seatKey(2), TierID: &f.tier2},
			{SeatKey: seatKey(3), TierID: &f.tier2}, {SeatKey: seatKey(4), TierID: &f.tier2},
			{SeatKey: "S|Z|99", TierID: &f.tier2},
		},
	})
	if err != nil {
		t.Fatalf("paint: %v", err)
	}
	if paint.SeatStatusVersion != before+1 || f.version_() != before+1 {
		t.Fatalf("version = %d (batch %d), want exactly one bump from %d", f.version_(), paint.SeatStatusVersion, before)
	}
	got := changedKeys(paint)
	if len(got) != 2 || got[seatKey(3)].To.Status != "available" || got[seatKey(4)].To.Status != "unavailable" {
		t.Fatalf("changes = %+v", paint.Changes)
	}
	if r := skippedReasons(paint); r[seatKey(1)] != "sold" || r[seatKey(2)] != "held" || r["S|Z|99"] != "seat_not_found" || len(r) != 3 {
		t.Fatalf("skipped = %+v", paint.Skipped)
	}
	for _, n := range []int{3, 4} {
		tier, _, v := f.seat(seatKey(n))
		if tier != f.tier2 || v != paint.SeatStatusVersion {
			t.Fatalf("seat %d: tier %s version %d", n, tier, v)
		}
	}
	if tier, _, _ := f.seat(seatKey(1)); tier != f.tier1 {
		t.Fatal("sold seat repainted")
	}
	if tier, status, _ := f.seat(seatKey(2)); tier != f.tier1 || status != "held" {
		t.Fatal("held seat touched")
	}

	// A later batch closes seat 3: the revert of the paint must leave it.
	closeB, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindClose,
		Changes: []seatlayout.Change{{SeatKey: seatKey(3), Status: strp("unavailable")}, {SeatKey: seatKey(4), Status: strp("unavailable")}},
	})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(closeB.Changes) != 1 || len(closeB.Noops) != 1 || closeB.Noops[0].SeatKey != seatKey(4) {
		t.Fatalf("close batch = %+v", closeB)
	}

	rev, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{OrgID: f.org, SessionID: f.session, BatchID: paint.ID})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if rev.Kind != seatlayout.KindRevert || rev.RevertsBatchID == nil || *rev.RevertsBatchID != paint.ID {
		t.Fatalf("revert batch = %+v", rev)
	}
	if c := changedKeys(rev); len(c) != 1 || c[seatKey(4)].To.TierID == nil || *c[seatKey(4)].To.TierID != f.tier1 {
		t.Fatalf("revert changes = %+v", rev.Changes)
	}
	if r := skippedReasons(rev); r[seatKey(3)] != seatlayout.ReasonChangedSince {
		t.Fatalf("revert skipped = %+v", rev.Skipped)
	}
	var revertedBy *uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT reverted_by_batch_id FROM session_seat_batches WHERE id = $1`, paint.ID).Scan(&revertedBy); err != nil || revertedBy == nil || *revertedBy != rev.ID {
		t.Fatalf("reverted_by_batch_id = %v (%v)", revertedBy, err)
	}
	if _, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: paint.ID}); !errors.Is(err, seatlayout.ErrAlreadyReverted) {
		t.Fatalf("second revert: %v", err)
	}
	if _, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: rev.ID}); !errors.Is(err, seatlayout.ErrRevertOfRevert) {
		t.Fatalf("revert of revert: %v", err)
	}
	if _, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{SessionID: f.session, BatchID: uuid.New()}); !errors.Is(err, seatlayout.ErrBatchNotFound) {
		t.Fatalf("unknown batch: %v", err)
	}

	// History of seat 3: the revert (skipped), the close and the paint.
	probe := seatlayout.SeatProbe(seatKey(3))
	rows, err := f.q.ListSeatBatchHistory(ctx, f.session, f.org, probe, 10, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("history = %d rows (%v)", len(rows), err)
	}
	if rows[0].ID != rev.ID || rows[1].ID != closeB.ID || rows[2].ID != paint.ID {
		t.Fatalf("history order = %s %s %s", rows[0].Kind, rows[1].Kind, rows[2].Kind)
	}
	if n, err := f.q.CountSeatBatchHistory(ctx, f.session, f.org, probe); err != nil || n != 3 {
		t.Fatalf("history count = %d (%v)", n, err)
	}
	var changes []map[string]any
	if err := json.Unmarshal(rows[2].Changes, &changes); err != nil || len(changes) != 2 {
		t.Fatalf("stored changes = %s (%v)", rows[2].Changes, err)
	}

	// Another organization's id: the session does not exist for it.
	if _, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		OrgID: f.foreign, SessionID: f.session, Kind: seatlayout.KindOpen,
		Changes: []seatlayout.Change{{SeatKey: seatKey(5), Status: strp("available")}},
	}); !errors.Is(err, seatlayout.ErrSessionNotFound) {
		t.Fatalf("foreign org: %v", err)
	}
	if _, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{OrgID: f.foreign, SessionID: f.session, BatchID: closeB.ID}); !errors.Is(err, seatlayout.ErrSessionNotFound) {
		t.Fatalf("foreign org revert: %v", err)
	}

	// A category of this session that does not exist is refused whole.
	ghost := uuid.New()
	if _, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint,
		Changes: []seatlayout.Change{{SeatKey: seatKey(5), TierID: &ghost}},
	}); err == nil {
		t.Fatal("unknown tier accepted")
	}
}

// TestApplyBatch_RacesBuyersHolds_LiveDB: one close batch over every seat
// and one buyer per seat, all started at once. Both sides lock the
// sessions row first, so they serialize: a seat is either held (the batch
// skipped it, reason held) or closed (the buyer got a seat conflict).
func TestApplyBatch_RacesBuyersHolds_LiveDB(t *testing.T) {
	const seats = 16
	f := newLayoutFixture(t, seats)
	ctx := context.Background()

	for round := 0; round < 3; round++ {
		if round > 0 {
			if _, err := f.pool.Exec(ctx, `DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE session_id = $1)`, f.session); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET status = 'available', reservation_id = NULL WHERE session_id = $1`, f.session); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `DELETE FROM reservations WHERE session_id = $1`, f.session); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE inventory_ledger SET capacity_held = 0 WHERE session_id = $1`, f.session); err != nil {
				t.Fatal(err)
			}
		}
		changes := make([]seatlayout.Change, 0, seats)
		for n := 1; n <= seats; n++ {
			changes = append(changes, seatlayout.Change{SeatKey: seatKey(n), Status: strp("unavailable")})
		}
		holdErr := make([]error, seats+1)
		var batch seatlayout.Batch
		var batchErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		for n := 1; n <= seats; n++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start
				holdErr[n] = f.hold(seatKey(n))
			}(n)
			if n == seats/2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					batch, batchErr = seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
						SessionID: f.session, Kind: seatlayout.KindClose, Changes: changes,
					})
				}()
			}
		}
		close(start)
		wg.Wait()
		if batchErr != nil {
			t.Fatalf("round %d: batch: %v", round, batchErr)
		}
		skipped, changed := skippedReasons(batch), changedKeys(batch)
		held := 0
		for n := 1; n <= seats; n++ {
			key := seatKey(n)
			_, status, _ := f.seat(key)
			if holdErr[n] == nil {
				held++
				if status != "held" || skipped[key] != seatlayout.ReasonHeld {
					t.Fatalf("round %d seat %s: hold won but status %s, batch skip %q", round, key, status, skipped[key])
				}
				continue
			}
			var conflict *hcheckout.SeatConflictsError
			if !errors.As(holdErr[n], &conflict) {
				t.Fatalf("round %d seat %s: hold failed with %v (want a seat conflict, never a deadlock)", round, key, holdErr[n])
			}
			if status != "unavailable" {
				t.Fatalf("round %d seat %s: batch won but status %s", round, key, status)
			}
			if _, ok := changed[key]; !ok {
				t.Fatalf("round %d seat %s: closed but not in the batch changes", round, key)
			}
		}
		var capHeld int
		if err := f.pool.QueryRow(ctx, `SELECT capacity_held FROM inventory_ledger WHERE session_id = $1 AND tier_id IS NULL`, f.session).Scan(&capHeld); err != nil {
			t.Fatal(err)
		}
		if capHeld != held {
			t.Fatalf("round %d: ledger capacity_held = %d, holds = %d", round, capHeld, held)
		}
		t.Logf("round %d: %d holds won, %d seats closed by the batch", round, held, len(changed))
	}
}
