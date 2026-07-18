// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: system.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const selectUUIDv7 = `-- name: SelectUUIDv7 :one
SELECT uuidv7() AS id
`

// SelectUUIDv7 calls the uuidv7() PostgreSQL function and returns a new
// time-ordered UUID (RFC 9562 §5.7).  It serves as the canonical example of a
// sqlc-generated typed query wrapper for arena_new.
//
// The uuidv7() function is defined in migration 0001_init.sql and provides
// the same API as the PostgreSQL 18 built-in so that column defaults written
// today will continue working after a PG18 upgrade (just drop the function).
func (q *Queries) SelectUUIDv7(ctx context.Context) (uuid.UUID, error) {
	row := q.db.QueryRow(ctx, selectUUIDv7)
	var id uuid.UUID
	err := row.Scan(&id)
	return id, err
}

const selectServerTime = `-- name: SelectServerTime :one
SELECT now() AS server_time
`

// SelectServerTime returns the current PostgreSQL server timestamp.
// It is used by GET /v1/server-info to demonstrate the full
// router → handler → sqlc → response chain.
func (q *Queries) SelectServerTime(ctx context.Context) (time.Time, error) {
	row := q.db.QueryRow(ctx, selectServerTime)
	var serverTime time.Time
	err := row.Scan(&serverTime)
	return serverTime, err
}
