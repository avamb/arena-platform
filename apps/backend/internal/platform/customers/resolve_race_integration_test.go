//go:build integration

// resolve_race_integration_test.go — live-DB coverage for the LOW-severity
// concurrency defect fixed 2026-09-14: customer_identities_strong_uq is a
// GLOBAL unique index (migration 0091), so two concurrent first-time
// Resolve calls for the identical brand-new email both miss the initial
// lookup, both attempt to create a customer, and the loser used to fail
// outright with SQLSTATE 23505 (hbil24 CREATE_USER logged an ERROR and
// answered resultCode -1; hfeed's checkout-confirm path swallowed the
// error as "non-fatal" while the ambient transaction was actually already
// aborted, risking a 25P02 on the very next statement).
//
// This test drives customers.Resolve directly with real concurrent
// goroutines against a live Postgres pool — no mutex, no test-controlled
// interleaving — and asserts every caller succeeds, agrees on the winning
// customer, and that the loser's InsertCustomer never survives as an
// orphan row (see resolve.go's withRace / postgres_store.go's
// WithSavepoint).
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_customers?sslmode=disable \
//	JWT_SIGNING_SECRET=x \
//	go test -tags=integration ./apps/backend/internal/platform/customers/... -run ConcurrentResolve
package customers_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
)

func TestPostgresStore_ConcurrentResolve_SameNewEmail_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	// >= goroutine count so every concurrent Resolve genuinely races on its
	// own connection instead of queueing behind the pool.
	const n = 20
	cfg.MaxConns = n + 5
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Unique per run: customer_identities_strong_uq and the display_name
	// probe below are both global, unscoped by org/channel (AGENTS.md).
	runID := uuid.New().String()[:8]
	email := "race-" + runID + "@example.com"
	displayName := "Race Buyer " + runID

	var (
		wg      sync.WaitGroup
		results = make([]customers.ResolveResult, n)
		errs    = make([]error, n)
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			store := customers.NewStoreFromQueries(gen.New(pool))
			results[i], errs[i] = customers.Resolve(ctx, store, customers.ResolveInput{
				Email: email,
				Name:  displayName,
			})
		}(i)
	}
	wg.Wait()

	var winner uuid.UUID
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: Resolve failed: %v", i, errs[i])
		}
		if i == 0 {
			winner = results[i].Customer.ID
			continue
		}
		if results[i].Customer.ID != winner {
			t.Fatalf("goroutine %d resolved to customer %v, want %v — all %d concurrent "+
				"first-time resolves for the same new email must agree on one winner",
				i, results[i].Customer.ID, winner, n)
		}
	}
	if winner == uuid.Nil {
		t.Fatal("no winning customer id recorded")
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, winner)
	})

	// Exactly one identity row for this email, pointing at the agreed winner.
	var identityCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM customer_identities WHERE kind = 'email' AND value_normalized = $1`,
		email).Scan(&identityCount); err != nil {
		t.Fatalf("count email identities: %v", err)
	}
	if identityCount != 1 {
		t.Fatalf("email identity rows for %s = %d, want exactly 1", email, identityCount)
	}
	var identityCustomerID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer_identities WHERE kind = 'email' AND value_normalized = $1`,
		email).Scan(&identityCustomerID); err != nil {
		t.Fatalf("read email identity customer_id: %v", err)
	}
	if identityCustomerID != winner {
		t.Fatalf("customer_identities.customer_id = %v, want the agreed winner %v", identityCustomerID, winner)
	}

	// No orphan customer row: every losing goroutine's InsertCustomer ran
	// inside the SAME savepoint as its losing identity insert, so a 23505
	// there must have rolled the customer row back too. display_name is
	// unique to this run, so ANY row carrying it — winner included — is
	// counted here; more than one means an orphan leaked.
	var customerRowsWithName int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM customers WHERE display_name = $1`,
		displayName).Scan(&customerRowsWithName); err != nil {
		t.Fatalf("count customers by display_name: %v", err)
	}
	if customerRowsWithName != 1 {
		t.Fatalf("customers rows with display_name %q = %d, want exactly 1 (no orphan from a losing race)",
			displayName, customerRowsWithName)
	}
}
