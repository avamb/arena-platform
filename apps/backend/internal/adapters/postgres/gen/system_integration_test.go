//go:build integration

package gen_test

import (
	"context"
	"os"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSelectUUIDv7_LiveDB is the integration counterpart of the unit mock test.
// It connects to a real PostgreSQL 17 instance (DATABASE_URL env var) and
// verifies that uuidv7() returns a properly structured UUIDv7 value.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/adapters/postgres/gen/
func TestSelectUUIDv7_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}

	q := gen.New(pool)
	id, err := q.SelectUUIDv7(ctx)
	if err != nil {
		t.Fatalf("SelectUUIDv7: %v", err)
	}

	// UUIDv7: byte[6] high nibble must be 0x7.
	const versionByte = 6
	version := id[versionByte] >> 4
	if version != 7 {
		t.Errorf("expected UUIDv7 version=7, got version=%d (uuid=%v)", version, id)
	}

	// UUIDv7: byte[8] high two bits must be 0b10 (RFC 9562 variant).
	const variantByte = 8
	variant := id[variantByte] >> 6
	if variant != 2 {
		t.Errorf("expected RFC 4122 variant=2 in byte[8], got %d (uuid=%v)", variant, id)
	}

	t.Logf("SelectUUIDv7 returned: %v (version=%d, variant=%d)", id, version, variant)
}
