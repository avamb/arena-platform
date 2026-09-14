package gaquota

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestUnitKeyPrefix_UsesTPrefixNotC guards the one thing about the key
// prefix that must never drift: 'ga|c' is the seating-geometry category
// index, so a quota category's own places must use a different letter or
// they collide with a geometry key under UNIQUE (session_id, seat_key).
func TestUnitKeyPrefix_UsesTPrefixNotC(t *testing.T) {
	got := unitKeyPrefix(3)
	if got != "ga|t3" {
		t.Fatalf("unitKeyPrefix(3) = %q, want %q", got, "ga|t3")
	}
	if strings.HasPrefix(got, "ga|c") {
		t.Fatalf("unitKeyPrefix must not use the geometry prefix ga|c, got %q", got)
	}
	if strings.HasPrefix(got, "ga|pool") {
		t.Fatalf("unitKeyPrefix must not use the legacy pool prefix, got %q", got)
	}
}

func TestUnitKeyPrefix_DistinctPerCategoryNumber(t *testing.T) {
	seen := map[string]bool{}
	for seq := int32(1); seq <= 25; seq++ {
		p := unitKeyPrefix(seq)
		if seen[p] {
			t.Fatalf("prefix %q produced twice", p)
		}
		seen[p] = true
	}
	// A prefix must not be a prefix of another prefix + '|', or the
	// LIKE '<prefix>|%' index scan of one category would match another.
	for a := range seen {
		for b := range seen {
			if a != b && strings.HasPrefix(b+"|", a+"|") {
				t.Fatalf("prefix %q is ambiguous with %q", a, b)
			}
		}
	}
}

func TestBelowUsedError_FloorAndMessage(t *testing.T) {
	tierID := uuid.New()
	err := &BelowUsedError{
		TierID: tierID, Requested: 2, Used: 4, Total: 10, Deletable: 5,
	}
	if got := err.Floor(); got != 5 {
		t.Fatalf("Floor() = %d, want 5 (10 owned - 5 deletable)", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, tierID.String()) {
		t.Errorf("message should name the category: %q", msg)
	}
	for _, want := range []string{"2", "4", "5", "10"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q is missing %q", msg, want)
		}
	}
}

// TestBelowUsedError_FloorIsUsedWhenEverythingFreeIsDeletable: with no
// undeletable place the floor collapses to the used count, which is the
// ordinary "cannot shrink below held+sold" rule.
func TestBelowUsedError_FloorIsUsedWhenEverythingFreeIsDeletable(t *testing.T) {
	err := &BelowUsedError{Requested: 1, Used: 4, Total: 10, Deletable: 6}
	if got := err.Floor(); got != int32(4) {
		t.Fatalf("Floor() = %d, want 4", got)
	}
}

func TestIsSerializationFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("boom"), false},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"wrapped deadlock", errors.Join(errors.New("ctx"), &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"fk violation", &pgconn.PgError{Code: "23503"}, false},
		{"check violation", &pgconn.PgError{Code: "23514"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSerializationFailure(tc.err); got != tc.want {
				t.Fatalf("IsSerializationFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryOnSerializationFailure_RetriesOnlyDeadlocks proves the retry
// contract: a deadlock restarts the whole body up to the attempt limit,
// every other error returns on the first attempt.
func TestRetryOnSerializationFailure_RetriesOnlyDeadlocks(t *testing.T) {
	t.Run("succeeds after a deadlock", func(t *testing.T) {
		calls := 0
		err := retryOnSerializationFailure(context.Background(), quotaRetryAttempts, func() error {
			calls++
			if calls == 1 {
				return &pgconn.PgError{Code: "40P01"}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("gives up after the attempt limit", func(t *testing.T) {
		calls := 0
		err := retryOnSerializationFailure(context.Background(), quotaRetryAttempts, func() error {
			calls++
			return &pgconn.PgError{Code: "40001"}
		})
		if !IsSerializationFailure(err) {
			t.Fatalf("expected the serialization error back, got %v", err)
		}
		if calls != quotaRetryAttempts {
			t.Fatalf("calls = %d, want %d", calls, quotaRetryAttempts)
		}
	})

	t.Run("a typed quota error is not retried", func(t *testing.T) {
		calls := 0
		err := retryOnSerializationFailure(context.Background(), quotaRetryAttempts, func() error {
			calls++
			return ErrSeatedCategory
		})
		if !errors.Is(err, ErrSeatedCategory) {
			t.Fatalf("expected ErrSeatedCategory, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("honours ctx cancellation between attempts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()
		err := retryOnSerializationFailure(ctx, quotaRetryAttempts, func() error {
			calls++
			return &pgconn.PgError{Code: "40P01"}
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if calls > quotaRetryAttempts {
			t.Fatalf("calls = %d, want at most %d", calls, quotaRetryAttempts)
		}
	})
}

func TestInTx_RejectsMissingDependencies(t *testing.T) {
	if err := InTx(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil pool")
	}
}

// TestNilQueriesGuards: every exported entry point refuses a nil *gen.Queries
// instead of panicking — handlers wire these from optional dependencies.
func TestNilQueriesGuards(t *testing.T) {
	ctx := context.Background()
	sess, tier := uuid.New(), uuid.New()

	if err := CreateCategory(ctx, nil, sess, tier, 5); err == nil {
		t.Error("CreateCategory: expected an error")
	}
	if _, err := SetQuantity(ctx, nil, sess, tier, 5); err == nil {
		t.Error("SetQuantity: expected an error")
	}
	if err := SetOpen(ctx, nil, sess, tier, false); err == nil {
		t.Error("SetOpen: expected an error")
	}
	if err := DeleteCategory(ctx, nil, sess, tier); err == nil {
		t.Error("DeleteCategory: expected an error")
	}
	if err := Recompute(ctx, nil, sess); err == nil {
		t.Error("Recompute: expected an error")
	}
	if _, err := CategoryKind(ctx, nil, sess, tier); err == nil {
		t.Error("CategoryKind: expected an error")
	}
	if _, err := SessionStats(ctx, nil, sess); err == nil {
		t.Error("SessionStats: expected an error")
	}
}

// TestQuantityMustBePositive: decision 3 — a GA category with a quantity of
// 0 is illegal, and the ticket_tiers_capacity_positive CHECK agrees. The
// guard fires before any query, so a nil *gen.Queries is fine here.
func TestQuantityMustBePositive(t *testing.T) {
	ctx := context.Background()
	sess, tier := uuid.New(), uuid.New()
	for _, q := range []int32{0, -1, -100} {
		if err := CreateCategory(ctx, nil, sess, tier, q); err == nil {
			t.Errorf("CreateCategory(%d): expected an error", q)
		}
		if _, err := SetQuantity(ctx, nil, sess, tier, q); err == nil {
			t.Errorf("SetQuantity(%d): expected an error", q)
		}
	}
}
