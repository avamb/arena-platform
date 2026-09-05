//go:build integration

package compatids_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// TestCompatIDs_W1A2a_LiveDB is the W1-A2a (feature #475) integration test
// that verifies migration 0090 applied and the compatids package works
// end-to-end against Docker PostgreSQL.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	JWT_SIGNING_SECRET=anything \
//	go test -tags integration ./apps/backend/internal/platform/compatids/...
func TestCompatIDs_W1A2a_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	t.Run("Ensure_MintsPersistentArenaID", func(t *testing.T) {
		pid := uuid.New()
		first, err := compatids.Ensure(ctx, pool, compatids.KindAction, pid)
		if err != nil {
			t.Fatalf("Ensure first: %v", err)
		}
		if first < 1_000_000_000 {
			t.Fatalf("Ensure first: id %d < 1e9 (arena-minted must be >= 1e9)", first)
		}
		second, err := compatids.Ensure(ctx, pool, compatids.KindAction, pid)
		if err != nil {
			t.Fatalf("Ensure second: %v", err)
		}
		if second != first {
			t.Errorf("Ensure second: %d, want %d (must be idempotent)", second, first)
		}
	})

	t.Run("EnsureMany_UniquePerPlatformID", func(t *testing.T) {
		a, b := uuid.New(), uuid.New()
		out, err := compatids.EnsureMany(ctx, pool, compatids.KindCategoryPrice, []uuid.UUID{a, b})
		if err != nil {
			t.Fatalf("EnsureMany: %v", err)
		}
		if out[a] == 0 || out[b] == 0 || out[a] == out[b] {
			t.Errorf("EnsureMany: expected distinct positive ids, got a=%d b=%d", out[a], out[b])
		}
	})

	t.Run("Resolve_RoundTrip", func(t *testing.T) {
		pid := uuid.New()
		sid, err := compatids.Ensure(ctx, pool, compatids.KindVenue, pid)
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		got, err := compatids.Resolve(ctx, pool, compatids.KindVenue, sid)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got != pid {
			t.Errorf("Resolve: got %s, want %s", got, pid)
		}
	})

	t.Run("Resolve_UnknownReturnsNotFound", func(t *testing.T) {
		_, err := compatids.Resolve(ctx, pool, compatids.KindVenue, 1)
		if !errors.Is(err, compatids.ErrNotFound) {
			t.Errorf("Resolve unknown: got %v; want ErrNotFound", err)
		}
	})

	t.Run("RegisterExternal_BelowCeiling", func(t *testing.T) {
		pid := uuid.New()
		// Pick a system_id in the bil24 range that is extremely unlikely to
		// collide with anything else the test suite creates.
		sid := int64(500_000_000) + int64(uint32(pid.ID())%1_000_000)
		if err := compatids.RegisterExternal(ctx, pool, compatids.KindCity, pid, sid); err != nil {
			t.Fatalf("RegisterExternal: %v", err)
		}
		// Idempotent second call.
		if err := compatids.RegisterExternal(ctx, pool, compatids.KindCity, pid, sid); err != nil {
			t.Errorf("RegisterExternal idempotent: %v; want nil", err)
		}
		// Round-trip via Resolve.
		got, err := compatids.Resolve(ctx, pool, compatids.KindCity, sid)
		if err != nil {
			t.Fatalf("Resolve after RegisterExternal: %v", err)
		}
		if got != pid {
			t.Errorf("Resolve: got %s, want %s", got, pid)
		}
		// Ensure after RegisterExternal must return the external id, not mint a new one.
		got2, err := compatids.Ensure(ctx, pool, compatids.KindCity, pid)
		if err != nil {
			t.Fatalf("Ensure after RegisterExternal: %v", err)
		}
		if got2 != sid {
			t.Errorf("Ensure after RegisterExternal: got %d; want %d", got2, sid)
		}
	})

	t.Run("RegisterExternal_RejectsCeiling", func(t *testing.T) {
		err := compatids.RegisterExternal(ctx, pool, compatids.KindCountry, uuid.New(), 1_000_000_000)
		if !errors.Is(err, compatids.ErrExternalIDOutOfRange) {
			t.Errorf("RegisterExternal(1e9): got %v; want ErrExternalIDOutOfRange", err)
		}
	})

	t.Run("RegisterExternal_Collision", func(t *testing.T) {
		pid1, pid2 := uuid.New(), uuid.New()
		// Pick a value that combines both pids so the seed varies per run.
		base := int64(400_000_000) + int64(uint32(pid1.ID())%1_000_000)
		if err := compatids.RegisterExternal(ctx, pool, compatids.KindCountry, pid1, base); err != nil {
			t.Fatalf("first: %v", err)
		}
		err := compatids.RegisterExternal(ctx, pool, compatids.KindCountry, pid2, base)
		if !errors.Is(err, compatids.ErrExternalIDCollision) {
			t.Errorf("collision: got %v; want ErrExternalIDCollision", err)
		}
	})
}
