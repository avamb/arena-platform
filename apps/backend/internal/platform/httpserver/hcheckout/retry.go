// retry.go — bounded retry for hold mutations that lose a Postgres deadlock
// or serialization race (feature: HIGH-severity GA RESERVATION deadlock
// fix). See hold_api.go / hold_mutation.go / hold_shrink.go for the lock
// order every hold-mutation transaction must follow:
//
//	sessions row (seat_status_version bump) → inventory_ledger row
//	(ReserveCapacity / ReleaseCapacity / ConfirmCapacity) → session_seats /
//	ga_unit rows.
//
// Even with that order enforced everywhere, two DIFFERENT reservations can
// still legitimately contend for the two locks in a way Postgres resolves by
// killing one transaction with a 40P01 deadlock_detected (or, more rarely
// under a stricter isolation level, a 40001 serialization_failure). Both are
// defined by Postgres as safe to retry the WHOLE transaction from scratch —
// nothing committed, so a fresh attempt is exactly equivalent to a fresh
// call.
package hcheckout

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// holdMutationRetryAttempts is the maximum number of times a hold-mutation
// transaction is attempted (the first try plus up to two retries) before the
// caller gives up and surfaces the deadlock/serialization error.
const holdMutationRetryAttempts = 3

// holdMutationRetryMinBackoff / holdMutationRetryMaxBackoff bound the
// jittered sleep between retry attempts. Small and randomized so two
// transactions that just deadlocked against each other do not immediately
// collide again in lockstep.
const (
	holdMutationRetryMinBackoff = 10 * time.Millisecond
	holdMutationRetryMaxBackoff = 50 * time.Millisecond
)

// isSerializationFailure reports whether err is (or wraps, via errors.As) a
// *pgconn.PgError carrying SQLSTATE 40P01 (deadlock_detected) or 40001
// (serialization_failure) — the two Postgres error classes that are always
// safe to retry by restarting the whole transaction.
func isSerializationFailure(err error) bool {
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

// IsSerializationFailure is the exported form of isSerializationFailure, for
// use by callers outside hcheckout that need the same classification (e.g.
// hbil24's error-envelope mapping).
func IsSerializationFailure(err error) bool {
	return isSerializationFailure(err)
}

// retryOnSerializationFailure calls fn up to attempts times (attempts < 1 is
// treated as 1). fn owns a whole transaction — begin, do the work, commit —
// and MUST NOT be resumed after a failed attempt: a retry always restarts
// fn from scratch inside a brand new transaction, never continues inside one
// that Postgres already aborted.
//
// Only a deadlock (40P01) or serialization failure (40001) triggers a retry;
// every other error — including the typed hold errors (*CapacityError,
// *SeatConflictsError, *NotMutableError, …) and plain infrastructure errors
// that are not a Postgres serialization race — is returned immediately on
// the first attempt. Retries sleep a small jittered backoff between
// attempts and honour ctx cancellation.
func retryOnSerializationFailure(ctx context.Context, attempts int, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = fn()
		if err == nil || !isSerializationFailure(err) {
			return err
		}
		if attempt == attempts-1 {
			// Retries exhausted — surface the last deadlock/serialization
			// error to the caller.
			return err
		}
		backoff := holdMutationRetryMinBackoff +
			time.Duration(rand.Int63n(int64(holdMutationRetryMaxBackoff-holdMutationRetryMinBackoff+1))) //nolint:gosec // jitter, not security-sensitive
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
