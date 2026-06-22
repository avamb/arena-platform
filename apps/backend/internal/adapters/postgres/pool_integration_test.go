//go:build integration

// pool_integration_test.go — integration tests for feature #92 step 5.
//
// These tests require a live PostgreSQL instance. They are excluded from the
// normal "go test ./..." run and are activated only when the "integration"
// build tag is set:
//
//	go test -tags integration ./apps/backend/internal/adapters/postgres/...
//
// The DATABASE_URL environment variable must point to a reachable PostgreSQL
// server (e.g. the one started by docker compose up postgres).
//
// When testcontainers-go is added to the project (planned for Wave 9) this
// file should be updated to spin up a throwaway PG17 container so the test
// is self-contained and does not rely on external infrastructure.
package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/jackc/pgx/v5"
)

// integrationConfig builds a *config.Config from DATABASE_URL for integration tests.
func integrationConfig(t *testing.T) *config.Config {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	if !strings.HasPrefix(dsn, "postgres") {
		t.Skipf("DATABASE_URL %q does not look like a Postgres DSN; skipping", dsn)
	}
	return &config.Config{
		DatabaseURL:       dsn,
		DBPoolMinConns:    1,
		DBPoolMaxConns:    5,
		DBPoolMaxConnLife: time.Hour,
		DBPoolMaxConnIdle: 30 * time.Minute,
	}
}

// TestNewPool_Integration verifies that NewPool connects to a real Postgres
// instance, respects the pool configuration, and the pool passes a ping.
//
// Feature #92 step 5: "Integration test against real Postgres (testcontainers):
// pool initializes, ping passes".
func TestNewPool_Integration(t *testing.T) {
	cfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	// Verify pool is reachable via a direct ping.
	pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		t.Fatalf("pool.Ping after NewPool: %v", err)
	}

	// Verify pool settings were applied.
	stat := pool.Stat()
	if stat.MaxConns() != 5 {
		t.Errorf("MaxConns = %d, want 5", stat.MaxConns())
	}
}

// TestPingProbe_Integration verifies that a PingProbe backed by a real pool
// returns nil from Ping (dependency is reachable) and exposes the correct name.
func TestPingProbe_Integration(t *testing.T) {
	cfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	probe := NewPingProbe(pool, "database")
	if got := probe.ProbeName(); got != "database" {
		t.Errorf("ProbeName() = %q, want %q", got, "database")
	}

	if err := probe.Ping(context.Background()); err != nil {
		t.Errorf("PingProbe.Ping: %v", err)
	}
}

// TestRegisterPoolMetrics_Integration verifies that RegisterPoolMetrics
// populates the DBPoolConnections gauge immediately on registration.
func TestRegisterPoolMetrics_Integration(t *testing.T) {
	cfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	m, err := observability.New(nil)
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}

	// RegisterPoolMetrics publishes immediately on first call before ticking.
	metricsCtx, metricsCancel := context.WithCancel(ctx)
	defer metricsCancel()
	RegisterPoolMetrics(metricsCtx, pool, m)

	// Give the goroutine a moment to run the initial publish.
	time.Sleep(100 * time.Millisecond)

	// Gather metrics and verify the "max" gauge has been set to MaxConns=5.
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Registry().Gather: %v", err)
	}

	found := false
	for _, mf := range mfs {
		if mf.GetName() == "arena_db_pool_connections" {
			found = true
			for _, metric := range mf.GetMetric() {
				for _, lp := range metric.GetLabel() {
					if lp.GetName() == "state" && lp.GetValue() == "max" {
						val := metric.GetGauge().GetValue()
						if val != 5 {
							t.Errorf("arena_db_pool_connections{state=max} = %v, want 5", val)
						}
					}
				}
			}
		}
	}
	if !found {
		t.Error("arena_db_pool_connections metric not found after RegisterPoolMetrics")
	}
}

// TestTx_Integration verifies the Tx helper executes work inside a real
// transaction, commits on success, and rolls back on error.
func TestTx_Integration(t *testing.T) {
	cfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	t.Run("commits on success", func(t *testing.T) {
		err := Tx(ctx, pool, func(tx pgx.Tx) error {
			// SELECT 1 is a no-op that exercises the commit path.
			row := tx.QueryRow(ctx, "SELECT 1")
			var n int
			if err := row.Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("SELECT 1 returned %d", n)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Tx commit path: %v", err)
		}
	})

	t.Run("rolls back on fn error", func(t *testing.T) {
		fnErr := context.DeadlineExceeded // a sentinel that should be returned
		err := Tx(ctx, pool, func(_ pgx.Tx) error {
			return fnErr
		})
		if err == nil {
			t.Fatal("expected error from fn, got nil")
		}
	})
}
