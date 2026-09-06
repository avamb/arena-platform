//go:build integration

// bind_external_ids_516_integration_test.go — live-DB coverage for
// W1-C3b (feature #516, spec §3.1 + §13.2 step 6): seat materialisation
// copies geometry.seats[].external_id (the Bil24 seatId, parsed by
// seating.ImportSBTSVG in #515) into session_seats.system_seat_id with
// system_seat_id_source='bil24', re-derives exactly the same integers on
// every rebind, and pushes session_seats_system_id_seq past the
// explicitly assigned range so a plain arena plan can never be handed a
// colliding id.
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0090).
//
// Run with:
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go test -tags integration \
//	    ./apps/backend/internal/platform/httpserver/hseating/ -run TestW1C3b
package hseating

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// bil24SeatIDBase mirrors the real Bil24 seatId magnitude (§3.1 note:
// "bil24: как в Bil24 (2.5e9+)") so the test also proves the bigint
// column and the sequence guard cope with ids far outside the arena
// sequence range.
const bil24SeatIDBase int64 = 2_500_000_000

// c3bFixture seeds org → venue → event → seating plan → version →
// session so bindSessionSeatingCore can run against real FKs.
type c3bFixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	planID    uuid.UUID
	planVerID uuid.UUID
}

// c3bGeometry builds a two-row / four-seat assigned-seats geometry. When
// withExternalIDs is true every seat carries a Bil24 external id.
func c3bGeometry(withExternalIDs bool) seating.Geometry {
	g := seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 400, Height: 300},
		Categories: []seating.Category{
			{Index: 1, Name: "Parter", Color: "#ff0000"},
		},
		Sections: []seating.Section{{
			Key:  "A",
			Name: "A",
			Rows: []seating.Row{
				{Key: "1", Name: "1"},
				{Key: "2", Name: "2"},
			},
		}},
		Tables: []seating.Table{},
	}
	n := int64(0)
	for r := range g.Sections[0].Rows {
		row := &g.Sections[0].Rows[r]
		for s := 1; s <= 2; s++ {
			n++
			seat := seating.Seat{
				Key:           seating.SeatKey("A", row.Key, fmt.Sprint(s)),
				Number:        fmt.Sprint(s),
				X:             float64(10 * s),
				Y:             float64(10 * (r + 1)),
				Radius:        5,
				CategoryIndex: 1,
			}
			if withExternalIDs {
				seat.ExternalID = bil24SeatIDBase + n
			}
			row.Seats = append(row.Seats, seat)
		}
	}
	return seating.Canonicalize(g)
}

func newC3bFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, geometry seating.Geometry) *c3bFixture {
	t.Helper()
	f := &c3bFixture{
		t: t, pool: pool,
		orgID:     uuid.New(),
		venueID:   uuid.New(),
		eventID:   uuid.New(),
		sessionID: uuid.New(),
		planID:    uuid.New(),
		planVerID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	geoJSON, err := json.Marshal(geometry)
	if err != nil {
		t.Fatalf("marshal geometry: %v", err)
	}
	checksum, err := seating.Checksum(geometry)
	if err != nil {
		t.Fatalf("checksum geometry: %v", err)
	}
	seatCap := int32(geometry.SeatCount()) //nolint:gosec // 4 seats
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "C3b Org " + suffix, "c3b-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "C3b Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "C3b Event " + suffix}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
			[]any{f.planID, f.venueID, f.orgID, "C3b Plan " + suffix}},
		{`INSERT INTO seating_plan_versions
		    (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		  VALUES ($1, $2, 1, $3::jsonb, $4, $5)`,
			[]any{f.planVerID, f.planID, string(geoJSON), checksum, seatCap}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source,
		    seating_plan_version_id)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', $4, 'draft', 'assigned_seats',
		    'EUR', 'override', $5)`,
			[]any{f.sessionID, f.eventID, f.venueID, seatCap, f.planVerID}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		  VALUES ($1, NULL, $2)`,
			[]any{f.sessionID, seatCap}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("C3b fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *c3bFixture) cleanup() {
	ctx := context.Background()
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM session_seats WHERE session_id = $1`, f.sessionID},
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
			f.t.Logf("C3b cleanup: %v", err)
		}
	}
}

// c3bSeatIDs returns seat_key → (system_seat_id, source) for the session.
func c3bSeatIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID uuid.UUID) map[string]struct {
	ID     int64
	Source string
} {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT seat_key, system_seat_id, system_seat_id_source
		FROM   session_seats
		WHERE  session_id = $1
		ORDER  BY seat_key`, sessionID)
	if err != nil {
		t.Fatalf("query session_seats: %v", err)
	}
	defer rows.Close()
	out := make(map[string]struct {
		ID     int64
		Source string
	})
	for rows.Next() {
		var key, source string
		var id int64
		if err := rows.Scan(&key, &id, &source); err != nil {
			t.Fatalf("scan session_seats: %v", err)
		}
		out[key] = struct {
			ID     int64
			Source string
		}{ID: id, Source: source}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate session_seats: %v", err)
	}
	return out
}

// TestW1C3b_MaterializationKeepsExternalSeatIDsAcrossRebind is the spec
// §15.3 scenario 8 invariant at the materialisation layer: bind twice,
// the seat ids are identical and equal to the Bil24 seatIds; a plan
// without external ids gets sequence ids that do not collide with them.
func TestW1C3b_MaterializationKeepsExternalSeatIDsAcrossRebind(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping W1-C3b integration test")
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

	// ── Imported (Bil24) plan ────────────────────────────────────────
	bil24Geo := c3bGeometry(true)
	bf := newC3bFixture(t, ctx, pool, bil24Geo)
	defer bf.cleanup()

	bindReq := bindRequest{
		AdmissionMode:   "assigned_seats",
		CategoryTierMap: map[string]*string{},
		AutoCreateTiers: true,
	}
	res, bErr := h.bindSessionSeatingCore(ctx, req, bf.eventID, bf.sessionID, bf.planVerID, bindReq)
	if bErr != nil {
		t.Fatalf("first bind: %s %s", bErr.Code, bErr.Message)
	}
	if res.Materialized != 4 {
		t.Fatalf("first bind materialized = %d, want 4", res.Materialized)
	}
	first := c3bSeatIDs(t, ctx, pool, bf.sessionID)
	if len(first) != 4 {
		t.Fatalf("first bind seat rows = %d, want 4", len(first))
	}
	var maxExternal int64
	for _, section := range bil24Geo.Sections {
		for _, row := range section.Rows {
			for _, seat := range row.Seats {
				got, ok := first[seat.Key]
				if !ok {
					t.Fatalf("seat %q missing from session_seats", seat.Key)
				}
				if got.ID != seat.ExternalID {
					t.Errorf("seat %q system_seat_id = %d, want the Bil24 seatId %d",
						seat.Key, got.ID, seat.ExternalID)
				}
				if got.Source != gen.SeatIDSourceBil24 {
					t.Errorf("seat %q system_seat_id_source = %q, want %q",
						seat.Key, got.Source, gen.SeatIDSourceBil24)
				}
				if seat.ExternalID > maxExternal {
					maxExternal = seat.ExternalID
				}
			}
		}
	}

	// ── Rebind twice: the ids must not move ─────────────────────────
	for pass := 2; pass <= 3; pass++ {
		if _, bErr := h.bindSessionSeatingCore(ctx, req, bf.eventID, bf.sessionID, bf.planVerID, bindReq); bErr != nil {
			t.Fatalf("bind pass %d: %s %s", pass, bErr.Code, bErr.Message)
		}
		again := c3bSeatIDs(t, ctx, pool, bf.sessionID)
		if len(again) != len(first) {
			t.Fatalf("bind pass %d: seat rows = %d, want %d", pass, len(again), len(first))
		}
		for key, want := range first {
			got, ok := again[key]
			if !ok {
				t.Fatalf("bind pass %d: seat %q disappeared", pass, key)
			}
			if got.ID != want.ID || got.Source != want.Source {
				t.Errorf("bind pass %d: seat %q = (%d,%s), want (%d,%s) — external ids must survive a rebind",
					pass, key, got.ID, got.Source, want.ID, want.Source)
			}
		}
	}

	// ── Plan WITHOUT external ids: sequence ids, no collision ───────
	af := newC3bFixture(t, ctx, pool, c3bGeometry(false))
	defer af.cleanup()
	if _, bErr := h.bindSessionSeatingCore(ctx, req, af.eventID, af.sessionID, af.planVerID, bindReq); bErr != nil {
		t.Fatalf("arena bind: %s %s", bErr.Code, bErr.Message)
	}
	arena := c3bSeatIDs(t, ctx, pool, af.sessionID)
	if len(arena) != 4 {
		t.Fatalf("arena bind seat rows = %d, want 4", len(arena))
	}
	seen := make(map[int64]bool, len(arena))
	for key, got := range arena {
		if got.Source != gen.SeatIDSourceArena {
			t.Errorf("arena seat %q source = %q, want %q", key, got.Source, gen.SeatIDSourceArena)
		}
		if got.ID <= maxExternal {
			t.Errorf("arena seat %q system_seat_id = %d, want > %d — the sequence must be pushed past explicitly assigned ids",
				key, got.ID, maxExternal)
		}
		if seen[got.ID] {
			t.Errorf("arena seat %q reuses system_seat_id %d", key, got.ID)
		}
		seen[got.ID] = true
	}
}
