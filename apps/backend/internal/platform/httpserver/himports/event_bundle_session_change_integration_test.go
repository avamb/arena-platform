//go:build integration

package himports

// An event center that re-saves a session with a new day moves it. When the
// session already has a paid, live ticket the move must tell the buyer: the
// import journals the change and queues the letter in its own transaction,
// and refuses (writing nothing) when the organizer has no contact e-mail for
// the buyer to answer. The organizer's text rides on `changeMessage`.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// seedPaidTicket gives the session a paid order with one live ticket, and
// returns its cleanup (to be deferred AFTER fixture.cleanup so it runs first).
func (f *bundle525Fixture) seedPaidTicket(ctx context.Context, t *testing.T, sessionID, eventID uuid.UUID) func() {
	t.Helper()
	chanID, resID, csID, orderID, ticketID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	email := "buyer-" + uuid.NewString()[:8] + "@example.com"
	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, []any{chanID, f.orgID, "SC channel " + chanID.String()[:8]}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, []any{resID, f.orgID, chanID, sessionID}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`,
			[]any{csID, f.orgID, chanID, resID}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, buyer_email)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, $8)`,
			[]any{orderID, f.orgID, chanID, eventID, sessionID, csID, resID, email}},
		{`INSERT INTO tickets (id, checkout_session_id, session_id, holder_email, order_id) VALUES ($1, $2, $3, $4, $5)`,
			[]any{ticketID, csID, sessionID, email, orderID}},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("seedPaidTicket step %d: %v", i, err)
		}
	}
	return func() {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM worker_jobs WHERE job_type = 'session.change_email' AND payload->>'order_id' = $1`, []any{orderID.String()}},
			{`DELETE FROM session_changes WHERE session_id = $1`, []any{sessionID}},
			{`DELETE FROM tickets WHERE id = $1`, []any{ticketID}},
			{`DELETE FROM orders WHERE id = $1`, []any{orderID}},
			{`DELETE FROM checkout_sessions WHERE id = $1`, []any{csID}},
			{`DELETE FROM reservations WHERE id = $1`, []any{resID}},
			{`DELETE FROM sales_channels WHERE id = $1`, []any{chanID}},
		} {
			if _, err := f.pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				t.Logf("seedPaidTicket cleanup: %s: %v", stmt.sql, err)
			}
		}
	}
}

func TestEventBundle_MovingASessionWithBuyersNeedsAContactAndQueuesTheLetter(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	defer f.seedPaidTicket(ctx, t, created.SessionID, created.EventID)()

	move := f.payload()
	move.ActionEvent.Day = "03.05.2026" // a week later than the 26.04 original
	move.ChangeMessage = "Moved by a week, sorry."

	startOf := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT start_at::text FROM sessions WHERE id = $1`, created.SessionID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := startOf()

	// No organizer e-mail yet: refused, nothing written.
	rec, _ = f.call(h, move)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "organization.contact_missing") {
		t.Fatalf("move without a contact: %d %s, want 422 organization.contact_missing", rec.Code, rec.Body.String())
	}
	if startOf() != before {
		t.Fatal("the session moved although the import was refused")
	}

	// With a contact the same bundle moves the session and queues one letter.
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'org-sc@example.com' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	rec, moved := f.call(h, move)
	if rec.Code != http.StatusOK || moved.SessionID != created.SessionID {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	if startOf() == before {
		t.Fatal("the session did not move")
	}
	var kinds []string
	var message string
	var orders int
	if err := pool.QueryRow(ctx, `SELECT kinds, message, orders_total FROM session_changes WHERE session_id = $1`, created.SessionID).
		Scan(&kinds, &message, &orders); err != nil {
		t.Fatalf("journal row: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "date" || message != "Moved by a week, sorry." || orders != 1 {
		t.Fatalf("journal = kinds %v message %q orders %d", kinds, message, orders)
	}
	var jobs int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = 'session.change_email'
	                         AND payload->>'change_id' IN (SELECT id::text FROM session_changes WHERE session_id = $1)`,
		created.SessionID).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("queued letters = %d, want 1", jobs)
	}

	// Saving the very same bundle again changes nothing a buyer sees: no new
	// journal row, no second letter.
	rec, _ = f.call(h, move)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-save: %d %s", rec.Code, rec.Body.String())
	}
	var changes int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, created.SessionID).Scan(&changes)
	if changes != 1 {
		t.Fatalf("a re-save wrote %d journal rows, want still 1", changes)
	}

	// An over-long message is refused before anything is written.
	long := f.payload()
	long.ActionEvent.Day = "10.05.2026"
	long.ChangeMessage = strings.Repeat("x", 1001)
	rec, _ = f.call(h, long)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "session.change_message_too_long") {
		t.Fatalf("long message: %d %s", rec.Code, rec.Body.String())
	}
}
