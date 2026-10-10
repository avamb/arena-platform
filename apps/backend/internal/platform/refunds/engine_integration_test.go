//go:build integration

// engine_integration_test.go — the refund engine against a live database
// migrated to 0138, with a FAKE payment module (so every provider answer can
// be scripted) and the REAL ticket cancellation (htickets.CancelTicketTx).
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_pay02?sslmode=disable \
//	    go test -tags integration -run TestEngine ./apps/backend/internal/platform/refunds/
package refunds_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// ─── fake module ────────────────────────────────────────────────────────────

type fakeCall struct {
	key, chargeRef string
	amount         int64
	ticketActive   bool // the ticket's status when the provider was called
}

type fakeModule struct {
	mu       sync.Mutex
	partial  bool
	calls    []fakeCall
	answers  []func(req payments.RefundRequest) (payments.RefundResult, error)
	lookup   payments.RefundResult
	resolved int
	// beforeRefund runs inside Refund, before the answer.
	beforeRefund func(req payments.RefundRequest) bool
	// buildErrs scripts what fakeSource.Build answers, one entry per call
	// (nil = build the module); the last entry repeats.
	buildErrs []error
	// onBuild runs inside fakeSource.Build after buildErrs; a non-nil error
	// is returned as the build error.
	onBuild func() error
}

func (m *fakeModule) nextBuildErr() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buildErrs) == 0 {
		return nil
	}
	err := m.buildErrs[0]
	if len(m.buildErrs) > 1 {
		m.buildErrs = m.buildErrs[1:]
	}
	return err
}

func (m *fakeModule) Descriptor() payments.Descriptor {
	return payments.Descriptor{Name: "pay03fake", Title: "Fake", Capabilities: payments.Capabilities{
		Refund: true, PartialRefund: m.partial, AsyncRefund: true, RefundLookup: true}}
}

func (m *fakeModule) Refund(_ context.Context, req payments.RefundRequest) (payments.RefundResult, error) {
	active := true
	if m.beforeRefund != nil {
		active = m.beforeRefund(req)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, fakeCall{key: req.IdempotencyKey, chargeRef: req.ProviderChargeRef, amount: req.AmountMinor, ticketActive: active})
	if len(m.answers) == 0 {
		return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundSucceeded}, nil
	}
	a := m.answers[0]
	if len(m.answers) > 1 {
		m.answers = m.answers[1:]
	}
	return a(req)
}

func (m *fakeModule) RefundStatus(_ context.Context, id string) (payments.RefundResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.lookup
	r.ProviderRefundID = id
	return r, nil
}

func (m *fakeModule) ResolveChargeRef(_ context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolved++
	return "pi_from_" + id, nil
}

func (m *fakeModule) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

type fakeSource struct{ m *fakeModule }

func (s fakeSource) Descriptor(p string) (payments.Descriptor, bool) {
	if p == "pay03fake" {
		return s.m.Descriptor(), true
	}
	return payments.Descriptor{}, false
}

func (s fakeSource) Build(context.Context, uuid.UUID, string) (payments.Module, error) {
	if err := s.m.nextBuildErr(); err != nil {
		return nil, err
	}
	s.m.mu.Lock()
	hook := s.m.onBuild
	s.m.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			return nil, err
		}
	}
	return s.m, nil
}

func pending(req payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundPending}, nil
}

func declined(payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, &payments.RefundDeclinedError{Code: "card_closed", Message: "the card is closed"}
}

func timeout(payments.RefundRequest) (payments.RefundResult, error) {
	return payments.RefundResult{}, errors.New("context deadline exceeded")
}

// ─── fixture ────────────────────────────────────────────────────────────────

type fixture struct {
	pool                    *pgxpool.Pool
	org, order, cs, session uuid.UUID
	tier, payment           uuid.UUID
	tickets                 [2]uuid.UUID
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newFixture seeds one paid public order of two 2500-minor tickets, paid by
// a 5000 payment of provider, and a legacy GA ledger the cancellation can
// restore capacity on.
func newFixture(t *testing.T, pool *pgxpool.Pool, provider string) *fixture {
	t.Helper()
	ctx := context.Background()
	q := gen.New(pool)
	suffix := uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "PAY-03 "+suffix, "pay03-"+uuid.NewString(), "ES", "en", 1200)
	if err != nil {
		t.Fatalf("org: %v", err)
	}
	f := &fixture{pool: pool, org: org.ID, order: uuid.New(), cs: uuid.New(), session: uuid.New(), tier: uuid.New(),
		tickets: [2]uuid.UUID{uuid.New(), uuid.New()}}
	venue, event, channel, res := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Minute)
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, []any{venue, f.org, "V " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, []any{event, f.org, "E " + suffix}},
		{`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		  VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 100, 'scheduled', 'EUR', 'override')`, []any{f.session, event, venue, start}},
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, unit_seq, is_open)
		  VALUES ($1, $2, 'Stalls', 'fixed', 2500, 'EUR', 1, true)`, []any{f.tier, f.session}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold) VALUES ($1, $2, 100, 2)`, []any{f.session, f.tier}},
		{`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, $3, '{}'::jsonb)`, []any{channel, f.org, "C " + suffix}},
		{`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		  VALUES ($1, $2, $3, $4, 2, 'converted', now() + interval '1 hour', now())`, []any{res, f.org, channel, f.session}},
		{`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, []any{f.cs, f.org, channel, res}},
		{`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                      source, status, currency, subtotal, discount, charge, total, paid_at)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 0, 0, 5000, now())`,
			[]any{f.order, f.org, channel, event, f.session, f.cs, res}},
	}
	for i, tk := range f.tickets {
		steps = append(steps,
			struct {
				sql  string
				args []any
			}{`INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id, ordinal)
			   VALUES ($1, $2, $3, $4, 'buyer@example.com', $5, $6)`, []any{tk, f.cs, f.session, f.tier, f.order, i}},
			struct {
				sql  string
				args []any
			}{`INSERT INTO order_items (order_id, ordinal, tier_id, ticket_id, unit_price, total) VALUES ($1, $2, $3, $4, 2500, 2500)`,
				[]any{f.order, i, f.tier, tk}})
	}
	t.Cleanup(func() { f.cleanup(t) })
	for _, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("fixture %q: %v", strings.Fields(s.sql)[2], err)
		}
	}
	ref := "cs_pay03_" + uuid.NewString()
	pi, err := q.InsertPaymentIntent(ctx, &f.cs, f.org, provider, &ref, 5000, "EUR", "succeeded", nil, nil)
	if err != nil {
		t.Fatalf("payment intent: %v", err)
	}
	f.payment = pi.ID
	return f
}

func (f *fixture) cleanup(t *testing.T) {
	ctx := context.Background()
	ids := []any{f.tickets[0].String(), f.tickets[1].String()}
	for _, s := range []struct {
		sql string
		arg any
	}{
		{`UPDATE tickets SET refund_id = NULL WHERE order_id = $1`, f.order},
		{`DELETE FROM refunds WHERE org_id = $1`, f.org},
		{`DELETE FROM refund_batches WHERE org_id = $1`, f.org},
		{`DELETE FROM order_events WHERE order_id = $1`, f.order},
		{`DELETE FROM order_items WHERE order_id = $1`, f.order},
		{`DELETE FROM outbox_events WHERE aggregate_id = ANY($1::text[])`, []string{ids[0].(string), ids[1].(string)}},
		{`DELETE FROM tickets WHERE order_id = $1`, f.order},
		{`DELETE FROM orders WHERE id = $1`, f.order},
		{`DELETE FROM payment_intents WHERE org_id = $1`, f.org},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, f.org},
		{`DELETE FROM reservations WHERE org_id = $1`, f.org},
		{`DELETE FROM inventory_ledger WHERE session_id = $1`, f.session},
		{`DELETE FROM ticket_tiers WHERE session_id = $1`, f.session},
		{`DELETE FROM sessions WHERE id = $1`, f.session},
		{`DELETE FROM events WHERE org_id = $1`, f.org},
		{`DELETE FROM venues WHERE org_id = $1`, f.org},
		{`DELETE FROM sales_channels WHERE org_id = $1`, f.org},
		{`DELETE FROM organizations WHERE id = $1`, f.org},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.arg); err != nil {
			t.Logf("cleanup %q: %v", s.sql, err)
		}
	}
}

func (f *fixture) ticketStatus(t *testing.T, id uuid.UUID) (status string, refundID *uuid.UUID) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), `SELECT status, refund_id FROM tickets WHERE id = $1`, id).Scan(&status, &refundID); err != nil {
		t.Fatalf("ticket: %v", err)
	}
	return status, refundID
}

func (f *fixture) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, f.org).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (f *fixture) engine(m *fakeModule, withCanceller bool) *refunds.Engine {
	return f.engineWith(m, withCanceller, nil)
}

// engineWith builds an engine and lets the test adjust its options first.
func (f *fixture) engineWith(m *fakeModule, withCanceller bool, adjust func(o *refunds.Options)) *refunds.Engine {
	q := gen.New(f.pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := refunds.Options{DB: f.pool, Modules: fakeSource{m: m}, Audit: audit.NewPGWriter(f.pool), Logger: logger,
		SweepOrgs: []uuid.UUID{f.org}}
	if withCanceller {
		opts.CancelTicket = htickets.New(q, q, nil, q, q, q, nil, nil, f.pool, f.pool, audit.NewPGWriter(f.pool), logger, nil, nil, nil).RefundCanceller()
	}
	if adjust != nil {
		adjust(&opts)
	}
	return refunds.New(opts)
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// staleMarker ages a refund's in-flight marker past CallStaleAfter, as if
// its last provider call started two minutes ago.
func (f *fixture) staleMarker(t *testing.T, id uuid.UUID) {
	t.Helper()
	f.exec(t, `UPDATE refunds SET provider_attempted_at = now() - interval '2 minutes' WHERE id = $1`, id)
}

func (f *fixture) batch(key string, cancel bool, items ...refunds.Item) refunds.BatchInput {
	return refunds.BatchInput{OrgID: f.org, OrderID: f.order, Items: items, Reason: "show cancelled",
		CancelTickets: cancel, NotifyBuyer: true, IdempotencyKey: key, Actor: refunds.Actor{Type: "user", ID: uuid.NewString()},
		Via: "telegram_bot", Approved: true}
}

func (f *fixture) activeChecker(t *testing.T) func(payments.RefundRequest) bool {
	return func(req payments.RefundRequest) bool {
		var status string
		if err := f.pool.QueryRow(context.Background(),
			`SELECT t.status FROM refunds r JOIN tickets t ON t.id = r.ticket_id WHERE r.id = $1`, req.IdempotencyKey).Scan(&status); err != nil {
			t.Errorf("ticket status at call time: %v", err)
		}
		return status == "active"
	}
}

func refusalCode(err error) string {
	var e *refunds.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestEngine_SuccessCancelsTheTicketOnlyAfterTheProvider(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	m.beforeRefund = f.activeChecker(t)
	res, err := f.engine(m, true).CreateBatch(context.Background(), f.batch("k-success", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateSucceeded || r.Amount != 2500 || deref(r.ProviderRefundID) != "re_"+r.ID.String() {
		t.Fatalf("refund = %+v; want succeeded 2500 with the provider id", r)
	}
	if len(m.calls) != 1 || !m.calls[0].ticketActive || m.calls[0].key != r.ID.String() {
		t.Fatalf("calls = %+v; want one call keyed by the refund id while the ticket was still active", m.calls)
	}
	// The cs_ payment had no pi_: the module resolved it and arena stored it.
	if m.calls[0].chargeRef == "" || m.resolved != 1 {
		t.Fatalf("charge ref = %q resolved %d; want the resolved ref", m.calls[0].chargeRef, m.resolved)
	}
	var stored string
	_ = f.pool.QueryRow(context.Background(), `SELECT COALESCE(provider_charge_ref, '') FROM payment_intents WHERE id = $1`, f.payment).Scan(&stored)
	if stored != m.calls[0].chargeRef {
		t.Fatalf("provider_charge_ref = %q; want %q stored", stored, m.calls[0].chargeRef)
	}
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "cancelled" || link == nil || *link != r.ID {
		t.Fatalf("ticket 0 = %s link %v; want cancelled and linked to the refund", st, link)
	}
	if st, _ := f.ticketStatus(t, f.tickets[1]); st != "active" {
		t.Fatalf("ticket 1 = %s; the other ticket must stay valid", st)
	}
	var orderStatus string
	_ = f.pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id = $1`, f.order).Scan(&orderStatus)
	if orderStatus != "partially_refunded" {
		t.Fatalf("order status = %s; want partially_refunded", orderStatus)
	}
	if n := f.count(t, `SELECT count(*) FROM audit_events WHERE action IN ('v1.refund.batch_create', 'v1.refund.provider_result') AND resource_id IN (SELECT id::text FROM refunds WHERE org_id = $1 UNION SELECT id::text FROM refund_batches WHERE org_id = $1)`); n != 2 {
		t.Fatalf("audit rows = %d; want batch_create + provider_result", n)
	}
}

// TestEngine_DeclineLeavesTicketsValid covers a refusal on the FIRST
// attempt only — the provider was reached once and said no, so nothing can
// have moved. A refusal after an unknown outcome is
// TestEngine_RefusalAfterAnUnknownOutcomeGoesToManualReview.
func TestEngine_DeclineLeavesTicketsValid(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){declined}}
	res, err := f.engine(m, true).CreateBatch(context.Background(), f.batch("k-decline", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateFailed || deref(r.FailureCode) != "card_closed" || r.ProviderAttempts != 1 {
		t.Fatalf("refund = %s / %s attempts %d; want failed with the provider's code on the first attempt",
			r.State, deref(r.FailureCode), r.ProviderAttempts)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s; a refused refund must not cancel it", st)
	}
	// A failed refund frees the ticket for a new attempt.
	m.answers = nil
	res, err = f.engine(m, true).CreateBatch(context.Background(), f.batch("k-decline-2", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("second attempt: %v %+v", err, res)
	}
}

func TestEngine_PendingThenSweepLookup(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch("k-pending", true, refunds.Item{TicketID: f.tickets[1]}))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateProviderPending || r.ProviderRefundID == nil {
		t.Fatalf("refund = %+v; want provider_pending with the provider id", r)
	}
	if st, _ := f.ticketStatus(t, f.tickets[1]); st != "cancelled" {
		t.Fatalf("ticket = %s; an accepted (pending) refund cancels the ticket", st)
	}
	// Nothing to look up yet (younger than ten minutes).
	if rep, err := e.Sweep(context.Background(), time.Now(), nil); err != nil || rep.LookedUp != 0 {
		t.Fatalf("early sweep: %+v %v", rep, err)
	}
	if _, err := f.pool.Exec(context.Background(), `UPDATE refunds SET updated_at = now() - interval '11 minutes' WHERE id = $1`, r.ID); err != nil {
		t.Fatal(err)
	}
	m.lookup = payments.RefundResult{Status: payments.RefundSucceeded}
	rep, err := e.Sweep(context.Background(), time.Now(), nil)
	if err != nil || rep.LookedUp != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	got, _ := e.GetRefund(context.Background(), r.ID)
	if got.State != refunds.StateSucceeded {
		t.Fatalf("after lookup: %s; want succeeded", got.State)
	}
}

func TestEngine_PendingThatFailsLaterGoesToManualReview(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){pending}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch("k-pending-fail", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.pool.Exec(context.Background(), `UPDATE refunds SET updated_at = now() - interval '11 minutes' WHERE id = $1`, res.Refunds[0].ID)
	m.lookup = payments.RefundResult{Status: payments.RefundFailed, FailureCode: "insufficient_funds"}
	n := &recordingNotifier{}
	if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil {
		t.Fatal(err)
	}
	got, _ := e.GetRefund(context.Background(), res.Refunds[0].ID)
	if got.State != refunds.StateManualReview || deref(got.FailureCode) != "insufficient_funds" {
		t.Fatalf("refund = %s %s; want manual_review (the ticket is already cancelled)", got.State, deref(got.FailureCode))
	}
	// PAY-03 review M1: accepted, then failed — the money did not go back
	// and the ticket is gone. An operator is told once, ids and amount only.
	if len(n.texts) != 1 || !strings.Contains(n.texts[0], got.ID.String()) || !strings.Contains(n.texts[0], "insufficient_funds") ||
		strings.Contains(n.texts[0], "buyer@example.com") {
		t.Fatalf("alerts = %v; want one alert naming the refund and the code, no buyer data", n.texts)
	}
	if _, err := e.Sweep(context.Background(), time.Now(), n); err != nil || len(n.texts) != 1 {
		t.Fatalf("second pass re-alerted: %v %v", n.texts, err)
	}
}

func TestEngine_UnknownOutcomeIsRetriedBySweepWithTheSameKey(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout, func(req payments.RefundRequest) (payments.RefundResult, error) {
		return payments.RefundResult{ProviderRefundID: "re_" + req.IdempotencyKey, Status: payments.RefundSucceeded}, nil
	}}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch("k-unknown", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	r := res.Refunds[0]
	if r.State != refunds.StateProviderPending || r.ProviderRefundID != nil {
		t.Fatalf("after a timeout: %+v; want provider_pending without a provider id", r)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s; an unknown outcome must not cancel it", st)
	}
	// A second Drive right away is refused by the claim (younger than a
	// minute): no second call.
	if _, err := e.Drive(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if m.callCount() != 1 {
		t.Fatalf("calls = %d; the in-flight marker must block an immediate re-call", m.callCount())
	}
	_, _ = f.pool.Exec(context.Background(), `UPDATE refunds SET provider_attempted_at = now() - interval '2 minutes' WHERE id = $1`, r.ID)
	rep, err := e.Sweep(context.Background(), time.Now(), nil)
	if err != nil || rep.Retried != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if m.callCount() != 2 || m.calls[0].key != m.calls[1].key {
		t.Fatalf("calls = %+v; want a retry with the same idempotency key", m.calls)
	}
	got, _ := e.GetRefund(context.Background(), r.ID)
	if got.State != refunds.StateSucceeded || got.ProviderAttempts != 2 {
		t.Fatalf("after retry: %s attempts %d", got.State, got.ProviderAttempts)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "cancelled" {
		t.Fatalf("ticket = %s; want cancelled after the retry succeeded", st)
	}
}

func TestEngine_DoubleSubmitWithTheSameKeyWritesOnce(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	in := f.batch("k-double", true, refunds.Item{TicketID: f.tickets[0]})
	first, err := e.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.CreateBatch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.BatchID != first.BatchID || len(second.Refunds) != 1 || second.Refunds[0].ID != first.Refunds[0].ID {
		t.Fatalf("replay = %+v; want the first batch", second)
	}
	if m.callCount() != 1 || f.count(t, `SELECT count(*) FROM refunds WHERE org_id = $1`) != 1 {
		t.Fatalf("calls %d; the replay must not write or call again", m.callCount())
	}
	// The same key for another order is refused.
	other := in
	other.OrderID = uuid.New()
	if _, err := e.CreateBatch(context.Background(), other); err == nil {
		t.Fatal("a key reused for another order was accepted")
	}
}

func TestEngine_ConcurrentRefundsOfOnePaymentSerialize(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Everyone asks for the SAME ticket in full, each with its own key.
			_, errs[i] = e.CreateBatch(context.Background(), f.batch(fmt.Sprintf("k-race-%d", i), true, refunds.Item{TicketID: f.tickets[0]}))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch code := refusalCode(err); {
		case err == nil:
			ok++
		case code == refunds.CodeInProgress || code == refunds.CodeTicketNotRefundable || code == refunds.CodeNothingToRefund:
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	var total int64
	_ = f.pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(amount), 0) FROM refunds WHERE org_id = $1 AND state NOT IN ('failed', 'rejected')`, f.org).Scan(&total)
	if ok != 1 || total != 2500 {
		t.Fatalf("ok = %d, refunded = %d; want exactly one refund of the ticket's 2500", ok, total)
	}
}

func TestEngine_RefusalsWriteNothing(t *testing.T) {
	pool := testPool(t)
	f := newFixture(t, pool, "pay03fake")
	m := &fakeModule{partial: true}
	e := f.engine(m, true)
	over := int64(2501)
	cases := []struct {
		name string
		in   refunds.BatchInput
		code string
	}{
		{"amount above the ticket", f.batch("k-over", true, refunds.Item{TicketID: f.tickets[0], Amount: &over}), refunds.CodeAmountExceeds},
		{"ticket of another order", f.batch("k-foreign", true, refunds.Item{TicketID: uuid.New()}), refunds.CodeTicketNotRefundable},
		{"missing reason", func() refunds.BatchInput {
			b := f.batch("k-reason", true, refunds.Item{TicketID: f.tickets[0]})
			b.Reason = ""
			return b
		}(), refunds.CodeInvalidRequest},
		{"another organization", func() refunds.BatchInput {
			b := f.batch("k-org", true, refunds.Item{TicketID: f.tickets[0]})
			b.OrgID = uuid.New()
			return b
		}(), refunds.CodeOrderNotFound},
	}
	for _, c := range cases {
		if _, err := e.CreateBatch(context.Background(), c.in); refusalCode(err) != c.code {
			t.Errorf("%s: %v; want %s", c.name, err, c.code)
		}
	}
	// A provider without partial refunds refuses one ticket of two.
	m.partial = false
	if _, err := e.CreateBatch(context.Background(), f.batch("k-partial", true, refunds.Item{TicketID: f.tickets[0]})); refusalCode(err) != refunds.CodePartialNotSupported {
		t.Errorf("partial: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM refunds WHERE org_id = $1`) + f.count(t, `SELECT count(*) FROM refund_batches WHERE org_id = $1`); n != 0 || m.callCount() != 0 {
		t.Fatalf("refusals wrote %d rows and made %d calls", n, m.callCount())
	}
	// ...but the whole payment at once is fine.
	if res, err := e.CreateBatch(context.Background(), f.batch("k-whole", true, refunds.Item{TicketID: f.tickets[0]}, refunds.Item{TicketID: f.tickets[1]})); err != nil || len(res.Refunds) != 2 {
		t.Fatalf("whole payment: %v", err)
	}

	// The seller's own site (manual) and an unknown provider are refused.
	seller := newFixture(t, pool, "manual")
	if _, err := seller.engine(m, true).CreateBatch(context.Background(), seller.batch("k-seller", true, refunds.Item{TicketID: seller.tickets[0]})); refusalCode(err) != refunds.CodeSellerSiteOrder {
		t.Errorf("manual: %v", err)
	}
	unknown := newFixture(t, pool, "mock")
	if _, err := unknown.engine(m, true).CreateBatch(context.Background(), unknown.batch("k-mock", true, refunds.Item{TicketID: unknown.tickets[0]})); refusalCode(err) != refunds.CodeProviderNotSupported {
		t.Errorf("unknown provider: %v", err)
	}
}

type recordingNotifier struct{ texts []string }

func (n *recordingNotifier) Send(_ context.Context, text string) error {
	n.texts = append(n.texts, text)
	return nil
}

func TestEngine_StuckRefundIsParkedWithAnAlert(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true, answers: []func(payments.RefundRequest) (payments.RefundResult, error){timeout}}
	e := f.engine(m, true)
	res, err := e.CreateBatch(context.Background(), f.batch("k-stuck", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE refunds SET created_at = now() - interval '25 hours', approved_at = now() - interval '25 hours',
		provider_attempted_at = now() - interval '2 minutes' WHERE id = $1`, res.Refunds[0].ID)
	n := &recordingNotifier{}
	rep, err := e.Sweep(context.Background(), time.Now(), n)
	if err != nil || rep.Stuck != 1 || rep.Retried != 0 || rep.Alerted != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	got, _ := e.GetRefund(context.Background(), res.Refunds[0].ID)
	if got.State != refunds.StateManualReview {
		t.Fatalf("state = %s; want manual_review", got.State)
	}
	if len(n.texts) != 1 || strings.Contains(n.texts[0], "buyer@example.com") || !strings.Contains(n.texts[0], got.ID.String()) {
		t.Fatalf("alert = %v; want one message naming the refund and no buyer data", n.texts)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s; nothing was ever accepted", st)
	}
}

func TestEngine_SweepRepairsATicketLeftActive(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	m := &fakeModule{partial: true}
	// No canceller: the money goes back, the ticket stays active — the
	// crash-between-commits case.
	res, err := f.engine(m, false).CreateBatch(context.Background(), f.batch("k-repair", true, refunds.Item{TicketID: f.tickets[0]}))
	if err != nil || res.Refunds[0].State != refunds.StateSucceeded {
		t.Fatalf("CreateBatch: %v", err)
	}
	if st, _ := f.ticketStatus(t, f.tickets[0]); st != "active" {
		t.Fatalf("ticket = %s; want still active before the repair", st)
	}
	_, _ = f.pool.Exec(context.Background(), `UPDATE refunds SET updated_at = now() - interval '2 minutes' WHERE id = $1`, res.Refunds[0].ID)
	var published [][]string
	repairer := f.engineWith(m, true, func(o *refunds.Options) {
		o.PublishRefunded = func(_ context.Context, ids []string, _, _, _ string, _ int64) { published = append(published, ids) }
	})
	rep, err := repairer.Sweep(context.Background(), time.Now(), nil)
	if err != nil || rep.Repaired != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	// The first settle could not cancel, so it published nothing; the repair
	// publishes v1.ticket.refunded once, and a later pass has nothing to do.
	if len(published) != 1 || len(published[0]) != 1 || published[0][0] != f.tickets[0].String() {
		t.Fatalf("published = %v; want the repaired ticket once", published)
	}
	if rep, err := repairer.Sweep(context.Background(), time.Now(), nil); err != nil || rep.Repaired != 0 || len(published) != 1 {
		t.Fatalf("second pass: %+v %v published %v", rep, err, published)
	}
	if st, link := f.ticketStatus(t, f.tickets[0]); st != "cancelled" || link == nil {
		t.Fatalf("ticket = %s link %v; want cancelled by the repair", st, link)
	}
	if m.callCount() != 1 {
		t.Fatalf("the repair must not call the provider again: %d calls", m.callCount())
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
