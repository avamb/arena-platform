//go:build integration

// staff_invitations_inv01_integration_test.go — live-DB coverage for task
// INV-01 of spec 46 (staff invitations with seats on the plan):
//
//   - a cancelled ticket returns its place to where the invitation took it
//     from (order_items.seat_origin): 'unavailable' back to the reserve,
//     'available' and a purchase (NULL) back on sale — for an assigned seat,
//     for a line that names the seat instead of the ticket, and for a GA unit;
//   - EnsureInternalChannel creates ONE "Invitations" channel per
//     organization, also under concurrency;
//   - that channel is invisible to the organizer: absent from the channel
//     list, 404 on the channel routes, refused as an API-key channel and as a
//     feed-token channel, never bindable, never resolved by the gateway.
//
// Run against a migrated scratch database:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ -run TestINV01
package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/invitations"
)

func inv01Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping INV-01 integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// inv01Order writes one invitation order over the fixture's checkout and
// returns a cleanup that removes it (and the tickets that point at it)
// before the fixture's own cleanup runs.
func inv01Order(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f *ab49Fixture) (uuid.UUID, func()) {
	t.Helper()
	orderID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                    source, status, currency, subtotal, discount, charge, total, buyer_name, issued_via)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'complimentary', 'paid', 'EUR', 0, 0, 0, 0, 'Guest', 'staff')`,
		orderID, f.orgID, f.channelID, f.eventID, f.sessionID, f.checkoutID, f.resID); err != nil {
		t.Fatalf("insert order: %v", err)
	}
	return orderID, func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM order_items WHERE order_id = $1`,
			`DELETE FROM tickets WHERE order_id = $1`,
			`DELETE FROM orders WHERE id = $1`,
		} {
			if _, err := pool.Exec(c, sql, orderID); err != nil {
				t.Logf("INV-01 cleanup: %v", err)
			}
		}
	}
}

// inv01Item adds one order line. ticketID nil + seatID set is the shape a
// line has when it names the seat rather than the ticket.
func inv01Item(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orderID uuid.UUID, ordinal int, f *ab49Fixture, ticketID, seatID *uuid.UUID, origin *string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO order_items (id, order_id, ordinal, kind, tier_id, ticket_id, session_seat_id,
		                         unit_price, discount, charge, total, seat_origin)
		VALUES ($1, $2, $3, 'ticket', $4, $5, $6, 2500, 2500, 0, 0, $7)`,
		uuid.New(), orderID, ordinal, f.tierID, ticketID, seatID, origin); err != nil {
		t.Fatalf("insert order item %d: %v", ordinal, err)
	}
}

// inv01CancelAndRelease cancels a ticket and runs the shared release core
// in its own transaction, as every cancellation path does.
func inv01CancelAndRelease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, ticketID uuid.UUID) htickets.TicketReleaseOutcome {
	t.Helper()
	cancelled, err := q.CancelTicket(ctx, ticketID, "inv-01 test", "none")
	if err != nil {
		t.Fatalf("CancelTicket: %v", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	out, err := htickets.ReleaseCancelledTicketInventoryTx(ctx, q.WithTx(tx), cancelled)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ReleaseCancelledTicketInventoryTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return out
}

func inv01SeatStatus(t *testing.T, ctx context.Context, q *gen.Queries, sessionID uuid.UUID, key string) gen.SessionSeatRow {
	t.Helper()
	seat, err := q.GetSessionSeatByKey(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("GetSessionSeatByKey(%s): %v", key, err)
	}
	return seat
}

func TestINV01_CancelledSeatReturnsToItsOrigin(t *testing.T) {
	pool := inv01Pool(t)
	ctx := context.Background()
	f := newAB49Fixture(t, ctx, pool, "assigned_seats")
	defer f.cleanup()
	q := gen.New(pool)

	keys := []string{"A|1|1", "A|1|2", "A|1|3", "A|1|4"}
	tierRef := f.tierID.String()
	if _, err := q.InsertSessionSeats(ctx, f.sessionID, keys,
		[]string{"A", "A", "A", "A"}, []string{"1", "1", "1", "1"}, []string{"1", "2", "3", "4"},
		[]*string{&tierRef, &tierRef, &tierRef, &tierRef}, nil, ""); err != nil {
		t.Fatalf("InsertSessionSeats: %v", err)
	}
	orderID, dropOrder := inv01Order(t, ctx, pool, f)
	defer dropOrder()

	unavailable, available := "unavailable", "available"
	// Every line of a staff invitation carries its origin: a NULL origin is a
	// purchase (seat_origin_test.go) and an error for an invitation
	// (TestINV02Review_MissingSeatOriginAbortsTheCancellation).
	origins := []*string{&unavailable, &available, &available, &unavailable}
	want := []string{"unavailable", "available", "available", "unavailable"}
	tickets := make([]uuid.UUID, len(keys))
	for i, key := range keys {
		seat := sellSeat(t, ctx, q, f, key)
		k := key
		tk, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil, &k, ptr("A"), ptr("1"), ptr(key[len(key)-1:]), int32(i))
		if err != nil {
			t.Fatalf("InsertTicket %s: %v", key, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE tickets SET order_id = $2 WHERE id = $1`, tk.ID, orderID); err != nil {
			t.Fatalf("link ticket: %v", err)
		}
		tickets[i] = tk.ID
		seatID := seat.ID
		if i == 3 {
			// The line names the seat, not the ticket.
			inv01Item(t, ctx, pool, orderID, i+1, f, nil, &seatID, origins[i])
		} else {
			inv01Item(t, ctx, pool, orderID, i+1, f, &tk.ID, &seatID, origins[i])
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE inventory_ledger SET capacity_sold = 4
		WHERE session_id = $1 AND tier_id IS NULL`, f.sessionID); err != nil {
		t.Fatalf("ledger seed: %v", err)
	}

	for i, key := range keys {
		out := inv01CancelAndRelease(t, ctx, pool, q, tickets[i])
		if !out.SeatReleased {
			t.Fatalf("%s: outcome = %+v, want SeatReleased", key, out)
		}
		seat := inv01SeatStatus(t, ctx, q, f.sessionID, key)
		if seat.Status != want[i] || seat.ReservationID != nil {
			t.Fatalf("%s: status = %s reservation = %v, want %s and no reservation", key, seat.Status, seat.ReservationID, want[i])
		}
	}

	// A seat back in the reserve is not on sale: the public hold (which
	// only takes 'available') must not see it.
	v, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("version bump: %v", err)
	}
	reserved := inv01SeatStatus(t, ctx, q, f.sessionID, "A|1|1")
	if _, err := q.HoldSessionSeat(ctx, reserved.ID, f.resID, v); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("hold on a seat back in the reserve: err = %v, want pgx.ErrNoRows", err)
	}
}

func TestINV01_CancelledGAUnitReturnsToItsOrigin(t *testing.T) {
	pool := inv01Pool(t)
	ctx := context.Background()
	f := newAB49Fixture(t, ctx, pool, "general_admission")
	defer f.cleanup()
	q := gen.New(pool)

	if _, err := q.InsertGAUnits(ctx, f.sessionID, "ga|t1", 0, &f.tierID, 2); err != nil {
		t.Fatalf("InsertGAUnits: %v", err)
	}
	v, err := q.IncrementSessionSeatStatusVersion(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("version bump: %v", err)
	}
	held, err := q.AllocateGAUnitsForHold(ctx, f.sessionID, f.resID, f.tierID, v, 2)
	if err != nil || len(held) != 2 {
		t.Fatalf("AllocateGAUnitsForHold: %d, %v", len(held), err)
	}
	for _, u := range held {
		if v, err = q.IncrementSessionSeatStatusVersion(ctx, f.sessionID); err != nil {
			t.Fatalf("version bump: %v", err)
		}
		if _, err := q.SellSessionSeat(ctx, u.ID, f.resID, v); err != nil {
			t.Fatalf("SellSessionSeat: %v", err)
		}
	}
	orderID, dropOrder := inv01Order(t, ctx, pool, f)
	defer dropOrder()

	unavailable, available := "unavailable", "available"
	origins := []*string{&unavailable, &available}
	want := []string{"unavailable", "available"}
	for i, u := range held {
		key := u.SeatKey
		tk, err := q.InsertTicket(ctx, f.checkoutID, f.sessionID, &f.tierID, nil, &key, nil, nil, nil, int32(i))
		if err != nil {
			t.Fatalf("InsertTicket: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE tickets SET order_id = $2 WHERE id = $1`, tk.ID, orderID); err != nil {
			t.Fatalf("link ticket: %v", err)
		}
		seatID := u.ID
		inv01Item(t, ctx, pool, orderID, i+1, f, &tk.ID, &seatID, origins[i])
		out := inv01CancelAndRelease(t, ctx, pool, q, tk.ID)
		if !out.GAUnitReleased {
			t.Fatalf("%s: outcome = %+v, want GAUnitReleased", key, out)
		}
		got := inv01SeatStatus(t, ctx, q, f.sessionID, key)
		if got.Status != want[i] || got.TierID == nil || *got.TierID != f.tierID {
			t.Fatalf("%s: status = %s tier = %v, want %s in its own category", key, got.Status, got.TierID, want[i])
		}
	}
}

// inv01Org is an organization with an owner token, one ordinary channel
// and its internal channel.
type inv01Org struct {
	org, sales, internal uuid.UUID
	owner                string
}

func newINV01Org(t *testing.T, ctx context.Context, pool *pgxpool.Pool, secret string) *inv01Org {
	t.Helper()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "INV01 Org "+suffix, "inv01-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	o := &inv01Org{org: org.ID}
	t.Cleanup(func() {
		c := context.Background()
		for _, sql := range []string{
			`DELETE FROM agent_feed_tokens WHERE sales_channel_id IN (SELECT id FROM sales_channels WHERE org_id = $1)`,
			`DELETE FROM api_keys WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM memberships WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(c, sql, o.org); err != nil {
				t.Logf("INV-01 org cleanup: %v", err)
			}
		}
	})
	ch, err := q.InsertSalesChannel(ctx, o.org, "INV01 Shop", "direct_merchant", "stripe", nil, "0", nil, nil)
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}
	o.sales = ch.ID
	if o.internal, err = invitations.EnsureInternalChannel(ctx, q, o.org); err != nil {
		t.Fatalf("EnsureInternalChannel: %v", err)
	}
	u, err := q.InsertUser(ctx, "inv01-owner-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, u.ID, o.org, "org_admin"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	if o.owner, _, err = auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour); err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return o
}

func TestINV01_EnsureInternalChannel_OneChannelEvenUnderConcurrency(t *testing.T) {
	pool := inv01Pool(t)
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "INV01 Race "+suffix, "inv01-race-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})

	const n = 12
	ids := make([]uuid.UUID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errs[i] = err
				return
			}
			ids[i], errs[i] = invitations.EnsureInternalChannel(ctx, q.WithTx(tx), org.ID)
			if errs[i] != nil {
				_ = tx.Rollback(ctx)
				return
			}
			errs[i] = tx.Commit(ctx)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("call %d got %s, call 0 got %s", i, ids[i], ids[0])
		}
	}
	again, err := invitations.EnsureInternalChannel(ctx, q, org.ID)
	if err != nil || again != ids[0] {
		t.Fatalf("second call: %s, %v; want %s", again, err, ids[0])
	}
	var count int
	var name, kind, settings string
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER (), name, kind, settings::text
		FROM sales_channels WHERE org_id = $1`, org.ID).Scan(&count, &name, &kind, &settings); err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if count != 1 || name != invitations.InternalChannelName || kind != "internal" || settings != "{}" {
		t.Fatalf("channels = %d, name %q kind %q settings %s", count, name, kind, settings)
	}
}

func TestINV01_InternalChannelIsInvisibleToTheOrganizer(t *testing.T) {
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	ctx := context.Background()
	q := gen.New(pool)
	o := newINV01Org(t, ctx, pool, secret)
	base := ts.URL + "/v1/organizations/" + o.org.String()

	do := func(method, url, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+o.owner)
		// The API-key routes ask every caller for a reason (superadmin-audit
		// convention); the header is inert on the other routes.
		req.Header.Set("X-Admin-Reason", "inv-01 integration test")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		return resp.StatusCode, integReadBody(t, resp)
	}

	// The channel list shows the ordinary channel only.
	code, body := do(http.MethodGet, base+"/channels", "")
	if code != http.StatusOK {
		t.Fatalf("GET channels: %d %s", code, body)
	}
	if !strings.Contains(body, o.sales.String()) || strings.Contains(body, o.internal.String()) {
		t.Fatalf("channel list must name the sales channel and not the internal one: %s", body)
	}

	// Every channel route answers 404 for the internal channel.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/channels/" + o.internal.String(), ""},
		{http.MethodPatch, "/channels/" + o.internal.String(), `{"name":"Mine now"}`},
		{http.MethodDelete, "/channels/" + o.internal.String(), ""},
	} {
		if code, body := do(c.method, base+c.path, c.body); code != http.StatusNotFound {
			t.Fatalf("%s %s: %d %s; want 404", c.method, c.path, code, body)
		}
	}

	// A feed token cannot be minted on it.
	if code, body := do(http.MethodPost, base+"/channels/"+o.internal.String()+"/feed-tokens", `{"label":"x"}`); code == http.StatusCreated || code/100 == 2 {
		t.Fatalf("feed token on the internal channel: %d %s", code, body)
	}

	// An API key cannot be bound to it; the ordinary channel still works.
	code, body = do(http.MethodPost, base+"/api-keys",
		`{"name":"inv01","scopes":["event.read"],"channel_id":"`+o.internal.String()+`"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "api_key.invalid_channel") {
		t.Fatalf("API key on the internal channel: %d %s; want 422 api_key.invalid_channel", code, body)
	}
	code, body = do(http.MethodPost, base+"/api-keys",
		`{"name":"inv01-ok","scopes":["event.read"],"channel_id":"`+o.sales.String()+`"}`)
	if code/100 != 2 {
		t.Fatalf("API key on the sales channel: %d %s", code, body)
	}

	// The internal channel is untouched and still the organization's.
	var name, kind string
	var deleted *time.Time
	var tokens int
	if err := pool.QueryRow(ctx, `
		SELECT name, kind, deleted_at,
		       (SELECT count(*) FROM agent_feed_tokens WHERE sales_channel_id = sc.id)
		FROM sales_channels sc WHERE id = $1`, o.internal).Scan(&name, &kind, &deleted, &tokens); err != nil {
		t.Fatalf("read internal channel: %v", err)
	}
	if name != invitations.InternalChannelName || kind != "internal" || deleted != nil || tokens != 0 {
		t.Fatalf("internal channel changed: name %q kind %q deleted %v tokens %d", name, kind, deleted, tokens)
	}

	// Query level: never bindable, never given a hosted page, never a gateway fid.
	bindable, err := q.ListBindableChannelsByOrg(ctx, o.org)
	if err != nil {
		t.Fatalf("ListBindableChannelsByOrg: %v", err)
	}
	for _, b := range bindable {
		if b.ID == o.internal {
			t.Fatalf("the internal channel is bindable")
		}
	}
	if changed, err := q.EnableChannelHostedPage(ctx, o.internal, o.org); err != nil || changed {
		t.Fatalf("EnableChannelHostedPage on the internal channel: %v, %v", changed, err)
	}
	var display int64
	if err := pool.QueryRow(ctx, `SELECT display_number FROM sales_channels WHERE id = $1`, o.internal).Scan(&display); err != nil {
		t.Fatalf("display number: %v", err)
	}
	if _, err := q.GetSalesChannelByDisplayNumber(ctx, display); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("gateway lookup of the internal channel: err = %v, want pgx.ErrNoRows", err)
	}
	// Order processing still resolves it.
	if ch, err := q.GetSalesChannelByIDAnyKind(ctx, o.internal, o.org); err != nil || ch.ID != o.internal {
		t.Fatalf("GetSalesChannelByIDAnyKind: %v", err)
	}
}
