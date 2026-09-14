// retry.go — transaction plumbing for the GA category quota mechanism.
//
// A quota mutation touches exactly the rows a hold mutation touches, so it
// MUST follow the platform-wide hold-mutation lock order (see the package
// doc on quota.go and AGENTS.md):
//
//	sessions row (IncrementSessionSeatStatusVersion) → inventory_ledger row
//	→ session_seats / GA place rows.
//
// Even with that order kept everywhere, two transactions contending for the
// same session can still be resolved by Postgres killing one with 40P01
// (deadlock_detected) or 40001 (serialization_failure). Both are defined as
// safe to retry by restarting the WHOLE transaction — nothing committed, so
// a fresh attempt is exactly equivalent to a fresh call.
//
// The logic below is a deliberate copy of hcheckout/retry.go's
// retryOnSerializationFailure rather than an import: gaquota sits BELOW the
// handler packages (they import it), and importing hcheckout from here would
// invert that and create a cycle the moment hcheckout needs the quota
// mechanism. Keep the two in step.
package gaquota

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TxStarter is the narrow subset of PoolDB the quota mechanism needs.
// *pgxpool.Pool and the platform PoolDB both satisfy it structurally.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// quotaRetryAttempts is the number of times a quota transaction is
// attempted (the first try plus up to two retries) before the caller sees
// the deadlock / serialization error.
const quotaRetryAttempts = 3

// quotaRetryMinBackoff / quotaRetryMaxBackoff bound the jittered sleep
// between attempts, small and randomized so two transactions that just
// deadlocked do not collide again in lockstep.
const (
	quotaRetryMinBackoff = 10 * time.Millisecond
	quotaRetryMaxBackoff = 50 * time.Millisecond
)

// IsSerializationFailure reports whether err is (or wraps) a *pgconn.PgError
// carrying SQLSTATE 40P01 (deadlock_detected) or 40001
// (serialization_failure) — the two classes that are always safe to retry by
// restarting the whole transaction.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40P01", "40001":
		return true
	default:
		return false
	}
}

// InTx runs fn inside a fresh transaction taken from pool, committing when
// fn returns nil and rolling back otherwise, and retries the WHOLE
// transaction on a deadlock / serialization failure (3 attempts, 10-50 ms
// jittered backoff, ctx-aware).
//
// fn is never resumed after a failed attempt: a retry always restarts it
// from scratch inside a brand new transaction, never continues inside one
// Postgres has already aborted. Every other error — including the typed
// quota errors (ErrSeatedCategory, *BelowUsedError, ErrCategoryInUse, …) —
// is returned immediately on the first attempt.
//
// Callers that already own a transaction call the Ctx-and-txq functions
// (CreateCategory, SetQuantity, …) directly instead; they are the ones that
// must then honour the lock order themselves.
func InTx(ctx context.Context, pool TxStarter, q *gen.Queries, fn func(txq *gen.Queries) error) error {
	if pool == nil || q == nil {
		return errors.New("gaquota: InTx requires a pool and queries")
	}
	if fn == nil {
		return errors.New("gaquota: InTx requires a function")
	}
	return retryOnSerializationFailure(ctx, quotaRetryAttempts, func() error {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("gaquota: begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := fn(q.WithTx(tx)); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("gaquota: commit tx: %w", err)
		}
		return nil
	})
}

// retryOnSerializationFailure calls fn up to attempts times (attempts < 1 is
// treated as 1), retrying only on 40P01 / 40001.
func retryOnSerializationFailure(ctx context.Context, attempts int, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = fn()
		if err == nil || !IsSerializationFailure(err) {
			return err
		}
		if attempt == attempts-1 {
			return err
		}
		backoff := quotaRetryMinBackoff +
			time.Duration(rand.Int63n(int64(quotaRetryMaxBackoff-quotaRetryMinBackoff+1))) //nolint:gosec // jitter, not security-sensitive
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}
