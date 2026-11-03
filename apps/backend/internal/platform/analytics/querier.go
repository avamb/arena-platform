package analytics

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the part of a pool (*pgxpool.Pool) or of a transaction (pgx.Tx)
// that the read helpers of this package need. It is the ONE declaration of
// the interface: the CRM export (export.go) and the admin screens use it, and
// a test can hand in either a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
