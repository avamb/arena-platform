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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

// holdMutationRetryAttempts is the maximum number of times a hold-mutation
// transaction is attempted (the first try plus up to two retries) before the
// caller gives up and surfaces the deadlock/serialization error.
const holdMutationRetryAttempts = txretry.DefaultAttempts

// isSerializationFailure reports whether err is (or wraps, via errors.As) a
// *pgconn.PgError carrying SQLSTATE 40P01 (deadlock_detected) or 40001
// (serialization_failure) — the two Postgres error classes that are always
// safe to retry by restarting the whole transaction. The rule lives in
// txretry, shared with seatlayout.ApplyBatch.
func isSerializationFailure(err error) bool {
	return txretry.IsSerializationFailure(err)
}

// IsSerializationFailure is the exported form of isSerializationFailure, for
// use by callers outside hcheckout that need the same classification (e.g.
// hbil24's error-envelope mapping).
func IsSerializationFailure(err error) bool {
	return isSerializationFailure(err)
}

// retryOnSerializationFailure calls fn up to attempts times (attempts < 1 is
// treated as 1). fn owns a whole transaction — begin, do the work, commit —
// and MUST NOT be resumed after a failed attempt: a retry always restarts fn
// from scratch inside a brand new transaction, never continues inside one
// that Postgres already aborted.
//
// Only a deadlock (40P01) or serialization failure (40001) triggers a retry;
// every other error — including the typed hold errors (*CapacityError,
// *SeatConflictsError, *NotMutableError, …) — is returned immediately on the
// first attempt. The loop itself is txretry.Do (10-50 ms jittered backoff,
// ctx-aware).
func retryOnSerializationFailure(ctx context.Context, attempts int, fn func() error) error {
	return txretry.Do(ctx, attempts, fn)
}
