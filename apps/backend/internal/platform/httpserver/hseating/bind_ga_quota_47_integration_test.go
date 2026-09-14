//go:build integration

// bind_ga_quota_47_integration_test.go — live-DB coverage for the seating
// half of step 7 of plan 08_architecture/23:
//
//   - a FIRST bind onto a session that already carries General Admission
//     places is refused with 409 seating.session_has_ga_places (the two sets
//     of places would double the capacity);
//   - binding a GA plan records each category's quantity on ticket_tiers and
//     reserves its stable per-session number, and the session capacity is the
//     places, not the version's declared standing capacity;
//   - decision 8: re-binding the SAME version keeps the category places and
//     whatever quantity an operator has set since — the geometry stops being
//     the source of the quantity after the first bind.
//
// Requires DATABASE_URL against a migrated database (see AGENTS.md).
package hseating

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/gaquota"
)

// ga47Geometry is a pure General Admission plan: two categories with bulk
// capacities and no coordinate-bearing seat at all.
func ga47Geometry() seating.Geometry {
	return seating.Canonicalize(seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 400, Height: 300},
		Categories: []seating.Category{
			{Index: 1, Name: "Floor", Kind: seating.KindGeneralAdmission, Capacity: 30},
			{Index: 2, Name: "Balcony", Kind: seating.KindGeneralAdmission, Capacity: 20},
		},
		Sections: []seating.Section{},
		Tables:   []seating.Table{},
	})
}

type ga47Fixture struct {
	t         *testing.T
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	venueID   uuid.UUID
	eventID   uuid.UUID
	sessionID uuid.UUID
	planID    uuid.UUID
	planVerID uuid.UUID
}

// newGA47Fixture seeds org → venue → event → GA plan → version → a PLAN-LESS
// general-admission session, which is what the bind under test binds.
func newGA47Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, geometry seating.Geometry) *ga47Fixture {
	t.Helper()
	f := &ga47Fixture{
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
	var standing int32
	for _, c := range geometry.GACategories() {
		standing += int32(c.Capacity) //nolint:gosec // fixture capacities are tiny
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "GA47 Org " + suffix, "ga47-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "GA47 Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "GA47 Event " + suffix}},
		{`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		  VALUES ($1, $2, $3, $4, 'general_admission', 'active')`,
			[]any{f.planID, f.venueID, f.orgID, "GA47 Plan " + suffix}},
		{`INSERT INTO seating_plan_versions
		    (id, seating_plan_id, version_number, geometry, geometry_checksum,
		     capacity_seated, capacity_standing)
		  VALUES ($1, $2, 1, $3::jsonb, $4, 0, $5)`,
			[]any{f.planVerID, f.planID, string(geoJSON), checksum, standing}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '30 days',
		    now() + interval '30 days 2 hours', $4, 'draft', 'general_admission',
		    'EUR', 'override')`,
			[]any{f.sessionID, f.eventID, f.venueID, standing}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("GA47 fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *ga47Fixture) cleanup() {
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
			f.t.Logf("GA47 cleanup: %v", err)
		}
	}
}

func ga47Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping GA47 bind integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func ga47Handler(pool *pgxpool.Pool) *Handler {
	return New(gen.New(pool), pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func ga47Count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query (%.70s): %v", sql, err)
	}
	return n
}

// TestGA47Bind_RefusesFirstBindOnSessionWithGAPlaces pins the guard: a plan
// bound onto a session that already carries category places would leave both
// sets of places behind and double the capacity.
func TestGA47Bind_RefusesFirstBindOnSessionWithGAPlaces(t *testing.T) {
	pool := ga47Pool(t)
	ctx := context.Background()
	f := newGA47Fixture(t, ctx, pool, ga47Geometry())
	defer f.cleanup()

	tierID := uuid.New()
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount,
		    currency, sort_order, capacity, unit_seq)
		  VALUES ($1, $2, 'Hand-made', 'fixed', 2500, 'EUR', 0, 4, 1)`,
			[]any{tierID, f.sessionID}},
		{`INSERT INTO session_seats
		    (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'ga|t1|' || lpad(gs::text, 6, '0'), '', '', '', $2, 'available', 'ga_unit'
		  FROM generate_series(1, 4) gs`,
			[]any{f.sessionID, tierID}},
	} {
		if _, err := pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("GA47 place fixture: %v", err)
		}
	}

	h := ga47Handler(pool)
	req := httptest.NewRequest("POST", "/v1/seating/bind", nil)
	_, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bindRequest{
		AdmissionMode:   "general_admission",
		CategoryTierMap: map[string]*string{},
		AutoCreateTiers: true,
	})
	if bErr == nil {
		t.Fatalf("bind onto a session with GA places succeeded, want 409")
	}
	if bErr.Code != "seating.session_has_ga_places" {
		t.Fatalf("bind error = %s (%d), want seating.session_has_ga_places", bErr.Code, bErr.Status)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1`, f.sessionID); n != 4 {
		t.Errorf("a refused bind changed the places: %d, want the original 4", n)
	}
}

// TestGA47Bind_GAPlanRecordsQuantitiesAndSurvivesRebind is decision 8: the
// first bind writes the geometry's capacities onto ticket_tiers, and a rebind
// of the SAME version keeps the places (and the quantity an operator edited
// in between) instead of re-minting from the geometry.
func TestGA47Bind_GAPlanRecordsQuantitiesAndSurvivesRebind(t *testing.T) {
	pool := ga47Pool(t)
	ctx := context.Background()
	f := newGA47Fixture(t, ctx, pool, ga47Geometry())
	defer f.cleanup()

	h := ga47Handler(pool)
	req := httptest.NewRequest("POST", "/v1/seating/bind", nil)
	bind := bindRequest{
		AdmissionMode:   "general_admission",
		CategoryTierMap: map[string]*string{},
		AutoCreateTiers: true,
	}

	res, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bind)
	if bErr != nil {
		t.Fatalf("first bind: %s %s", bErr.Code, bErr.Message)
	}
	if len(res.CreatedTierIDs) != 2 {
		t.Fatalf("auto-created tiers = %d, want 2", len(res.CreatedTierIDs))
	}

	// Every auto-created category owns its places, carries the quantity and
	// has a reserved per-session number.
	rows, err := pool.Query(ctx, `
		SELECT tt.id, tt.capacity, tt.unit_seq,
		       (SELECT count(*) FROM session_seats ss
		         WHERE ss.tier_id = tt.id AND ss.kind='ga_unit')
		FROM   ticket_tiers tt
		WHERE  tt.session_id = $1 AND tt.deleted_at IS NULL
		ORDER  BY tt.sort_order`, f.sessionID)
	if err != nil {
		t.Fatalf("read categories: %v", err)
	}
	type cat struct {
		id       uuid.UUID
		capacity *int32
		unitSeq  *int32
		places   int64
	}
	var cats []cat
	for rows.Next() {
		var c cat
		if err := rows.Scan(&c.id, &c.capacity, &c.unitSeq, &c.places); err != nil {
			rows.Close()
			t.Fatalf("scan category: %v", err)
		}
		cats = append(cats, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate categories: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("categories = %d, want 2", len(cats))
	}
	for i, c := range cats {
		if c.capacity == nil || *c.capacity <= 0 {
			t.Errorf("category %d: capacity = %v, want the geometry's quantity", i, c.capacity)
		}
		if c.unitSeq == nil || *c.unitSeq <= 0 {
			t.Errorf("category %d: unit_seq = %v, want a reserved per-session number", i, c.unitSeq)
		}
		if c.capacity != nil && int64(*c.capacity) != c.places {
			t.Errorf("category %d: capacity %d != places %d", i, *c.capacity, c.places)
		}
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, f.sessionID); n != 50 {
		t.Fatalf("capacity_total after the first bind = %d, want 50 (30 + 20)", n)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT capacity_total FROM inventory_ledger WHERE session_id=$1 AND tier_id IS NULL`,
		f.sessionID); n != 50 {
		t.Fatalf("ledger capacity_total after the first bind = %d, want 50", n)
	}

	// An operator raises the first category from 30 to 45.
	edited := cats[0].id
	if err := gaquota.InTx(ctx, pool, gen.New(pool), func(txq *gen.Queries) error {
		_, err := gaquota.SetQuantity(ctx, txq, f.sessionID, edited, 45)
		return err
	}); err != nil {
		t.Fatalf("edit category quantity: %v", err)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, f.sessionID); n != 65 {
		t.Fatalf("capacity_total after the edit = %d, want 65 (45 + 20)", n)
	}

	// Rebind the SAME version: the geometry must not reclaim the quantity.
	if _, bErr := h.bindSessionSeatingCore(ctx, req, f.eventID, f.sessionID, f.planVerID, bind); bErr != nil {
		t.Fatalf("rebind: %s %s", bErr.Code, bErr.Message)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1 AND tier_id=$2`,
		f.sessionID, edited); n != 45 {
		t.Errorf("rebind re-minted the edited category: %d places, want 45 kept", n)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT count(*) FROM session_seats WHERE session_id=$1`, f.sessionID); n != 65 {
		t.Errorf("rebind changed the place count: %d, want 65", n)
	}
	if n := ga47Count(t, ctx, pool,
		`SELECT capacity_total FROM sessions WHERE id=$1`, f.sessionID); n != 65 {
		t.Errorf("rebind recomputed the capacity from the version: %d, want 65", n)
	}
}
