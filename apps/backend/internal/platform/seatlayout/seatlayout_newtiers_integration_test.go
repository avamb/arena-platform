//go:build integration

// seatlayout_newtiers_integration_test.go — a batch that creates categories
// (new_tiers) against a live PostgreSQL:
//
//   - two new tiers are created and the seats painted into them in ONE
//     transaction, with exactly one seat_status_version bump;
//   - a sold and a held seat named in that batch keep their category and come
//     back in skipped;
//   - a batch that fails after the categories were inserted (a change names a
//     category of another session, an audit failure) or before (a name taken
//     by a live category, too many categories) leaves NO category behind;
//   - the revert of such a batch puts the seats back and keeps the categories.
//
// Requires DATABASE_URL against a migrated database.
package seatlayout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

func (f *layoutFixture) liveTierCount() int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ticket_tiers WHERE session_id = $1 AND deleted_at IS NULL`, f.session).Scan(&n); err != nil {
		f.t.Fatalf("count tiers: %v", err)
	}
	return n
}

func (f *layoutFixture) tierRow(id uuid.UUID) (name, mode string, price int64, currency string, color *string, deleted bool) {
	f.t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name, pricing_mode, price_amount, trim(currency), color, deleted_at IS NOT NULL
		 FROM ticket_tiers WHERE id = $1 AND session_id = $2`, id, f.session).
		Scan(&name, &mode, &price, &currency, &color, &deleted); err != nil {
		f.t.Fatalf("read tier %s: %v", id, err)
	}
	return
}

func TestApplyBatch_NewTiers_CreateAndPaintAndRevert_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 6)
	ctx := context.Background()

	// Seat 1 sold, seat 2 held; 3..6 free in tier 1.
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2`, f.session, seatKey(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.hold(seatKey(2)); err != nil {
		t.Fatalf("hold seat 2: %v", err)
	}
	before, tiersBefore := f.version_(), f.liveTierCount()

	var audited []seatlayout.CreatedTier
	b, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		OrgID: f.org, SessionID: f.session, Kind: seatlayout.KindPaint, Actor: seatlayout.Actor{Label: "user"},
		NewTiers: []seatlayout.NewTier{
			{Key: "vip", Name: "  VIP  ", PriceAmount: 5000, Color: "#FF0000"},
			{Key: "cheap", Name: "Cheap", PriceAmount: 0},
		},
		Changes: []seatlayout.Change{
			{SeatKey: seatKey(1), NewTierKey: "vip"}, // sold: skipped
			{SeatKey: seatKey(2), NewTierKey: "vip"}, // held: skipped
			{SeatKey: seatKey(3), NewTierKey: "vip"},
			{SeatKey: seatKey(4), NewTierKey: "cheap"},
			{SeatKey: seatKey(5), NewTierKey: "cheap"},
			{SeatKey: seatKey(6), TierID: &f.tier2}, // an existing category in the same batch
		},
		Audit: func(_ context.Context, _ pgx.Tx, b seatlayout.Batch) error {
			audited = b.CreatedTiers
			return nil
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if b.SeatStatusVersion != before+1 || f.version_() != before+1 {
		t.Fatalf("version = %d (batch %d), want exactly one bump from %d", f.version_(), b.SeatStatusVersion, before)
	}
	if len(b.CreatedTiers) != 2 || b.CreatedTiers[0].Key != "vip" || b.CreatedTiers[1].Key != "cheap" {
		t.Fatalf("created = %+v", b.CreatedTiers)
	}
	if len(audited) != 2 || audited[0].TierID != b.CreatedTiers[0].TierID {
		t.Fatalf("the audit saw %+v", audited)
	}
	vip, cheap := b.CreatedTiers[0].TierID, b.CreatedTiers[1].TierID
	if f.liveTierCount() != tiersBefore+2 {
		t.Fatalf("tiers = %d, want %d", f.liveTierCount(), tiersBefore+2)
	}
	if name, mode, price, cur, color, _ := f.tierRow(vip); name != "VIP" || mode != "fixed" || price != 5000 || cur != "EUR" || color == nil || *color != "#ff0000" {
		t.Fatalf("vip = %q %q %d %q %v", name, mode, price, cur, color)
	}
	if name, mode, price, _, color, _ := f.tierRow(cheap); name != "Cheap" || mode != "free" || price != 0 || color != nil {
		t.Fatalf("cheap = %q %q %d %v", name, mode, price, color)
	}
	for key, want := range map[string]uuid.UUID{seatKey(3): vip, seatKey(4): cheap, seatKey(5): cheap, seatKey(6): f.tier2} {
		if tier, _, v := f.seat(key); tier != want || v != b.SeatStatusVersion {
			t.Fatalf("seat %s: tier %s version %d, want %s", key, tier, v, want)
		}
	}
	if r := skippedReasons(b); len(r) != 2 || r[seatKey(1)] != seatlayout.ReasonSold || r[seatKey(2)] != seatlayout.ReasonHeld {
		t.Fatalf("skipped = %+v", b.Skipped)
	}
	if tier, status, _ := f.seat(seatKey(1)); tier != f.tier1 || status != "sold" {
		t.Fatalf("sold seat touched: %s %s", tier, status)
	}
	if tier, status, _ := f.seat(seatKey(2)); tier != f.tier1 || status != "held" {
		t.Fatalf("held seat touched: %s %s", tier, status)
	}

	// Revert: the seats go back, the categories stay (now empty).
	rev, err := seatlayout.Revert(ctx, f.pool, f.q, seatlayout.RevertInput{OrgID: f.org, SessionID: f.session, BatchID: b.ID})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if len(rev.Changes) != 4 || len(rev.CreatedTiers) != 0 {
		t.Fatalf("revert = %d changes, %d created", len(rev.Changes), len(rev.CreatedTiers))
	}
	for n := 3; n <= 6; n++ {
		if tier, _, _ := f.seat(seatKey(n)); tier != f.tier1 {
			t.Fatalf("seat %d after revert: %s", n, tier)
		}
	}
	if f.liveTierCount() != tiersBefore+2 {
		t.Fatalf("revert removed categories: %d, want %d", f.liveTierCount(), tiersBefore+2)
	}
	for _, id := range []uuid.UUID{vip, cheap} {
		if _, _, _, _, _, deleted := f.tierRow(id); deleted {
			t.Fatalf("category %s deleted by the revert", id)
		}
	}
}

func TestApplyBatch_NewTiers_FailureLeavesNoCategory_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 4)
	ctx := context.Background()
	before, tiersBefore, batchesBefore := f.version_(), f.liveTierCount(), f.batchCount()
	unchanged := func(label string) {
		t.Helper()
		if f.version_() != before || f.liveTierCount() != tiersBefore || f.batchCount() != batchesBefore {
			t.Fatalf("%s: version %d->%d, tiers %d->%d, batches %d->%d — a refused batch must leave no trace",
				label, before, f.version_(), tiersBefore, f.liveTierCount(), batchesBefore, f.batchCount())
		}
		if tier, _, _ := f.seat(seatKey(1)); tier != f.tier1 {
			t.Fatalf("%s: seat 1 repainted", label)
		}
	}
	newTier := []seatlayout.NewTier{{Key: "n", Name: "Brand new", PriceAmount: 700}}

	// A category of ANOTHER session, refused after the new one was inserted.
	other := uuid.New()
	otherSession := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		VALUES ($1, $2, $3, now() + interval '40 days', now() + interval '40 days 2 hours', 10, 'scheduled', 'EUR', 'override')`,
		otherSession, f.event, f.venue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM ticket_tiers WHERE session_id = $1`, otherSession)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM sessions WHERE id = $1`, otherSession)
	})
	if _, err := f.pool.Exec(ctx, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		VALUES ($1, $2, 'Other', 'fixed', 100, 'EUR', 1, true)`, other, otherSession); err != nil {
		t.Fatal(err)
	}
	_, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, NewTiers: newTier,
		Changes: []seatlayout.Change{{SeatKey: seatKey(1), NewTierKey: "n"}, {SeatKey: seatKey(2), TierID: &other}},
	})
	var tierErr *seatlayout.TierError
	if !errors.As(err, &tierErr) || len(tierErr.TierIDs) != 1 || tierErr.TierIDs[0] != other {
		t.Fatalf("foreign tier: %v", err)
	}
	unchanged("foreign tier")

	// The audit fails after everything else was written.
	_, err = seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, NewTiers: newTier,
		Changes: []seatlayout.Change{{SeatKey: seatKey(1), NewTierKey: "n"}},
		Audit: func(context.Context, pgx.Tx, seatlayout.Batch) error {
			return errors.New("audit down")
		},
	})
	var auditErr *seatlayout.AuditError
	if !errors.As(err, &auditErr) {
		t.Fatalf("audit failure: %v", err)
	}
	unchanged("audit failure")

	// A name of a live category (trimmed, ignoring case).
	var ie *seatlayout.InputError
	_, err = seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint,
		NewTiers: []seatlayout.NewTier{{Key: "a", Name: "Fresh", PriceAmount: 1}, {Key: "b", Name: "  pARTER ", PriceAmount: 1}},
		Changes:  []seatlayout.Change{{SeatKey: seatKey(1), NewTierKey: "a"}, {SeatKey: seatKey(2), NewTierKey: "b"}},
	})
	if !errors.As(err, &ie) || ie.Code != seatlayout.CodeTierNameTaken {
		t.Fatalf("name taken: %v", err)
	}
	unchanged("name taken")

	// Live (2) plus new (14) above the ceiling of 15.
	many := make([]seatlayout.NewTier, 14)
	changes := make([]seatlayout.Change, 0, 14)
	for i := range many {
		k := "k" + string(rune('a'+i))
		many[i] = seatlayout.NewTier{Key: k, Name: "Many " + k, PriceAmount: 1}
		changes = append(changes, seatlayout.Change{SeatKey: "S|Z|" + k, NewTierKey: k})
	}
	_, err = seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		SessionID: f.session, Kind: seatlayout.KindPaint, NewTiers: many, Changes: changes,
	})
	if !errors.As(err, &ie) || ie.Code != seatlayout.CodeTooManyTiers {
		t.Fatalf("too many: %v", err)
	}
	unchanged("too many")
}
