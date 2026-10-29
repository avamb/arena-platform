package main

// Fixture of the web twin test of tasks S2b and S2c (the seat manager of a
// seated session; apps/event-center/tests/smoke/live/seats.spec.ts).
//
// One extra event of the fixture organization, "E2E Hall Show", PUBLISHED (the
// public map route /v1/event-sessions/{id}/schema answers only for a
// published event), with ONE seated date. The hall is a real geometry
// materialized by seatmint (the code the seating bind runs), so the keys are
// the ones production writes:
//
//	Parter   6 rows x 12 seats   rows 1-2 Premium, rows 3-6 Standard
//	Balcony  3 rows x 10 seats   Economy
//
//	E2E Hall Premium   50.00 EUR   (plan category 1)
//	E2E Hall Standard  30.00 EUR   (plan category 2)
//	E2E Hall Economy   15.00 EUR   (plan category 3)
//
// Three seats of Parter row 1 are SOLD and two of row 2 are in a HOLD, so the
// twin can prove that frozen seats are shown, never painted and never counted
// as changed, and that the automatic layout counts them at the price they were
// sold for.
//
// The spec calls these operations through the seed binary, because the test
// process has no SQL driver:
//
//	--seats-op reset                          puts the date back exactly as above
//	                                          (no batches, the seats as listed);
//	--seats-op inspect                        prints seat_key -> {tier, status} and
//	                                          the number of batches of the date;
//	--seats-op sell --seats-key <seat_key>    marks one seat sold: a sale that lands
//	                                          while the organizer's tray is open.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/seatmint"
)

const (
	seatsEventID   = "fe0000e2-0000-7000-8000-00000000e601"
	seatsSessionID = "fe0000e2-0000-7000-8000-00000000e602"
	seatsPlanID    = "fe0000e2-0000-7000-8000-00000000e603"
	seatsVersionID = "fe0000e2-0000-7000-8000-00000000e604"

	seatsEventName = "E2E Hall Show"
)

type seatsTier struct {
	ID    string
	Name  string
	Price int64
	Index int // the plan category it sells
}

func seatsTiers() []seatsTier {
	return []seatsTier{
		{ID: "fe0000e2-0000-7000-8000-00000000e611", Name: "E2E Hall Premium", Price: 5000, Index: 1},
		{ID: "fe0000e2-0000-7000-8000-00000000e612", Name: "E2E Hall Standard", Price: 3000, Index: 2},
		{ID: "fe0000e2-0000-7000-8000-00000000e613", Name: "E2E Hall Economy", Price: 1500, Index: 3},
	}
}

// seatsGeometry is the hall: 102 seats, the plan's own categories as listed above.
func seatsGeometry() seating.Geometry {
	build := func(sectionKey string, rows, perRow int, firstRow int, y0 float64, catOf func(row int) int) seating.Section {
		s := seating.Section{Key: sectionKey, Name: map[string]string{"P": "Parter", "B": "Balcony"}[sectionKey]}
		for r := 0; r < rows; r++ {
			rowNo := firstRow + r
			row := seating.Row{Key: strconv.Itoa(rowNo), Name: strconv.Itoa(rowNo)}
			for n := 1; n <= perRow; n++ {
				num := strconv.Itoa(n)
				row.Seats = append(row.Seats, seating.Seat{
					Key:           seating.SeatKey(sectionKey, row.Key, num),
					Number:        num,
					X:             90 + float64(n)*34,
					Y:             y0 + float64(r)*38,
					Radius:        11,
					CategoryIndex: catOf(rowNo),
				})
			}
			s.Rows = append(s.Rows, row)
		}
		return s
	}
	g := seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 620, Height: 470},
		Categories: []seating.Category{
			{Index: 1, Name: "Premium", Color: "#7c3aed"},
			{Index: 2, Name: "Standard", Color: "#0891b2"},
			{Index: 3, Name: "Economy", Color: "#65a30d"},
		},
		Sections: []seating.Section{
			build("P", 6, 12, 1, 90, func(row int) int {
				if row <= 2 {
					return 1
				}
				return 2
			}),
			build("B", 3, 10, 1, 350, func(int) int { return 3 }),
		},
		Tables:   []seating.Table{},
		DecorSVG: `<rect x="150" y="20" width="320" height="34" fill="#d1d5db"/>`,
	}
	return seating.Canonicalize(g)
}

func applySeatsFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("seats fixture %s: %w", what, err)
		}
		return nil
	}
	geo := seatsGeometry()
	geoJSON, err := seating.CanonicalJSON(geo)
	if err != nil {
		return err
	}
	checksum, err := seating.Checksum(geo)
	if err != nil {
		return err
	}
	seatCount := geo.SeatCount()

	if err := run("event", `
		INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, $3, 'published', 'public')
		ON CONFLICT (id) DO NOTHING`, seatsEventID, orgID, seatsEventName); err != nil {
		return err
	}
	if err := run("plan", `
		INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		VALUES ($1, $2, $3, 'E2E Hall Plan', 'assigned_seats', 'active')
		ON CONFLICT (id) DO NOTHING`, seatsPlanID, venueID, orgID); err != nil {
		return err
	}
	if err := run("plan version", `
		INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, locked_at)
		VALUES ($1, $2, 1, $3::jsonb, $4, $5, now())
		ON CONFLICT (id) DO NOTHING`, seatsVersionID, seatsPlanID, string(geoJSON), checksum, seatCount); err != nil {
		return err
	}
	start := time.Now().UTC().AddDate(0, 0, 45).Truncate(time.Hour)
	if err := run("session", `
		INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, seating_plan_version_id, currency, currency_source)
		VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', $5, 'scheduled', 'assigned_seats', $6, 'EUR', 'override')
		ON CONFLICT (id) DO NOTHING`, seatsSessionID, seatsEventID, venueID, start, seatCount, seatsVersionID); err != nil {
		return err
	}
	// Everything an earlier run changed, children first. A staff invitation issued by the
	// invitations twin (invitations-seats.spec.ts) leaves a reservation, a checkout session,
	// an order, its tickets and worker jobs on these places, and two foreign keys point at
	// session_seats without a cascade (reservation_seats and order_items), so those rows
	// must go first. Only rows of THIS session are touched.
	const (
		sessionReservations = `SELECT id FROM reservations WHERE session_id = $1`
		sessionCheckouts    = `SELECT id FROM checkout_sessions WHERE reservation_id IN (` + sessionReservations + `)`
		sessionOrders       = `SELECT id FROM orders WHERE session_id = $1`
		sessionTickets      = `SELECT id FROM tickets WHERE session_id = $1`
	)
	for _, step := range []struct{ what, sql string }{
		{"jobs of checkouts", `DELETE FROM worker_jobs WHERE payload->>'checkout_session_id' IN (SELECT id::text FROM (` + sessionCheckouts + `) c)`},
		{"jobs of reservations", `DELETE FROM worker_jobs WHERE payload->>'reservation_id' IN (SELECT id::text FROM (` + sessionReservations + `) r)`},
		{"jobs of tickets", `DELETE FROM worker_jobs WHERE payload->>'ticket_id' IN (SELECT id::text FROM (` + sessionTickets + `) t)`},
		// order_items.ticket_id and tickets.order_id point at each other, as do tickets.refund_id and refunds.ticket_id.
		{"item tickets", `UPDATE order_items SET ticket_id = NULL WHERE order_id IN (` + sessionOrders + `)`},
		{"ticket refunds", `UPDATE tickets SET refund_id = NULL WHERE session_id = $1`},
		{"refunds", `DELETE FROM refunds WHERE order_id IN (` + sessionOrders + `)`},
		{"barcodes", `DELETE FROM barcodes WHERE ticket_id IN (` + sessionTickets + `)`},
		{"scans", `DELETE FROM scan_events WHERE ticket_id IN (` + sessionTickets + `)`},
		{"tickets", `DELETE FROM tickets WHERE session_id = $1`},
		{"payments", `DELETE FROM payment_intents WHERE checkout_session_id IN (` + sessionCheckouts + `)`},
		{"orders", `DELETE FROM orders WHERE session_id = $1`},
		// checkout_consents are never deleted (dispute evidence, retention guard in
		// tests/staticanalysis). Staff invitations write none, and the seats
		// fixture is never bought through the public checkout.
		{"checkout sessions", `DELETE FROM checkout_sessions WHERE reservation_id IN (` + sessionReservations + `)`},
		{"held places", `DELETE FROM reservation_seats WHERE reservation_id IN (` + sessionReservations + `)`},
		{"batches", `DELETE FROM session_seat_batches WHERE session_id = $1`},
		{"places", `DELETE FROM session_seats WHERE session_id = $1`},
		{"reservations", `DELETE FROM reservations WHERE session_id = $1`},
		{"ledger", `DELETE FROM inventory_ledger WHERE session_id = $1`},
		{"categories", `DELETE FROM ticket_tiers WHERE session_id = $1`},
	} {
		if err := run(step.what, step.sql, seatsSessionID); err != nil {
			return err
		}
	}
	if err := run("ledger row", `INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, $2)`, seatsSessionID, seatCount); err != nil {
		return err
	}
	byIndex := map[int]uuid.UUID{}
	for i, t := range seatsTiers() {
		if err := run("category "+t.Name, `
			INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order, is_open)
			VALUES ($1, $2, $3, 'fixed', $4, 'EUR', $5, true)`, t.ID, seatsSessionID, t.Name, t.Price, i); err != nil {
			return err
		}
		id, _ := uuid.Parse(t.ID)
		byIndex[t.Index] = id
	}
	sid, _ := uuid.Parse(seatsSessionID)
	if _, err := seatmint.Materialize(ctx, gen.New(tx), sid, geo, byIndex, false, seatmint.Strict); err != nil {
		return fmt.Errorf("seats fixture materialize: %w", err)
	}
	// Three sold seats in Parter row 1, two in a hold in row 2.
	if err := run("sold", `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = ANY($2::text[])`,
		seatsSessionID, []string{"P|1|1", "P|1|2", "P|1|3"}); err != nil {
		return err
	}
	return run("held", `UPDATE session_seats SET status = 'held' WHERE session_id = $1 AND seat_key = ANY($2::text[])`,
		seatsSessionID, []string{"P|2|1", "P|2|2"})
}

type seatsInspectSeat struct {
	Tier   string `json:"tier"`
	Status string `json:"status"`
}

// runSeatsOp serves --seats-op. It prints one JSON line.
func runSeatsOp(op, seatKey string) error {
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	switch op {
	case "reset":
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applySeatsFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printCancelOrderJSON(map[string]string{"ok": "reset"})
	case "sell":
		if seatKey == "" {
			return fmt.Errorf("--seats-op sell needs --seats-key")
		}
		tag, err := pool.Exec(ctx, `UPDATE session_seats SET status = 'sold' WHERE session_id = $1 AND seat_key = $2 AND status = 'available'`, seatsSessionID, seatKey)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("seat %q is not an available seat of the date", seatKey)
		}
		return printCancelOrderJSON(map[string]string{"ok": "sold", "seat": seatKey})
	case "inspect":
		rows, err := pool.Query(ctx, `
			SELECT ss.seat_key, COALESCE(tt.name, ''), ss.status
			  FROM session_seats ss LEFT JOIN ticket_tiers tt ON tt.id = ss.tier_id
			 WHERE ss.session_id = $1 AND ss.kind = 'seat'`, seatsSessionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		seats := map[string]seatsInspectSeat{}
		for rows.Next() {
			var key string
			var s seatsInspectSeat
			if err := rows.Scan(&key, &s.Tier, &s.Status); err != nil {
				return err
			}
			seats[key] = s
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var batches int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_seat_batches WHERE session_id = $1`, seatsSessionID).Scan(&batches); err != nil {
			return err
		}
		out, err := json.Marshal(map[string]any{"seats": seats, "batches": batches})
		if err != nil {
			return err
		}
		return printCancelOrderJSON(json.RawMessage(out))
	default:
		return fmt.Errorf("unknown --seats-op %q (reset, inspect, sell)", op)
	}
}
