package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// BacklogQuerier is the minimal read interface required to sample the outbox
// backlog.  *pgxpool.Pool satisfies it automatically.
type BacklogQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MonitorBacklog starts a background goroutine that refreshes the supplied
// Prometheus gauge every interval by counting undelivered outbox rows
// (WHERE dispatched_at IS NULL).
//
// The goroutine exits when ctx is cancelled.  Query errors are logged at
// Warn level and do not stop the loop — a transient DB failure simply skips
// that tick.
//
// Typical usage in main():
//
//	outbox.MonitorBacklog(ctx, dbPool, metrics.OutboxBacklog, 15*time.Second, logger)
func MonitorBacklog(
	ctx context.Context,
	querier BacklogQuerier,
	gauge prometheus.Gauge,
	interval time.Duration,
	logger *slog.Logger,
) {
	if logger == nil {
		logger = slog.Default()
	}
	go runBacklogMonitor(ctx, querier, gauge, interval, logger)
}

const backlogSQL = `SELECT COUNT(*) FROM outbox WHERE dispatched_at IS NULL`

func runBacklogMonitor(
	ctx context.Context,
	querier BacklogQuerier,
	gauge prometheus.Gauge,
	interval time.Duration,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Sample immediately so the gauge is populated before the first tick fires.
	sampleBacklog(ctx, querier, gauge, logger)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sampleBacklog(ctx, querier, gauge, logger)
		}
	}
}

func sampleBacklog(ctx context.Context, querier BacklogQuerier, gauge prometheus.Gauge, logger *slog.Logger) {
	var count int64
	if err := querier.QueryRow(ctx, backlogSQL).Scan(&count); err != nil {
		logger.Warn("outbox: backlog count query failed", "error", err.Error())
		return
	}
	gauge.Set(float64(count))
}
