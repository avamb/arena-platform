//go:build integration

package promoattempts

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each attempt is written in its own goroutine, so two identical checks in
// a row reach insertRow side by side. Under READ COMMITTED both used to pass
// the NOT EXISTS and the journal got two rows where one was promised (CI on
// 2026-10-25: TestPromoAttempts_GatewayJournalsWrongSessionAndApplied saw 2).
func TestInsertRow_ConcurrentSameCheckStoresOneRow(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM organizations ORDER BY created_at LIMIT 1`).Scan(&orgID); err != nil {
		t.Skipf("no organization to attach attempts to: %v", err)
	}
	key := "race-test:" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM promo_code_attempts WHERE dedup_key = $1`, key)
	})

	r := New(pool, "salt", nil)
	row := Row{
		Attempt:  Attempt{OrgID: orgID, Surface: SurfaceGatewayAddPromoCodes},
		Code:     "RED30",
		Result:   ResultRejected,
		Reason:   ReasonWrongSession,
		DedupKey: key,
		Sessions: []uuid.UUID{},
	}
	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rw := row
			if i%2 == 1 {
				rw.Code = "red30" // the same check typed in another case
			}
			errs <- r.insertRow(ctx, rw)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("insertRow: %v", err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM promo_code_attempts WHERE dedup_key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows for one check written %d times = %d, want 1", writers, n)
	}
}
