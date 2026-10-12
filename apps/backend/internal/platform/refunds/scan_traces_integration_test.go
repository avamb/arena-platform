//go:build integration

package refunds_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// N10: who was told about a scan-blocked refund, and who tried, are personal
// data with a retention: the per-address delivery marks go after 30 days, and
// the attempt journal forgets the person's name after 180 days (the counts
// stay). Erasing a user forgets both at once.
func TestScanTraces_RetentionAndErasure(t *testing.T) {
	f := newFixture(t, testPool(t), "pay03fake")
	ctx := context.Background()
	oldNotice, freshNotice := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO refund_scan_notice_sends (notice_id, email, sent_at) VALUES ($1, 'old@owner.test', now() - interval '40 days'),
		($2, 'fresh@owner.test', now())`, oldNotice, freshNotice)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM refund_scan_notice_sends WHERE notice_id = ANY($1)`, []uuid.UUID{oldNotice, freshNotice})
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM refund_scan_blocks WHERE org_id = $1`, f.org)
	})
	f.exec(t, `INSERT INTO refund_scan_blocks (org_id, ticket_id, actor_key, actor_label, first_at, last_at)
		VALUES ($1, $2, 'user:old', 'Old Person', now() - interval '200 days', now() - interval '200 days'),
		       ($1, $3, 'user:fresh', 'Fresh Person', now(), now())`, f.org, f.tickets[0], f.tickets[1])

	e := f.engine(&fakeModule{partial: true}, true)
	if _, err := e.Sweep(ctx, time.Now(), nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var sends, labels int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM refund_scan_notice_sends WHERE notice_id = ANY($1)`, []uuid.UUID{oldNotice, freshNotice}).Scan(&sends)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM refund_scan_blocks WHERE org_id = $1 AND actor_label IS NOT NULL`, f.org).Scan(&labels)
	if sends != 1 || labels != 1 {
		t.Fatalf("after the sweep: %d notice marks, %d names; want 1 and 1 (only the fresh ones)", sends, labels)
	}

	// Erasure of the fresh actor and owner.
	q := gen.New(f.pool)
	u, err := q.InsertUser(ctx, "fresh@owner.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	f.exec(t, `UPDATE refund_scan_blocks SET actor_key = 'user:' || $2::text WHERE org_id = $1 AND actor_key = 'user:fresh'`, f.org, u.ID)
	if err := q.ForgetUserRefundScanTraces(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM refund_scan_notice_sends WHERE notice_id = $1`, freshNotice).Scan(&sends)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM refund_scan_blocks WHERE org_id = $1 AND actor_label IS NOT NULL`, f.org).Scan(&labels)
	if sends != 0 || labels != 0 {
		t.Fatalf("after erasing the user: %d notice marks, %d names; want none", sends, labels)
	}
}
