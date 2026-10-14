//go:build integration

// letter_retention_integration_test.go — live-DB proof of the retention rule
// (owner decision 2026-10-14): a letter tied to a session is kept from
// max(sent_at, the session's end), a letter tied to none from sent_at.
//
// Same fixture and DATABASE_URL pattern as order_letter_integration_test.go:
// point it at a FRESH scratch database, never at the shared dev stand.
package delivery

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

func TestLetterRetention_CountsFromTheEventDate(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()
	var letters []uuid.UUID
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM session_changes WHERE session_id = $1`, seed.SessionID)
		_, _ = pool.Exec(ctx, `DELETE FROM email_letters WHERE order_id = $1 OR id = ANY($2)`, seed.OrderID, letters)
	}()
	org := seedOrgID(ctx, t, pool, seed.OrderID)

	// The show is more than a year away: a ticket bought now can be disputed
	// long after 24 months from its letter would have run out.
	var sessionEnd time.Time
	if err := pool.QueryRow(ctx, `UPDATE sessions SET start_at = now() + interval '400 days',
		end_at = now() + interval '400 days 2 hours' WHERE id = $1 RETURNING end_at`, seed.SessionID).Scan(&sessionEnd); err != nil {
		t.Fatal(err)
	}

	begin := func(l emailstatus.NewLetter) uuid.UUID {
		t.Helper()
		l.OrgID, l.Recipient = org, seed.RecipientEmail
		id, err := emailstatus.Begin(ctx, pool, l)
		if err != nil {
			t.Fatal(err)
		}
		letters = append(letters, id)
		return id
	}
	start := func(id uuid.UUID) time.Time {
		t.Helper()
		at, err := emailstatus.RetentionStart(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	sentAt := func(id uuid.UUID) time.Time {
		t.Helper()
		var at time.Time
		if err := pool.QueryRow(ctx, `SELECT sent_at FROM email_letters WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	var number int64
	if err := pool.QueryRow(ctx, `SELECT system_ticket_id FROM tickets WHERE id = $1`, seed.TicketIDs[0]).Scan(&number); err != nil {
		t.Fatal(err)
	}

	// 1. A ticket letter of the order: from the session's end.
	ticket := begin(emailstatus.NewLetter{OrderID: &seed.OrderID, Kind: emailstatus.KindTicket})
	// 2. An invitation has no order: its ticket numbers find the session.
	invitation := begin(emailstatus.NewLetter{Kind: emailstatus.KindInvitation,
		TicketNumbers: []string{strconv.FormatInt(number, 10), "AB12CD34"}})
	// 3. A change letter found only through its notice.
	change := seedSessionChange(ctx, t, pool, seed, org)
	changeLetter := begin(emailstatus.NewLetter{Kind: emailstatus.KindChange})
	if _, err := pool.Exec(ctx, `UPDATE session_change_notices SET email_letter_id = $1 WHERE change_id = $2`,
		changeLetter, change); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"ticket": ticket, "invitation": invitation, "change": changeLetter} {
		if got := start(id); !got.Equal(sessionEnd) {
			t.Errorf("%s letter: retention starts %v, want the session's end %v", name, got, sessionEnd)
		}
	}

	// 4. A letter tied to no session counts from the moment it was sent.
	loose := begin(emailstatus.NewLetter{Kind: emailstatus.KindInvitation, TicketNumbers: []string{"AB12CD34"}})
	if got, want := start(loose), sentAt(loose); !got.Equal(want) {
		t.Errorf("letter without a session: retention starts %v, want sent_at %v", got, want)
	}

	// 5. A show that is already over: the letter is the later moment.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET start_at = now() - interval '30 days',
		end_at = now() - interval '30 days' + interval '2 hours' WHERE id = $1`, seed.SessionID); err != nil {
		t.Fatal(err)
	}
	if got, want := start(ticket), sentAt(ticket); !got.Equal(want) {
		t.Errorf("letter sent after the show: retention starts %v, want sent_at %v", got, want)
	}
}
