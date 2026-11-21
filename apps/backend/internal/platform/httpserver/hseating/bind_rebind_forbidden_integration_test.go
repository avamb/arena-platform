//go:build integration

// bind_rebind_forbidden_integration_test.go — live-DB coverage of the rebind
// guard in bindSessionSeatingCore: a rebind of a session that already has a
// reservation, a ticket or a 'sold' session_seats row is refused with 409
// seating.rebind_forbidden and leaves the binding and the seats untouched.
//
// Run with:
//
//	DATABASE_URL=... go test -tags integration \
//	    ./apps/backend/internal/platform/httpserver/hseating/ -run TestRebindForbidden
package hseating

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type rebindSeatRow struct {
	Key    string
	Status string
	SysID  int64
}

// rebindSnapshot captures what a refused rebind must leave unchanged.
func rebindSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) (uuid.UUID, []rebindSeatRow) {
	t.Helper()
	var ver uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT seating_plan_version_id FROM sessions WHERE id = $1`, sessionID).Scan(&ver); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	rows, err := pool.Query(ctx,
		`SELECT seat_key, status, system_seat_id FROM session_seats WHERE session_id = $1`, sessionID)
	if err != nil {
		t.Fatalf("read session_seats: %v", err)
	}
	defer rows.Close()
	var out []rebindSeatRow
	for rows.Next() {
		var r rebindSeatRow
		if err := rows.Scan(&r.Key, &r.Status, &r.SysID); err != nil {
			t.Fatalf("scan session_seats: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session_seats: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return ver, out
}

func TestRebindForbidden(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping rebind-forbidden integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(gen.New(pool), pool, nil, logger)
	req := httptest.NewRequest("POST", "/v1/seating/bind", nil)
	bindReq := bindRequest{
		AdmissionMode:   "assigned_seats",
		CategoryTierMap: map[string]*string{},
		AutoCreateTiers: true,
	}

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec failed: %v\n%s", err, sql)
		}
	}

	// assertRefused runs a rebind and checks the 409, the detail counts and
	// that nothing moved.
	assertRefused := func(f *c3bFixture, wantRes, wantTkt, wantSold int64) {
		t.Helper()
		verBefore, seatsBefore := rebindSnapshot(t, ctx, pool, f.sessionID)
		_, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindReq)
		if bErr == nil {
			t.Fatalf("rebind succeeded, want 409 seating.rebind_forbidden")
		}
		if bErr.Status != http.StatusConflict || bErr.Code != "seating.rebind_forbidden" {
			t.Fatalf("rebind error = %d %s, want 409 seating.rebind_forbidden", bErr.Status, bErr.Code)
		}
		for key, want := range map[string]int64{
			"reservations": wantRes, "tickets": wantTkt, "sold_seats": wantSold,
		} {
			got, ok := bErr.Details[key].(int64)
			if !ok || got != want {
				t.Errorf("details.%s = %v (%T), want %d", key, bErr.Details[key], bErr.Details[key], want)
			}
		}
		verAfter, seatsAfter := rebindSnapshot(t, ctx, pool, f.sessionID)
		if verAfter != verBefore {
			t.Errorf("binding changed: %s -> %s", verBefore, verAfter)
		}
		if !reflect.DeepEqual(seatsBefore, seatsAfter) {
			t.Errorf("session_seats changed by a refused rebind:\nbefore=%v\nafter=%v", seatsBefore, seatsAfter)
		}
	}

	t.Run("sold seat blocks rebind", func(t *testing.T) {
		f := newC3bFixture(t, ctx, pool, c3bGeometry(false))
		defer f.cleanup()

		if _, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindReq); bErr != nil {
			t.Fatalf("first bind: %s %s", bErr.Code, bErr.Message)
		}
		// Nothing sold: a rebind goes through, so the guard is not always on.
		if _, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindReq); bErr != nil {
			t.Fatalf("rebind with nothing sold: %s %s", bErr.Code, bErr.Message)
		}
		_, seats := rebindSnapshot(t, ctx, pool, f.sessionID)
		if len(seats) != 4 {
			t.Fatalf("seat rows = %d, want 4", len(seats))
		}

		mustExec(`UPDATE session_seats SET status = 'sold'
		          WHERE session_id = $1 AND seat_key = $2`, f.sessionID, seats[0].Key)
		assertRefused(f, 0, 0, 1)
	})

	// seedChannelReservation inserts a channel, a reservation and a
	// checkout session for the fixture's session and returns their ids plus
	// a cleanup that removes them (ordered before the fixture's own).
	seedReservation := func(f *c3bFixture) (channelID, resID, csID uuid.UUID) {
		t.Helper()
		channelID, resID, csID = uuid.New(), uuid.New(), uuid.New()
		mustExec(`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		          VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			channelID, f.orgID, "Rebind Channel "+channelID.String()[:8])
		mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at)
		          VALUES ($1, $2, $3, $4, 1, 'expired', now() - interval '1 hour')`,
			resID, f.orgID, channelID, f.sessionID)
		mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		          VALUES ($1, $2, $3, $4, 'expired')`, csID, f.orgID, channelID, resID)
		return channelID, resID, csID
	}
	cleanupReservation := func(channelID, resID, csID, tktID uuid.UUID) {
		bg := context.Background()
		for _, s := range []struct {
			sql string
			id  uuid.UUID
		}{
			{`DELETE FROM tickets WHERE id = $1`, tktID},
			{`DELETE FROM checkout_sessions WHERE id = $1`, csID},
			{`DELETE FROM reservations WHERE id = $1`, resID},
			{`DELETE FROM sales_channels WHERE id = $1`, channelID},
		} {
			if _, err := pool.Exec(bg, s.sql, s.id); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	}

	t.Run("reservation blocks rebind", func(t *testing.T) {
		f := newC3bFixture(t, ctx, pool, c3bGeometry(false))
		defer f.cleanup()
		if _, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindReq); bErr != nil {
			t.Fatalf("first bind: %s %s", bErr.Code, bErr.Message)
		}
		ch, res, cs := seedReservation(f)
		defer cleanupReservation(ch, res, cs, uuid.Nil)
		assertRefused(f, 1, 0, 0)
	})

	t.Run("ticket blocks rebind", func(t *testing.T) {
		f := newC3bFixture(t, ctx, pool, c3bGeometry(false))
		defer f.cleanup()
		if _, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindReq); bErr != nil {
			t.Fatalf("first bind: %s %s", bErr.Code, bErr.Message)
		}
		ch, res, cs := seedReservation(f)
		tkt := uuid.New()
		defer cleanupReservation(ch, res, cs, tkt)
		mustExec(`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email)
		          VALUES ($1, $2, $3, 'rebind@example.test')`, tkt, cs, f.sessionID)
		assertRefused(f, 1, 1, 0)
	})
}
