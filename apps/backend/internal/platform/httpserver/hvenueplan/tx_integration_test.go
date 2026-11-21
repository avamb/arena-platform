//go:build integration

// tx_integration_test.go — the transaction rules of the intake routes that
// need a live PostgreSQL: a failed commit is reported as failed (so the bytes
// stored before the transaction are removed), and the billing fact never
// aborts the approval transaction.
package hvenueplan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

func vpiTxPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// vpiTxIntake makes an organization, a venue and an intake in operator.
func vpiTxIntake(t *testing.T, pool *pgxpool.Pool) gen.VenuePlanIntakeRow {
	t.Helper()
	ctx := context.Background()
	q := gen.New(pool)
	org, err := q.InsertOrganization(ctx, "VPI tx "+uuid.NewString()[:8], "vpi-tx-"+uuid.NewString(), "ES", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	venueID := uuid.New()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM venue_plan_intakes WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'VPI tx hall', 'Europe/Madrid')`, venueID, org.ID); err != nil {
		t.Fatalf("venue: %v", err)
	}
	in, err := q.InsertVenuePlanIntake(ctx, gen.InsertVenuePlanIntakeParams{
		OrgID: org.ID, VenueID: venueID, Channel: string(venueplan.ChannelEventCenterWeb), State: string(venueplan.StateOperator),
	})
	if err != nil {
		t.Fatalf("InsertVenuePlanIntake: %v", err)
	}
	return in
}

// locked reports false when the commit fails, so approve and the admin draft
// keep the stored SVG only after a real commit.
func TestVenuePlanIntake_LockedReportsAFailedCommit(t *testing.T) {
	pool := vpiTxPool(t)
	in := vpiTxIntake(t, pool)
	h := New(pool, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	committed := h.locked(rec, req, in.ID, true, http.StatusOK, func(ctx context.Context, tx pgx.Tx, _ *gen.Queries, cur gen.VenuePlanIntakeRow) (gen.VenuePlanIntakeRow, bool) {
		// An error the callback ignores leaves the transaction aborted: the
		// commit then rolls back and fails.
		_, _ = tx.Exec(ctx, `SELECT 1 / 0`)
		return cur, true
	})
	if committed {
		t.Fatal("locked reported a commit that rolled back")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("answer %d %s, want 500", rec.Code, rec.Body.String())
	}
}

// The reads of the billing fact run inside its SAVEPOINT: an error there
// leaves the approval transaction usable.
func TestVenuePlanIntake_BillingFactReadErrorKeepsTheTransaction(t *testing.T) {
	pool := vpiTxPool(t)
	in := vpiTxIntake(t, pool)
	h := New(pool, nil, nil, nil)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The rename is part of this transaction and goes away with its rollback;
	// inside it the questions read fails.
	if _, err := tx.Exec(ctx, `ALTER TABLE venue_plan_questions RENAME TO venue_plan_questions_hidden_by_test`); err != nil {
		t.Fatalf("rename: %v", err)
	}
	h.billingFact(ctx, tx, gen.New(pool).WithTx(tx), in, false)
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatalf("the approval transaction is unusable after a billing read error: %v", err)
	}
}
