// Package outbox — OutboxEventsDispatcher for the outbox_events table.
//
// The echo handler (httpserver/echo.go) writes domain events to the
// outbox_events table inside the same transaction as the business mutation.
// This dispatcher polls that table, delivers each unprocessed row to the
// configured Dispatcher, and marks the row processed_at = now().
//
// Persistent semantics guarantee that rows written before the worker process
// stopped are still present when the worker restarts — the at-least-once
// delivery contract holds across any number of restarts.
//
// Feature coverage: #38 "Outbox dispatcher resumes after worker restart"
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// OutboxEventRow is a snapshot of one outbox_events row at claim time.
//
//nolint:revive // intentional: "Outbox" prefix mirrors the table name outbox_events
type OutboxEventRow struct {
	// ID is the outbox_events primary key (uuid as text).
	ID string

	// AggregateType identifies the domain aggregate (e.g. "echo").
	AggregateType string

	// AggregateID is the owning aggregate's UUID (stored as text in the
	// outbox_events table).
	AggregateID string

	// EventType is the stable event name (e.g. "v1.echo.created").
	EventType string

	// Payload is the deserialized JSONB payload. May be empty but never nil.
	Payload map[string]any

	// OccurredAt is the wall-clock time the event was created.
	OccurredAt time.Time

	// Attempts is the number of delivery tries made so far (default 0).
	Attempts int
}

// OutboxEventStore is the persistence interface used by OutboxEventsDispatcher.
// PGOutboxEventStore provides the production implementation; the interface
// exists to allow in-memory test doubles without a live database.
//
//nolint:revive // intentional: "Outbox" prefix mirrors the table name outbox_events
type OutboxEventStore interface {
	// ClaimNext returns the next unprocessed row (processed_at IS NULL) in
	// occurred_at order, claiming it with FOR UPDATE SKIP LOCKED so concurrent
	// dispatcher instances never process the same row.
	//
	// Returns (nil, nil) when the queue is empty.
	ClaimNext(ctx context.Context) (*OutboxEventRow, error)

	// MarkDispatched sets processed_at = now() and increments attempts on the
	// row identified by id. Called after a successful Dispatcher.Dispatch.
	MarkDispatched(ctx context.Context, id string) error

	// MarkFailed increments attempts and stores lastErr on the row identified
	// by id. The row remains unprocessed (processed_at IS NULL) so it will be
	// retried by the next dispatch cycle.
	MarkFailed(ctx context.Context, id string, lastErr string) error
}

// =============================================================================
// PGOutboxEventStore — production implementation
// =============================================================================

// PGOutboxEventStore implements OutboxEventStore against the outbox_events
// PostgreSQL table (created by 0001_init.sql).
type PGOutboxEventStore struct {
	pool *pgxpool.Pool
}

// NewPGOutboxEventStore wraps a pgxpool.Pool into a PGOutboxEventStore.
func NewPGOutboxEventStore(pool *pgxpool.Pool) *PGOutboxEventStore {
	return &PGOutboxEventStore{pool: pool}
}

// claimSQL uses a CTE to SELECT with FOR UPDATE SKIP LOCKED and immediately
// return the full row. Running both SELECT and RETURNING inside one statement
// means the pool needs no explicit transaction from the caller — the implicit
// single-statement transaction provides the required lock scope.
const claimSQL = `
	WITH next AS (
		SELECT id
		  FROM outbox_events
		 WHERE processed_at IS NULL
		 ORDER BY occurred_at ASC
		   FOR UPDATE SKIP LOCKED
		 LIMIT 1
	)
	SELECT e.id::text,
	       e.aggregate_type,
	       e.aggregate_id,
	       e.event_type,
	       e.payload,
	       e.occurred_at,
	       e.attempts
	  FROM outbox_events e
	  JOIN next ON e.id = next.id
`

// ClaimNext implements OutboxEventStore.
func (s *PGOutboxEventStore) ClaimNext(ctx context.Context) (*OutboxEventRow, error) {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// FOR UPDATE SKIP LOCKED requires an explicit transaction.
	tx, err := s.pool.BeginTx(claimCtx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("outbox events dispatcher: begin tx: %w", err)
	}
	// Rollback is a no-op after Commit; keep the defer to handle error paths.
	defer func() { _ = tx.Rollback(context.Background()) }()

	var (
		id            string
		aggregateType string
		aggregateID   string
		eventType     string
		payloadBytes  []byte
		occurredAt    time.Time
		attempts      int
	)
	err = tx.QueryRow(claimCtx, claimSQL).Scan(
		&id, &aggregateType, &aggregateID, &eventType,
		&payloadBytes, &occurredAt, &attempts,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Commit(claimCtx)
			return nil, nil
		}
		return nil, fmt.Errorf("outbox events dispatcher: claim query: %w", err)
	}

	if err := tx.Commit(claimCtx); err != nil {
		return nil, fmt.Errorf("outbox events dispatcher: commit claim: %w", err)
	}

	var payload map[string]any
	if len(payloadBytes) > 0 {
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			payload = map[string]any{}
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	return &OutboxEventRow{
		ID:            id,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       payload,
		OccurredAt:    occurredAt,
		Attempts:      attempts,
	}, nil
}

const markDispatchedSQL = `
	UPDATE outbox_events
	   SET processed_at = now(),
	       attempts = attempts + 1
	 WHERE id = $1::uuid
`

// MarkDispatched implements OutboxEventStore.
func (s *PGOutboxEventStore) MarkDispatched(ctx context.Context, id string) error {
	updCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(updCtx, markDispatchedSQL, id); err != nil {
		return fmt.Errorf("outbox events dispatcher: mark dispatched: %w", err)
	}
	return nil
}

const markFailedSQL = `
	UPDATE outbox_events
	   SET attempts = attempts + 1,
	       last_error = $2
	 WHERE id = $1::uuid
`

// MarkFailed implements OutboxEventStore.
func (s *PGOutboxEventStore) MarkFailed(ctx context.Context, id string, lastErr string) error {
	updCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(updCtx, markFailedSQL, id, lastErr); err != nil {
		return fmt.Errorf("outbox events dispatcher: mark failed: %w", err)
	}
	return nil
}

// Compile-time interface guard.
var _ OutboxEventStore = (*PGOutboxEventStore)(nil)

// =============================================================================
// OutboxEventsDispatcher
// =============================================================================

// OutboxEventsDispatcherOptions configures an OutboxEventsDispatcher.
//
//nolint:revive // intentional: "Outbox" prefix mirrors the table name outbox_events
type OutboxEventsDispatcherOptions struct {
	// Store is the outbox_events persistence backend. Required.
	Store OutboxEventStore

	// Dispatcher is the event delivery backend. When nil, NoopDispatcher is used.
	Dispatcher Dispatcher

	// Logger receives structured log records. Defaults to slog.Default().
	Logger *slog.Logger

	// DispatchedCounter is a Prometheus CounterVec with label "event_type".
	// Incremented once per successfully dispatched row.
	// Optional — pass nil to disable metric recording.
	DispatchedCounter *prometheus.CounterVec

	// PollInterval is the wait between empty-queue polls. Defaults to 1s.
	PollInterval time.Duration

	// ShutdownTimeout bounds the graceful Stop path. Defaults to 20s.
	ShutdownTimeout time.Duration
}

// NewOutboxEventsDispatchedCounter returns a prometheus.CounterVec pre-configured
// for the outbox_events_dispatched_total metric. Register it with a prometheus
// Registry before passing it to OutboxEventsDispatcherOptions.DispatchedCounter.
func NewOutboxEventsDispatchedCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "outbox_events_dispatched_total",
		Help: "Total number of outbox_events rows successfully dispatched.",
	}, []string{"event_type"})
}

// OutboxEventsDispatcher polls the outbox_events table and delivers each
// unprocessed row via the configured Dispatcher. It survives process restarts:
// any row with processed_at IS NULL when the worker was stopped will be
// claimed and delivered in the next poll cycle after the worker starts again.
//
//nolint:revive // intentional: "Outbox" prefix mirrors the table name outbox_events
type OutboxEventsDispatcher struct {
	store           OutboxEventStore
	dispatcher      Dispatcher
	logger          *slog.Logger
	dispCounter     *prometheus.CounterVec
	pollInterval    time.Duration
	shutdownTimeout time.Duration

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewOutboxEventsDispatcher validates opts and returns a ready-to-Run dispatcher.
// Returns an error when the required Store is nil.
func NewOutboxEventsDispatcher(opts OutboxEventsDispatcherOptions) (*OutboxEventsDispatcher, error) {
	if opts.Store == nil {
		return nil, errors.New("outbox events dispatcher: Store is required")
	}

	d := opts.Dispatcher
	if d == nil {
		d = NoopDispatcher{}
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	poll := opts.PollInterval
	if poll <= 0 {
		poll = time.Second
	}

	shutdown := opts.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = 20 * time.Second
	}

	return &OutboxEventsDispatcher{
		store:           opts.Store,
		dispatcher:      d,
		logger:          logger.With(slog.String("component", "outbox_events_dispatcher")),
		dispCounter:     opts.DispatchedCounter,
		pollInterval:    poll,
		shutdownTimeout: shutdown,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}, nil
}

// Run polls the outbox_events table until ctx is cancelled or Stop is called.
// Each iteration attempts to claim and deliver one unprocessed row. An empty
// queue triggers a pollInterval sleep before the next attempt.
//
// Run returns nil on clean shutdown. Non-nil errors indicate non-recoverable
// conditions such as a closed pool.
func (od *OutboxEventsDispatcher) Run(ctx context.Context) error {
	defer func() {
		od.logger.Info("outbox events dispatcher: shutdown complete")
		close(od.doneCh)
	}()

	od.logger.Info("outbox events dispatcher started",
		slog.String("poll_interval", od.pollInterval.String()),
		slog.String("shutdown_timeout", od.shutdownTimeout.String()),
	)

	for {
		select {
		case <-ctx.Done():
			od.logger.Info("outbox events dispatcher: context cancelled; exiting run loop")
			return nil
		case <-od.stopCh:
			od.logger.Info("outbox events dispatcher: stop requested; exiting run loop")
			return nil
		default:
		}

		row, err := od.store.ClaimNext(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			od.logger.Warn("outbox events dispatcher: claim next failed",
				slog.String("error", err.Error()),
			)
			if !od.waitOrStop(ctx, od.pollInterval) {
				return nil
			}
			continue
		}

		if row == nil {
			// Empty queue — backoff for one poll interval.
			if !od.waitOrStop(ctx, od.pollInterval) {
				return nil
			}
			continue
		}

		od.deliverRow(ctx, row)
	}
}

// deliverRow dispatches one outbox_events row and records the outcome.
func (od *OutboxEventsDispatcher) deliverRow(ctx context.Context, row *OutboxEventRow) {
	// Extract trace_id from the payload so it can be propagated and logged.
	traceID, _ := row.Payload["trace_id"].(string)

	od.logger.Info("outbox events dispatcher: delivering event",
		slog.String("event_id", row.ID),
		slog.String("event_type", row.EventType),
		slog.String("aggregate_type", row.AggregateType),
		slog.String("aggregate_id", row.AggregateID),
		slog.String("trace_id", traceID),
		slog.Int("attempts", row.Attempts),
	)

	ev := Event{
		AggregateType: row.AggregateType,
		AggregateID:   row.AggregateID,
		EventType:     row.EventType,
		Payload:       row.Payload,
		OccurredAt:    row.OccurredAt,
	}

	dispErr := od.dispatcher.Dispatch(ctx, ev)
	if dispErr != nil {
		od.logger.Warn("outbox events dispatcher: dispatch failed",
			slog.String("event_id", row.ID),
			slog.String("event_type", row.EventType),
			slog.String("aggregate_id", row.AggregateID),
			slog.String("trace_id", traceID),
			slog.String("error", dispErr.Error()),
		)
		errText := outboxTruncate(dispErr.Error(), 4000)
		if err := od.store.MarkFailed(ctx, row.ID, errText); err != nil {
			od.logger.Error("outbox events dispatcher: mark failed error",
				slog.String("event_id", row.ID),
				slog.String("error", err.Error()),
			)
		}
		return
	}

	if err := od.store.MarkDispatched(ctx, row.ID); err != nil {
		od.logger.Error("outbox events dispatcher: mark dispatched error",
			slog.String("event_id", row.ID),
			slog.String("error", err.Error()),
		)
		return
	}

	od.logger.Info("outbox events dispatcher: event dispatched successfully",
		slog.String("event_id", row.ID),
		slog.String("event_type", row.EventType),
		slog.String("aggregate_type", row.AggregateType),
		slog.String("aggregate_id", row.AggregateID),
		slog.String("trace_id", traceID),
	)

	if od.dispCounter != nil {
		od.dispCounter.WithLabelValues(row.EventType).Inc()
	}
}

// Stop initiates a graceful shutdown. Returns nil when the Run loop has exited,
// or context.DeadlineExceeded if shutdownTimeout elapsed first.
// Stop is idempotent — calling it multiple times is safe.
func (od *OutboxEventsDispatcher) Stop() error {
	od.stopOnce.Do(func() { close(od.stopCh) })

	select {
	case <-od.doneCh:
		return nil
	case <-time.After(od.shutdownTimeout):
		od.logger.Warn("outbox events dispatcher: stop timed out",
			slog.String("timeout", od.shutdownTimeout.String()),
		)
		return context.DeadlineExceeded
	}
}

// waitOrStop sleeps for d, returning true if the wait completed normally,
// false if ctx was cancelled or Stop was called during the wait.
func (od *OutboxEventsDispatcher) waitOrStop(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-od.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

// outboxTruncate clips s to at most n characters, with an ellipsis when truncated.
// Named to avoid collision with the truncate function in the worker package.
func outboxTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
