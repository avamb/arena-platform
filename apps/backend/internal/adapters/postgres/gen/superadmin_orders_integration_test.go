//go:build integration

package gen_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestListAllOrders_SearchAndNames_LiveDB covers the superadmin orders console
// query (functional run 2026-09-19, F-35): support is asked about an order by
// the number the site shows, the buyer's email or phone, and needs the
// organization and event by name, not by UUID.
func TestListAllOrders_SearchAndNames_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgID, venueID, eventID, sessionID, channelID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	suffix := orgID.String()[:8]
	cleanup := func() {
		for _, s := range []string{
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), s, orgID); err != nil {
				t.Logf("cleanup (%s): %v", s, err)
			}
		}
	}
	defer cleanup()

	for i, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{orgID, "Search Org " + suffix, "search-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{venueID, orgID, "Search Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{eventID, orgID, "Search Event " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
		    capacity_total, status, admission_mode, currency, currency_source)
		  VALUES ($1, $2, $3, now() + interval '20 days', now() + interval '20 days 2 hours',
		    100, 'scheduled', 'general_admission', 'EUR', 'override')`,
			[]any{sessionID, eventID, venueID}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{channelID, orgID, "Search Channel " + suffix}},
	} {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture step %d: %v", i, err)
		}
	}

	q := gen.New(pool)
	// Buyer identities feed no unique index here, but stay per-run anyway so a
	// leftover row of an interrupted run can never answer a search.
	seed := func(email, name, phone, extRef string) int64 {
		t.Helper()
		res, err := q.InsertReservation(ctx, orgID, channelID, sessionID, nil, nil, 1, time.Now().UTC().Add(time.Hour))
		if err != nil {
			t.Fatalf("seed reservation: %v", err)
		}
		checkoutID := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		    VALUES ($1, $2, $3, $4, 'pricing_confirmed')`, checkoutID, orgID, channelID, res.ID); err != nil {
			t.Fatalf("seed checkout session: %v", err)
		}
		var systemID int64
		if err := pool.QueryRow(ctx, `INSERT INTO orders (org_id, channel_id, event_id, session_id,
		    checkout_session_id, reservation_id, external_ref, source, status, currency,
		    subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'bil24_gateway', 'paid', 'EUR', 1000, 0, 0, 1000, $8, $9, $10)
		  RETURNING system_id`,
			orgID, channelID, eventID, sessionID, checkoutID, res.ID, extRef, name, email, phone,
		).Scan(&systemID); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		return systemID
	}
	emailA := "Anna." + suffix + "@example.test"
	phoneA := "+42060" + strconv.FormatInt(time.Now().UnixNano()%10_000_000, 10)
	sysA := seed(emailA, "Anna Novak", phoneA, "wc-"+suffix+"-1")
	sysB := seed("bob."+suffix+"@example.test", "Bob Smith", "+37120000000", "wc-"+suffix+"-2")

	list := func(search string) []gen.AdminOrderRow {
		t.Helper()
		var s *string
		if search != "" {
			s = &search
		}
		rows, err := q.ListAllOrders(ctx, &orgID, nil, s, 50, 0)
		if err != nil {
			t.Fatalf("ListAllOrders(%q): %v", search, err)
		}
		return rows
	}
	only := func(search string, want int64) {
		t.Helper()
		rows := list(search)
		if len(rows) != 1 || rows[0].SystemID != want {
			got := make([]int64, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.SystemID)
			}
			t.Errorf("search %q: got orders %v, want only %d", search, got, want)
		}
	}

	all := list("")
	if len(all) != 2 {
		t.Fatalf("no search: got %d orders, want 2", len(all))
	}
	for _, r := range all {
		if r.OrgName != "Search Org "+suffix || r.EventName != "Search Event "+suffix {
			t.Errorf("order %d: org_name=%q event_name=%q", r.SystemID, r.OrgName, r.EventName)
		}
	}

	only(strconv.FormatInt(sysA, 10), sysA)
	only(strconv.FormatInt(sysB, 10), sysB)
	only("wc-"+suffix+"-2", sysB)
	only("anna."+suffix, sysA) // case-insensitive part of the email
	only("NOVAK", sysA)
	only(phoneA, sysA)
	if rows := list("nobody-" + suffix); len(rows) != 0 {
		t.Errorf("unknown search: got %d orders, want 0", len(rows))
	}
	// A number that only appears inside another order number must not match it.
	if rows := list(strconv.FormatInt(sysA, 10)[1:]); len(rows) != 0 {
		t.Errorf("partial order number: got %d orders, want 0", len(rows))
	}
}
