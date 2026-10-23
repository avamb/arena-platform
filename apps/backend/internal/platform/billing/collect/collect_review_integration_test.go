//go:build integration

// collect_review_integration_test.go — the findings of the independent review
// of task B2-2, one test each (same fixtures as collect_integration_test.go).
package collect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing/collect"
)

// failingStore refuses to write one subject, as a poison row would.
type failingStore struct {
	scopedStore
	bad  uuid.UUID
	fail bool
}

func (s *failingStore) InsertBillableEvent(ctx context.Context, p gen.InsertBillableEventParams) (uuid.UUID, bool, error) {
	if s.fail && p.SubjectID == s.bad {
		return uuid.Nil, false, errors.New("poison row")
	}
	return s.scopedStore.InsertBillableEvent(ctx, p)
}

func (f *fixture) cursor(name string) (time.Time, bool) {
	var at time.Time
	err := f.pool.QueryRow(context.Background(), `SELECT cursor_at FROM billing_job_cursors WHERE key = $1`, f.prefix+name).Scan(&at)
	return at, err == nil
}

// A refund that failed does not hide its cancelled ticket: the cancellation is
// then a fact of its own. tickets.refund_id points at refunds in any state and
// is linked best effort, so it is not the rule; a succeeded refund that
// cancels its ticket hides the cancellation even when refund_id was never set.
func TestCollect_FailedRefundDoesNotHideTheCancellation(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	orderID, _, tickets := f.paidOrder("public_feed", "EUR", now.Add(-5*time.Hour), line{1500, false}, line{1500, false})
	failed := uuid.New()
	f.must(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, cancel_ticket)
	        VALUES ($1, $2, 1500, 'EUR', 'failed', 'external', $3, $4, true)`, failed, f.orgID, orderID, tickets[0])
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2, refund_id = $3 WHERE id = $1`, tickets[0], now.Add(-4*time.Hour), failed)
	ok := uuid.New()
	f.must(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, cancel_ticket, succeeded_at)
	        VALUES ($1, $2, 1500, 'EUR', 'succeeded', 'external', $3, $4, true, $5)`, ok, f.orgID, orderID, tickets[1], now.Add(-3*time.Hour))
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, tickets[1], now.Add(-3*time.Hour))

	pass(t, f.collector(now.Add(-48*time.Hour)))
	cancelled := f.events("ticket_cancelled")
	if len(cancelled) != 1 || cancelled[0].SubjectID != tickets[0] {
		t.Errorf("ticket_cancelled = %+v, want only the ticket whose refund failed", cancelled)
	}
	refunded := f.events("ticket_refunded")
	if len(refunded) != 1 || refunded[0].SubjectID != ok {
		t.Errorf("ticket_refunded = %+v, want only the succeeded refund", refunded)
	}
}

// Orders refunded later in part or in full are still sales, and the date X
// bounds every other source too.
func TestCollect_RefundedOrdersAndTheDateX(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	since := now.Add(-10 * time.Hour)
	partly, _, _ := f.paidOrder("public_feed", "EUR", now.Add(-3*time.Hour), line{1500, false})
	fully, _, _ := f.paidOrder("public_feed", "EUR", now.Add(-2*time.Hour), line{1500, false})
	f.must(`UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, partly)
	f.must(`UPDATE orders SET status = 'refunded' WHERE id = $1`, fully)

	// Everything below happened BEFORE X.
	orderID, _, tickets := f.paidOrder("public_feed", "EUR", since.Add(-5*time.Hour), line{1500, false})
	f.must(`INSERT INTO refunds (org_id, amount, currency, state, settlement, order_id, ticket_id, cancel_ticket, succeeded_at)
	        VALUES ($1, 1500, 'EUR', 'succeeded', 'external', $2, $3, false, $4)`, f.orgID, orderID, tickets[0], since.Add(-time.Hour))
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, tickets[0], since.Add(-time.Hour))
	issuanceID := uuid.New()
	f.must(`INSERT INTO complimentary_issuances (id, org_id, session_id, qty, batch_id) VALUES ($1, $2, $3, 1, 'collect-x')`, issuanceID, f.orgID, f.sessionID)
	f.must(`INSERT INTO tickets (id, session_id, status, complimentary_issuance_id, issued_at) VALUES ($1, $2, 'active', $3, $4)`,
		uuid.New(), f.sessionID, issuanceID, since.Add(-time.Hour))
	f.must(`INSERT INTO session_changes (session_id, event_id, org_id, kinds, old_state, new_state, created_at)
	        VALUES ($1, $2, $3, ARRAY['cancelled'], '{}', '{}', $4)`, f.sessionID, f.eventID, f.orgID, since.Add(-time.Hour))
	tokenID := uuid.New()
	f.must(`INSERT INTO agent_feed_tokens (id, token, sales_channel_id) VALUES ($1, $2, $3)`, tokenID, "collect-"+tokenID.String(), f.channelID)
	f.must(`INSERT INTO event_publications (event_id, feed_token_id, published_at) VALUES ($1, $2, $3)`, f.eventID, tokenID, since.Add(-time.Hour))

	pass(t, f.collector(since))
	if got := len(f.events("ticket_sold")); got != 2 {
		t.Errorf("ticket_sold = %d, want the partly and the fully refunded order only", got)
	}
	for _, kind := range []string{"ticket_refunded", "ticket_cancelled", "ticket_complimentary", "session_cancelled", "event_published"} {
		if got := f.events(kind); len(got) != 0 {
			t.Errorf("%s before the date X was collected: %+v", kind, got)
		}
	}
}

// A refund's time is the one it succeeded at; settling it later (the repair
// pass stamps settled_at) never moves it.
func TestCollect_RefundTimeDoesNotDependOnSettledAt(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC().Truncate(time.Second)
	orderID, _, tickets := f.paidOrder("public_feed", "EUR", now.Add(-5*time.Hour), line{1500, false})
	succeeded := now.Add(-4 * time.Hour)
	id := uuid.New()
	f.must(`INSERT INTO refunds (id, org_id, amount, currency, state, settlement, order_id, ticket_id, cancel_ticket, succeeded_at, settled_at)
	        VALUES ($1, $2, 1500, 'EUR', 'succeeded', 'external', $3, $4, false, $5, $6)`, id, f.orgID, orderID, tickets[0], succeeded, now.Add(-time.Hour))
	pass(t, f.collector(now.Add(-48*time.Hour)))
	got := f.events("ticket_refunded")
	if len(got) != 1 || !got[0].OccurredAt.Equal(succeeded) {
		t.Fatalf("ticket_refunded = %+v, want occurred_at %s", got, succeeded)
	}
}

// A row that cannot be written stops its own source and leaves its cursor
// before it; the other sources still run; the next pass finishes the job.
func TestCollect_PoisonRowIsIsolatedAndResumed(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC()
	since := now.Add(-48 * time.Hour)
	_, items, tickets := f.paidOrder("public_feed", "EUR", now.Add(-5*time.Hour), line{1500, false}, line{1500, false})
	f.must(`UPDATE tickets SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, tickets[0], now.Add(-4*time.Hour))

	// Both lines are paid at the same instant, so they are read in subject id
	// order: the poison row is the later one.
	bad := items[1]
	if items[0].String() > items[1].String() {
		bad = items[0]
	}
	store := &failingStore{scopedStore: scopedStore{gen.New(f.pool), f.orgID}, bad: bad, fail: true}
	c := collect.New(store, collect.Options{Since: since, CursorPrefix: f.prefix})
	if _, err := c.Run(context.Background()); err == nil {
		t.Fatal("a pass with a poison row must report it")
	}
	if got := f.events("ticket_cancelled"); len(got) != 1 {
		t.Errorf("the cancellation source was blocked by the poison sale: %+v", got)
	}
	if got := f.events("ticket_sold"); len(got) != 1 {
		t.Errorf("ticket_sold = %d, want the row before the poison one", len(got))
	}
	if at, ok := f.cursor("ticket_sold"); ok && at.After(now.Add(-5*time.Hour)) {
		t.Errorf("the failing source's cursor moved to %s, past the poison row", at)
	}

	store.fail = false
	if n := pass(t, c); n != 1 {
		t.Fatalf("the pass after the repair inserted %d, want the poison row only", n)
	}
	if got := f.events("ticket_sold"); len(got) != 2 {
		t.Fatalf("ticket_sold = %d, want 2", len(got))
	}
}

// The date X may not precede the first account terms: such events could not
// be classified, so the pass refuses to run.
func TestCollect_RefusesADateBeforeTheFirstTerms(t *testing.T) {
	f := newFixture(t, "CZ")
	c := f.collector(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC))
	if _, err := c.Run(context.Background()); err == nil {
		t.Fatal("a date X before 2026-01-01 must be refused")
	}
}

// The SQL itself - the real LIMIT and the keyset - walks every row once, also
// when several rows share one instant.
func TestCollect_RealPagingWalksEveryRowOnce(t *testing.T) {
	f := newFixture(t, "CZ")
	now := time.Now().UTC().Truncate(time.Second)
	same := now.Add(-20 * time.Minute)
	_, a, _ := f.paidOrder("public_feed", "EUR", same, line{1500, false}, line{1500, false}, line{1500, false})
	_, b, _ := f.paidOrder("public_feed", "EUR", same, line{1500, false}, line{1500, false})
	_, c2, _ := f.paidOrder("public_feed", "EUR", now.Add(-5*time.Minute), line{1500, false})
	want := map[uuid.UUID]bool{}
	for _, id := range append(append(a, b...), c2...) {
		want[id] = true
	}
	q := gen.New(f.pool)
	afterAt, afterID := now.Add(-time.Hour), uuid.Nil
	seen := map[uuid.UUID]int{}
	for guard := 0; guard < 10000; guard++ {
		page, err := q.ListCollectOrderItems(context.Background(), afterAt, afterID, 2, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			if r.OrgID == f.orgID {
				seen[r.SubjectID]++
			}
			afterAt, afterID = r.OccurredAt, r.SubjectID
		}
		if len(page) < 2 {
			break
		}
	}
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("item %s seen %d times, want once", id, seen[id])
		}
	}
	if len(seen) != len(want) {
		t.Errorf("walk saw %d items of the fixture, want %d", len(seen), len(want))
	}
}
