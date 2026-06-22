// Package postgres provides the pgx/v5 connection pool adapter for arena_new.
//
// Responsibilities:
//
//   - NewPool — constructs a *pgxpool.Pool from *config.Config, applying all
//     pool tuning knobs (min/max conns, lifetimes). A single initial ping
//     verifies connectivity before returning the pool to the caller.
//
//   - RegisterPoolMetrics — starts a background goroutine that scrapes pool
//     statistics on every tick and publishes them to the DBPoolConnections
//     GaugeVec in *observability.Metrics.
//
//   - PingProbe — implements httpserver.ReadinessProbe so a *pgxpool.Pool
//     can be plugged directly into the /readyz probe chain with a 2-second
//     per-probe timeout.
//
//   - Tx — wraps a unit of work in an explicit database transaction, rolling
//     back automatically on error or panic and committing on success.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// -----------------------------------------------------------------------------
// Pool construction
// -----------------------------------------------------------------------------

// NewPool creates a *pgxpool.Pool configured from cfg, applying:
//   - MinConns  (DB_POOL_MIN_CONNS)
//   - MaxConns  (DB_POOL_MAX_CONNS)
//   - MaxConnLifetime  (DB_POOL_MAX_CONN_LIFETIME)
//   - MaxConnIdleTime  (DB_POOL_MAX_CONN_IDLE_TIME)
//
// An initial ping (5-second timeout) verifies the database is reachable before
// the pool is returned. On failure the pool is closed and the error is wrapped.
//
// For production use-cases that need exponential-backoff retry on startup, see
// platform/database.Open which adds a health-checker goroutine on top of this
// plain pool.
func NewPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error) {
	if cfg == nil {
		return nil, errors.New("postgres: nil config")
	}

	pgxCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse DATABASE_URL: %w", err)
	}

	pgxCfg.MinConns = cfg.DBPoolMinConns
	pgxCfg.MaxConns = cfg.DBPoolMaxConns
	pgxCfg.MaxConnLifetime = cfg.DBPoolMaxConnLife
	pgxCfg.MaxConnIdleTime = cfg.DBPoolMaxConnIdle

	pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: initial ping failed: %w", err)
	}

	return pool, nil
}

// -----------------------------------------------------------------------------
// Pool metrics registration
// -----------------------------------------------------------------------------

// poolMetricScrapeInterval is how often RegisterPoolMetrics refreshes gauges.
const poolMetricScrapeInterval = 15 * time.Second

// poolStatReader is the narrow interface satisfied by *pgxpool.Stat.
// Extracted here so publishPoolStatSnapshot can be exercised in unit tests
// without a live PostgreSQL pool (feature #40 burst tests).
type poolStatReader interface {
	AcquiredConns() int32
	IdleConns() int32
	MaxConns() int32
	TotalConns() int32
	NewConnsCount() int64
	// EmptyAcquireCount is the number of times the pool had no available
	// connection and a caller had to wait (maps to db_pool_wait_count).
	EmptyAcquireCount() int64
	// AcquireDuration is the total cumulative time spent waiting for a
	// connection (maps to db_pool_wait_duration_seconds).
	AcquireDuration() time.Duration
}

// RegisterPoolMetrics starts a background goroutine that periodically scrapes
// pgxpool.Stat() and publishes the values to the DBPoolConnections GaugeVec
// in m (labels: acquired, idle, max, total, new_total).
//
// The goroutine exits when ctx is cancelled. Callers should pass the server's
// root context so metrics stop when the process shuts down.
//
// If m or pool is nil the call is a no-op (useful in tests without metrics).
func RegisterPoolMetrics(ctx context.Context, pool *pgxpool.Pool, m *observability.Metrics) {
	if m == nil || pool == nil {
		return
	}
	if m.DBPoolConnections == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(poolMetricScrapeInterval)
		defer ticker.Stop()

		// Publish immediately so the first scrape doesn't wait 15 s.
		publishPoolStats(pool, m)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				publishPoolStats(pool, m)
			}
		}
	}()
}

// publishPoolStats reads a single Stat snapshot from pool and updates the
// DBPoolConnections gauges. It delegates to publishPoolStatSnapshot so the
// gauge-update logic can be unit-tested without a live pool.
func publishPoolStats(pool *pgxpool.Pool, m *observability.Metrics) {
	publishPoolStatSnapshot(pool.Stat(), m)
}

// publishPoolStatSnapshot writes the pool-connection gauge values from a
// poolStatReader snapshot into both the DBPoolConnections GaugeVec and the
// individual named gauges (feature #79: db_pool_open_connections, db_pool_idle,
// db_pool_in_use, db_pool_wait_count, db_pool_wait_duration_seconds).
//
// This function is the unit-testable core of the pool-metrics pipeline: tests
// supply a fakePoolStat instead of a real *pgxpool.Stat so they can control
// AcquiredConns / IdleConns values and assert burst-rise / post-burst-drain
// behaviour without a PostgreSQL connection (feature #40).
func publishPoolStatSnapshot(s poolStatReader, m *observability.Metrics) {
	// Legacy GaugeVec (feature #40 / #87) — kept for backward compatibility.
	m.DBPoolConnections.WithLabelValues("acquired").Set(float64(s.AcquiredConns()))
	m.DBPoolConnections.WithLabelValues("idle").Set(float64(s.IdleConns()))
	m.DBPoolConnections.WithLabelValues("max").Set(float64(s.MaxConns()))
	m.DBPoolConnections.WithLabelValues("total").Set(float64(s.TotalConns()))
	m.DBPoolConnections.WithLabelValues("new_total").Set(float64(s.NewConnsCount()))

	// Individual named gauges (feature #79).
	if m.DBPoolOpenConnections != nil {
		m.DBPoolOpenConnections.Set(float64(s.TotalConns()))
	}
	if m.DBPoolIdle != nil {
		m.DBPoolIdle.Set(float64(s.IdleConns()))
	}
	if m.DBPoolInUse != nil {
		m.DBPoolInUse.Set(float64(s.AcquiredConns()))
	}
	if m.DBPoolWaitCount != nil {
		m.DBPoolWaitCount.Set(float64(s.EmptyAcquireCount()))
	}
	if m.DBPoolWaitDurationSeconds != nil {
		m.DBPoolWaitDurationSeconds.Set(s.AcquireDuration().Seconds())
	}
}

// -----------------------------------------------------------------------------
// ReadinessProbe implementation
// -----------------------------------------------------------------------------

// PingProbe implements httpserver.ReadinessProbe by pinging a *pgxpool.Pool.
//
// Each Ping call applies a 2-second context deadline, independent of any
// deadline already present on the parent context, so the readiness check
// always completes quickly and never holds up the /readyz response.
type PingProbe struct {
	pool      *pgxpool.Pool
	probeName string
}

// NewPingProbe returns a *PingProbe that uses the supplied pool.
// probeName is the key published in the /readyz checks map (e.g. "database").
// If probeName is empty it defaults to "database".
func NewPingProbe(pool *pgxpool.Pool, probeName string) *PingProbe {
	if probeName == "" {
		probeName = "database"
	}
	return &PingProbe{pool: pool, probeName: probeName}
}

// ProbeName returns the stable identifier used in the /readyz checks map.
func (p *PingProbe) ProbeName() string { return p.probeName }

// Ping pings the database with a 2-second deadline. Returns nil on success
// or a wrapped error on failure.
func (p *PingProbe) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("postgres ping: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Transaction helper
// -----------------------------------------------------------------------------

// TxBeginner is the narrow interface satisfied by *pgxpool.Pool (and any pool
// wrapper) that Tx requires. Defining it as an interface makes the helper
// testable without a live PostgreSQL connection.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Tx wraps fn in an explicit pgx transaction using the default isolation level
// (read committed). Typical usage:
//
//	err := postgres.Tx(ctx, pool, func(tx pgx.Tx) error {
//	    _, err := tx.Exec(ctx, "INSERT INTO ...")
//	    return err
//	})
//
// Behaviour:
//   - If fn returns a non-nil error the transaction is rolled back and that
//     error is returned to the caller (the rollback error, if any, is discarded
//     because the original error is more informative).
//   - If fn panics the transaction is rolled back and the panic is re-raised.
//   - If fn returns nil the transaction is committed; commit errors are wrapped
//     and returned.
//
// Tx accepts any TxBeginner (including *pgxpool.Pool and *pgx.Conn).
func Tx(ctx context.Context, pool TxBeginner, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}

	// Ensure rollback on panic so the connection is returned to the pool in a
	// clean state even if fn panics.
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(ctx)
			panic(r)
		}
	}()

	if fnErr := fn(tx); fnErr != nil {
		_ = tx.Rollback(ctx)
		return fnErr
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("postgres: commit tx: %w", commitErr)
	}
	return nil
}
