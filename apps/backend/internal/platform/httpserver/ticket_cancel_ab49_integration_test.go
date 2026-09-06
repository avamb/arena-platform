//go:build integration

// ticket_cancel_ab49_integration_test.go — live-DB coverage for the AB-49
// cancellation core: the seat state machine's sold->available edge, the
// forbidden sold->unavailable edge, GA unit release, the review-hold
// order flow, and the full sell -> cancel -> resell cycle with both
// ticket rows retained in history.
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0086).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAB49Integration
package httpserver

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// ab49Fixture seeds org -> venue -> event -> session (+tier, ledger,
// channel, reservation, checkout session) so tickets can be issued and
// cancelled against real FKs.
type ab49Fixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	orgID      uuid.UUID
	venueID    uuid.UUID
	eventID    uuid.UUID
	sessionID  uuid.UUID
	tierID     uuid.UUID
	channelID  uuid.UUID
	resID      uuid.UUID
	checkoutID uuid.UUID
	planID     uuid.UUID
	planVerID  uuid.UUID
}

func newAB49Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admissionMode string) *ab49Fixture {
	f := &ab49Fixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		tierID:    uuid.New(),
		channelID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	// assigned_seats sessions require a bound seating plan version
	// (sessions_seated_requires_plan CHECK).
	f.planID = uuid.New()
	f.planVerID = uuid.New()
	planID := f.planID
	planVersionID := f.planVerID
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "AB49 Org " + suffix, "ab49-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "AB49 Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "AB49 Event " + suffix}},
	}
	sessionPlanVersion := "NULL"
	if admissionMode == "assigned_seats" || admissionMode == "hybrid" {
		sessionPlanVersion = "$5"
		steps = append(steps,
			struct {
				sql  string
				args []any
			}{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
			  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
				[]any{planID, f.venueID, f.orgID, "AB49 Plan " + suffix}},
			struct {
				sql  string
				args []any
			}{`INSERT INTO seating_plan_versions
			    (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
			  VALUES ($1, $2, 1, '{}'::jsonb, 'ab49-checksum', 1)`,
				[]any{planVersionID, planID}},
		)
	}
	sessionInsert := struct {
		sql  string
		args []any
	}{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
	    capacity_total, status, admission_mode, currency, currency_source,
	    seating_plan_version_id)
	  VALUES ($1, $2, $3, now() + interval '30 days',
	    now() + interval '30 days 2 hours', 100, 'draft', $4, 'EUR', 'override', ` +
		sessionPlanVersion + `)`,
		[]any{f.sessionID, f.eventID, f.venueID, admissionMode}}
	if sessionPlanVersion == "$5" {
		sessionInsert.args = append(sessionInsert.args, planVersionID)
	}
	steps = append(steps, sessionInsert)
	steps = append(steps, []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ticket_tiers
		    (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		  VALUES ($1, $2, 'AB49 Tier', 'fixed', 2500, 'EUR', 0)`,
			[]any{f.tierID, f.sessionID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, 100)`,
			[]any{f.sessionID}},
		{`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.channelID, f.orgID, "AB49 Channel " + suffix}},
	}...)
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("AB49 fixture step %d failed: %v", i, err)
		}
	}

	q := gen.New(pool)
	res, err := q.InsertReservation(ctx, f.orgID, f.channelID, f.sessionID, nil, nil, 3, time.Now().UTC().Add(30*time.Minute))
	if err != nil {
		f.cleanup()
		t.Fatalf("AB49 fixture reservation: %v", err)
	}
	f.resID = res.ID

	f.checkoutID = uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		VALUES ($1, $2, $3, $4, 'completed')`,
		f.checkoutID, f.orgID, f.channelID, f.resID); err != nil {
		f.cleanup()
		t.Fatalf("AB49 fixture checkout: %v", err)
	}
	return f
}

func (f *ab49Fixture) cleanup() {
	ctx := context.Background()
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM tickets WHERE checkout_session_id = $1`, f.checkoutID},
		{`DELETE FROM checkout_sessions WHERE id = $1`, f.checkoutID},
		{`DELETE FROM reservation_seats WHERE reservation_id = $1`, f.resID},
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM reservations WHERE id = $1`, f.resID},
		{`DELETE FROM sales_channels WHERE id = $1`, f.channelID},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.sessionID},
		{`DELETE FROM sessions WHERE id = $1`, f.sessionID},
		{`DELETE FROM seating_plan_versions WHERE id = $1`, f.planVerID},
		{`DELETE FROM seating_plans WHERE id = $1`, f.planID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("AB49 cleanup: %v", err)
		}
	}
}

// sellSeat drives one seat through available -> held -> sold with the
// production conditional UPDATEs.
func sellSeat(t *testing.T, ctx context.Context, q *gen.Queries, f *ab49Fixture, seatKey string) gen.SessionSeatRow {
	t.Helper()
	seat, err := q.GetSessionSeatByKey(ctx, f.sessionID, seatKey)
	if err != nil {
		t.Fatalf("GetSessionSeatByKey(%s): %v", seatKey, err)
	}
	v, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("version bump: %v", err)
	}
	if _, err := q.HoldSessionSeat(ctx, seat.ID, f.resID, v); err != nil {
		t.Fatalf("HoldSessionSeat(%s): %v", seatKey, err)
	}
	v, err = q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("version bump 2: %v", err)
	}
	sold, err := q.SellSessionSeat(ctx, seat.ID, f.resID, v)
	if err != nil {
		t.Fatalf("SellSessionSeat(%s): %v", seatKey, err)
	}
	return sold
}

// TestAB49Integration_AssignedSeat_CancelResellCycle proves the whole
// point of AB-49: sell -> cancel (mode=manual, NO provider call) ->
// seat purchasable again in the same request -> resell -> both ticket
// rows retained; plus the forbidden sold->unavailable edge and the
// active-ticket release guard.
func TestAB49Integration_AssignedSeat_CancelResellCycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping AB-49 integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newAB49Fixture(t, ctx, pool, "assigned_seats")
	defer f.cleanup()
	q := gen.New(pool)

	// Materialize one seat and sell it.
	if _, err := q.InsertSessionSeats(ctx, f.sessionID,
		[]string{"A|1|1"}, []string{"A"}, []string{"1"}, []string{"1"},
		[]*string{ptr(f.tierID.String())}, nil, ""); err != nil {
		t.Fatalf("InsertSessionSeats: %v", err)
	}
	sold := sellSeat(t, ctx, q, f, "A|1|1")

	// Mirror checkout: session-level confirm (held was not incremented by
	// sellSeat, so poke the ledger directly to the post-sale state).
	if _, err := pool.Exec(ctx, `
		UPDATE inventory_ledger SET capacity_sold = 1
		WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID); err != nil {
		t.Fatalf("ledger seed: %v", err)
	}

	// Issue the ticket (active, seat denormalized).
	ticket, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil,
		ptr("A|1|1"), ptr("A"), ptr("1"), ptr("1"), 0)
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// ── Forbidden edge: sold -> unavailable must be impossible in SQL ──
	if _, err := q.BlockSessionSeat(ctx, sold.ID, 999); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("BlockSessionSeat on a sold seat: err = %v, want pgx.ErrNoRows (sold->unavailable is forbidden)", err)
	}

	// ── Guard: the seat cannot be released while its ticket is ACTIVE ──
	if _, err := q.ReleaseSoldSessionSeat(ctx, f.sessionID, "A|1|1", 999); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ReleaseSoldSessionSeat with an active ticket: err = %v, want pgx.ErrNoRows", err)
	}

	// ── Cancel (manual mode — no provider call anywhere in this test) ──
	cancelled, err := q.CancelTicket(ctx, ticket.ID, "customer request", "manual")
	if err != nil {
		t.Fatalf("CancelTicket: %v", err)
	}
	if cancelled.Status != "cancelled" || cancelled.CancelledAt == nil ||
		cancelled.RefundMode == nil || *cancelled.RefundMode != "manual" {
		t.Fatalf("cancelled row wrong: status=%s cancelled_at=%v refund_mode=%v",
			cancelled.Status, cancelled.CancelledAt, cancelled.RefundMode)
	}
	// Double-cancel is a conflict, not a silent success.
	if _, err := q.CancelTicket(ctx, ticket.ID, "again", "none"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second CancelTicket: err = %v, want pgx.ErrNoRows", err)
	}

	// ── Release inventory in a tx via the shared core ──
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	txq := q.WithTx(tx)
	outcome, err := htickets.ReleaseCancelledTicketInventoryTx(ctx, txq, cancelled)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ReleaseCancelledTicketInventoryTx: %v", err)
	}
	if !outcome.SeatReleased || outcome.GAUnitReleased {
		_ = tx.Rollback(ctx)
		t.Fatalf("outcome = %+v, want SeatReleased only", outcome)
	}
	if _, err := txq.RestoreSoldCapacity(ctx, f.sessionID, nil, 1); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("RestoreSoldCapacity: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Seat is back on sale with the reservation link cleared.
	seat, err := q.GetSessionSeatByKey(ctx, f.sessionID, "A|1|1")
	if err != nil {
		t.Fatalf("re-read seat: %v", err)
	}
	if seat.Status != "available" || seat.ReservationID != nil {
		t.Fatalf("seat after cancel = %s/%v, want available/nil", seat.Status, seat.ReservationID)
	}
	var soldCap int
	if err := pool.QueryRow(ctx, `SELECT capacity_sold FROM inventory_ledger
		WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID).Scan(&soldCap); err != nil {
		t.Fatalf("ledger read: %v", err)
	}
	if soldCap != 0 {
		t.Fatalf("capacity_sold = %d, want 0", soldCap)
	}

	// ── Resell: same seat, new ticket, both rows retained in history ──
	resold := sellSeat(t, ctx, q, f, "A|1|1")
	if resold.Status != "sold" {
		t.Fatalf("resell status = %s", resold.Status)
	}
	ticket2, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil,
		ptr("A|1|1"), ptr("A"), ptr("1"), ptr("1"), 1)
	if err != nil {
		t.Fatalf("InsertTicket #2 (resell): %v", err)
	}
	rows, err := q.ListTicketsByCheckoutSession(ctx, f.checkoutID)
	if err != nil {
		t.Fatalf("ListTicketsByCheckoutSession: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ticket history rows = %d, want 2 (cancelled + active)", len(rows))
	}
	// Only one is valid at a time.
	active, err := q.CountActiveTicketsForSeat(ctx, f.sessionID, "A|1|1")
	if err != nil {
		t.Fatalf("CountActiveTicketsForSeat: %v", err)
	}
	if active != 1 {
		t.Fatalf("active tickets for seat = %d, want 1", active)
	}
	_ = ticket2
}

// TestAB49Integration_GAUnit_ReleaseExactlyOne proves GA cancellation
// frees exactly one sold unit of the ticket's reservation, and that a
// legacy reservation without unit rows degrades to ledger-only.
func TestAB49Integration_GAUnit_ReleaseExactlyOne(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping AB-49 GA integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f.cleanup()
	q := gen.New(pool)

	// Materialize 3 pool units; hold+sell 2 under the reservation.
	if _, err := q.InsertGAUnits(ctx, f.sessionID, "ga|pool", 0, &f.tierID, 3); err != nil {
		t.Fatalf("InsertGAUnits: %v", err)
	}
	v, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("version bump: %v", err)
	}
	held, err := q.AllocateGAUnitsForHold(ctx, f.sessionID, f.resID, &f.tierID, v, &f.tierID, 2)
	if err != nil {
		t.Fatalf("AllocateGAUnitsForHold: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("held units = %d, want 2", len(held))
	}
	for _, u := range held {
		v, err = q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
		if err != nil {
			t.Fatalf("version bump: %v", err)
		}
		if _, err := q.SellSessionSeat(ctx, u.ID, f.resID, v); err != nil {
			t.Fatalf("SellSessionSeat GA unit: %v", err)
		}
	}

	// GA ticket carries no seat_key.
	ticket, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil,
		nil, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("InsertTicket GA: %v", err)
	}
	cancelled, err := q.CancelTicket(ctx, ticket.ID, "ga cancel", "none")
	if err != nil {
		t.Fatalf("CancelTicket GA: %v", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	outcome, err := htickets.ReleaseCancelledTicketInventoryTx(ctx, q.WithTx(tx), cancelled)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ReleaseCancelledTicketInventoryTx GA: %v", err)
	}
	if !outcome.GAUnitReleased || outcome.SeatReleased {
		_ = tx.Rollback(ctx)
		t.Fatalf("outcome = %+v, want GAUnitReleased only", outcome)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Exactly one unit came back: 1 sold + 2 available.
	soldLeft, err := q.CountGAUnitsHeldSoldByTier(ctx, f.sessionID, f.tierID)
	if err != nil {
		t.Fatalf("CountGAUnitsHeldSoldByTier: %v", err)
	}
	if soldLeft != 1 {
		t.Fatalf("units still held/sold = %d, want 1", soldLeft)
	}

	// Legacy path: fresh fixture with NO unit rows — release degrades to
	// ledger-only (no row released, no error).
	f2 := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f2.cleanup()
	legacyTicket, err := q.InsertTicket(ctx, f2.checkoutID, f2.sessionID, &f2.tierID, nil,
		nil, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("InsertTicket legacy GA: %v", err)
	}
	legacyCancelled, err := q.CancelTicket(ctx, legacyTicket.ID, "legacy", "none")
	if err != nil {
		t.Fatalf("CancelTicket legacy: %v", err)
	}
	tx2, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx legacy: %v", err)
	}
	outcome2, err := htickets.ReleaseCancelledTicketInventoryTx(ctx, q.WithTx(tx2), legacyCancelled)
	if err != nil {
		_ = tx2.Rollback(ctx)
		t.Fatalf("legacy release: %v", err)
	}
	_ = tx2.Commit(ctx)
	if outcome2.RowReleased() {
		t.Fatalf("legacy outcome = %+v, want no rows released", outcome2)
	}
}

// TestAB49Integration_OrderScope_FullAndPartial proves the inbound
// refund semantics at the query layer: a full refund cancels every
// active ticket of the order with the refund linkage stamped; a partial
// refund flags — and only flags — the order's active tickets.
func TestAB49Integration_OrderScope_FullAndPartial(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping AB-49 order-scope integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	f := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f.cleanup()
	q := gen.New(pool)

	t1, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil, nil, nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("InsertTicket 1: %v", err)
	}
	if _, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil, nil, nil, nil, nil, 1); err != nil {
		t.Fatalf("InsertTicket 2: %v", err)
	}

	// Partial: both active tickets flagged, none cancelled.
	held, err := q.SetTicketsReviewHoldByCheckoutSession(ctx, f.checkoutID, "partial inbound refund test")
	if err != nil {
		t.Fatalf("SetTicketsReviewHoldByCheckoutSession: %v", err)
	}
	if held != 2 {
		t.Fatalf("review holds = %d, want 2", held)
	}
	rows, err := q.ListTicketsByCheckoutSession(ctx, f.checkoutID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.Status != "active" || !r.ReviewHold {
			t.Fatalf("ticket %s: status=%s hold=%v, want active+hold", r.ID, r.Status, r.ReviewHold)
		}
	}
	// Re-flagging is a no-op (idempotent escalation).
	held, err = q.SetTicketsReviewHoldByCheckoutSession(ctx, f.checkoutID, "again")
	if err != nil || held != 0 {
		t.Fatalf("second hold pass = (%d, %v), want (0, nil)", held, err)
	}
	// Operator resolves one hold.
	if _, err := q.ClearTicketReviewHold(ctx, t1.ID); err != nil {
		t.Fatalf("ClearTicketReviewHold: %v", err)
	}

	// Full: every active ticket cancelled with the linkage stamped.
	cancelled, err := q.CancelTicketsByCheckoutSession(ctx, f.checkoutID, "inbound provider refund test", nil)
	if err != nil {
		t.Fatalf("CancelTicketsByCheckoutSession: %v", err)
	}
	if len(cancelled) != 2 {
		t.Fatalf("cancelled = %d, want 2", len(cancelled))
	}
	for _, r := range cancelled {
		if r.Status != "cancelled" || r.RefundMode == nil || *r.RefundMode != "automatic" || r.RefundDate == nil {
			t.Fatalf("ticket %s: status=%s mode=%v date=%v", r.ID, r.Status, r.RefundMode, r.RefundDate)
		}
	}
	// Idempotent: nothing left to cancel.
	again, err := q.CancelTicketsByCheckoutSession(ctx, f.checkoutID, "again", nil)
	if err != nil || len(again) != 0 {
		t.Fatalf("second cancel pass = (%d, %v), want (0, nil)", len(again), err)
	}
}

func ptr[T any](v T) *T { return &v }
