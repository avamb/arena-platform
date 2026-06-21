// Tests for the logging package.
//
// The unit tests use a bytes.Buffer-backed JSON handler so each log record can
// be parsed and asserted on. They exercise:
//
//   - New(...) writes valid JSON at the requested level.
//   - NewWithOptions(...) attaches app/env/version default attributes.
//   - Defaults(...) honours explicit overrides and the APP_ENV fallback.
//   - WithLogger / FromContext round-trip a custom logger.
//   - FromContext enriches records with request_id, correlation_id, and
//     trace_id values stored on the context.
//   - parseLevel honours unknown values and defaults to Info.
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// decode parses the most recent line written to buf as a JSON object. The
// JSONHandler emits one record per line, so we keep the last non-empty line
// to make multi-record tests forward-compatible.
func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	raw := strings.TrimSpace(buf.String())
	if raw == "" {
		t.Fatalf("decode: no log output captured")
	}
	lines := strings.Split(raw, "\n")
	last := lines[len(lines)-1]
	var out map[string]any
	if err := json.Unmarshal([]byte(last), &out); err != nil {
		t.Fatalf("decode: invalid JSON %q: %v", last, err)
	}
	return out
}

func TestNew_EmitsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "json", "info")
	logger.Info("hello", "key", "value")

	rec := decode(t, &buf)
	if rec["msg"] != "hello" {
		t.Fatalf("msg: want %q, got %v", "hello", rec["msg"])
	}
	if rec["level"] != "INFO" {
		t.Fatalf("level: want INFO, got %v", rec["level"])
	}
	if rec["key"] != "value" {
		t.Fatalf("key: want value, got %v", rec["key"])
	}
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "json", "warn")
	logger.Info("ignored")
	if buf.Len() != 0 {
		t.Fatalf("Info at warn level should produce no output, got %q", buf.String())
	}
	logger.Warn("kept")
	rec := decode(t, &buf)
	if rec["msg"] != "kept" {
		t.Fatalf("msg: want kept, got %v", rec["msg"])
	}
}

func TestNew_UnknownLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "json", "verbose-bogus")
	logger.Debug("suppressed")
	if buf.Len() != 0 {
		t.Fatalf("Debug should be suppressed at info default, got %q", buf.String())
	}
	logger.Info("kept")
	rec := decode(t, &buf)
	if rec["msg"] != "kept" {
		t.Fatalf("msg: want kept, got %v", rec["msg"])
	}
}

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "text", "info")
	logger.Info("textmsg")
	out := buf.String()
	if !strings.Contains(out, "msg=textmsg") {
		t.Fatalf("text format missing msg=textmsg, got %q", out)
	}
	// Text records aren't valid JSON; ensure we did not accidentally
	// fall back to the JSON handler.
	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("text format produced JSON: %q", out)
	}
}

func TestNewWithOptions_AttachesDefaultFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithOptions(Options{
		Writer:  &buf,
		Format:  "json",
		Level:   "info",
		App:     "arena-api",
		Env:     "production",
		Version: "1.2.3",
	})
	logger.Info("boot")

	rec := decode(t, &buf)
	if rec[FieldApp] != "arena-api" {
		t.Fatalf("%s: want arena-api, got %v", FieldApp, rec[FieldApp])
	}
	if rec[FieldEnv] != "production" {
		t.Fatalf("%s: want production, got %v", FieldEnv, rec[FieldEnv])
	}
	if rec[FieldVersion] != "1.2.3" {
		t.Fatalf("%s: want 1.2.3, got %v", FieldVersion, rec[FieldVersion])
	}
}

func TestDefaults_HonoursOverrides(t *testing.T) {
	attrs := Defaults("svc", "staging", "9.9.9")
	want := map[string]string{
		FieldApp:     "svc",
		FieldEnv:     "staging",
		FieldVersion: "9.9.9",
	}
	got := attrsToMap(t, attrs)
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Defaults[%s]: want %q, got %q", k, v, got[k])
		}
	}
}

func TestDefaults_FallsBackToAppEnv(t *testing.T) {
	// Setenv automatically restores the previous value when the test ends.
	t.Setenv("APP_ENV", "development")
	attrs := Defaults("svc", "", "")
	got := attrsToMap(t, attrs)
	if got[FieldEnv] != "development" {
		t.Fatalf("Defaults env fallback: want development, got %q", got[FieldEnv])
	}
}

func TestWithLogger_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	custom := New(&buf, "json", "info")
	ctx := WithLogger(context.Background(), custom)

	got := FromContext(ctx)
	got.Info("via ctx")
	rec := decode(t, &buf)
	if rec["msg"] != "via ctx" {
		t.Fatalf("FromContext did not return custom logger; got rec %+v", rec)
	}
}

func TestWithLogger_NilLoggerNoOp(t *testing.T) {
	ctx := context.Background()
	out := WithLogger(ctx, nil)
	if out != ctx {
		t.Fatalf("WithLogger(nil) must return ctx unchanged")
	}
}

func TestFromContext_NilContextUsesDefault(t *testing.T) {
	// Should not panic and should return slog.Default().
	got := FromContext(context.TODO())
	if got == nil {
		t.Fatal("FromContext returned nil logger")
	}
}

func TestFromContext_AttachesCorrelationIDs(t *testing.T) {
	var buf bytes.Buffer
	base := New(&buf, "json", "info")
	ctx := WithLogger(context.Background(), base)
	ctx = WithRequestID(ctx, "req-123")
	ctx = WithCorrelationID(ctx, "corr-456")
	ctx = WithTraceID(ctx, "trace-789")

	FromContext(ctx).Info("event")

	rec := decode(t, &buf)
	if rec[FieldRequestID] != "req-123" {
		t.Fatalf("%s: want req-123, got %v", FieldRequestID, rec[FieldRequestID])
	}
	if rec[FieldCorrelationID] != "corr-456" {
		t.Fatalf("%s: want corr-456, got %v", FieldCorrelationID, rec[FieldCorrelationID])
	}
	if rec[FieldTraceID] != "trace-789" {
		t.Fatalf("%s: want trace-789, got %v", FieldTraceID, rec[FieldTraceID])
	}
}

func TestFromContext_OmitsUnsetIDs(t *testing.T) {
	var buf bytes.Buffer
	base := New(&buf, "json", "info")
	ctx := WithLogger(context.Background(), base)
	// Only set request_id; correlation_id and trace_id should be absent.
	ctx = WithRequestID(ctx, "req-only")

	FromContext(ctx).Info("event")

	rec := decode(t, &buf)
	if rec[FieldRequestID] != "req-only" {
		t.Fatalf("%s: want req-only, got %v", FieldRequestID, rec[FieldRequestID])
	}
	if _, ok := rec[FieldCorrelationID]; ok {
		t.Fatalf("%s should be absent when not set", FieldCorrelationID)
	}
	if _, ok := rec[FieldTraceID]; ok {
		t.Fatalf("%s should be absent when not set", FieldTraceID)
	}
}

func TestIDHelpers_EmptyOnUnsetContext(t *testing.T) {
	ctx := context.Background()
	if got := RequestID(ctx); got != "" {
		t.Fatalf("RequestID: want \"\", got %q", got)
	}
	if got := CorrelationID(ctx); got != "" {
		t.Fatalf("CorrelationID: want \"\", got %q", got)
	}
	if got := TraceID(ctx); got != "" {
		t.Fatalf("TraceID: want \"\", got %q", got)
	}
	// Nil context paths should be safe too.
	//nolint:staticcheck // intentional nil-context probe
	if got := RequestID(nil); got != "" {
		t.Fatalf("RequestID(nil): want \"\", got %q", got)
	}
}

func TestIDHelpers_EmptyIDsAreNoOp(t *testing.T) {
	ctx := context.Background()
	if WithRequestID(ctx, "") != ctx {
		t.Fatal("WithRequestID(\"\") must return ctx unchanged")
	}
	if WithCorrelationID(ctx, "") != ctx {
		t.Fatal("WithCorrelationID(\"\") must return ctx unchanged")
	}
	if WithTraceID(ctx, "") != ctx {
		t.Fatal("WithTraceID(\"\") must return ctx unchanged")
	}
}

// attrsToMap converts a []any returned by Defaults() into a map keyed by attr
// name. It expects only slog.Attr values; any other type is a test bug.
func attrsToMap(t *testing.T, attrs []any) map[string]string {
	t.Helper()
	out := make(map[string]string, len(attrs))
	for _, a := range attrs {
		attr, ok := a.(slog.Attr)
		if !ok {
			t.Fatalf("attrsToMap: unexpected type %T", a)
		}
		out[attr.Key] = attr.Value.String()
	}
	return out
}
