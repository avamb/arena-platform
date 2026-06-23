// Package outbox — unit tests for feature #110 "Outbox dispatcher".
//
// Feature #110 is the worker-based publisher that:
//   Step 1: registers OutboxEventsDispatcher in the arena-worker process
//   Step 2: serialises events with stable JSON key ordering
//   Step 3: adds X-Arena-Signature: sha256=<hex> HMAC header
//   Step 4: wires to an HTTP webhook delivery client (WebhookDispatcher)
//   Step 5: integration contract — tx commit → event published;
//            tx rollback → event absent; replay safety guaranteed
//
// These tests do NOT require a live database.  They exercise:
//   - WebhookDispatcher (steps 2–4)
//   - StableJSONPayload / StableJSONMap (step 2)
//   - ComputeHMAC / VerifyHMACSignature (step 3)
//   - In-memory outbox store + OutboxEventsDispatcher (step 5 contract)
//   - Worker main wiring: structural assertions on cmd/arena-worker/main.go
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Step 2: Stable JSON serialisation
// =============================================================================

// TestOutboxDispatcher110_StableJSONMapIsAlphabetical verifies that
// StableJSONMap always serialises map keys in alphabetical order.
func TestOutboxDispatcher110_StableJSONMapIsAlphabetical(t *testing.T) {
	m := map[string]any{
		"zebra":  1,
		"alpha":  2,
		"middle": 3,
	}
	got, err := StableJSONMap(m)
	if err != nil {
		t.Fatalf("StableJSONMap: %v", err)
	}

	// The serialised form must have keys in alphabetical order.
	s := string(got)
	alphaPos := strings.Index(s, `"alpha"`)
	middlePos := strings.Index(s, `"middle"`)
	zebraPos := strings.Index(s, `"zebra"`)

	if alphaPos < 0 || middlePos < 0 || zebraPos < 0 {
		t.Fatalf("expected all keys in output; got: %s", s)
	}
	if !(alphaPos < middlePos && middlePos < zebraPos) {
		t.Errorf("keys not in alphabetical order; positions: alpha=%d middle=%d zebra=%d; json=%s",
			alphaPos, middlePos, zebraPos, s)
	}
}

// TestOutboxDispatcher110_StableJSONMapIsDeterministic verifies that two
// serialisations of the same map always produce the same bytes.
func TestOutboxDispatcher110_StableJSONMapIsDeterministic(t *testing.T) {
	m := map[string]any{
		"event_type":     "v1.echo.created",
		"aggregate_type": "echo",
		"trace_id":       "trace-abc-123",
		"message":        "hello",
	}

	out1, _ := StableJSONMap(m)
	out2, _ := StableJSONMap(m)
	out3, _ := StableJSONMap(m)

	if !bytes.Equal(out1, out2) || !bytes.Equal(out2, out3) {
		t.Errorf("StableJSONMap produced different outputs for identical inputs:\n%s\n%s\n%s",
			out1, out2, out3)
	}
}

// TestOutboxDispatcher110_StableJSONPayloadIsValidJSON verifies that
// StableJSONPayload returns parseable JSON.
func TestOutboxDispatcher110_StableJSONPayloadIsValidJSON(t *testing.T) {
	env := webhookEnvelope{
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		AggregateType: "echo",
		EventType:     "v1.echo.created",
		OccurredAt:    time.Now().UTC(),
		Payload: map[string]any{
			"trace_id": "trace-110",
			"message":  "hello",
		},
	}
	body, err := StableJSONPayload(env)
	if err != nil {
		t.Fatalf("StableJSONPayload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("output is not valid JSON: %v; got: %s", err, body)
	}
	if _, ok := out["event_type"]; !ok {
		t.Errorf("expected 'event_type' field in JSON; got: %s", body)
	}
}

// TestOutboxDispatcher110_StableJSONNilMap verifies that a nil payload
// serialises to "{}" rather than "null".
func TestOutboxDispatcher110_StableJSONNilMap(t *testing.T) {
	got, err := StableJSONMap(nil)
	if err != nil {
		t.Fatalf("StableJSONMap(nil): %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("StableJSONMap(nil) = %q, want {}", got)
	}
}

// =============================================================================
// Step 3: HMAC signature header
// =============================================================================

// TestOutboxDispatcher110_ComputeHMACIsHex verifies ComputeHMAC returns a
// lower-case hex string of the expected SHA-256 HMAC length (64 chars).
func TestOutboxDispatcher110_ComputeHMACIsHex(t *testing.T) {
	sig := ComputeHMAC([]byte("hello"), []byte("secret"))
	if len(sig) != 64 {
		t.Errorf("HMAC hex length = %d, want 64; got %q", len(sig), sig)
	}
	for _, c := range sig {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("HMAC contains non-hex character %q; full value: %s", c, sig)
			break
		}
	}
}

// TestOutboxDispatcher110_ComputeHMACIsDeterministic verifies same input
// always produces same HMAC.
func TestOutboxDispatcher110_ComputeHMACIsDeterministic(t *testing.T) {
	body := []byte(`{"event_type":"v1.echo.created"}`)
	secret := []byte("my-test-secret-key-32chars-padded!")

	sig1 := ComputeHMAC(body, secret)
	sig2 := ComputeHMAC(body, secret)
	if sig1 != sig2 {
		t.Errorf("ComputeHMAC is not deterministic: %q vs %q", sig1, sig2)
	}
}

// TestOutboxDispatcher110_VerifyHMACSignatureValid verifies that a valid
// "sha256=<hex>" header passes verification.
func TestOutboxDispatcher110_VerifyHMACSignatureValid(t *testing.T) {
	body := []byte(`{"event_type":"v1.echo.created","message":"hello"}`)
	secret := []byte("super-secret-key")

	sig := ComputeHMAC(body, secret)
	header := "sha256=" + sig

	if !VerifyHMACSignature(body, secret, header) {
		t.Error("VerifyHMACSignature returned false for a valid signature")
	}
}

// TestOutboxDispatcher110_VerifyHMACSignatureInvalidSecret verifies that
// a different secret fails verification (prevents secret confusion).
func TestOutboxDispatcher110_VerifyHMACSignatureInvalidSecret(t *testing.T) {
	body := []byte(`{"event_type":"v1.echo.created"}`)
	correctSecret := []byte("correct-secret")
	wrongSecret := []byte("wrong-secret")

	sig := ComputeHMAC(body, correctSecret)
	header := "sha256=" + sig

	if VerifyHMACSignature(body, wrongSecret, header) {
		t.Error("VerifyHMACSignature returned true with wrong secret — HMAC verification is broken")
	}
}

// TestOutboxDispatcher110_VerifyHMACSignatureEmptyHeader verifies that an
// empty or truncated header is rejected.
func TestOutboxDispatcher110_VerifyHMACSignatureEmptyHeader(t *testing.T) {
	body := []byte(`{}`)
	secret := []byte("secret")

	cases := []string{"", "sha256=", "bad-prefix=abc"}
	for _, h := range cases {
		if VerifyHMACSignature(body, secret, h) {
			t.Errorf("VerifyHMACSignature(%q) returned true, want false", h)
		}
	}
}

// TestOutboxDispatcher110_VerifyHMACSignatureTamperedBody verifies that
// modifying the body invalidates the signature.
func TestOutboxDispatcher110_VerifyHMACSignatureTamperedBody(t *testing.T) {
	original := []byte(`{"event_type":"v1.echo.created"}`)
	tampered := []byte(`{"event_type":"v1.evil.action"}`)
	secret := []byte("secret")

	sig := ComputeHMAC(original, secret)
	header := "sha256=" + sig

	if VerifyHMACSignature(tampered, secret, header) {
		t.Error("VerifyHMACSignature returned true for tampered body — HMAC is not protecting integrity")
	}
}

// =============================================================================
// Step 4: Wire to webhook delivery client (WebhookDispatcher)
// =============================================================================

// sigCapture is an HTTP test server that records every request body and
// X-Arena-Signature header for assertion.
type sigCapture struct {
	server  *httptest.Server
	bodies  [][]byte
	headers []string
	status  int
	calls   atomic.Int64
}

func newSigCapture(statusCode int) *sigCapture {
	s := &sigCapture{status: statusCode}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		s.bodies = append(s.bodies, body)
		s.headers = append(s.headers, r.Header.Get("X-Arena-Signature"))
		w.WriteHeader(s.status)
	}))
	return s
}

func (s *sigCapture) URL() string     { return s.server.URL }
func (s *sigCapture) Close()          { s.server.Close() }
func (s *sigCapture) CallCount() int  { return int(s.calls.Load()) }
func (s *sigCapture) LastBody() []byte {
	if len(s.bodies) == 0 {
		return nil
	}
	return s.bodies[len(s.bodies)-1]
}
func (s *sigCapture) LastSigHeader() string {
	if len(s.headers) == 0 {
		return ""
	}
	return s.headers[len(s.headers)-1]
}

// TestOutboxDispatcher110_WebhookDispatcherPOSTsToURL verifies the
// WebhookDispatcher makes exactly one POST to the configured URL.
func TestOutboxDispatcher110_WebhookDispatcherPOSTsToURL(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL: srv.URL(),
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000001",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{"message": "hello"},
		OccurredAt:    time.Now(),
	}

	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if srv.CallCount() != 1 {
		t.Errorf("expected 1 POST, got %d", srv.CallCount())
	}
}

// TestOutboxDispatcher110_WebhookDispatcherBodyIsValidJSON verifies the
// POST body is valid JSON containing the event fields.
func TestOutboxDispatcher110_WebhookDispatcherBodyIsValidJSON(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL()})

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000002",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{"trace_id": "trace-110-body"},
		OccurredAt:    time.Now(),
	}

	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	body := srv.LastBody()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("response body is not JSON: %v; body=%s", err, body)
	}
	if out["event_type"] != "v1.echo.created" {
		t.Errorf("event_type=%v, want v1.echo.created; body=%s", out["event_type"], body)
	}
	if out["aggregate_type"] != "echo" {
		t.Errorf("aggregate_type=%v, want echo", out["aggregate_type"])
	}
}

// TestOutboxDispatcher110_WebhookDispatcherAddsSignatureHeader verifies that
// a non-empty SigningSecret causes X-Arena-Signature to be added to the request.
func TestOutboxDispatcher110_WebhookDispatcherAddsSignatureHeader(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	secret := []byte("test-signing-secret-32byteslong!!")
	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL(),
		SigningSecret: secret,
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000003",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{"message": "signed"},
		OccurredAt:    time.Now(),
	}

	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	sigHeader := srv.LastSigHeader()
	if sigHeader == "" {
		t.Fatal("X-Arena-Signature header is missing when SigningSecret is configured")
	}
	if !strings.HasPrefix(sigHeader, "sha256=") {
		t.Errorf("X-Arena-Signature must start with 'sha256='; got %q", sigHeader)
	}

	// Verify the signature against the received body.
	body := srv.LastBody()
	if !VerifyHMACSignature(body, secret, sigHeader) {
		t.Errorf("X-Arena-Signature=%q is not a valid HMAC of the body", sigHeader)
	}
}

// TestOutboxDispatcher110_WebhookDispatcherNoSignatureWhenNoSecret verifies
// that an empty SigningSecret produces no X-Arena-Signature header.
func TestOutboxDispatcher110_WebhookDispatcherNoSignatureWhenNoSecret(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL(),
		SigningSecret: nil, // deliberately no secret
	})

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000004",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{"message": "unsigned"},
		OccurredAt:    time.Now(),
	}
	_ = d.Dispatch(context.Background(), ev)

	if srv.LastSigHeader() != "" {
		t.Errorf("expected no X-Arena-Signature header when no secret; got %q", srv.LastSigHeader())
	}
}

// TestOutboxDispatcher110_WebhookDispatcherReturnsErrorOn4xx verifies that
// a 4xx response from the subscriber is returned as a non-nil error (so
// OutboxEventsDispatcher retries the event).
func TestOutboxDispatcher110_WebhookDispatcherReturnsErrorOn4xx(t *testing.T) {
	srv := newSigCapture(http.StatusBadRequest) // 400
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL()})

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000005",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{},
		OccurredAt:    time.Now(),
	}

	err := d.Dispatch(context.Background(), ev)
	if err == nil {
		t.Error("Dispatch must return error when subscriber returns 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error must mention the status code; got %v", err)
	}
}

// TestOutboxDispatcher110_WebhookDispatcherReturnsErrorOn5xx verifies that
// a 5xx response triggers a retry (non-nil error from Dispatch).
func TestOutboxDispatcher110_WebhookDispatcherReturnsErrorOn5xx(t *testing.T) {
	srv := newSigCapture(http.StatusInternalServerError) // 500
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL()})

	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000006",
		EventType:     "v1.echo.created",
		Payload:       map[string]any{},
		OccurredAt:    time.Now(),
	}

	if err := d.Dispatch(context.Background(), ev); err == nil {
		t.Error("Dispatch must return error when subscriber returns 500")
	}
}

// TestOutboxDispatcher110_WebhookDispatcherEmptyURLErrors verifies that
// NewWebhookDispatcher returns an error when TargetURL is empty.
func TestOutboxDispatcher110_WebhookDispatcherEmptyURLErrors(t *testing.T) {
	_, err := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: ""})
	if err == nil {
		t.Error("NewWebhookDispatcher must return error when TargetURL is empty")
	}
}

// TestOutboxDispatcher110_WebhookDispatcherHasSigningSecretGetter verifies
// the HasSigningSecret helper reports correctly.
func TestOutboxDispatcher110_WebhookDispatcherHasSigningSecretGetter(t *testing.T) {
	d1, _ := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     "http://example.com/webhook",
		SigningSecret: []byte("secret"),
	})
	if !d1.HasSigningSecret() {
		t.Error("HasSigningSecret() must return true when SigningSecret is set")
	}

	d2, _ := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     "http://example.com/webhook",
		SigningSecret: nil,
	})
	if d2.HasSigningSecret() {
		t.Error("HasSigningSecret() must return false when SigningSecret is nil")
	}
}

// TestOutboxDispatcher110_WebhookDispatcherTargetURLGetter verifies the
// TargetURL getter returns the configured URL.
func TestOutboxDispatcher110_WebhookDispatcherTargetURLGetter(t *testing.T) {
	const target = "https://hooks.example.com/events"
	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: target})
	if d.TargetURL() != target {
		t.Errorf("TargetURL()=%q, want %q", d.TargetURL(), target)
	}
}

// TestOutboxDispatcher110_WebhookDispatcherCompileTimeGuard is the runtime
// expression of the compile-time interface guard at the bottom of
// webhook_dispatcher.go.
func TestOutboxDispatcher110_WebhookDispatcherCompileTimeGuard(t *testing.T) {
	var _ Dispatcher = (*WebhookDispatcher)(nil)
}

// =============================================================================
// Step 5: Integration contract (in-memory simulation)
// =============================================================================

// TestOutboxDispatcher110_TxCommitEventPublished verifies the core step 5
// contract: an event written to the store (simulating a committed transaction)
// is picked up by the dispatcher and delivered via WebhookDispatcher.
//
// This proves "tx commit → event published" without a live database by using
// the inMemOutboxStore seeded with a pre-committed row.
func TestOutboxDispatcher110_TxCommitEventPublished(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	secret := []byte("step5-signing-secret")
	d, err := NewWebhookDispatcher(WebhookDispatcherOptions{
		TargetURL:     srv.URL(),
		SigningSecret: secret,
	})
	if err != nil {
		t.Fatalf("NewWebhookDispatcher: %v", err)
	}

	store := newInMemOutboxStore()
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000001101"
	// Seed simulates a committed INSERT (processed_at IS NULL).
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000001", "trace-110-commit"))

	disp, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      d,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = disp.Run(ctx) }()

	// Wait for the dispatcher to process the row.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		r := store.findRow(rowID)
		if r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = disp.Stop()

	// Verify: event was published (HTTP hit).
	if srv.CallCount() < 1 {
		t.Error("step 5: committed event must be delivered to the subscriber")
	}

	// Verify: row is now stamped (processed_at set).
	r := store.findRow(rowID)
	if r == nil || r.processedAt == nil {
		t.Error("step 5: processed_at must be set after successful dispatch")
	}

	// Verify: subscriber received a signed payload.
	sigHeader := srv.LastSigHeader()
	if sigHeader == "" {
		t.Error("step 5: subscriber must receive X-Arena-Signature header")
	}
	body := srv.LastBody()
	if !VerifyHMACSignature(body, secret, sigHeader) {
		t.Errorf("step 5: X-Arena-Signature is invalid for the received body")
	}
}

// TestOutboxDispatcher110_TxRollbackEventAbsent verifies the "tx rollback →
// no event" contract: a rolled-back transaction means no row exists in the
// store, so the dispatcher delivers nothing.
//
// The in-memory store perfectly models this: we simply do NOT seed the row,
// which is what a rolled-back INSERT produces.
func TestOutboxDispatcher110_TxRollbackEventAbsent(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL()})
	store := newInMemOutboxStore()
	logger, _ := logBuffer()

	// No rows seeded — simulates a rolled-back transaction.
	disp, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      d,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go func() { _ = disp.Run(ctx) }()
	time.Sleep(80 * time.Millisecond) // let dispatcher poll several times
	_ = disp.Stop()

	if srv.CallCount() != 0 {
		t.Errorf("step 5: rolled-back event must not be delivered; got %d deliveries", srv.CallCount())
	}
}

// TestOutboxDispatcher110_ReplaySafety verifies that once processed_at is
// set, the same row is never re-delivered (replay safety).
func TestOutboxDispatcher110_ReplaySafety(t *testing.T) {
	srv := newSigCapture(http.StatusOK)
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL()})
	store := newInMemOutboxStore()
	logger, _ := logBuffer()

	const rowID = "00000000-0000-0000-0000-000000001102"
	store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000007", "trace-110-replay"))

	disp, err := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
		Store:           store,
		Dispatcher:      d,
		Logger:          logger,
		PollInterval:    5 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOutboxEventsDispatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = disp.Run(ctx) }()

	// Wait for the first delivery.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		r := store.findRow(rowID)
		if r != nil && r.processedAt != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Record hit count at the moment of first delivery.
	hitsAfterFirstDelivery := srv.CallCount()
	time.Sleep(60 * time.Millisecond) // let dispatcher poll a few more times
	_ = disp.Stop()

	// After processed_at is set, no further deliveries should occur.
	hitsAfterStop := srv.CallCount()
	if hitsAfterStop != hitsAfterFirstDelivery {
		t.Errorf("replay safety violated: %d extra deliveries after processed_at was set",
			hitsAfterStop-hitsAfterFirstDelivery)
	}
	if hitsAfterFirstDelivery < 1 {
		t.Error("step 5 replay: event must have been delivered at least once")
	}
}

// TestOutboxDispatcher110_IdempotencyKeyInPayload verifies that when the
// event payload contains an "event_id" key it is propagated to the
// X-Arena-Event-ID header by the WebhookDispatcher.
func TestOutboxDispatcher110_IdempotencyKeyInPayload(t *testing.T) {
	var capturedEventID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedEventID = r.Header.Get("X-Arena-Event-ID")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL})

	const wantEventID = "00000000-0000-0000-0000-000000001103"
	ev := Event{
		AggregateType: "echo",
		AggregateID:   "00000000-0000-0000-0000-000000000008",
		EventType:     "v1.echo.created",
		Payload: map[string]any{
			"event_id": wantEventID,
			"message":  "idempotency test",
		},
		OccurredAt: time.Now(),
	}

	if err := d.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if capturedEventID != wantEventID {
		t.Errorf("X-Arena-Event-ID=%q, want %q", capturedEventID, wantEventID)
	}
}

// =============================================================================
// Step 1: Register outbox dispatcher in worker (structural test)
// =============================================================================

// TestOutboxDispatcher110_WorkerMainRegistersOutboxEventsDispatcher checks
// that the arena-worker main.go source file contains the OutboxEventsDispatcher
// registration (step 1) by reading the source file as text.
//
// This is a structural/compilation test — no live binary is required.
func TestOutboxDispatcher110_WorkerMainRegistersOutboxEventsDispatcher(t *testing.T) {
	// Walk up from the test file's directory to find cmd/arena-worker/main.go.
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile is in apps/backend/internal/platform/outbox/
	// main.go is in apps/backend/cmd/arena-worker/
	dir := filepath.Dir(thisFile)
	mainGo := filepath.Join(dir, "..", "..", "..", "cmd", "arena-worker", "main.go")

	data, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("could not read cmd/arena-worker/main.go: %v", err)
	}
	src := string(data)

	// Step 1: OutboxEventsDispatcher must be constructed and started.
	if !strings.Contains(src, "NewOutboxEventsDispatcher") {
		t.Error("step 1: cmd/arena-worker/main.go must call NewOutboxEventsDispatcher")
	}
	if !strings.Contains(src, "outboxEventsDisp") {
		t.Error("step 1: outbox events dispatcher variable not found in main.go")
	}

	// Step 3: HMAC signing must be wired (buildOutboxDispatcher uses SigningSecret).
	if !strings.Contains(src, "SigningSecret") {
		t.Error("step 3: main.go must wire HMAC signing secret to the dispatcher")
	}

	// Step 4: WebhookDispatcher or buildOutboxDispatcher must be called.
	if !strings.Contains(src, "buildOutboxDispatcher") && !strings.Contains(src, "NewWebhookDispatcher") {
		t.Error("step 4: main.go must build a WebhookDispatcher (or wrapper)")
	}

	// Graceful shutdown: Stop() must be called.
	if !strings.Contains(src, "outboxEventsDisp.Stop()") {
		t.Error("step 1: outbox events dispatcher must be gracefully stopped in main.go")
	}
}

// TestOutboxDispatcher110_BuildOutboxDispatcherNoopWhenNoURL verifies the
// structural wiring: when OUTBOX_WEBHOOK_URL is empty a NoopDispatcher is used.
// This is tested indirectly through the source of buildOutboxDispatcher.
func TestOutboxDispatcher110_BuildOutboxDispatcherNoopWhenNoURL(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	mainGo := filepath.Join(dir, "..", "..", "..", "cmd", "arena-worker", "main.go")

	data, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatalf("could not read cmd/arena-worker/main.go: %v", err)
	}
	src := string(data)

	// The buildOutboxDispatcher function must exist and handle the empty-URL case.
	if !strings.Contains(src, "buildOutboxDispatcher") {
		t.Error("buildOutboxDispatcher function not found in main.go")
	}
	if !strings.Contains(src, "NoopDispatcher") {
		t.Error("main.go must fall back to NoopDispatcher when OUTBOX_WEBHOOK_URL is empty")
	}
}

// =============================================================================
// Full verification
// =============================================================================

// TestOutboxDispatcher110_FullVerification runs all 5 feature steps as
// named subtests, providing one omnibus test entry-point.
func TestOutboxDispatcher110_FullVerification(t *testing.T) {
	t.Run("step1_worker_registers_dispatcher", func(t *testing.T) {
		_, thisFile, _, _ := runtime.Caller(0)
		dir := filepath.Dir(thisFile)
		mainGo := filepath.Join(dir, "..", "..", "..", "cmd", "arena-worker", "main.go")
		data, err := os.ReadFile(mainGo)
		if err != nil {
			t.Fatalf("could not read main.go: %v", err)
		}
		if !strings.Contains(string(data), "NewOutboxEventsDispatcher") {
			t.Error("main.go must call NewOutboxEventsDispatcher")
		}
	})

	t.Run("step2_stable_json_ordering", func(t *testing.T) {
		m := map[string]any{"z": 3, "a": 1, "m": 2}
		b1, _ := StableJSONMap(m)
		b2, _ := StableJSONMap(m)
		if !bytes.Equal(b1, b2) {
			t.Error("StableJSONMap is not deterministic")
		}
		s := string(b1)
		if strings.Index(s, `"a"`) > strings.Index(s, `"z"`) {
			t.Error("map keys not sorted alphabetically")
		}
	})

	t.Run("step3_hmac_signature_header", func(t *testing.T) {
		var captured string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = r.Header.Get("X-Arena-Signature")
			w.WriteHeader(200)
		}))
		defer srv.Close()

		secret := []byte("full-verification-secret")
		d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{
			TargetURL:     srv.URL,
			SigningSecret: secret,
		})
		ev := Event{
			AggregateType: "echo",
			AggregateID:   "00000000-0000-0000-0000-000000001199",
			EventType:     "v1.echo.created",
			Payload:       map[string]any{"test": "fv-step3"},
			OccurredAt:    time.Now(),
		}
		_ = d.Dispatch(context.Background(), ev)
		if !strings.HasPrefix(captured, "sha256=") {
			t.Errorf("X-Arena-Signature must start with sha256=; got %q", captured)
		}
	})

	t.Run("step4_http_post_delivery", func(t *testing.T) {
		var hitCount atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitCount.Add(1)
			w.WriteHeader(200)
		}))
		defer srv.Close()

		d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL})
		ev := Event{
			AggregateType: "echo",
			AggregateID:   "00000000-0000-0000-0000-000000001200",
			EventType:     "v1.echo.created",
			Payload:       map[string]any{"test": "fv-step4"},
			OccurredAt:    time.Now(),
		}
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if hitCount.Load() != 1 {
			t.Errorf("expected 1 POST, got %d", hitCount.Load())
		}
	})

	t.Run("step5_commit_published_rollback_absent", func(t *testing.T) {
		var deliveries atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deliveries.Add(1)
			w.WriteHeader(200)
		}))
		defer srv.Close()

		d, _ := NewWebhookDispatcher(WebhookDispatcherOptions{TargetURL: srv.URL})
		store := newInMemOutboxStore()
		logger, _ := logBuffer()

		// Simulate rollback: do NOT seed any row.
		disp1, _ := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      d,
			Logger:          logger,
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 500 * time.Millisecond,
		})
		ctx1, cancel1 := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel1()
		go func() { _ = disp1.Run(ctx1) }()
		time.Sleep(60 * time.Millisecond)
		_ = disp1.Stop()
		if deliveries.Load() != 0 {
			t.Errorf("rollback: expected 0 deliveries, got %d", deliveries.Load())
		}

		// Simulate commit: seed a row.
		const rowID = "00000000-0000-0000-0000-000000001201"
		store.seed(newTestRow(rowID, "v1.echo.created", "00000000-0000-0000-0000-000000000099", "trace-fv-5"))

		disp2, _ := NewOutboxEventsDispatcher(OutboxEventsDispatcherOptions{
			Store:           store,
			Dispatcher:      d,
			Logger:          logger,
			PollInterval:    5 * time.Millisecond,
			ShutdownTimeout: 500 * time.Millisecond,
		})
		ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel2()
		go func() { _ = disp2.Run(ctx2) }()

		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			r := store.findRow(rowID)
			if r != nil && r.processedAt != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		_ = disp2.Stop()

		if deliveries.Load() < 1 {
			t.Error("commit: event must have been delivered at least once")
		}
	})
}
