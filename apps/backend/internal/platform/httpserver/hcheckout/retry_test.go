// retry_test.go — unit tests for retryOnSerializationFailure /
// isSerializationFailure (see retry.go). Covers: retries on wrapped 40P01
// and 40001, stops after the configured number of attempts, does not retry
// any other error, and honours context cancellation while sleeping between
// attempts.
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func deadlockErr() error {
	return fmt.Errorf("hcheckout: bump seat_status_version: %w", &pgconn.PgError{
		Code:    "40P01",
		Message: "deadlock detected",
	})
}

func serializationErr() error {
	return fmt.Errorf("hcheckout: reserve capacity: %w", &pgconn.PgError{
		Code:    "40001",
		Message: "could not serialize access due to concurrent update",
	})
}

func TestIsSerializationFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadlock wrapped", deadlockErr(), true},
		{"serialization wrapped", serializationErr(), true},
		{"unrelated pg error", fmt.Errorf("wrap: %w", &pgconn.PgError{Code: "23505", Message: "unique violation"}), false},
		{"plain error", errors.New("boom"), false},
		{"typed hold error", &CapacityError{Requested: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSerializationFailure(tc.err); got != tc.want {
				t.Errorf("isSerializationFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if got := IsSerializationFailure(tc.err); got != tc.want {
				t.Errorf("IsSerializationFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryOnSerializationFailure_RetriesDeadlock proves a 40P01 on the
// first two attempts is retried and a success on the third attempt is
// returned without error, and that the function was called exactly 3 times.
func TestRetryOnSerializationFailure_RetriesDeadlock(t *testing.T) {
	calls := 0
	err := retryOnSerializationFailure(context.Background(), 3, func() error {
		calls++
		if calls < 3 {
			return deadlockErr()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

// TestRetryOnSerializationFailure_RetriesSerializationFailure mirrors the
// deadlock case for SQLSTATE 40001.
func TestRetryOnSerializationFailure_RetriesSerializationFailure(t *testing.T) {
	calls := 0
	err := retryOnSerializationFailure(context.Background(), 3, func() error {
		calls++
		if calls < 2 {
			return serializationErr()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

// TestRetryOnSerializationFailure_StopsAfterN proves the helper gives up
// after exactly `attempts` tries and surfaces the last deadlock error.
func TestRetryOnSerializationFailure_StopsAfterN(t *testing.T) {
	calls := 0
	err := retryOnSerializationFailure(context.Background(), 3, func() error {
		calls++
		return deadlockErr()
	})
	if err == nil {
		t.Fatal("expected the exhausted deadlock error, got nil")
	}
	if !isSerializationFailure(err) {
		t.Fatalf("expected the returned error to still classify as a serialization failure, got: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", calls)
	}
}

// TestRetryOnSerializationFailure_DoesNotRetryOtherErrors proves a typed
// hold error (or any non-serialization error) returns immediately on the
// first attempt with no retry.
func TestRetryOnSerializationFailure_DoesNotRetryOtherErrors(t *testing.T) {
	wantErr := &CapacityError{Requested: 5}
	calls := 0
	err := retryOnSerializationFailure(context.Background(), 3, func() error {
		calls++
		return wantErr
	})
	if !errors.Is(err, error(wantErr)) && err != error(wantErr) { //nolint:errorlint // identity check
		var capErr *CapacityError
		if !errors.As(err, &capErr) || capErr != wantErr {
			t.Fatalf("expected the exact typed error back, got: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry) for a non-serialization error, got %d", calls)
	}
}

// TestRetryOnSerializationFailure_HonoursContextCancel proves that a
// cancelled context aborts the retry loop during the backoff sleep instead
// of looping until the attempt budget is exhausted.
func TestRetryOnSerializationFailure_HonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryOnSerializationFailure(ctx, 5, func() error {
		calls++
		if calls == 1 {
			// Cancel right after the first failed attempt so the backoff
			// sleep before attempt 2 observes ctx.Done().
			cancel()
		}
		return deadlockErr()
	})
	if err == nil {
		t.Fatal("expected an error (context cancellation), got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected the loop to stop after 1 attempt once ctx was cancelled, got %d calls", calls)
	}
}

// TestRetryOnSerializationFailure_ZeroOrNegativeAttemptsMeansOne proves the
// documented floor: attempts < 1 behaves like attempts == 1 (a single try,
// no retry).
func TestRetryOnSerializationFailure_ZeroOrNegativeAttemptsMeansOne(t *testing.T) {
	calls := 0
	err := retryOnSerializationFailure(context.Background(), 0, func() error {
		calls++
		return deadlockErr()
	})
	if err == nil {
		t.Fatal("expected the deadlock error back")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", calls)
	}
}

// TestRetryOnSerializationFailure_BackoffIsBounded is a cheap sanity check
// that retries do not take an unreasonable amount of wall-clock time (the
// documented jitter window is 10-50ms per attempt).
func TestRetryOnSerializationFailure_BackoffIsBounded(t *testing.T) {
	start := time.Now()
	calls := 0
	_ = retryOnSerializationFailure(context.Background(), 3, func() error {
		calls++
		return deadlockErr()
	})
	elapsed := time.Since(start)
	// 2 backoff sleeps between 3 attempts, each capped at 50ms — allow
	// generous headroom for CI/host scheduling jitter.
	if elapsed > 2*time.Second {
		t.Fatalf("retry backoff took too long: %v", elapsed)
	}
}
