//go:build integration

// convert_expire_lock_order_integration_test.go — P0-1: the held→sold
// conversion and every way a hold ends must take their locks in one order:
// the reservation row, then the sessions row (seat_status_version), then
// inventory_ledger.
//
// ConvertReservationInTx locks the reservation row first so duplicate
// conversions cannot sell a hold twice. The two expiry paths used to read
// the reservation unlocked, bump the sessions row while releasing the seats,
// and only then write the reservation row: the opposite order. A conversion
// and an expiry of the same seated hold could then deadlock (40P01). Both
// now lock the reservation first, like ReleaseHold and the cart mutations.
//
// The test races, for one seated hold whose TTL has passed, a conversion,
// ExpireHoldForOrderTx (PAY_ORDER's expiry), one pass of the TTL sweep and
// ReleaseHold. No deadlock may surface, exactly one of them wins, and the
// seats and inventory_ledger agree with the winner.
package hcheckout_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// lockedBuffer is a goroutine-safe log sink for the sweep's own logger,
// which logs a failed expiry instead of returning it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func isDeadlock(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "40P01") || strings.Contains(s, "deadlock")
}

func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *gen.Queries) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(gen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestConvertVsExpiry_SeatedHold_NoDeadlockOneWinner_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	const rounds, seatsPerHold = 12, 2
	f := newHoldFixture(t, ctx, pool, "assigned_seats", 100, rounds*seatsPerHold)
	defer f.cleanup()
	q := gen.New(pool)
	allKeys := f.seatKeys(rounds * seatsPerHold)

	sweepLog := &lockedBuffer{}
	proc := hcheckout.NewReservationProcessor(pool, q,
		slog.New(slog.NewTextHandler(sweepLog, nil))).WithCheckoutQueries(q)

	converted := 0
	for round := 0; round < rounds; round++ {
		keys := allKeys[round*seatsPerHold : (round+1)*seatsPerHold]
		hold, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
			OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
			SeatKeys: keys, ExpiresAt: time.Now().Add(20 * time.Minute),
		})
		if err != nil {
			t.Fatalf("round %d CreateSeatedHold: %v", round, err)
		}
		resID := hold.Reservation.ID
		expireNow(t, ctx, pool, resID)

		errs := make([]error, 4)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		run := func(i int, fn func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				errs[i] = fn()
			}()
		}
		run(0, func() error {
			return inTx(ctx, pool, func(q *gen.Queries) error { return hcheckout.ConvertReservationInTx(ctx, q, resID) })
		})
		run(1, func() error {
			return inTx(ctx, pool, func(q *gen.Queries) error { return hcheckout.ExpireHoldForOrderTx(ctx, q, resID) })
		})
		run(2, func() error { _, err := proc.ProcessExpiredReservations(ctx, 100); return err })
		run(3, func() error {
			_, err := hcheckout.ReleaseHold(ctx, pool, q, resID)
			var notReleasable *hcheckout.NotReleasableError
			if errors.As(err, &notReleasable) {
				return nil // another path ended the hold first
			}
			return err
		})
		close(gate)
		wg.Wait()

		for i, err := range errs {
			if isDeadlock(err) {
				t.Errorf("round %d path %d: deadlock: %v", round, i, err)
			} else if err != nil {
				t.Errorf("round %d path %d: %v", round, i, err)
			}
		}

		got, err := q.GetReservationByID(ctx, resID)
		if err != nil {
			t.Fatalf("round %d reload: %v", round, err)
		}
		var sold, available int
		if err := pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'sold'),
			count(*) FILTER (WHERE status = 'available' AND reservation_id IS NULL)
			FROM session_seats WHERE session_id = $1 AND seat_key = ANY($2)`,
			f.sessionID, keys).Scan(&sold, &available); err != nil {
			t.Fatal(err)
		}
		switch got.State {
		case "converted":
			converted++
			if sold != seatsPerHold {
				t.Errorf("round %d: converted hold has %d sold seats, want %d", round, sold, seatsPerHold)
			}
		case "expired", "cancelled":
			if available != seatsPerHold {
				t.Errorf("round %d: %s hold left %d seats available, want %d", round, got.State, available, seatsPerHold)
			}
		default:
			t.Errorf("round %d: hold ended in state %q", round, got.State)
		}
	}

	var held, soldCap int32
	if err := pool.QueryRow(ctx, `SELECT capacity_held, capacity_sold FROM inventory_ledger
		WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID).Scan(&held, &soldCap); err != nil {
		t.Fatal(err)
	}
	if held != 0 || soldCap != int32(converted*seatsPerHold) {
		t.Errorf("inventory_ledger held = %d, sold = %d; want 0 and %d (%d holds converted)",
			held, soldCap, converted*seatsPerHold, converted)
	}
	if log := sweepLog.String(); strings.Contains(log, "40P01") || strings.Contains(log, "deadlock") {
		t.Errorf("the TTL sweep hit a deadlock:\n%s", log)
	}
	t.Logf("%d of %d holds converted, the rest expired or released", converted, rounds)
}

// TestConvertTwice_SecondConversionWaitsAndChangesNothing_LiveDB pins the
// order of two conversions of ONE hold (commit 198025b2), with no timing
// luck: the first conversion runs and stays open, the second starts and is
// seen waiting on a lock, then the first commits. The second must then read
// 'converted' and change nothing. Before the reservation row was locked, the
// second had already read 'active' unlocked: once released it confirmed the
// hold's places again and took them from the OTHER hold on the session.
func TestConvertTwice_SecondConversionWaitsAndChangesNothing_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newHoldFixture(t, ctx, pool, "assigned_seats", 100, 4)
	defer f.cleanup()
	q := gen.New(pool)
	keys := f.seatKeys(4)
	hold := func(k []string) uuid.UUID {
		h, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
			OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
			SeatKeys: k, ExpiresAt: time.Now().Add(20 * time.Minute),
		})
		if err != nil {
			t.Fatalf("CreateSeatedHold: %v", err)
		}
		return h.Reservation.ID
	}
	resA := hold(keys[:2])
	resB := hold(keys[2:]) // another buyer's live hold on the same session

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if err := hcheckout.ConvertReservationInTx(ctx, gen.New(tx1), resA); err != nil {
		t.Fatalf("first conversion: %v", err)
	}

	pidCh, errCh := make(chan int32, 1), make(chan error, 1)
	go func() {
		tx2, err := pool.Begin(ctx)
		if err != nil {
			pidCh <- 0
			errCh <- err
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		var pid int32
		if err := tx2.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			pidCh <- 0
			errCh <- err
			return
		}
		pidCh <- pid
		if err := hcheckout.ConvertReservationInTx(ctx, gen.New(tx2), resA); err != nil {
			errCh <- err
			return
		}
		errCh <- tx2.Commit(ctx)
	}()

	pid := <-pidCh
	if pid == 0 {
		t.Fatalf("second conversion could not start: %v", <-errCh)
	}
	waiting := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var wait *string
		if err := pool.QueryRow(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&wait); err != nil {
			t.Fatal(err)
		}
		if wait != nil && *wait == "Lock" {
			waiting = true
			break
		}
	}
	if !waiting {
		t.Fatal("the second conversion never waited on a lock behind the first")
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit first conversion: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("second conversion of the same hold: %v", err)
	}

	var held, sold int32
	if err := pool.QueryRow(ctx, `SELECT capacity_held, capacity_sold FROM inventory_ledger
		WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID).Scan(&held, &sold); err != nil {
		t.Fatal(err)
	}
	if held != 2 || sold != 2 {
		t.Errorf("inventory_ledger held = %d, sold = %d after converting one 2-seat hold twice; want 2 and 2 (the other hold keeps its places)", held, sold)
	}
	if r, err := q.GetReservationByID(ctx, resA); err != nil || r.State != "converted" {
		t.Errorf("converted hold = %q (%v), want converted", r.State, err)
	}
	if r, err := q.GetReservationByID(ctx, resB); err != nil || (r.State != "draft" && r.State != "active") {
		t.Errorf("the other hold = %q (%v), want it still held", r.State, err)
	}
}
