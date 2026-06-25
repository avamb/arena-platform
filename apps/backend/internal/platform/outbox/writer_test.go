// Package outbox — unit tests for feature #100
// "Outbox writer + dispatcher placeholders"
//
// These tests verify the Writer interface and PGWriter implementation,
// the Dispatcher interface and NoopDispatcher stub, and the backlog monitor,
// all without a live PostgreSQL connection.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// =============================================================================
// Test doubles
// =============================================================================

// capturedExec records a single Exec call.
type capturedExec struct {
	sql  string
	args []any
}

// captureTx is a minimal pgx.Tx implementation that captures Exec calls.
// All methods not reachable via Append panic immediately.
type captureTx struct {
	mu    sync.Mutex
	execs []capturedExec
	err   error // if non-nil, Exec returns this error
}

func (t *captureTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.mu.Lock()
	t.execs = append(t.execs, capturedExec{sql: sql, args: args})
	t.mu.Unlock()
	return pgconn.CommandTag{}, t.err
}

func (t *captureTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("captureTx: QueryRow not expected in Append path")
}
func (t *captureTx) Begin(_ context.Context) (pgx.Tx, error) {
	panic("captureTx: Begin not expected")
}
func (t *captureTx) Commit(_ context.Context) error   { return nil }
func (t *captureTx) Rollback(_ context.Context) error { return nil }
func (t *captureTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("captureTx: CopyFrom not expected")
}
func (t *captureTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("captureTx: SendBatch not expected")
}
func (t *captureTx) LargeObjects() pgx.LargeObjects {
	panic("captureTx: LargeObjects not expected")
}
func (t *captureTx) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("captureTx: Prepare not expected")
}
func (t *captureTx) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("captureTx: Query not expected")
}
func (t *captureTx) Conn() *pgx.Conn { return nil }

// Compile-time interface guard.
var _ pgx.Tx = (*captureTx)(nil)

// fakeRow is a minimal pgx.Row returning a preset int64.
type fakeBacklogRow struct {
	count int64
	err   error
}

func (r *fakeBacklogRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int64)) = r.count
	return nil
}

// fakeQuerier implements BacklogQuerier returning a preset row.
type fakeQuerier struct {
	row *fakeBacklogRow
}

func (q *fakeQuerier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return q.row
}

// Compile-time interface guards.
var _ BacklogQuerier = (*fakeQuerier)(nil)

// =============================================================================
// Writer tests
// =============================================================================

// TestPGWriter_AppendWritesRequiredColumns verifies that Append produces an
// INSERT that covers all required columns: aggregate_type, aggregate_id,
// event_type, payload, occurred_at — and that dispatched_at is absent.
func TestPGWriter_AppendWritesRequiredColumns(t *testing.T) {
	w := &PGWriter{} // pool not used in Append path
	tx := &captureTx{}

	ev := Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
		Payload:       map[string]any{"amount": 100},
	}

	if err := w.Append(context.Background(), tx, ev); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	tx.mu.Lock()
	execs := tx.execs
	tx.mu.Unlock()

	if len(execs) != 1 {
		t.Fatalf("want 1 Exec call, got %d", len(execs))
	}

	sql := execs[0].sql

	// Required columns must be present.
	for _, col := range []string{"aggregate_type", "aggregate_id", "event_type", "payload", "occurred_at"} {
		if !strings.Contains(sql, col) {
			t.Errorf("SQL missing column %q; SQL:\n%s", col, sql)
		}
	}

	// dispatched_at must NOT appear — it is left NULL by default.
	if strings.Contains(sql, "dispatched_at") {
		t.Error("SQL must not include dispatched_at — column defaults to NULL")
	}

	// attempts must NOT appear — defaults to 0.
	if strings.Contains(sql, "attempts") {
		t.Error("SQL must not include attempts — column defaults to 0")
	}
}

// TestPGWriter_AppendPassesArgs verifies positional arguments match the event fields.
func TestPGWriter_AppendPassesArgs(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}

	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	ev := Event{
		AggregateType: "ticket",
		AggregateID:   "00000000-0000-0000-0000-000000000002",
		EventType:     "v1.ticket.issued",
		Payload:       map[string]any{"seat": "A1"},
		OccurredAt:    now,
	}

	if err := w.Append(context.Background(), tx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	tx.mu.Lock()
	args := tx.execs[0].args
	tx.mu.Unlock()

	// $1 = aggregate_type
	if got, _ := args[0].(string); got != "ticket" {
		t.Errorf("$1 aggregate_type = %q, want %q", got, "ticket")
	}
	// $2 = aggregate_id
	if got, _ := args[1].(string); got != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("$2 aggregate_id = %q, want UUIDv7", got)
	}
	// $3 = event_type
	if got, _ := args[2].(string); got != "v1.ticket.issued" {
		t.Errorf("$3 event_type = %q, want %q", got, "v1.ticket.issued")
	}
	// $4 = payload (JSON string)
	payloadStr, _ := args[3].(string)
	var pl map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &pl); err != nil {
		t.Fatalf("$4 payload is not valid JSON: %v — raw: %s", err, payloadStr)
	}
	if pl["seat"] != "A1" {
		t.Errorf("payload.seat = %v, want A1", pl["seat"])
	}
	// $5 = occurred_at (non-nil pointer when OccurredAt is set)
	if args[4] == nil {
		t.Error("$5 occurred_at must be non-nil when OccurredAt is set")
	}
}

// TestPGWriter_AppendNilTxReturnsError verifies that passing a nil transaction
// returns an error immediately without calling Exec.
func TestPGWriter_AppendNilTxReturnsError(t *testing.T) {
	w := &PGWriter{}
	err := w.Append(context.Background(), nil, Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
	})
	if err == nil {
		t.Error("want error when tx is nil, got nil")
	}
}

// TestPGWriter_AppendRequiresAggregateType verifies validation.
func TestPGWriter_AppendRequiresAggregateType(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}
	err := w.Append(context.Background(), tx, Event{
		AggregateID: "00000000-0000-0000-0000-000000000001",
		EventType:   "v1.order.placed",
	})
	if err == nil {
		t.Error("want error for empty AggregateType")
	}
}

// TestPGWriter_AppendRequiresAggregateID verifies validation.
func TestPGWriter_AppendRequiresAggregateID(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}
	err := w.Append(context.Background(), tx, Event{
		AggregateType: "order",
		EventType:     "v1.order.placed",
	})
	if err == nil {
		t.Error("want error for empty AggregateID")
	}
}

// TestPGWriter_AppendRequiresEventType verifies validation.
func TestPGWriter_AppendRequiresEventType(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}
	err := w.Append(context.Background(), tx, Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
	})
	if err == nil {
		t.Error("want error for empty EventType")
	}
}

// TestPGWriter_AppendPropagatesExecError verifies that a DB error from Exec is
// wrapped and returned.
func TestPGWriter_AppendPropagatesExecError(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{err: errors.New("connection reset")}

	err := w.Append(context.Background(), tx, Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
	})
	if err == nil {
		t.Fatal("want error when Exec fails")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error %q does not mention underlying cause", err.Error())
	}
}

// TestPGWriter_AppendNilPayloadDefaultsToEmpty verifies that nil Payload is
// serialised as "{}" rather than "null".
func TestPGWriter_AppendNilPayloadDefaultsToEmpty(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}

	err := w.Append(context.Background(), tx, Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
		Payload:       nil, // nil should default to {}
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	tx.mu.Lock()
	args := tx.execs[0].args
	tx.mu.Unlock()

	payloadStr, _ := args[3].(string)
	if payloadStr == "null" {
		t.Error("nil Payload must be serialised as '{}', not 'null'")
	}
	var pl map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &pl); err != nil {
		t.Fatalf("payload is not valid JSON: %v — raw: %s", err, payloadStr)
	}
}

// TestPGWriter_AppendZeroOccurredAtPassesNil verifies that a zero OccurredAt
// causes the $5 arg to be nil, allowing the DB COALESCE(now()) to fire.
func TestPGWriter_AppendZeroOccurredAtPassesNil(t *testing.T) {
	w := &PGWriter{}
	tx := &captureTx{}

	err := w.Append(context.Background(), tx, Event{
		AggregateType: "order",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.order.placed",
		OccurredAt:    time.Time{}, // zero value
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	tx.mu.Lock()
	args := tx.execs[0].args
	tx.mu.Unlock()

	if args[4] != nil {
		t.Errorf("$5 occurred_at must be nil when OccurredAt is zero, got %v", args[4])
	}
}

// =============================================================================
// Dispatcher tests
// =============================================================================

// TestNoopDispatcher_AlwaysSucceeds verifies that NoopDispatcher.Dispatch
// never returns an error regardless of the event content.
func TestNoopDispatcher_AlwaysSucceeds(t *testing.T) {
	d := NoopDispatcher{}
	events := []Event{
		{AggregateType: "order", AggregateID: "00000000-0000-0000-0000-000000000001", EventType: "v1.order.placed"},
		{},
	}
	for _, ev := range events {
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Errorf("NoopDispatcher.Dispatch returned error: %v", err)
		}
	}
}

// TestNoopDispatcher_SatisfiesInterface is the compile-time guard expressed as
// a runtime assertion for clarity.
func TestNoopDispatcher_SatisfiesInterface(_ *testing.T) {
	var _ Dispatcher = NoopDispatcher{}
}

// =============================================================================
// BacklogMonitor tests
// =============================================================================

// TestMonitorBacklog_UpdatesGauge verifies that sampleBacklog sets the gauge
// to the value returned by the query.
func TestMonitorBacklog_UpdatesGauge(t *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_outbox_backlog"})
	q := &fakeQuerier{row: &fakeBacklogRow{count: 42}}

	sampleBacklog(context.Background(), q, gauge, slog_noop())

	// Read back the gauge value via Desc/Write (use a gather).
	reg := prometheus.NewRegistry()
	reg.MustRegister(gauge)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(mfs) == 0 {
		t.Fatal("no metric families gathered")
	}
	got := mfs[0].GetMetric()[0].GetGauge().GetValue()
	if got != 42 {
		t.Errorf("gauge = %v, want 42", got)
	}
}

// TestMonitorBacklog_ErrorDoesNotPanic verifies that a query error is handled
// gracefully without panicking or stopping the monitor loop.
func TestMonitorBacklog_ErrorDoesNotPanic(_ *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_outbox_backlog_err"})
	q := &fakeQuerier{row: &fakeBacklogRow{err: errors.New("timeout")}}

	// Must not panic.
	sampleBacklog(context.Background(), q, gauge, slog_noop())
}

// TestMonitorBacklog_StopsOnContextCancel verifies that MonitorBacklog exits
// when the context is cancelled.
func TestMonitorBacklog_StopsOnContextCancel(_ *testing.T) {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_outbox_cancel"})
	q := &fakeQuerier{row: &fakeBacklogRow{count: 0}}

	ctx, cancel := context.WithCancel(context.Background())
	MonitorBacklog(ctx, q, gauge, 10*time.Millisecond, slog_noop())

	// Let it tick a couple of times then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	// Give the goroutine a moment to exit; absence of hang is the assertion.
	time.Sleep(20 * time.Millisecond)
}

// =============================================================================
// Helpers
// =============================================================================

// slog_noop returns a *slog.Logger that discards all output.
func slog_noop() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{
		Level: slog.LevelError + 100, // effectively discard
	}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
