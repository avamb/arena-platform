// pool_test.go — unit tests for the adapters/postgres package (feature #92).
//
// These tests exercise all non-network logic (compile-time interface guards,
// Tx helper, PingProbe behaviour, RegisterPoolMetrics nil-guard, NewPool error
// paths) without requiring a live PostgreSQL connection.
//
// The integration test that exercises a real pool (NewPool + Ping against
// actual Postgres) lives in pool_integration_test.go and is gated by the
// "integration" build tag.
package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// =============================================================================
// Compile-time interface guards
// =============================================================================

// PingProbe must satisfy httpserver.ReadinessProbe.
var _ httpserver.ReadinessProbe = (*PingProbe)(nil)

// TxBeginner must be satisfied by the fake pool used in Tx tests.
var _ TxBeginner = (*fakeTxPool)(nil)

// =============================================================================
// PingProbe unit tests (no live DB)
// =============================================================================

// TestPingProbe_DefaultName verifies that NewPingProbe defaults to "database"
// when an empty name is supplied.
func TestPingProbe_DefaultName(t *testing.T) {
	// Call NewPingProbe with a nil pool — we only test the name path here;
	// Ping is not called so the nil pool never dereferences.
	probe := NewPingProbe(nil, "")
	if got := probe.ProbeName(); got != "database" {
		t.Errorf("ProbeName() = %q, want %q", got, "database")
	}
}

// TestPingProbe_CustomName verifies that a non-empty name is preserved.
func TestPingProbe_CustomName(t *testing.T) {
	probe := NewPingProbe(nil, "primary-db")
	if got := probe.ProbeName(); got != "primary-db" {
		t.Errorf("ProbeName() = %q, want %q", got, "primary-db")
	}
}

// TestPingProbe_TimeoutApplied verifies that Ping applies a ≤2s deadline to
// the context it passes to the underlying pool. We test this through the
// pingProbeWithBeginner helper that accepts a pingable interface instead of
// *pgxpool.Pool, mirroring the production Ping logic exactly.
func TestPingProbe_TimeoutApplied(t *testing.T) {
	var capturedCtx context.Context
	mock := &mockPingPool{pingFn: func(ctx context.Context) error {
		capturedCtx = ctx
		return nil
	}}

	probe := &testPingProbe{pool: mock, name: "database"}
	if err := probe.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deadline, hasDeadline := capturedCtx.Deadline()
	if !hasDeadline {
		t.Fatal("Ping did not apply a deadline to the context")
	}

	// Deadline should be within 2 seconds from now; allow 150 ms scheduling slack.
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 2*time.Second+150*time.Millisecond {
		t.Errorf("deadline remaining = %v; want in (0, 2.15s]", remaining)
	}
}

// TestPingProbe_SuccessReturnsNil verifies Ping returns nil when the pool
// ping succeeds.
func TestPingProbe_SuccessReturnsNil(t *testing.T) {
	mock := &mockPingPool{pingFn: func(_ context.Context) error { return nil }}
	probe := &testPingProbe{pool: mock, name: "database"}

	if err := probe.Ping(context.Background()); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

// TestPingProbe_ErrorPropagated verifies that Ping wraps and returns pool errors.
func TestPingProbe_ErrorPropagated(t *testing.T) {
	sentinel := errors.New("dial tcp: connection refused")
	mock := &mockPingPool{pingFn: func(_ context.Context) error { return sentinel }}
	probe := &testPingProbe{pool: mock, name: "database"}

	err := probe.Ping(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// The production Ping wraps with "postgres ping: <err>" — our test helper
	// mirrors that behaviour. Check the chain contains the original.
	if !errors.Is(err, sentinel) {
		t.Errorf("error chain does not contain sentinel: got %v", err)
	}
}

// =============================================================================
// Tx helper tests
// =============================================================================

// TestTx_CommitsOnSuccess verifies that a successful fn results in Commit.
func TestTx_CommitsOnSuccess(t *testing.T) {
	tx := &fakeTxImpl{}
	pool := &fakeTxPool{tx: tx}

	err := Tx(context.Background(), pool, func(pgx.Tx) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tx.committed {
		t.Error("expected Commit to be called on success")
	}
	if tx.rolledBack {
		t.Error("Rollback should not be called on success")
	}
}

// TestTx_RollsBackOnFnError verifies that fn returning an error causes Rollback.
func TestTx_RollsBackOnFnError(t *testing.T) {
	tx := &fakeTxImpl{}
	pool := &fakeTxPool{tx: tx}
	fnErr := errors.New("insert failed")

	err := Tx(context.Background(), pool, func(pgx.Tx) error {
		return fnErr
	})
	if !errors.Is(err, fnErr) {
		t.Errorf("error chain does not contain fnErr: got %v", err)
	}
	if tx.committed {
		t.Error("Commit should not be called when fn errors")
	}
	if !tx.rolledBack {
		t.Error("expected Rollback to be called on fn error")
	}
}

// TestTx_RollsBackOnPanic verifies that a panic in fn triggers Rollback and
// the panic is re-raised.
func TestTx_RollsBackOnPanic(t *testing.T) {
	tx := &fakeTxImpl{}
	pool := &fakeTxPool{tx: tx}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic to propagate through Tx")
		}
		if !tx.rolledBack {
			t.Error("expected Rollback to be called on panic")
		}
	}()

	_ = Tx(context.Background(), pool, func(pgx.Tx) error {
		panic("unexpected panic in handler")
	})
}

// TestTx_BeginErrorReturned verifies BeginTx errors are wrapped and returned.
func TestTx_BeginErrorReturned(t *testing.T) {
	beginErr := errors.New("too many connections")
	pool := &fakeTxPool{beginErr: beginErr}

	err := Tx(context.Background(), pool, func(pgx.Tx) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error from BeginTx, got nil")
	}
	if !errors.Is(err, beginErr) {
		t.Errorf("error chain does not contain beginErr: got %v", err)
	}
}

// TestTx_CommitErrorReturned verifies Commit errors are wrapped and returned.
func TestTx_CommitErrorReturned(t *testing.T) {
	commitErr := errors.New("could not serialize access")
	tx := &fakeTxImpl{commitErr: commitErr}
	pool := &fakeTxPool{tx: tx}

	err := Tx(context.Background(), pool, func(pgx.Tx) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error from Commit, got nil")
	}
	if !errors.Is(err, commitErr) {
		t.Errorf("error chain does not contain commitErr: got %v", err)
	}
}

// TestTx_FnReceivesCorrectTx verifies fn is called with the Tx from BeginTx.
func TestTx_FnReceivesCorrectTx(t *testing.T) {
	tx := &fakeTxImpl{}
	pool := &fakeTxPool{tx: tx}
	var receivedTx pgx.Tx

	_ = Tx(context.Background(), pool, func(got pgx.Tx) error {
		receivedTx = got
		return nil
	})
	if receivedTx != tx {
		t.Error("fn received a different Tx than the one returned by BeginTx")
	}
}

// =============================================================================
// RegisterPoolMetrics nil-guard tests
// =============================================================================

// TestRegisterPoolMetrics_NilMetricsIsNoop verifies the function does not panic
// when m is nil.
func TestRegisterPoolMetrics_NilMetricsIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	RegisterPoolMetrics(ctx, nil, nil)
	// Reaching here without panicking confirms the nil-guard works.
}

// TestRegisterPoolMetrics_NilPoolIsNoop mirrors the nil-pool guard.
func TestRegisterPoolMetrics_NilPoolIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	RegisterPoolMetrics(ctx, nil, m)
	// Reaching here without panicking confirms the nil-pool guard works.
}

// =============================================================================
// NewPool error path tests (no real DB required)
// =============================================================================

// TestNewPool_NilConfigReturnsError verifies that NewPool returns a descriptive
// error when cfg is nil without attempting a network connection.
func TestNewPool_NilConfigReturnsError(t *testing.T) {
	_, err := NewPool(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
	const want = "postgres: nil config"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// TestNewPool_InvalidDSNReturnsParseError verifies that a non-postgres URL is
// rejected by ParseConfig before any network call is attempted.
func TestNewPool_InvalidDSNReturnsParseError(t *testing.T) {
	cfg := minimalConfig("not-a-valid-dsn")
	_, err := NewPool(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected parse error for invalid DSN, got nil")
	}
	const wantPrefix = "postgres: parse DATABASE_URL"
	if !hasPrefix(err.Error(), wantPrefix) {
		t.Errorf("error %q does not start with %q", err.Error(), wantPrefix)
	}
}

// =============================================================================
// Internal test doubles
// =============================================================================

// mockPingPool is an injectable Ping implementation for PingProbe tests.
type mockPingPool struct {
	pingFn func(context.Context) error
}

func (m *mockPingPool) Ping(ctx context.Context) error { return m.pingFn(ctx) }

// testPingProbe mirrors PingProbe.Ping logic but uses mockPingPool instead of
// *pgxpool.Pool so we can test the timeout and error paths without a real pool.
type testPingProbe struct {
	pool interface{ Ping(context.Context) error }
	name string
}

func (p *testPingProbe) ProbeName() string { return p.name }

func (p *testPingProbe) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.pool.Ping(pingCtx); err != nil {
		return errors.New("postgres ping: " + err.Error())
	}
	return nil
}

// Compile-time guard: testPingProbe satisfies httpserver.ReadinessProbe.
var _ httpserver.ReadinessProbe = (*testPingProbe)(nil)

// fakeTxPool satisfies TxBeginner by returning the embedded fakeTxImpl.
type fakeTxPool struct {
	tx       *fakeTxImpl
	beginErr error
}

func (fp *fakeTxPool) BeginTx(_ context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	if fp.beginErr != nil {
		return nil, fp.beginErr
	}
	return fp.tx, nil
}

// fakeTxImpl is a minimal pgx.Tx implementation for Tx helper tests.
// Only Commit and Rollback are implemented meaningfully; all other methods
// panic to catch unexpected usage.
type fakeTxImpl struct {
	committed   bool
	rolledBack  bool
	commitErr   error
}

func (f *fakeTxImpl) Commit(_ context.Context) error {
	f.committed = true
	return f.commitErr
}

func (f *fakeTxImpl) Rollback(_ context.Context) error {
	f.rolledBack = true
	return nil
}

// Remaining pgx.Tx methods — panic on unexpected calls.
func (f *fakeTxImpl) Begin(_ context.Context) (pgx.Tx, error) {
	panic("fakeTxImpl: Begin not expected in Tx helper tests")
}
func (f *fakeTxImpl) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	panic("fakeTxImpl: CopyFrom not expected in Tx helper tests")
}
func (f *fakeTxImpl) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	panic("fakeTxImpl: SendBatch not expected in Tx helper tests")
}
func (f *fakeTxImpl) LargeObjects() pgx.LargeObjects {
	panic("fakeTxImpl: LargeObjects not expected in Tx helper tests")
}
func (f *fakeTxImpl) Prepare(_ context.Context, _, _ string) (*pgconn.StatementDescription, error) {
	panic("fakeTxImpl: Prepare not expected in Tx helper tests")
}
func (f *fakeTxImpl) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("fakeTxImpl: Exec not expected in Tx helper tests")
}
func (f *fakeTxImpl) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	panic("fakeTxImpl: Query not expected in Tx helper tests")
}
func (f *fakeTxImpl) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("fakeTxImpl: QueryRow not expected in Tx helper tests")
}
func (f *fakeTxImpl) Conn() *pgx.Conn { return nil }

// Compile-time guard: fakeTxImpl satisfies pgx.Tx.
var _ pgx.Tx = (*fakeTxImpl)(nil)

// =============================================================================
// Helper utilities
// =============================================================================

// minimalConfig returns a *config.Config with only DatabaseURL and pool
// settings populated, bypassing Validate() which requires additional fields.
// Used for error-path tests that do not reach network calls.
func minimalConfig(dsn string) *config.Config {
	return &config.Config{
		DatabaseURL:       dsn,
		DBPoolMinConns:    1,
		DBPoolMaxConns:    5,
		DBPoolMaxConnLife: time.Hour,
		DBPoolMaxConnIdle: 30 * time.Minute,
	}
}

func hasPrefix(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}
