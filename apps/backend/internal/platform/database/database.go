// Package database provides PostgreSQL connectivity through a pgx/v5 pool
// with exponential-backoff retry on initial connect and a continuous
// health-checker suitable for /readyz probes.
//
// The pool tolerates container startup races (PostgreSQL not yet accepting
// connections) and transient outages (PostgreSQL temporarily unavailable
// after boot). Recovery is automatic — pgxpool re-establishes connections
// when the database comes back; Status.IsHealthy() reflects the live state.
package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps a pgxpool.Pool together with a continuous health-checker.
type Pool struct {
	*pgxpool.Pool
	cfg     *config.Config
	logger  *slog.Logger
	healthy atomic.Bool
	lastErr atomic.Value // string
	closed  atomic.Bool
	stopCh  chan struct{}
}

// Open establishes a pgxpool.Pool to the database described by cfg.DatabaseURL,
// retrying with exponential backoff until either the context is cancelled or
// the connection succeeds. A background health-checker goroutine is started
// that refreshes the healthy/unhealthy state every checkInterval.
func Open(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Pool, error) {
	if cfg == nil {
		return nil, errors.New("database: nil config")
	}
	if logger == nil {
		logger = slog.Default()
	}

	pgxCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("database: parse DATABASE_URL: %w", err)
	}
	pgxCfg.MinConns = cfg.DBPoolMinConns
	pgxCfg.MaxConns = cfg.DBPoolMaxConns
	pgxCfg.MaxConnLifetime = cfg.DBPoolMaxConnLife
	pgxCfg.MaxConnIdleTime = cfg.DBPoolMaxConnIdle
	pgxCfg.HealthCheckPeriod = 30 * time.Second

	pool, err := connectWithRetry(ctx, pgxCfg, logger)
	if err != nil {
		return nil, err
	}

	p := &Pool{
		Pool:   pool,
		cfg:    cfg,
		logger: logger,
		stopCh: make(chan struct{}),
	}
	p.healthy.Store(true)
	p.lastErr.Store("")

	logger.Info("database connection established",
		"max_conns", cfg.DBPoolMaxConns,
		"min_conns", cfg.DBPoolMinConns,
		"max_conn_lifetime", cfg.DBPoolMaxConnLife.String(),
		"max_conn_idle_time", cfg.DBPoolMaxConnIdle.String(),
	)

	go p.runHealthChecker(5 * time.Second)

	return p, nil
}

// connectWithRetry repeatedly attempts to create a pgxpool.Pool and ping it,
// using exponential backoff (capped). Returns the live pool on success or an
// error if ctx is cancelled.
func connectWithRetry(ctx context.Context, pgxCfg *pgxpool.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	const (
		initialBackoff = 500 * time.Millisecond
		maxBackoff     = 10 * time.Second
	)
	attempt := 0
	backoff := initialBackoff

	for {
		attempt++
		pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}

		logger.Warn("database connection attempt failed",
			"attempt", attempt,
			"error", err.Error(),
			"retry_in", backoff.String(),
		)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("database: gave up connecting after %d attempts: %w", attempt, ctx.Err())
		case <-time.After(backoff):
		}

		backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
	}
}

// runHealthChecker pings the database every interval and updates the
// healthy/lastErr fields. Exits when stopCh is closed.
func (p *Pool) runHealthChecker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.checkOnce()
		}
	}
}

func (p *Pool) checkOnce() {
	if p.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasHealthy := p.healthy.Load()
	err := p.Pool.Ping(ctx)
	if err != nil {
		p.healthy.Store(false)
		p.lastErr.Store(err.Error())
		if wasHealthy {
			p.logger.Warn("database ping failed", "error", err.Error())
		}
		return
	}
	p.healthy.Store(true)
	p.lastErr.Store("")
	if !wasHealthy {
		p.logger.Info("database connection restored")
	}
}

// Ping performs an immediate, synchronous ping and returns the result.
// Also updates the cached health state.
func (p *Pool) Ping(ctx context.Context) error {
	if p.closed.Load() {
		return errors.New("database: pool is closed")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := p.Pool.Ping(pingCtx)
	if err != nil {
		p.healthy.Store(false)
		p.lastErr.Store(err.Error())
		return err
	}
	p.healthy.Store(true)
	p.lastErr.Store("")
	return nil
}

// IsHealthy reports whether the most recent health check succeeded.
func (p *Pool) IsHealthy() bool {
	if p.closed.Load() {
		return false
	}
	return p.healthy.Load()
}

// LastError returns the most recent ping error string, or "" if healthy.
func (p *Pool) LastError() string {
	v := p.lastErr.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Close stops the health-checker and releases the underlying pool.
func (p *Pool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.stopCh)
	if p.Pool != nil {
		p.Pool.Close()
	}
}

// Stats returns the underlying pgxpool stats snapshot for telemetry callers.
type Stats struct {
	AcquiredConns    int32
	IdleConns        int32
	MaxConns         int32
	TotalConns       int32
	NewConnsCount    int64
	AcquireCount     int64
	AcquireDuration  time.Duration
	CanceledAcquires int64
}

func (p *Pool) Snapshot() Stats {
	s := p.Pool.Stat()
	return Stats{
		AcquiredConns:    s.AcquiredConns(),
		IdleConns:        s.IdleConns(),
		MaxConns:         s.MaxConns(),
		TotalConns:       s.TotalConns(),
		NewConnsCount:    s.NewConnsCount(),
		AcquireCount:     s.AcquireCount(),
		AcquireDuration:  s.AcquireDuration(),
		CanceledAcquires: s.CanceledAcquireCount(),
	}
}
