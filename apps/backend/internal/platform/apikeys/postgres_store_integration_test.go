//go:build integration

// postgres_store_integration_test.go — round-trip against Docker PG for the
// api_keys.key_prefix UNIQUE constraint (migration 0096) and the full
// Issue -> Authenticate -> TouchLastUsed cycle against a real Store.
//
// Skip when DATABASE_URL is unset (same pattern as
// internal/platform/customers/postgres_store_integration_test.go). Run
// locally with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	JWT_SIGNING_SECRET=x \
//	go test -tags=integration ./apps/backend/internal/platform/apikeys/...
package apikeys_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
)

func TestPostgresStore_IssueAuthenticateAndUniquePrefix_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	orgID := uuid.New()
	userID := uuid.New()
	suffix := orgID.String()[:8]

	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{orgID, "API Key Store Org " + suffix, "apikey-store-" + suffix}},
		{`INSERT INTO users (id, email, password_hash, email_verified_at)
		  VALUES ($1, $2, 'x', now())`,
			[]any{userID, "apikey-owner+" + suffix + "@example.com"}},
	} {
		if _, err := pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	q := gen.New(pool)
	store := apikeys.NewStoreFromQueries(q)

	key, raw, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID:     orgID,
		Name:      "integration key " + suffix,
		Scopes:    []string{"event.read", "import.bil24_session"},
		CreatedBy: userID,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got := key.KeyPrefix; len(got) != apikeys.KeyPrefixLen {
		t.Fatalf("stored KeyPrefix length = %d, want %d", len(got), apikeys.KeyPrefixLen)
	}

	now := time.Now()
	authed, err := apikeys.Authenticate(ctx, store, raw, now)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if authed.ID != key.ID {
		t.Fatalf("Authenticate returned ID %v, want %v", authed.ID, key.ID)
	}

	touched, err := apikeys.TouchLastUsed(ctx, store, authed, now)
	if err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	if !touched {
		t.Fatal("expected first TouchLastUsed to write")
	}
	reloaded, err := apikeys.Authenticate(ctx, store, raw, now)
	if err != nil {
		t.Fatalf("Authenticate (reload): %v", err)
	}
	if reloaded.LastUsedAt == nil {
		t.Fatal("expected last_used_at to be set in the DB after TouchLastUsed")
	}

	// The DB-level UNIQUE constraint on key_prefix must reject a second row
	// that reuses the same prefix, independent of the package's in-process
	// random generation never colliding.
	_, err = q.InsertAPIKey(ctx, orgID, nil, "prefix collision", key.KeyPrefix, "some-other-hash",
		[]string{"event.read"}, userID, nil)
	if err == nil {
		t.Fatal("expected a UNIQUE violation when reusing key_prefix, got nil error")
	}
}
