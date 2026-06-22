package worker

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGOutboxBacklogQuerier implements OutboxBacklogQuerier against the
// outbox PostgreSQL table created by the 0002_outbox.sql migration.
type PGOutboxBacklogQuerier struct {
	pool *pgxpool.Pool
}

// NewPGOutboxBacklogQuerier wraps pool into a PGOutboxBacklogQuerier.
func NewPGOutboxBacklogQuerier(pool *pgxpool.Pool) *PGOutboxBacklogQuerier {
	return &PGOutboxBacklogQuerier{pool: pool}
}

// CountUndispatched runs SELECT count(*) FROM outbox WHERE dispatched_at IS NULL.
func (q *PGOutboxBacklogQuerier) CountUndispatched(ctx context.Context) (int64, error) {
	const sql = `SELECT count(*) FROM outbox WHERE dispatched_at IS NULL`

	var n int64
	err := q.pool.QueryRow(ctx, sql).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("outbox backlog count: %w", err)
	}
	return n, nil
}

// Compile-time interface check.
var _ OutboxBacklogQuerier = (*PGOutboxBacklogQuerier)(nil)
