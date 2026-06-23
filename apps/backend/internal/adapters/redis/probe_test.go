// Package redis — unit tests for the Redis readiness probe (feature #112).
//
// All tests in this file run without a live Redis server. Real-network
// behaviour is exercised via a mock TCP listener (TestDialRedisPinger112_*)
// or a mock RedisPinger (TestRedisPingProbe112_*), keeping the tests fast
// and hermetic.
package redis

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
)

// =============================================================================
// Compile-time interface guards
// =============================================================================

// RedisPingProbe must satisfy httpserver.ReadinessProbe structurally.
var _ httpserver.ReadinessProbe = (*RedisPingProbe)(nil)

// DialRedisPinger must satisfy RedisPinger.
var _ RedisPinger = (*DialRedisPinger)(nil)

// =============================================================================
// mockRedisPinger — controllable RedisPinger test double
// =============================================================================

type mockRedisPinger struct {
	pingErr error
	captCtx context.Context // captured by Ping for deadline assertions
}

func (m *mockRedisPinger) Ping(ctx context.Context) error {
	m.captCtx = ctx
	return m.pingErr
}

// =============================================================================
// RedisPingProbe tests
// =============================================================================

// TestRedisPingProbe112_DefaultName verifies that NewRedisPingProbe defaults
// probeName to "redis" when an empty string is supplied.
func TestRedisPingProbe112_DefaultName(t *testing.T) {
	t.Parallel()
	probe := NewRedisPingProbe(&mockRedisPinger{}, "")
	if got := probe.ProbeName(); got != "redis" {
		t.Errorf("ProbeName() = %q, want %q", got, "redis")
	}
}

// TestRedisPingProbe112_CustomName verifies that a non-empty probeName is
// preserved verbatim.
func TestRedisPingProbe112_CustomName(t *testing.T) {
	t.Parallel()
	probe := NewRedisPingProbe(&mockRedisPinger{}, "session-cache")
	if got := probe.ProbeName(); got != "session-cache" {
		t.Errorf("ProbeName() = %q, want %q", got, "session-cache")
	}
}

// TestRedisPingProbe112_SuccessReturnsNil verifies Ping returns nil when the
// underlying pinger succeeds.
func TestRedisPingProbe112_SuccessReturnsNil(t *testing.T) {
	t.Parallel()
	mock := &mockRedisPinger{pingErr: nil}
	probe := NewRedisPingProbe(mock, "redis")
	if err := probe.Ping(context.Background()); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// TestRedisPingProbe112_ErrorPropagated verifies that a pinger error is
// wrapped and returned with the "redis ping:" prefix, preserving the error
// chain so errors.Is still works.
func TestRedisPingProbe112_ErrorPropagated(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("NOAUTH Authentication required")
	mock := &mockRedisPinger{pingErr: sentinel}
	probe := NewRedisPingProbe(mock, "redis")

	err := probe.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain must contain sentinel; got %v", err)
	}
}

// TestRedisPingProbe112_TimeoutApplied verifies that Ping applies a ≤2s
// deadline to the context passed to the underlying pinger, independently
// of any deadline on the parent context.
func TestRedisPingProbe112_TimeoutApplied(t *testing.T) {
	t.Parallel()
	mock := &mockRedisPinger{}
	probe := NewRedisPingProbe(mock, "redis")

	_ = probe.Ping(context.Background())

	deadline, hasDeadline := mock.captCtx.Deadline()
	if !hasDeadline {
		t.Fatal("Ping did not apply a deadline to the context")
	}
	remaining := time.Until(deadline)
	// Allow 150 ms scheduling slack around the 2-second budget.
	if remaining <= 0 || remaining > 2*time.Second+150*time.Millisecond {
		t.Errorf("deadline remaining = %v; want in (0, 2.15s]", remaining)
	}
}

// =============================================================================
// parseRedisAddr tests
// =============================================================================

// TestParseRedisAddr112_EmptyURL verifies that an empty URL produces the
// default address "localhost:6379".
func TestParseRedisAddr112_EmptyURL(t *testing.T) {
	t.Parallel()
	got, err := parseRedisAddr("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "localhost:6379" {
		t.Errorf("got %q, want %q", got, "localhost:6379")
	}
}

// TestParseRedisAddr112_StandardURL verifies a standard redis:// URL with an
// explicit port.
func TestParseRedisAddr112_StandardURL(t *testing.T) {
	t.Parallel()
	got, err := parseRedisAddr("redis://cache.example.com:6380")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cache.example.com:6380" {
		t.Errorf("got %q, want %q", got, "cache.example.com:6380")
	}
}

// TestParseRedisAddr112_URLWithCredentials verifies that credentials in the
// URL are stripped; only host:port is returned.
func TestParseRedisAddr112_URLWithCredentials(t *testing.T) {
	t.Parallel()
	got, err := parseRedisAddr("redis://:s3cr3t@redis.internal:6379/0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "redis.internal:6379" {
		t.Errorf("got %q, want %q", got, "redis.internal:6379")
	}
}

// TestParseRedisAddr112_HostWithoutPort verifies that a URL with no port
// gets the default Redis port 6379 appended.
func TestParseRedisAddr112_HostWithoutPort(t *testing.T) {
	t.Parallel()
	got, err := parseRedisAddr("redis://myredis")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "myredis:6379" {
		t.Errorf("got %q, want %q", got, "myredis:6379")
	}
}

// TestParseRedisAddr112_InvalidURL verifies that an unparseable URL
// is rejected with an error at construction time.
func TestParseRedisAddr112_InvalidURL(t *testing.T) {
	t.Parallel()
	_, err := parseRedisAddr("://bad url")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

// =============================================================================
// NewDialRedisPinger construction tests
// =============================================================================

// TestNewDialRedisPinger112_ValidURL verifies that a valid URL produces no
// construction error.
func TestNewDialRedisPinger112_ValidURL(t *testing.T) {
	t.Parallel()
	pinger, err := NewDialRedisPinger("redis://localhost:6379")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pinger == nil {
		t.Fatal("expected non-nil pinger")
	}
}

// TestNewDialRedisPinger112_EmptyURL verifies that an empty URL defaults to
// "localhost:6379" without returning an error.
func TestNewDialRedisPinger112_EmptyURL(t *testing.T) {
	t.Parallel()
	pinger, err := NewDialRedisPinger("")
	if err != nil {
		t.Fatalf("unexpected error for empty URL: %v", err)
	}
	if pinger.addr != "localhost:6379" {
		t.Errorf("addr = %q, want %q", pinger.addr, "localhost:6379")
	}
}

// =============================================================================
// DialRedisPinger behaviour tests (mock TCP server)
// =============================================================================

// startMockRedisServer starts a minimal TCP server that accepts one connection,
// reads the PING command, and replies with the supplied response. Returns the
// server's address. The server goroutine exits after the first exchange.
func startMockRedisServer(t *testing.T, response string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		defer conn.Close()
		// Drain the PING command (6 bytes: "PING\r\n").
		_, _ = io.ReadFull(conn, make([]byte, 6))
		_, _ = conn.Write([]byte(response))
	}()

	return ln.Addr().String()
}

// TestDialRedisPinger112_SuccessfulPing verifies that a +PONG\r\n response
// causes Ping to return nil.
func TestDialRedisPinger112_SuccessfulPing(t *testing.T) {
	t.Parallel()
	addr := startMockRedisServer(t, "+PONG\r\n")
	pinger := &DialRedisPinger{addr: addr}
	if err := pinger.Ping(context.Background()); err != nil {
		t.Errorf("Ping returned unexpected error: %v", err)
	}
}

// TestDialRedisPinger112_ConnRefused verifies that a refused connection
// returns a wrapped error (not nil).
func TestDialRedisPinger112_ConnRefused(t *testing.T) {
	t.Parallel()
	// Port 1 is conventionally un-used on all OS; the connect should fail fast.
	pinger := &DialRedisPinger{addr: "127.0.0.1:1"}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := pinger.Ping(ctx)
	if err == nil {
		t.Error("expected error for refused connection, got nil")
	}
}

// TestDialRedisPinger112_UnexpectedResponse verifies that a response other
// than "+PONG\r\n" returns a descriptive error.
func TestDialRedisPinger112_UnexpectedResponse(t *testing.T) {
	t.Parallel()
	// Send an error response: -ERR...\r\n (7 bytes to match ReadFull size).
	addr := startMockRedisServer(t, "-ERR xx\r\n")
	pinger := &DialRedisPinger{addr: addr}
	err := pinger.Ping(context.Background())
	if err == nil {
		t.Error("expected error for non-PONG response, got nil")
	}
}

// TestDialRedisPinger112_ContextCancelled verifies that Ping respects context
// cancellation before completing the dial.
func TestDialRedisPinger112_ContextCancelled(t *testing.T) {
	t.Parallel()
	// Start a server that deliberately delays its accept so the context fires first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// Do NOT accept — the connection will be queued but never handled.

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	pinger := &DialRedisPinger{addr: ln.Addr().String()}
	err = pinger.Ping(ctx)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
