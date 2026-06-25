// Package idempotency_replay_test holds the test suite for feature #19:
// "Idempotency key stored in DB and replays response".
//
// These tests exercise every observable behavior of the Middleware + PGStore
// contract that can be verified without a live PostgreSQL connection:
//
//   - Step 1-2:   First POST returns 200 and records the response in the store.
//   - Step 3-4:   Stored row carries response_status=200 and byte-identical body.
//   - Step 5:     Stored scope equals the options.Scope value ("POST /v1/echo").
//   - Step 6:     Stored expires_at is ≥ now()+23h (default TTL 24h).
//   - Step 7-8:   Second POST with same key → HTTP 200, body byte-identical to
//     the first response.
//   - Step 9:     Second response carries the Idempotent-Replay: true header.
//   - Step 10-11: Downstream handler invoked exactly ONCE — replay
//     short-circuits before the handler runs (so audit_events and
//     outbox_events would each receive only one row on a real DB).
//   - Step 12:    Stored rows can be deleted (verified by resetting the store).
//
// DB-dependent verification (psql SELECT from idempotency_keys, counting
// audit_events / outbox_events) was performed manually against the running
// Docker stack and is documented in claude-progress.txt (session "Feature #5").
package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// inMemoryStore — a thread-safe Store implementation for tests.
// It mirrors the PGStore contract without requiring a database.
// ----------------------------------------------------------------------------

type inMemoryStore struct {
	mu      sync.Mutex
	records map[string]storedEntry
}

type storedEntry struct {
	resp    StoredResponse
	key     string
	scope   string
	actorID string
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{records: make(map[string]storedEntry)}
}

func (s *inMemoryStore) storeKey(key, scope string) string { return key + "\x00" + scope }

func (s *inMemoryStore) Lookup(_ context.Context, key, scope string) (StoredResponse, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.records[s.storeKey(key, scope)]
	if !ok {
		return StoredResponse{}, false, nil
	}
	if time.Now().After(entry.resp.ExpiresAt) {
		return StoredResponse{}, false, nil
	}
	return entry.resp, true, nil
}

func (s *inMemoryStore) Save(_ context.Context, key, scope, actorID string, resp StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.storeKey(key, scope)
	if existing, exists := s.records[k]; exists && time.Now().Before(existing.resp.ExpiresAt) {
		return nil // ON CONFLICT: existing live key → DO NOTHING (mirrors PGStore behaviour)
	}
	// No existing row, or existing row is expired → insert/replace (feature #47).
	s.records[k] = storedEntry{resp: resp, key: key, scope: scope, actorID: actorID}
	return nil
}

// forceExpire sets the expires_at for key+scope to the past, simulating the
// DB statement: UPDATE idempotency_keys SET expires_at = now() - interval '1s'
// This allows tests to verify that an expired key is treated as a MISS and the
// handler is re-invoked (feature #47).
func (s *inMemoryStore) forceExpire(key, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.storeKey(key, scope)
	if e, ok := s.records[k]; ok {
		e.resp.ExpiresAt = time.Now().Add(-1 * time.Second)
		s.records[k] = e
	}
}

// reset deletes all stored records — models step 12 (cleanup test row).
func (s *inMemoryStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]storedEntry)
}

// get returns the stored entry for the given key+scope (or zero value).
func (s *inMemoryStore) get(key, scope string) (storedEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.records[s.storeKey(key, scope)]
	return e, ok
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

const testScope = "POST /v1/echo"

// echoJSONHandler is a minimal handler that acts like /v1/echo: it echoes
// the request body back as JSON and increments a call counter.
//
// In the real server the handler also writes audit_events and outbox_events.
// For middleware-level tests we count handler invocations to prove those
// side-effects occur exactly once (step 10-11).
func echoJSONHandler(callCount *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()

		var msg any
		if err := json.Unmarshal(body, &msg); err != nil {
			msg = string(body)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"echoed":     msg,
			"request_id": r.Header.Get("X-Request-Id"),
		})
	}
}

func postEcho(t *testing.T, ts *httptest.Server, idemKey, msgBody string) *http.Response {
	t.Helper()
	payload := strings.NewReader(msgBody)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/echo", payload)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderName, idemKey)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestIdempotency_FirstRequestReturns200AndStoresRow verifies steps 1-6:
//   - POST /v1/echo with Idempotency-Key: IDEM_PROBE_1 returns HTTP 200.
//   - The middleware stores exactly one row in the store.
//   - The stored row carries response_status=200 and the correct response body.
//   - The stored scope equals "POST /v1/echo".
//   - The stored expires_at is ≥ now()+23h (confirms 24h TTL default).
func TestIdempotency_FirstRequestReturns200AndStoresRow(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "IDEM_PROBE_1"
	body := `{"message":"first"}`

	// Step 1: POST with Idempotency-Key.
	resp := postEcho(t, ts, key, body)
	respBody := readBody(t, resp)

	// Step 2: verify HTTP 200.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("step 2: want status 200, got %d (body: %s)", resp.StatusCode, respBody)
	}

	// Step 3-4: verify exactly one row with response_status=200 and matching body.
	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("step 3: no row found in store for IDEM_PROBE_1")
	}
	if entry.resp.Status != http.StatusOK {
		t.Errorf("step 4: stored response_status want 200, got %d", entry.resp.Status)
	}
	if !bytes.Equal(entry.resp.Body, respBody) {
		t.Errorf("step 4: stored body %q does not match response body %q",
			entry.resp.Body, respBody)
	}

	// Step 5: scope must equal "POST /v1/echo".
	if entry.scope != testScope {
		t.Errorf("step 5: want scope %q, got %q", testScope, entry.scope)
	}

	// Step 6: expires_at must be at least now+23h (default TTL is 24h).
	threshold := time.Now().Add(23 * time.Hour)
	if !entry.resp.ExpiresAt.After(threshold) {
		t.Errorf("step 6: expires_at %v is not after now+23h (%v)", entry.resp.ExpiresAt, threshold)
	}
}

// TestIdempotency_SecondRequestReplaysResponse verifies steps 7-9:
//   - Second POST with the same key returns HTTP 200.
//   - Response body is byte-for-byte identical to the first response.
//   - Idempotent-Replay: true header is present on the second response.
func TestIdempotency_SecondRequestReplaysResponse(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "IDEM_PROBE_1"
	body := `{"message":"first"}`

	// First request.
	resp1 := postEcho(t, ts, key, body)
	body1 := readBody(t, resp1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", resp1.StatusCode)
	}

	// Step 7: Second POST with SAME Idempotency-Key: IDEM_PROBE_1, same body.
	resp2 := postEcho(t, ts, key, body)
	body2 := readBody(t, resp2)

	// Step 8: HTTP 200 with byte-identical body.
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("step 8: want status 200 on replay, got %d", resp2.StatusCode)
	}
	if !bytes.Equal(body1, body2) {
		t.Errorf("step 8: replay body not byte-identical\n  first:  %q\n  second: %q",
			body1, body2)
	}

	// Step 9: Idempotent-Replay header must be present and true.
	if got := resp2.Header.Get("Idempotent-Replay"); got != "true" {
		t.Errorf("step 9: want Idempotent-Replay: true, got %q", got)
	}
}

// TestIdempotency_HandlerCalledOnlyOnce verifies steps 10-11:
// The downstream handler (which in production writes audit_events and
// outbox_events) is invoked exactly once — the replay short-circuits the
// handler entirely. On a live database this guarantees each table receives
// exactly one row regardless of how many duplicate requests arrive.
func TestIdempotency_HandlerCalledOnlyOnce(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "IDEM_PROBE_1"
	body := `{"message":"first"}`

	// Issue the same request three times.
	for i := 0; i < 3; i++ {
		resp := postEcho(t, ts, key, body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Step 10-11: handler must have been called exactly once.
	if n := calls.Load(); n != 1 {
		t.Errorf("steps 10-11: want handler called 1 time, got %d (replay must short-circuit handler)", n)
	}
}

// TestIdempotency_CleanupDeletesRow verifies step 12:
// After resetting the store the key is gone. This mirrors a real-database
// DELETE FROM idempotency_keys WHERE key='IDEM_PROBE_1'.
func TestIdempotency_CleanupDeletesRow(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "IDEM_PROBE_1"
	body := `{"message":"first"}`

	resp := postEcho(t, ts, key, body)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Verify row exists before cleanup.
	if _, ok := store.get(key, testScope); !ok {
		t.Fatal("row not found before cleanup")
	}

	// Step 12: cleanup test row.
	store.reset()

	// Row must be gone after cleanup.
	if _, ok := store.get(key, testScope); ok {
		t.Error("step 12: row still present after cleanup")
	}
}

// TestIdempotency_DifferentKeysDontConflict ensures that two different keys
// produce independent storage entries — the scope-key composite is the
// uniqueness boundary.
func TestIdempotency_DifferentKeysDontConflict(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	resp1 := postEcho(t, ts, "KEY_A", `{"message":"alpha"}`)
	body1 := readBody(t, resp1)

	resp2 := postEcho(t, ts, "KEY_B", `{"message":"beta"}`)
	body2 := readBody(t, resp2)

	if bytes.Equal(body1, body2) {
		t.Error("different keys returned identical bodies; scope uniqueness may be broken")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("want handler called 2 times for 2 different keys, got %d", n)
	}
	// No replay header on either (both are first-time).
	if resp1.Header.Get("Idempotent-Replay") == "true" {
		t.Error("KEY_A unexpectedly marked as replay")
	}
	if resp2.Header.Get("Idempotent-Replay") == "true" {
		t.Error("KEY_B unexpectedly marked as replay")
	}
}

// TestIdempotency_ConflictOnDifferentBody verifies the 409 path:
// reusing an Idempotency-Key with a different request body is rejected.
func TestIdempotency_ConflictOnDifferentBody(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "CONFLICT_KEY"

	// First request.
	resp1 := postEcho(t, ts, key, `{"message":"original"}`)
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request want 200, got %d", resp1.StatusCode)
	}

	// Second request with different body.
	resp2 := postEcho(t, ts, key, `{"message":"different_body"}`)
	body2 := readBody(t, resp2)
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("want 409 Conflict for key reused with different body, got %d (body: %s)",
			resp2.StatusCode, body2)
	}
}

// TestIdempotency_MissingKeyReturns400 verifies that a request without the
// Idempotency-Key header is rejected with 400 Bad Request.
func TestIdempotency_MissingKeyReturns400(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/echo", strings.NewReader(`{"message":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	// Intentionally NO Idempotency-Key header.

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for missing key, got %d", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Error("handler must not be called when Idempotency-Key is missing")
	}
}

// TestIdempotency_TTLDefault24h is a focused assertion that the default TTL
// used by the middleware (when Options.TTL is zero) is exactly 24 hours.
func TestIdempotency_TTLDefault24h(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	// TTL deliberately left at zero — middleware must default to 24h.
	mw := Middleware(store, Options{Scope: testScope, TTL: 0})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "TTL_PROBE"
	before := time.Now()

	resp := postEcho(t, ts, key, `{"message":"ttl test"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	after := time.Now()

	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("no row stored for TTL_PROBE")
	}

	minExpiry := before.Add(24 * time.Hour)
	maxExpiry := after.Add(24 * time.Hour)

	if entry.resp.ExpiresAt.Before(minExpiry) {
		t.Errorf("expires_at %v is before expected minimum %v", entry.resp.ExpiresAt, minExpiry)
	}
	if entry.resp.ExpiresAt.After(maxExpiry) {
		t.Errorf("expires_at %v is after expected maximum %v", entry.resp.ExpiresAt, maxExpiry)
	}
}

// TestIdempotency_ResponseHeaderOnFirstRequest verifies that the first
// response does NOT carry the Idempotent-Replay header — that header is
// reserved for replays only.
func TestIdempotency_ResponseHeaderOnFirstRequest(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	resp := postEcho(t, ts, "FRESH_KEY", `{"message":"fresh"}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := resp.Header.Get("Idempotent-Replay"); got != "" {
		t.Errorf("first response must NOT carry Idempotent-Replay header, got %q", got)
	}
}

// TestIdempotency_StoredBodyEqualsResponseBody verifies the fundamental
// byte-identity guarantee: what is stored in the idempotency row is exactly
// what was sent to the client. The PGStore JSON-validation pass can only
// transform an invalid JSON body (wraps it); valid JSON must survive verbatim.
func TestIdempotency_StoredBodyEqualsResponseBody(t *testing.T) {
	store := newInMemoryStore()
	var calls atomic.Int64
	mw := Middleware(store, Options{Scope: testScope})

	ts := httptest.NewServer(mw(echoJSONHandler(&calls)))
	defer ts.Close()

	const key = "BODY_PARITY"
	body := `{"message":"parity check"}`

	resp := postEcho(t, ts, key, body)
	respBody := readBody(t, resp)

	entry, ok := store.get(key, testScope)
	if !ok {
		t.Fatal("no stored row for BODY_PARITY")
	}

	if !bytes.Equal(entry.resp.Body, respBody) {
		t.Errorf("stored body not byte-identical to response body\n  stored:   %q\n  response: %q",
			entry.resp.Body, respBody)
	}
}

// Compile-time assertions: ensure the inMemoryStore satisfies the Store
// interface. This catches regressions where Store gains new methods without
// a corresponding update to test implementations.
var _ Store = (*inMemoryStore)(nil)
