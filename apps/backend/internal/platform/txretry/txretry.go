// Package txretry is the one implementation of "retry the WHOLE transaction
// when Postgres resolves a lock race by killing it".
//
// Every transaction that mutates seat or place inventory follows the
// platform-wide lock order (AGENTS.md): the sessions row
// (IncrementSessionSeatStatusVersion) first, then inventory_ledger, then the
// session_seats rows. Even with that order kept everywhere, two transactions
// contending for the same session can still be resolved by Postgres with
// 40P01 (deadlock_detected) or 40001 (serialization_failure). Both are
// defined as safe to retry by restarting the whole transaction: nothing was
// committed, so a fresh attempt is exactly equivalent to a fresh call.
//
// hcheckout's hold primitives and seatlayout.ApplyBatch both go through Do.
// (gaquota keeps its own copy for now: it sits below the handler packages
// and was left untouched by task S1c.)
package txretry

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultAttempts is the number of times a transaction is attempted (the
// first try plus up to two retries) before the caller sees the error.
const DefaultAttempts = 3

// MinBackoff / MaxBackoff bound the jittered sleep between attempts. Small
// and randomized so two transactions that just deadlocked against each other
// do not collide again in lockstep.
const (
	MinBackoff = 10 * time.Millisecond
	MaxBackoff = 50 * time.Millisecond
)

// IsSerializationFailure reports whether err is (or wraps, via errors.As) a
// *pgconn.PgError carrying SQLSTATE 40P01 (deadlock_detected) or 40001
// (serialization_failure).
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

// Do calls fn up to attempts times (attempts < 1 is treated as 1). fn owns a
// whole transaction — begin, do the work, commit — and is never resumed after
// a failed attempt: a retry always restarts it from scratch inside a brand
// new transaction.
//
// Only a deadlock (40P01) or a serialization failure (40001) triggers a
// retry; every other error is returned at once. Retries sleep a jittered
// backoff and honour ctx cancellation.
func Do(ctx context.Context, attempts int, fn func() error) error {
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
		backoff := MinBackoff +
			time.Duration(rand.Int63n(int64(MaxBackoff-MinBackoff+1))) //nolint:gosec // jitter, not security-sensitive
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
