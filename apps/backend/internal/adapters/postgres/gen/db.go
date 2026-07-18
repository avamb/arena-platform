// Hand-maintained DBTX infrastructure; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.

// Package gen contains hand-maintained typed query wrappers for arena_new.
// These files follow sqlc v1 output conventions but are maintained by hand
// because sqlc generate has not been run against this repository.
// To regenerate from SQL source: make sqlc-generate (requires sqlc >= v1.26).
//
// Files with additions that sqlc cannot express carry explicit
// "hand-written, not sqlc-generated" markers at the affected functions.
package gen

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the narrow database interface accepted by every generated Queries
// method. Both *pgxpool.Pool and pgx.Tx satisfy this interface, so generated
// code can be used inside or outside a transaction without modification.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// New returns a *Queries bound to db.
// Pass either a *pgxpool.Pool (outside a transaction) or a pgx.Tx (inside one).
func New(db DBTX) *Queries {
	return &Queries{db: db}
}

// Queries holds the DBTX used for every generated query method.
type Queries struct {
	db DBTX
}

// WithTx returns a new *Queries that executes all queries within tx.
// Typical usage:
//
//	q := gen.New(pool)
//	err := postgres.Tx(ctx, pool, func(tx pgx.Tx) error {
//	    qtx := q.WithTx(tx)
//	    return qtx.SelectUUIDv7(ctx)
//	})
func (q *Queries) WithTx(tx pgx.Tx) *Queries {
	return &Queries{
		db: tx,
	}
}
