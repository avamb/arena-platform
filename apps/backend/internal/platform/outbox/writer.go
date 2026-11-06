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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event is the value-object written to the outbox table.  AggregateID must be
// a valid UUID string — it is stored in the aggregate_id uuid column.
type Event struct {
	// ID is the outbox_events row id when the event is being DISPATCHED
	// (set by OutboxEventsDispatcher); empty when the event is being
	// written. Dispatchers that need a per-event identity (the MACS
	// envelope id) use it; the writer ignores it.
	ID string

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
	// Stored as `any` (not *time.Time) so that a zero OccurredAt produces an
	// untyped nil in the []any slice — pgx treats a typed (*time.Time)(nil)
	// differently from an untyped nil when binding parameters.
	var occurredAt any // nil by default (untyped interface nil)
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
// outbox_events writer (the table the dispatchers actually consume)
// -----------------------------------------------------------------------------

// PGEventsWriter is the production outbox writer backed by the outbox_events
// table — the one every dispatcher reads (PGOutboxEventStore, macs.Dispatcher,
// bil24wire.Dispatcher, cmd/arena-worker). The legacy PGWriter above targets
// the older `outbox` table, which no dispatcher polls, so domain events written
// through it were silently never delivered. New wiring must use this writer;
// PGWriter is kept for the legacy table and its tests.
type PGEventsWriter struct {
	pool *pgxpool.Pool
}

// NewPGEventsWriter constructs a PGEventsWriter around a live pgx pool. The
// pool is retained only for health-check purposes; all writes go through the
// caller-supplied pgx.Tx in Append.
func NewPGEventsWriter(pool *pgxpool.Pool) *PGEventsWriter { return &PGEventsWriter{pool: pool} }

// Append inserts event into outbox_events using the supplied transaction.
// processed_at is intentionally omitted so the database leaves it NULL, which
// is what marks the row as pending delivery for the dispatcher poll query.
func (w *PGEventsWriter) Append(ctx context.Context, tx pgx.Tx, event Event) error {
	if tx == nil {
		return errors.New("outbox: Append requires a non-nil tx")
	}
	args, err := prepareArgs(event)
	if err != nil {
		return err
	}
	orderRef, ticketRef := testOrderRefs(event)
	args = append(args, orderRef, ticketRef)
	if _, err := tx.Exec(ctx, insertEventsSQL, args...); err != nil {
		return fmt.Errorf("outbox: insert outbox_events: %w", err)
	}
	return nil
}

// insertEventsSQL writes one row to outbox_events. aggregate_id is a text
// column there (unlike the uuid column on the legacy outbox table), so no cast
// is needed.
//
// P-13: when the event names an order ($6) or a ticket ($7) of a TEST order
// (orders.is_test, a test sales channel), the stored payload gains
// "is_test": true, so the outward legs (MACS, the selling site's webhook)
// can skip it without a lookup of their own (IsTestEvent). It is decided
// here, on the publisher's own transaction, so every publisher of an order
// or ticket event gets it without knowing about test channels.
const insertEventsSQL = `
	INSERT INTO outbox_events
	    (aggregate_type, aggregate_id, event_type, payload, occurred_at)
	SELECT $1, $2, $3,
	       CASE WHEN EXISTS (
	                SELECT 1 FROM orders o
	                WHERE  o.is_test
	                  AND  (o.id = $6::uuid
	                        OR o.id = (SELECT t.order_id FROM tickets t WHERE t.id = $7::uuid)))
	            THEN $4::jsonb || jsonb_build_object('` + PayloadIsTest + `', true)
	            ELSE $4::jsonb
	       END,
	       COALESCE($5, now())
`

// PayloadIsTest is the payload key that marks an event of a test order.
const PayloadIsTest = "is_test"

// IsTestEvent reports whether the event belongs to a test order (P-13). The
// MACS and selling-site legs skip such an event; the Telegram sales leg
// still delivers it with its TEST prefix.
func IsTestEvent(ev Event) bool {
	v, _ := ev.Payload[PayloadIsTest].(bool)
	return v
}

// testOrderRefs is the order and ticket an event names, for the test-order
// stamp of insertEventsSQL: the payload's order_id / ticket_id, else the
// aggregate id of an "order" / "ticket" aggregate. Untyped nil when absent
// or not a UUID, and both nil when the publisher already decided is_test.
func testOrderRefs(ev Event) (orderRef, ticketRef any) {
	if _, decided := ev.Payload[PayloadIsTest]; decided {
		return nil, nil
	}
	pick := func(key, aggregate string) any {
		if s, ok := ev.Payload[key].(string); ok {
			if id, err := uuid.Parse(s); err == nil {
				return id
			}
		}
		if ev.AggregateType == aggregate {
			if id, err := uuid.Parse(ev.AggregateID); err == nil {
				return id
			}
		}
		return nil
	}
	return pick("order_id", "order"), pick("ticket_id", "ticket")
}

// -----------------------------------------------------------------------------
// Compile-time interface guard
// -----------------------------------------------------------------------------

var (
	_ Writer = (*PGWriter)(nil)
	_ Writer = (*PGEventsWriter)(nil)
)
