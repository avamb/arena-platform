//go:build integration

package trackingconsent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStore_WithdrawalAlwaysApplies runs the real UPDATE against a temporary
// checkout_sessions table (it shadows the real one inside the transaction, so
// nothing is left behind): a "yes" stamped ahead of the server clock must not
// block a later "no", and a stale "yes" arriving after the "no" is refused.
func TestStore_WithdrawalAlwaysApplies(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE checkout_sessions (
		    id                           uuid PRIMARY KEY,
		    tracking_consent_granted     boolean,
		    tracking_consent_version     text,
		    tracking_consent_at          timestamptz,
		    tracking_consent_categories  jsonb,
		    tracking_consent_received_at timestamptz
		) ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO pg_temp.checkout_sessions (id) VALUES ($1)`, id); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	store := func(granted bool, at time.Time) bool {
		t.Helper()
		ok, err := Store(ctx, tx, id, Consent{Granted: granted, Version: "v1", At: at})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	read := func() (bool, time.Time) {
		t.Helper()
		var g bool
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT tracking_consent_granted, tracking_consent_at FROM pg_temp.checkout_sessions WHERE id = $1`, id).
			Scan(&g, &at); err != nil {
			t.Fatal(err)
		}
		return g, at.UTC()
	}

	// A "yes" whose browser clock runs 4 minutes ahead (inside maxFutureSkew).
	if !store(true, now.Add(4*time.Minute)) {
		t.Fatal("the first yes was not stored")
	}
	// The buyer says "no" a minute later by the server clock: it applies.
	if !store(false, now.Add(time.Minute)) {
		t.Fatal("a later no was blocked by an earlier yes stamped in the future")
	}
	if g, at := read(); g || !at.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("after the no: granted=%v at=%v, want false at the later of the two times", g, at)
	}
	// A stale "yes" (older than what is stored) arriving after the no is refused.
	if store(true, now.Add(2*time.Minute)) {
		t.Fatal("a stale yes overwrote the withdrawal")
	}
	if g, _ := read(); g {
		t.Fatal("the stale yes turned consent back on")
	}
	// A genuinely later "yes" still applies.
	if !store(true, now.Add(5*time.Minute)) {
		t.Fatal("a later yes was refused")
	}
	if g, _ := read(); !g {
		t.Fatal("the later yes was not stored")
	}
}
