// Package audit — unit tests for feature #99: Audit writer boundary.
//
// Verifies that SlogWriter records every Event as a structured slog entry
// with the expected fields and "audit" category. All 5 feature steps covered:
//
//	Step 1: Event type has all required fields (ActorID, Action, ResourceType,
//	         ResourceID, Metadata, OccurredAt).
//	Step 2: Writer interface exposes Write(ctx, event) error.
//	Step 3: SlogWriter writes the event to the slog logger with category "audit".
//	Step 4: Helper functions WriteEvent and NewEvent available for workflows.
//	Step 5: Unit test — event written to logger with expected fields and category.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// =============================================================================
// capturingSlogHandler — captures all log records into a buffer as JSON
// =============================================================================

// capturingHandler is a slog.Handler that appends each record as a JSON line to
// buf. It is used to assert on log output in tests without mocking.
type capturingHandler struct {
	buf *bytes.Buffer
}

func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// parseLastRecord parses the last JSON line written to buf into a
// map[string]any for field assertions.
func parseLastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("capturingHandler: no log records written")
	}
	// Use the last non-empty line.
	lines := strings.Split(line, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = strings.TrimSpace(lines[i])
			break
		}
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(last), &rec); err != nil {
		t.Fatalf("capturingHandler: unmarshal log record: %v — raw: %s", err, last)
	}
	return rec
}

// =============================================================================
// Step 1: Event struct has all required fields
// =============================================================================

// TestAuditEvent_RequiredFields ensures the Event type carries every field
// required by the feature spec: ActorID, Action, ResourceType, ResourceID,
// Metadata, OccurredAt. Additional fields (ActorType, RequestID, TraceID, IP)
// are present and also verified.
func TestAuditEvent_RequiredFields(t *testing.T) {
	ev := Event{
		ActorID:      "actor-1",
		ActorType:    "user",
		Action:       "example.create",
		ResourceType: "example",
		ResourceID:   "res-1",
		Metadata:     map[string]any{"key": "value"},
		OccurredAt:   time.Now().UTC(),
		RequestID:    "req-1",
		TraceID:      "trace-1",
		IP:           "127.0.0.1",
	}

	// All required fields must be settable and readable.
	if ev.ActorID != "actor-1" {
		t.Errorf("ActorID: want actor-1, got %q", ev.ActorID)
	}
	if ev.Action != "example.create" {
		t.Errorf("Action: want example.create, got %q", ev.Action)
	}
	if ev.ResourceType != "example" {
		t.Errorf("ResourceType: want example, got %q", ev.ResourceType)
	}
	if ev.ResourceID != "res-1" {
		t.Errorf("ResourceID: want res-1, got %q", ev.ResourceID)
	}
	if ev.Metadata["key"] != "value" {
		t.Errorf("Metadata: want key=value, got %v", ev.Metadata)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("OccurredAt must not be zero")
	}
	if ev.ActorType != "user" {
		t.Errorf("ActorType: want user, got %q", ev.ActorType)
	}
	if ev.RequestID != "req-1" {
		t.Errorf("RequestID: want req-1, got %q", ev.RequestID)
	}
	if ev.TraceID != "trace-1" {
		t.Errorf("TraceID: want trace-1, got %q", ev.TraceID)
	}
	if ev.IP != "127.0.0.1" {
		t.Errorf("IP: want 127.0.0.1, got %q", ev.IP)
	}
}

// =============================================================================
// Step 2: Writer interface
// =============================================================================

// TestWriter_InterfaceHasWriteMethod verifies the Writer interface compiles and
// exposes Write(ctx, Event) error via the SlogWriter implementation.
func TestWriter_InterfaceHasWriteMethod(t *testing.T) {
	logger, _ := newCapturingLogger()
	w := NewSlogWriter(logger)

	// Compile-time check: SlogWriter satisfies Writer.
	var _ Writer = w

	// Runtime check: Write returns nil (no DB involved).
	ev := Event{
		Action:       "test.action",
		ResourceType: "test",
		ResourceID:   "1",
	}
	if err := w.Write(context.Background(), ev); err != nil {
		t.Errorf("Write returned error: %v", err)
	}
}

// =============================================================================
// Step 3: SlogWriter writes event with category "audit"
// =============================================================================

// TestSlogWriter_CategoryIsAudit verifies that every log record emitted by
// SlogWriter has a top-level "category" field equal to "audit".
func TestSlogWriter_CategoryIsAudit(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	ev := Event{
		Action:       "test.create",
		ResourceType: "item",
		ResourceID:   "42",
	}
	if err := w.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec := parseLastRecord(t, buf)
	if got, ok := rec["category"].(string); !ok || got != "audit" {
		t.Errorf("category: want %q, got %v", "audit", rec["category"])
	}
}

// TestSlogWriter_AllEventFieldsPresent verifies that every field of the Event
// struct appears in the slog record emitted by Write.
func TestSlogWriter_AllEventFieldsPresent(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	ev := Event{
		ActorType:    "user",
		ActorID:      "00000000-0000-0000-0000-000000000001",
		Action:       "item.create",
		ResourceType: "item",
		ResourceID:   "00000000-0000-0000-0000-000000000002",
		RequestID:    "req-abc",
		TraceID:      "trace-xyz",
		IP:           "192.168.1.1",
		Metadata:     map[string]any{"plan": "pro"},
		OccurredAt:   time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
	}

	if err := w.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec := parseLastRecord(t, buf)

	checks := map[string]string{
		"category":      "audit",
		"actor_type":    "user",
		"actor_id":      "00000000-0000-0000-0000-000000000001",
		"action":        "item.create",
		"resource_type": "item",
		"resource_id":   "00000000-0000-0000-0000-000000000002",
		"request_id":    "req-abc",
		"trace_id":      "trace-xyz",
		"ip":            "192.168.1.1",
	}
	for field, want := range checks {
		got, ok := rec[field].(string)
		if !ok || got != want {
			t.Errorf("field %q: want %q, got %v", field, want, rec[field])
		}
	}

	// Metadata is a nested object in the JSON.
	if meta, ok := rec["metadata"].(map[string]any); !ok {
		t.Errorf("metadata field missing or wrong type, got %T %v", rec["metadata"], rec["metadata"])
	} else if meta["plan"] != "pro" {
		t.Errorf("metadata.plan: want pro, got %v", meta["plan"])
	}

	// occurred_at must be present (slog renders it as a string).
	if rec["occurred_at"] == nil {
		t.Error("occurred_at field must be present in log record")
	}
}

// TestSlogWriter_MessageIsAuditEvent verifies the log message.
func TestSlogWriter_MessageIsAuditEvent(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	ev := Event{Action: "x", ResourceType: "y", ResourceID: "z"}
	if err := w.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec := parseLastRecord(t, buf)
	if got, ok := rec["msg"].(string); !ok || got != "audit event" {
		t.Errorf("msg: want %q, got %v", "audit event", rec["msg"])
	}
}

// TestSlogWriter_WriteTxIgnoresTx ensures WriteTx records the event even when
// called with a nil pgx.Tx — the transaction is ignored by SlogWriter.
func TestSlogWriter_WriteTxIgnoresTx(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	ev := Event{
		Action:       "item.update",
		ResourceType: "item",
		ResourceID:   "99",
		ActorID:      "actor-2",
	}
	// nil tx is fine for SlogWriter.
	if err := w.WriteTx(context.Background(), nil, ev); err != nil {
		t.Fatalf("WriteTx: %v", err)
	}

	rec := parseLastRecord(t, buf)
	if got, ok := rec["category"].(string); !ok || got != "audit" {
		t.Errorf("category: want %q, got %v", "audit", rec["category"])
	}
	if got, ok := rec["actor_id"].(string); !ok || got != "actor-2" {
		t.Errorf("actor_id: want actor-2, got %v", rec["actor_id"])
	}
}

// TestSlogWriter_WriteTxNilTxNotPanics verifies WriteTx(ctx, nil, ev) does not
// panic — SlogWriter explicitly ignores the transaction argument.
func TestSlogWriter_WriteTxNilTxNotPanics(t *testing.T) {
	logger, _ := newCapturingLogger()
	w := NewSlogWriter(logger)

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WriteTx with nil tx panicked: %v", r)
		}
	}()

	var nilTx pgx.Tx
	ev := Event{Action: "a", ResourceType: "b", ResourceID: "c"}
	_ = w.WriteTx(context.Background(), nilTx, ev)
}

// TestSlogWriter_NilLoggerFallsBackToDefault verifies NewSlogWriter(nil) does
// not panic — it falls back to slog.Default().
func TestSlogWriter_NilLoggerFallsBackToDefault(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewSlogWriter(nil) panicked: %v", r)
		}
	}()

	w := NewSlogWriter(nil)
	ev := Event{Action: "a", ResourceType: "b", ResourceID: "c"}
	// Just ensure it does not panic; we cannot capture default logger output
	// without replacing it globally.
	_ = w.Write(context.Background(), ev)
}

// TestSlogWriter_NilMetadataBecomesEmptyMap verifies that an Event with nil
// Metadata is logged with an empty object rather than panicking.
func TestSlogWriter_NilMetadataBecomesEmptyMap(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	ev := Event{
		Action:       "test",
		ResourceType: "x",
		ResourceID:   "1",
		Metadata:     nil, // explicitly nil
	}
	if err := w.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec := parseLastRecord(t, buf)
	meta, ok := rec["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata field missing or wrong type, got %T %v", rec["metadata"], rec["metadata"])
	}
	if len(meta) != 0 {
		t.Errorf("expected empty metadata map, got %v", meta)
	}
}

// =============================================================================
// Step 4: Helper functions WriteEvent and NewEvent
// =============================================================================

// TestWriteEvent_Helper verifies the WriteEvent convenience function writes a
// log entry through the Writer.
func TestWriteEvent_Helper(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	err := WriteEvent(
		context.Background(), w,
		"user", "actor-3",
		"order.create", "order", "order-1",
		map[string]any{"amount": 100},
	)
	if err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	rec := parseLastRecord(t, buf)
	checks := map[string]string{
		"category":      "audit",
		"actor_type":    "user",
		"actor_id":      "actor-3",
		"action":        "order.create",
		"resource_type": "order",
		"resource_id":   "order-1",
	}
	for field, want := range checks {
		if got, ok := rec[field].(string); !ok || got != want {
			t.Errorf("field %q: want %q, got %v", field, want, rec[field])
		}
	}
}

// TestWriteEvent_OccurredAtIsSetByHelper verifies that WriteEvent always sets
// OccurredAt to a non-zero recent UTC time.
func TestWriteEvent_OccurredAtIsSetByHelper(t *testing.T) {
	logger, buf := newCapturingLogger()
	w := NewSlogWriter(logger)

	before := time.Now().UTC().Add(-time.Second)
	_ = WriteEvent(context.Background(), w, "user", "a", "b.c", "b", "1", nil)
	after := time.Now().UTC().Add(time.Second)

	rec := parseLastRecord(t, buf)
	raw, ok := rec["occurred_at"]
	if !ok {
		t.Fatal("occurred_at field missing from log record")
	}
	// slog.Time renders as RFC3339Nano string in JSON handler.
	ts, err := time.Parse(time.RFC3339Nano, raw.(string))
	if err != nil {
		t.Fatalf("parse occurred_at %q: %v", raw, err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("occurred_at %v outside expected window [%v, %v]", ts, before, after)
	}
}

// TestNewEvent_Helper verifies the NewEvent constructor fills required fields.
func TestNewEvent_Helper(t *testing.T) {
	ev := NewEvent("service", "svc-1", "ticket.issue", "ticket", "t-123")

	if ev.ActorType != "service" {
		t.Errorf("ActorType: want service, got %q", ev.ActorType)
	}
	if ev.ActorID != "svc-1" {
		t.Errorf("ActorID: want svc-1, got %q", ev.ActorID)
	}
	if ev.Action != "ticket.issue" {
		t.Errorf("Action: want ticket.issue, got %q", ev.Action)
	}
	if ev.ResourceType != "ticket" {
		t.Errorf("ResourceType: want ticket, got %q", ev.ResourceType)
	}
	if ev.ResourceID != "t-123" {
		t.Errorf("ResourceID: want t-123, got %q", ev.ResourceID)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("OccurredAt must not be zero")
	}
	if ev.Metadata == nil {
		t.Error("Metadata must not be nil (empty map expected)")
	}
}

// =============================================================================
// Step 5: Full verification — all feature steps as sub-tests
// =============================================================================

// TestAuditWriterBoundary_FullVerification runs all 5 feature steps as subtests
// to give a clear pass/fail summary matching the feature step list.
func TestAuditWriterBoundary_FullVerification(t *testing.T) {
	t.Run("step1_event_has_required_fields", func(t *testing.T) {
		ev := NewEvent("user", "u1", "a.b", "res", "1")
		fields := []string{ev.ActorType, ev.ActorID, ev.Action, ev.ResourceType, ev.ResourceID}
		for i, f := range fields {
			if f == "" {
				t.Errorf("field[%d] is empty", i)
			}
		}
		if ev.Metadata == nil {
			t.Error("Metadata must not be nil")
		}
		if ev.OccurredAt.IsZero() {
			t.Error("OccurredAt must not be zero")
		}
	})

	t.Run("step2_writer_interface_write_signature", func(t *testing.T) {
		// Verifies Writer.Write(ctx, Event) error compiles with SlogWriter.
		logger, _ := newCapturingLogger()
		var w Writer = NewSlogWriter(logger)
		ev := NewEvent("user", "u2", "x.y", "x", "2")
		if err := w.Write(context.Background(), ev); err != nil {
			t.Errorf("Write: %v", err)
		}
	})

	t.Run("step3_slog_writer_category_is_audit", func(t *testing.T) {
		logger, buf := newCapturingLogger()
		w := NewSlogWriter(logger)
		ev := NewEvent("user", "u3", "p.q", "p", "3")
		_ = w.Write(context.Background(), ev)
		rec := parseLastRecord(t, buf)
		if cat, _ := rec["category"].(string); cat != "audit" {
			t.Errorf("category: want audit, got %v", rec["category"])
		}
	})

	t.Run("step4_helper_WriteEvent_available", func(t *testing.T) {
		logger, buf := newCapturingLogger()
		w := NewSlogWriter(logger)
		err := WriteEvent(context.Background(), w, "user", "u4", "m.n", "m", "4", nil)
		if err != nil {
			t.Errorf("WriteEvent: %v", err)
		}
		rec := parseLastRecord(t, buf)
		if rec["action"] != "m.n" {
			t.Errorf("action: want m.n, got %v", rec["action"])
		}
	})

	t.Run("step4_helper_NewEvent_available", func(t *testing.T) {
		ev := NewEvent("svc", "s1", "d.e", "d", "5")
		if ev.Action != "d.e" {
			t.Errorf("NewEvent action: want d.e, got %q", ev.Action)
		}
	})

	t.Run("step5_all_fields_written_to_logger", func(t *testing.T) {
		logger, buf := newCapturingLogger()
		w := NewSlogWriter(logger)
		ev := Event{
			ActorType:    "admin",
			ActorID:      "a1",
			Action:       "cfg.update",
			ResourceType: "config",
			ResourceID:   "cfg-1",
			RequestID:    "rq-1",
			TraceID:      "tr-1",
			IP:           "10.0.0.1",
			Metadata:     map[string]any{"env": "prod"},
			OccurredAt:   time.Now().UTC(),
		}
		_ = w.Write(context.Background(), ev)
		rec := parseLastRecord(t, buf)

		mustHave := []string{
			"category", "actor_type", "actor_id", "action",
			"resource_type", "resource_id", "request_id", "trace_id",
			"ip", "metadata", "occurred_at",
		}
		for _, key := range mustHave {
			if rec[key] == nil {
				t.Errorf("missing field %q in log record", key)
			}
		}
		if rec["category"] != "audit" {
			t.Errorf("category: want audit, got %v", rec["category"])
		}
	})
}
