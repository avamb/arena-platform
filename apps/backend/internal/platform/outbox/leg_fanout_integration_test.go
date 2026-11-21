//go:build integration

// leg_fanout_integration_test.go — finding F-FC-01: a failing MACS leg must
// neither block nor re-trigger the WordPress-sites leg.
//
// Everything below the two HTTP stubs is REAL: a seeded paid order, the real
// macs.Dispatcher and bil24wire.Dispatcher behind outbox.LegFanOut with the
// PG leg-state store, driven by the real OutboxEventsDispatcher polling loop
// over the real PGOutboxEventStore (scoped to this test's own row, see
// docs/agents/testing-integration.md "A test that drains outbox_events must
// claim only its own rows").
//
// Run with:
//
//	go test -tags integration -run TestLegFanOut_Integration ./apps/backend/internal/platform/outbox/
package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

func legsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping the outbox leg fan-out integration test")
	}
	if !strings.HasPrefix(dsn, "post") {
		t.Skipf("DATABASE_URL %q is not a Postgres DSN; skipping", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// legsScopedStore claims ONLY this test's own outbox_events rows; the rest is
// the production PGOutboxEventStore.
type legsScopedStore struct {
	*outbox.PGOutboxEventStore
	pool        *pgxpool.Pool
	aggregateID string
}

// ClaimNext mirrors PGOutboxEventStore.ClaimNext with an aggregate_id filter.
func (s *legsScopedStore) ClaimNext(ctx context.Context) (*outbox.OutboxEventRow, error) {
	row := &outbox.OutboxEventRow{}
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM outbox_events
			 WHERE processed_at IS NULL AND dead_lettered_at IS NULL
			   AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			   AND aggregate_id = $1
			 ORDER BY COALESCE(next_attempt_at, occurred_at)
			   FOR UPDATE SKIP LOCKED
			 LIMIT 1
		)
		UPDATE outbox_events o
		   SET next_attempt_at = now() + '5 minutes'::interval
		  FROM next WHERE o.id = next.id
		RETURNING o.id::text, o.aggregate_type, o.aggregate_id, o.event_type,
		          o.payload, o.occurred_at, o.attempts`, s.aggregateID,
	).Scan(&row.ID, &row.AggregateType, &row.AggregateID, &row.EventType,
		&payload, &row.OccurredAt, &row.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.Payload = map[string]any{}
	_ = json.Unmarshal(payload, &row.Payload)
	return row, nil
}

// macsSwitchStub answers 500 until healthy is set, then the MACS ack.
type macsSwitchStub struct {
	srv     *httptest.Server
	calls   atomic.Int64
	healthy atomic.Bool
}

func newMACSSwitchStub() *macsSwitchStub {
	s := &macsSwitchStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if !s.healthy.Load() {
			http.Error(w, "scanner down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	}))
	return s
}

// siteStub is a WordPress site that always answers 200 and counts order.paid.
type siteStub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	paid int
}

func newSiteStub() *siteStub {
	s := &siteStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Arena-Event-Type") == bil24wire.SiteEventOrderPaid {
			s.mu.Lock()
			s.paid++
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	return s
}

func (s *siteStub) paidCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paid
}

func TestLegFanOut_Integration_FailingMACSDoesNotBlockOrRepeatTheSite(t *testing.T) {
	pool := legsPool(t)
	ctx := context.Background()

	macsStub := newMACSSwitchStub()
	defer macsStub.srv.Close()
	site := newSiteStub()
	defer site.srv.Close()

	// ── Seed a paid order with one ticket (copied from the W1-B7e and MACS
	// round-trip fixtures) ────────────────────────────────────────────────
	const (
		currency  = "CZK"
		unitPrice = int64(1000)
	)
	suffix := uuid.New().String()[:8]
	orgID, venueID, eventID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	channelID, tierID, resID, csID, orderID, ticketID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed exec: %v\nSQL: %s", err, sql)
		}
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM outbox_events WHERE aggregate_id = $1`, orderID.String())
		_, _ = pool.Exec(c, `DELETE FROM webhook_subscribers WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE session_id = $1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM orders WHERE id = $1`, orderID)
		_, _ = pool.Exec(c, `DELETE FROM checkout_sessions WHERE id = $1`, csID)
		_, _ = pool.Exec(c, `DELETE FROM reservations WHERE id = $1`, resID)
		_, _ = pool.Exec(c, `DELETE FROM inventory_ledger WHERE session_id = $1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM ticket_tiers WHERE id = $1`, tierID)
		_, _ = pool.Exec(c, `DELETE FROM compatibility_id_map WHERE platform_id = ANY($1)`,
			[]uuid.UUID{sessionID, eventID, venueID})
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE id = $1`, channelID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id = $1`, sessionID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id = $1`, venueID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	mustExec(`INSERT INTO organizations (id, name, legal_name, slug, tax_id) VALUES ($1,$2,$3,$4,$5)`,
		orgID, "FC01 Org "+suffix, "FC01 Legal s.r.o.", "fc01-"+suffix, "CZ12345678")
	mustExec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1,$2,$3,'Europe/Prague')`,
		venueID, orgID, "FC01 Venue "+suffix)
	mustExec(`INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1,$2,$3,'published','public')`, eventID, orgID, "FC01 Event "+suffix)
	startAt := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	mustExec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total,
			status, admission_mode, currency, currency_source)
		VALUES ($1,$2,$3,$4,$5,100,'scheduled','general_admission',$6,'override')`,
		sessionID, eventID, venueID, startAt, startAt.Add(3*time.Hour), currency)
	mustExec(`INSERT INTO sales_channels (id, org_id, name) VALUES ($1,$2,$3)`,
		channelID, orgID, "FC01 Channel "+suffix)
	mustExec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
		VALUES ($1,$2,'FC01 Standard','fixed',$3,$4,0)`, tierID, sessionID, unitPrice, currency)
	mustExec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_sold)
		VALUES ($1,$2,100,1)`, sessionID, tierID)
	mustExec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, expires_at)
		VALUES ($1,$2,$3,$4,1,$5)`, resID, orgID, channelID, sessionID, time.Now().Add(30*time.Minute))
	mustExec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state,
			subtotal, discount, total, currency, payment_provider, completed_at)
		VALUES ($1,$2,$3,$4,'completed',$5,0,$5,$6,'yookassa',NOW())`,
		csID, orgID, channelID, resID, unitPrice, currency)
	mustExec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id,
			reservation_id, source, status, currency, subtotal, discount, charge, total)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'checkout_api','paid',$8,$9,0,0,$9)`,
		orderID, orgID, channelID, eventID, sessionID, csID, resID, currency, unitPrice)
	mustExec(`INSERT INTO tickets (id, session_id, checkout_session_id, order_id, tier_id,
			status, issued_at, ordinal, holder_email)
		VALUES ($1,$2,$3,$4,$5,'active',NOW(),1,'fc01-buyer@example.com')`,
		ticketID, sessionID, csID, orderID, tierID)

	// The scanner (MACS) of the organization and the WordPress site of the channel.
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types, active, kind, org_id)
		VALUES ('', $1, 'fc01-macs-secret', '{}', TRUE, 'macs', $2)`, macsStub.srv.URL+"/webhook", orgID)
	mustExec(`INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, event_types,
			active, kind, org_id, channel_id)
		VALUES ('', $1, 'fc01-site-secret', '{}', TRUE, 'bil24_wp', $2, $3)`, site.srv.URL, orgID, channelID)

	var outboxID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, occurred_at)
		VALUES ('order', $1::text, $2,
			jsonb_build_object('order_id', $1::text, 'org_id', $3::text, 'channel_id', $4::text), NOW())
		RETURNING id::text`,
		orderID.String(), bil24wire.EventOrderPaid, orgID.String(), channelID.String(),
	).Scan(&outboxID); err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}

	// ── The worker's chain: real legs, real fan-out, real PG state ─────────
	wp := bil24wire.NewDispatcher(pool)
	if wp == nil {
		t.Fatal("bil24wire.NewDispatcher returned nil for a live pool")
	}
	fanOut, err := outbox.NewLegFanOut(outbox.NewPGLegStateStore(pool), nil,
		outbox.Leg{Name: "macs", Dispatcher: macs.NewDispatcher(pool)},
		outbox.Leg{Name: "bil24_wp", Dispatcher: wp},
	)
	if err != nil {
		t.Fatalf("NewLegFanOut: %v", err)
	}
	opts := outbox.OutboxEventsDispatcherOptions{
		Store: &legsScopedStore{
			PGOutboxEventStore: outbox.NewPGOutboxEventStore(pool),
			pool:               pool,
			aggregateID:        orderID.String(),
		},
		Dispatcher:   fanOut,
		PollInterval: 20 * time.Millisecond,
		MaxAttempts:  30,
		// A zero backoff lets the loop retry the failing row at once, so a
		// few seconds are several real outbox passes.
		BackoffFunc: func(int) time.Duration { return 0 },
	}

	readRow := func() (attempts int, processed, deadLettered bool) {
		t.Helper()
		var processedAt, deadAt *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT attempts, processed_at, dead_lettered_at FROM outbox_events WHERE id = $1::uuid`, outboxID,
		).Scan(&attempts, &processedAt, &deadAt); err != nil {
			t.Fatalf("read outbox row: %v", err)
		}
		return attempts, processedAt != nil, deadAt != nil
	}

	// ── Phase 1: MACS is down for several passes ──────────────────────────
	if !legsDrain(t, opts, func() bool {
		attempts, _, _ := readRow()
		return attempts >= 3 && macsStub.calls.Load() >= 3
	}, 20*time.Second) {
		attempts, _, _ := readRow()
		t.Fatalf("MACS outage never produced 3 passes: attempts=%d macs calls=%d site=%d",
			attempts, macsStub.calls.Load(), site.paidCount())
	}

	attempts, processed, dead := readRow()
	if processed {
		t.Fatal("the outbox row is processed while MACS still fails")
	}
	if dead {
		t.Fatal("the outbox row is dead-lettered after a few attempts")
	}
	if got := site.paidCount(); got != 1 {
		t.Fatalf("the WordPress site got order.paid %d time(s) over %d attempts, want exactly 1", got, attempts)
	}
	if got := macsStub.calls.Load(); got < 2 {
		t.Fatalf("MACS was called %d time(s), want >= 2", got)
	}

	var (
		macsAttempts  int
		macsLastErr   *string
		macsDelivered *time.Time
		siteAttempts  int
		siteDelivered *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT attempts, last_error, delivered_at FROM outbox_event_legs WHERE event_id = $1::uuid AND leg = 'macs'`,
		outboxID).Scan(&macsAttempts, &macsLastErr, &macsDelivered); err != nil {
		t.Fatalf("read macs leg state: %v", err)
	}
	if macsDelivered != nil || macsAttempts < 2 || macsLastErr == nil || !strings.Contains(*macsLastErr, "500") {
		t.Fatalf("macs leg state attempts=%d last_error=%v delivered=%v, want >=2 failed attempts with a 500",
			macsAttempts, macsLastErr, macsDelivered)
	}
	if err := pool.QueryRow(ctx, `
		SELECT attempts, delivered_at FROM outbox_event_legs WHERE event_id = $1::uuid AND leg = 'bil24_wp'`,
		outboxID).Scan(&siteAttempts, &siteDelivered); err != nil {
		t.Fatalf("read bil24_wp leg state: %v", err)
	}
	if siteDelivered == nil || siteAttempts != 1 {
		t.Fatalf("bil24_wp leg state attempts=%d delivered=%v, want 1 delivered attempt", siteAttempts, siteDelivered)
	}

	// ── Phase 2: MACS recovers ─────────────────────────────────────────────
	macsStub.healthy.Store(true)
	callsBefore := macsStub.calls.Load()
	if !legsDrain(t, opts, func() bool {
		_, processed, _ := readRow()
		return processed
	}, 20*time.Second) {
		t.Fatalf("the outbox row was not processed after MACS recovered (macs calls=%d)", macsStub.calls.Load())
	}
	if got := macsStub.calls.Load() - callsBefore; got != 1 {
		t.Errorf("MACS was called %d time(s) after recovering, want 1", got)
	}
	if got := site.paidCount(); got != 1 {
		t.Fatalf("the WordPress site got order.paid %d time(s) in total, want still 1", got)
	}
	if err := pool.QueryRow(ctx, `
		SELECT delivered_at FROM outbox_event_legs WHERE event_id = $1::uuid AND leg = 'macs'`,
		outboxID).Scan(&macsDelivered); err != nil {
		t.Fatalf("read macs leg state after recovery: %v", err)
	}
	if macsDelivered == nil {
		t.Error("macs leg not marked delivered after recovery")
	}
}

// legsDrain runs a real OutboxEventsDispatcher until `until` reports true or
// the timeout expires, then stops it. Stop waits for Run to return, so the
// row's MarkFailed / MarkDispatched has committed before the caller asserts.
func legsDrain(t *testing.T, opts outbox.OutboxEventsDispatcherOptions, until func() bool, timeout time.Duration) bool {
	t.Helper()
	oed, err := outbox.NewOutboxEventsDispatcher(opts)
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}
	go func() { _ = oed.Run(context.Background()) }()
	deadline := time.Now().Add(timeout)
	satisfied := false
	for time.Now().Before(deadline) {
		if until() {
			satisfied = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = oed.Stop()
	return satisfied
}
