// Package outbox implements the transactional outbox pattern for arena_new.
//
// Domain mutations and their corresponding domain events are persisted in the
// same PostgreSQL transaction via Writer.Append.  A background Dispatcher
// (see dispatcher.go) reads the outbox table and delivers events to external
// targets at-least-once.
//
// Placeholder status: the OutboxDispatcher worker loop is out of scope for
// the foundation milestone.  The NoopDispatcher is wired in its place so
// the metric and interfaces are available without any blocking side effects.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is the value-object written to the outbox table.  AggregateID must be
// a valid UUID string — it is stored in the aggregate_id uuid column.
type Event struct {
	// AggregateType identifies the domain aggregate (e.g. "order", "ticket").
	AggregateType string

	// AggregateID is the UUID of the domain aggregate that produced the event.
	AggregateID string

	// EventType is the stable event name (e.g. "v1.order.placed").
	EventType string

	// Payload is the arbitrary event data serialised as JSON.
	Payload map[string]any

	// OccurredAt is the wall-clock time the event was created.
	// If zero, the database NOW() default is used.
	OccurredAt time.Time
}

// Writer persists Event rows to the outbox table within a caller-supplied
// transaction.  The Write operation is deliberately limited to INSERT in the
// same transaction as the domain mutation so that delivery and mutation are
// atomic — either both commit or both roll back.
//
// Implementations must be safe for concurrent use.
type Writer interface {
	Append(ctx context.Context, tx pgx.Tx, event Event) error
}

// -----------------------------------------------------------------------------
// PostgreSQL implementation
// -----------------------------------------------------------------------------

// PGWriter is the production outbox writer backed by the outbox table.
type PGWriter struct {
	pool *pgxpool.Pool
}

// NewPGWriter constructs a PGWriter around a live pgx pool.  The pool is
// retained only for health-check purposes; all writes go through the
// caller-supplied pgx.Tx in Append.
func NewPGWriter(pool *pgxpool.Pool) *PGWriter { return &PGWriter{pool: pool} }

// Append inserts event into the outbox table using the supplied transaction.
// The INSERT covers all required columns; dispatched_at is intentionally
// omitted so the database leaves it NULL (undelivered).
//
// Returns an error if tx is nil, if the event fails validation, or if the
// INSERT itself fails.
func (w *PGWriter) Append(ctx context.Context, tx pgx.Tx, event Event) error {
	if tx == nil {
		return errors.New("outbox: Append requires a non-nil tx")
	}
	args, err := prepareArgs(event)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertSQL, args...); err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}

// insertSQL writes one row to the outbox table.  occurred_at defaults to the
// event's OccurredAt when non-zero; otherwise the DB now() default fires.
// dispatched_at is left NULL (not in the column list) to mark the event as
// pending delivery.
const insertSQL = `
	INSERT INTO outbox
	    (aggregate_type, aggregate_id, event_type, payload, occurred_at)
	VALUES
	    ($1, $2::uuid, $3, $4::jsonb, COALESCE($5, now()))
`

func prepareArgs(ev Event) ([]any, error) {
	if ev.AggregateType == "" {
		return nil, errors.New("outbox: AggregateType is required")
	}
	if ev.AggregateID == "" {
		return nil, errors.New("outbox: AggregateID is required")
	}
	if ev.EventType == "" {
		return nil, errors.New("outbox: EventType is required")
	}
	if ev.Payload == nil {
		ev.Payload = map[string]any{}
	}
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("outbox: marshal payload: %w", err)
	}
	// Use nil for occurred_at so COALESCE falls through to now().
	var occurredAt *time.Time
	if !ev.OccurredAt.IsZero() {
		t := ev.OccurredAt.UTC()
		occurredAt = &t
	}
	return []any{
		ev.AggregateType,
		ev.AggregateID,
		ev.EventType,
		string(payload),
		occurredAt,
	}, nil
}

// -----------------------------------------------------------------------------
// Compile-time interface guard
// -----------------------------------------------------------------------------

var _ Writer = (*PGWriter)(nil)
