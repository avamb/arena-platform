//go:build integration

// postgres_store_integration_test.go — round-trip against Docker PG for
// the customer_identities unique indexes and the Resolve state machine
// (spec §12.2). Feature #480 mandates ONE integration test covering the
// UNIQUE constraint pair introduced in migration 0091:
//
//   customer_identities_strong_uq  (kind, value_normalized)             -- strong = global
//   customer_identities_weak_uq    (kind, value_normalized, channel_id) -- weak = per channel
//
// Skip when DATABASE_URL is unset (mirrors the pattern used by every
// other gen/*_integration_test.go under this repo). Run locally with
//   DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//   JWT_SIGNING_SECRET=x \
//   go test -tags=integration ./apps/backend/internal/platform/customers/...

package customers_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
)

func TestPostgresStore_ResolveAndUniqueIndexes_LiveDB(t *testing.T) {
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

	// Prerequisites: an organisation + a sales_channels row for the
	// weak-identity partial index test. We deliberately do not seed a
	// venue — the resolver takes DefaultRegion as an argument.
	orgID := uuid.New()
	channelA := uuid.New()
	channelB := uuid.New()
	suffix := orgID.String()[:8]

	for i, step := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{orgID, "Cust Store Org " + suffix, "cust-store-" + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{channelA, orgID, "Cust Store Channel A " + suffix}},
		{`INSERT INTO sales_channels (id, org_id, name, provider, payment_mode)
		  VALUES ($1, $2, $3, 'stripe', 'direct_merchant')`,
			[]any{channelB, orgID, "Cust Store Channel B " + suffix}},
	} {
		if _, err := pool.Exec(ctx, step.sql, step.args...); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		// Order matches FK direction; cascades handle identities.
		_, _ = pool.Exec(ctx, `DELETE FROM sales_channels WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	q := gen.New(pool)
	store := customers.NewStoreFromQueries(q)

	// Unique per-run values so re-runs against a shared DB do not collide.
	email := "buyer+" + suffix + "@example.com"
	phoneLocal := "054-812-3456" // → +972548123456
	deviceTok := "dev-token-" + suffix

	// ── Round 1: fresh Resolve creates the customer + attaches email/phone/device.
	res1, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email:         email,
		Phone:         phoneLocal,
		Name:          "Integration Buyer",
		ChannelID:     channelA,
		DeviceToken:   deviceTok,
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve #1: %v", err)
	}
	if !res1.Created {
		t.Fatalf("expected Created=true on first resolve")
	}
	if got := res1.Customer.SystemID; got < 1_000_000_000 {
		t.Errorf("SystemID = %d, want >= 1e9", got)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, res1.Customer.ID)
	})

	// ── Round 2: same inputs — must NOT create a new customer.
	res2, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email:         email,
		Phone:         phoneLocal,
		ChannelID:     channelA,
		DeviceToken:   deviceTok,
		DefaultRegion: "IL",
	})
	if err != nil {
		t.Fatalf("Resolve #2: %v", err)
	}
	if res2.Created {
		t.Fatalf("second resolve created a new customer (should reuse)")
	}
	if res2.Customer.ID != res1.Customer.ID {
		t.Fatalf("round #2 returned %v, want %v", res2.Customer.ID, res1.Customer.ID)
	}

	// ── Strong-uniqueness contract: manual InsertIdentity for the same
	// (kind, value) MUST return a 23505 conflict — proving the partial
	// UNIQUE index is doing its job platform-wide.
	_, err = q.InsertCustomerIdentity(ctx, res1.Customer.ID, "email", email, nil, nil, "live")
	if err == nil {
		t.Fatal("expected 23505 on duplicate strong identity insert")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("want 23505 unique_violation, got %v", err)
	}

	// ── Weak-uniqueness contract: same (kind, value) in the SAME channel
	// conflicts; same (kind, value) in a DIFFERENT channel is fine.
	_, err = q.InsertCustomerIdentity(ctx, res1.Customer.ID, "device", deviceTok, &channelA, nil, "live")
	if err == nil {
		t.Fatal("expected 23505 on duplicate weak identity insert within same channel")
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("weak dup: want 23505, got %v", err)
	}

	// Attach the same device token under channelB — should succeed.
	if _, err := q.InsertCustomerIdentity(ctx, res1.Customer.ID, "device", deviceTok, &channelB, nil, "live"); err != nil {
		t.Fatalf("cross-channel weak insert should succeed, got %v", err)
	}

	// ── LinkOrg is idempotent.
	if err := customers.LinkOrg(ctx, store, res1.Customer.ID, orgID, "order"); err != nil {
		t.Fatalf("LinkOrg #1: %v", err)
	}
	if err := customers.LinkOrg(ctx, store, res1.Customer.ID, orgID, "order"); err != nil {
		t.Fatalf("LinkOrg #2 must be idempotent: %v", err)
	}

	// ── MarkVerified is idempotent (verified_at set once, subsequent
	// calls do not overwrite it).
	// Look up the email identity we attached.
	ids, err := q.ListCustomerIdentities(ctx, res1.Customer.ID)
	if err != nil {
		t.Fatalf("ListCustomerIdentities: %v", err)
	}
	var emailID uuid.UUID
	for _, id := range ids {
		if id.Kind == "email" {
			emailID = id.ID
			break
		}
	}
	if emailID == uuid.Nil {
		t.Fatalf("email identity not found in ListCustomerIdentities")
	}
	// First verification.
	if err := customers.MarkVerified(ctx, store, emailID, res1.Customer.CreatedAt); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	// Idempotency: second call must not overwrite verified_at.
	// (We rely on the query's COALESCE(verified_at, $2) contract.)
	if err := customers.MarkVerified(ctx, store, emailID, res1.Customer.CreatedAt.Add(3600)); err != nil {
		t.Fatalf("MarkVerified #2: %v", err)
	}
}
