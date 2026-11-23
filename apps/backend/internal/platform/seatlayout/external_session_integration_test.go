//go:build integration

// external_session_integration_test.go — spec 63 §14.2 (migration 0221): a
// batch on a session a partner box office sells is refused under the
// sessions row lock with ErrSessionSoldExternally and writes nothing, while
// another organization still gets ErrSessionNotFound.
//
// Requires DATABASE_URL against a migrated database.
package seatlayout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

func TestApplyBatch_ExternalSessionRefused_LiveDB(t *testing.T) {
	f := newLayoutFixture(t, 4)
	ctx := context.Background()
	// Straight to the column: the lock rules of the PATCH are not under test.
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET sales_mode = 'external',
		external_ticket_url = 'https://tickets.example.cz/x', external_seller_name = 'Partner'
		WHERE id = $1`, f.session); err != nil {
		t.Fatalf("make the session external: %v", err)
	}
	defer func() {
		if _, err := f.pool.Exec(ctx, `UPDATE sessions SET sales_mode = 'platform',
			external_ticket_url = NULL, external_seller_name = NULL WHERE id = $1`, f.session); err != nil {
			t.Logf("restore the session: %v", err)
		}
	}()
	before := f.batchCount()
	_, _, version := f.seat(seatKey(1))

	if _, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		OrgID: f.org, SessionID: f.session, Kind: seatlayout.KindClose,
		Changes: []seatlayout.Change{{SeatKey: seatKey(1), Status: strp("unavailable")}},
	}); !errors.Is(err, seatlayout.ErrSessionSoldExternally) {
		t.Fatalf("batch on an external session: %v, want ErrSessionSoldExternally", err)
	}
	if _, err := seatlayout.ApplyBatch(ctx, f.pool, f.q, seatlayout.BatchInput{
		OrgID: f.foreign, SessionID: f.session, Kind: seatlayout.KindClose,
		Changes: []seatlayout.Change{{SeatKey: seatKey(1), Status: strp("unavailable")}},
	}); !errors.Is(err, seatlayout.ErrSessionNotFound) {
		t.Fatalf("another organization: %v, want ErrSessionNotFound", err)
	}
	if n := f.batchCount(); n != before {
		t.Fatalf("batches = %d, want %d: the refusal wrote a batch", n, before)
	}
	if _, status, v := f.seat(seatKey(1)); status != "available" || v != version {
		t.Fatalf("seat 1 = %s (version %d), want available at version %d: the refusal wrote", status, v, version)
	}
}
