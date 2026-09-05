//go:build integration

// seed_test.go — feature #470 (W1-A1a) harness seeding.
//
// Boots the fixture that every §15.3 wire-contract scenario in harness_test.go
// runs against, using the real arena_new schema and (where possible) the
// generated `gen.Queries` API so schema drift breaks compilation instead of
// silently producing wrong shapes.
//
// Layout seeded (spec §15.1–15.3, §9.3):
//
//   organizations(legal_name)
//     └── sales_channels(display_number = FID, settings.gateway_token_hash =
//         bcrypt(ChannelToken), fee_percent = 5)
//     └── cities(Praha, CZ)  ← seed once via ON CONFLICT
//     └── venues(city_id=Praha, timezone='Europe/Prague')
//     └── seating_plans(Palác Akropolis) → seating_plan_versions from
//         06_venue_maps_and_seating/Palac_Akropolis.svg via seating.ImportSVG
//     └── events(status='published', visibility='public')
//         ├── sessions(admission_mode='assigned_seats', bound to plan version)
//         │     └── session_seats materialised from geometry (system_seat_id
//         │         is a sequence-backed bigint; harnessState.SeatIDs keyed
//         │         "<Section>-<Row>-<Number>" → strconv(system_seat_id))
//         └── sessions(admission_mode='general_admission', capacity=50)
//               ├── inventory_ledger(session_id, NULL tier, capacity_total=50)
//               ├── session_seats (kind='ga_unit', 50 rows)
//               └── ticket_tiers × 2 (Early / Standard, currency EUR)
//     └── promo_codes(code='WAVE1', 10% off)
//
// Everything above is registered with t.Cleanup so a failed test does not
// leak fixtures across runs on Docker PG.
//
// The svg importer round-trips the Palác Akropolis file that lives under
// 06_venue_maps_and_seating/. The relative path uses runtime.Caller so we
// do not depend on `go test` cwd conventions across CI runners.

package compat_bil24_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// palacAkropolisSVGPath returns the absolute path to the reference SVG.
// Uses runtime.Caller so the lookup works regardless of the test binary's
// working directory (Docker CI, `go test` from repo root, IDE runner).
func palacAkropolisSVGPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate SVG fixture")
	}
	// apps/backend/tests/compat/bil24 → repo root is 5 dirs up
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", ".."))
	return filepath.Join(repoRoot, "06_venue_maps_and_seating", "Palac_Akropolis.svg")
}

// seedHarness creates the wave-1 fixture against the live DB pointed to by
// DATABASE_URL and returns a populated harnessState. Registers cleanup that
// removes every row inserted so the harness is safe to re-run.
func seedHarness(t *testing.T) *harnessState {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("seedHarness: DATABASE_URL not set")
	}

	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// gen.Queries is instantiated so scenarios that un-skip can reuse the
	// same conn pool for real reads; keeping the reference here makes the
	// intent obvious and forces a compile break on gen API drift.
	_ = gen.New(pool)

	// ── 1. Country + city (idempotent, other tests may run in parallel) ──
	//
	// Czechia is not in the 0006_geo.sql seed list; INSERT once via
	// ON CONFLICT so parallel test packages do not race.
	if _, err := pool.Exec(ctx,
		`INSERT INTO countries (iso2, iso3, slug, currency)
		 VALUES ('CZ','CZE','czechia','CZK')
		 ON CONFLICT (iso2) DO NOTHING`); err != nil {
		t.Fatalf("seed country CZ: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cities (country_id, slug)
		 SELECT id, 'praha' FROM countries WHERE iso2='CZ'
		 ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed city praha: %v", err)
	}
	var cityID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT c.id FROM cities c JOIN countries co ON co.id=c.country_id
		 WHERE co.iso2='CZ' AND c.slug='praha'`).Scan(&cityID); err != nil {
		t.Fatalf("resolve praha city id: %v", err)
	}

	// ── 2. Organisation with legal_name (spec §9.3 actionLegalOwner) ─────
	orgID := uuid.New()
	suffix := orgID.String()[:8]
	if _, err := pool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, legal_name)
		 VALUES ($1, $2, $3, $4)`,
		orgID,
		"W1-A1a Org "+suffix,
		"w1-a1a-"+suffix,
		"Lampyris Events s.r.o.",
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	// ── 3. Sales channel (spec §5.1) ─────────────────────────────────────
	// - display_number is auto-assigned by the seq from 0072; we read it
	//   back to populate harnessState.ChannelFID (Bil24 FID).
	// - Raw ChannelToken is kept in state; the DB carries only the bcrypt
	//   hash under settings.gateway_token_hash (see hbil24 usage).
	channelID := uuid.New()
	rawToken := "wave1-seed-token-" + suffix
	tokenHash, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	settingsJSON, err := json.Marshal(map[string]string{
		"gateway_token_hash": string(tokenHash),
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var channelFID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO sales_channels
		     (id, org_id, name, provider, payment_mode, fee_percent, settings)
		 VALUES ($1, $2, $3, 'stripe', 'direct_merchant', 5.00, $4::jsonb)
		 RETURNING display_number`,
		channelID, orgID, "WP Bil24 gateway "+suffix, settingsJSON,
	).Scan(&channelFID); err != nil {
		t.Fatalf("seed sales_channel: %v", err)
	}

	// ── 4. Venue (Palác Akropolis, Praha) ────────────────────────────────
	venueID := uuid.New()
	const venueTZ = "Europe/Prague"
	if _, err := pool.Exec(ctx,
		`INSERT INTO venues (id, org_id, city_id, name, country, timezone)
		 VALUES ($1, $2, $3, $4, 'CZ', $5)`,
		venueID, orgID, cityID, "Palác Akropolis "+suffix, venueTZ,
	); err != nil {
		t.Fatalf("seed venue: %v", err)
	}

	// ── 5. Seating plan from the checked-in SVG (via seating.ImportSVG) ──
	svgPath := palacAkropolisSVGPath(t)
	svgRaw, err := os.ReadFile(svgPath)
	if err != nil {
		t.Fatalf("read Palac_Akropolis.svg: %v", err)
	}
	geom, warns, errs := seating.ImportSVG(svgRaw)
	if len(errs) > 0 {
		t.Fatalf("seating.ImportSVG returned %d errors: %v", len(errs), errs)
	}
	_ = warns // non-fatal per svg_import contract

	geomJSON, err := json.Marshal(geom)
	if err != nil {
		t.Fatalf("marshal geometry: %v", err)
	}
	capSeated := countSeats(geom)
	if capSeated == 0 {
		t.Fatalf("Palác Akropolis geometry parsed with zero seats — check the SVG fixture")
	}

	planID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO seating_plans
		     (id, venue_id, owner_org_id, name, plan_type, status, visibility)
		 VALUES ($1, $2, $3, 'Palác Akropolis (harness)', 'assigned_seats',
		         'active', 'private')`,
		planID, venueID, orgID,
	); err != nil {
		t.Fatalf("seed seating_plan: %v", err)
	}
	planVersionID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO seating_plan_versions
		     (id, seating_plan_id, version_number, geometry,
		      geometry_checksum, capacity_seated, capacity_standing, locked_at)
		 VALUES ($1, $2, 1, $3::jsonb, $4, $5, 0, now())`,
		planVersionID, planID, geomJSON,
		fmt.Sprintf("sha256:%x", []byte("harness-v1")), // deterministic placeholder
		capSeated,
	); err != nil {
		t.Fatalf("seed seating_plan_version: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE seating_plans SET current_version_id=$1 WHERE id=$2`,
		planVersionID, planID); err != nil {
		t.Fatalf("point plan at current version: %v", err)
	}

	// ── 6. Event (published, public) ────────────────────────────────────
	// events no longer carries venue_id/start_at/end_at as of the
	// 0075+ schema — those live on sessions now (see \d events / \d
	// sessions). The venue link happens per session below.
	eventID := uuid.New()
	start := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	end := start.Add(3 * time.Hour)
	if _, err := pool.Exec(ctx,
		`INSERT INTO events (id, org_id, name, status, visibility)
		 VALUES ($1, $2, $3, 'published', 'public')`,
		eventID, orgID, "W1 Harness Event "+suffix,
	); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	// ── 7. Assigned-seats session bound to the plan ──────────────────────
	assignedSessID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions
		     (id, event_id, venue_id, start_at, end_at, capacity_total,
		      status, admission_mode, seating_plan_version_id,
		      currency, currency_source)
		 VALUES ($1, $2, $3, $4, $5, $6, 'scheduled', 'assigned_seats',
		         $7, 'CZK', 'derived')`,
		assignedSessID, eventID, venueID, start, end, capSeated, planVersionID,
	); err != nil {
		t.Fatalf("seed assigned session: %v", err)
	}

	// Feature #484 (spec §7.4): the assigned-seats session needs a priced
	// tier so a RESERVATION response carries a real sum / charge / totalSum.
	// Every session_seat below is stamped with it, which is what hbil24's
	// cart projection reads to price a seat row.
	var assignedTierID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1,'Parter','fixed',500,'CZK',0)
		 RETURNING id`,
		assignedSessID,
	).Scan(&assignedTierID); err != nil {
		t.Fatalf("seed assigned ticket_tier: %v", err)
	}

	// Materialise session_seats from the geometry and capture the
	// generated system_seat_id per canonical "Section-Row-Number" label
	// (harnessState.SeatIDs key style per feature #470 description).
	seatIDs := map[string]string{}
	for _, sec := range geom.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				seatKey := seating.SeatKey(sec.Key, row.Key, s.Number)
				var systemSeatID int64
				if err := pool.QueryRow(ctx,
					`INSERT INTO session_seats
					     (session_id, seat_key, sector_name, row_name,
					      seat_number, tier_id, status, kind)
					 VALUES ($1, $2, $3, $4, $5, $6, 'available', 'seat')
					 RETURNING system_seat_id`,
					assignedSessID, seatKey, sec.Name, row.Name, s.Number,
					assignedTierID,
				).Scan(&systemSeatID); err != nil {
					t.Fatalf("materialise session_seat %s: %v", seatKey, err)
				}
				label := sec.Name + "-" + row.Name + "-" + s.Number
				seatIDs[label] = strconv.FormatInt(systemSeatID, 10)
			}
		}
	}

	// The seated session needs the same session-level inventory_ledger row a
	// platform-created session gets: hcheckout's seated hold path calls
	// ReserveCapacity(session, NULL tier, qty) before touching a seat, and a
	// missing ledger row surfaces as pgx.ErrNoRows → CapacityError → a
	// bogus "sold out" 101 on the Bil24 wire (feature #484).
	if _, err := pool.Exec(ctx,
		`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		 VALUES ($1, NULL, $2)`,
		assignedSessID, len(seatIDs),
	); err != nil {
		t.Fatalf("seed assigned inventory_ledger: %v", err)
	}

	// ── 8. GA session (AB-51 path, 50 units) + tiers ────────────────────
	gaSessID := uuid.New()
	const gaCap = 50
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions
		     (id, event_id, venue_id, start_at, end_at, capacity_total,
		      status, admission_mode, currency, currency_source)
		 VALUES ($1, $2, $3, $4, $5, $6, 'scheduled', 'general_admission',
		         'EUR', 'override')`,
		gaSessID, eventID, venueID, start, end, gaCap,
	); err != nil {
		t.Fatalf("seed GA session: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
		 VALUES ($1, NULL, $2)`,
		gaSessID, gaCap,
	); err != nil {
		t.Fatalf("seed inventory_ledger: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO session_seats
		     (session_id, seat_key, sector_name, row_name, seat_number,
		      tier_id, status, kind)
		 SELECT $1, 'ga|pool|' || lpad(gs::text, 6, '0'), '', '', '',
		        NULL, 'available', 'ga_unit'
		 FROM generate_series(1, $2::int) gs`,
		gaSessID, gaCap,
	); err != nil {
		t.Fatalf("seed GA session_seats: %v", err)
	}
	// Two tiers (Early Bird + Standard), EUR — matches spec §9.3 sample.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_tiers (session_id, name, pricing_mode,
		     price_amount, currency, sort_order)
		 VALUES ($1,'Early Bird','fixed',900,'EUR',0),
		        ($1,'Standard','fixed',1250,'EUR',1)`,
		gaSessID,
	); err != nil {
		t.Fatalf("seed ticket_tiers: %v", err)
	}

	// ── 9. Promo code (spec §7.5 ADD_PROMO_CODES fixture) ────────────────
	if _, err := pool.Exec(ctx,
		`INSERT INTO promo_codes
		     (org_id, code, discount_type, discount_value)
		 VALUES ($1, 'WAVE1', 'percent', 10)`,
		orgID,
	); err != nil {
		t.Fatalf("seed promo_code: %v", err)
	}

	// ── 10. Cleanup registration (LIFO) ─────────────────────────────────
	t.Cleanup(func() {
		cctx := context.Background()
		// Order matters: children first. Errors are logged, not fatal —
		// leaked rows on a failed test are annoying but not corrupting.
		stmts := []struct {
			sql string
			arg any
		}{
			{`DELETE FROM promo_codes WHERE org_id=$1`, orgID},
			// session_seats before ticket_tiers: feature #484 stamps
			// session_seats.tier_id on the assigned-seats session, and
			// session_seats_tier_id_fkey blocks the reverse order.
			{`DELETE FROM session_seats WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id=$1)`, eventID},
			{`DELETE FROM ticket_tiers WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id=$1)`, eventID},
			{`DELETE FROM inventory_ledger WHERE session_id IN
			      (SELECT id FROM sessions WHERE event_id=$1)`, eventID},
			{`DELETE FROM sessions WHERE event_id=$1`, eventID},
			{`DELETE FROM events WHERE id=$1`, eventID},
			{`UPDATE seating_plans SET current_version_id=NULL WHERE id=$1`, planID},
			{`DELETE FROM seating_plan_versions WHERE seating_plan_id=$1`, planID},
			{`DELETE FROM seating_plans WHERE id=$1`, planID},
			{`DELETE FROM venues WHERE id=$1`, venueID},
			{`DELETE FROM sales_channels WHERE id=$1`, channelID},
			{`DELETE FROM organizations WHERE id=$1`, orgID},
		}
		for _, s := range stmts {
			if _, err := pool.Exec(cctx, s.sql, s.arg); err != nil {
				t.Logf("cleanup %.60s… : %v", s.sql, err)
			}
		}
	})

	return &harnessState{
		OrgID:          orgID.String(),
		ChannelFID:     channelFID,
		ChannelToken:   rawToken,
		VenueID:        venueID.String(),
		VenueTimezone:  venueTZ,
		EventID:        eventID.String(),
		AssignedSessID: assignedSessID.String(),
		AssignedTierID: assignedTierID.String(),
		GAsessID:       gaSessID.String(),
		SeatIDs:        seatIDs,
		Pool:           pool,
	}
}

// countSeats totals the assigned-seat count across all sections/rows in a
// geometry. GA polygons carry no seats and are ignored — the harness GA
// session uses a separate ga_unit pool sized to 50 (§15.1).
func countSeats(g seating.Geometry) int {
	n := 0
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			n += len(row.Seats)
		}
	}
	return n
}
