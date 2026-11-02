//go:build integration

package marketing

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The retention rule В-7: a withdrawn or never confirmed consent goes 3 years
// after it stopped (or never came) into force, with its journal; a confirmed
// one stays whatever its age.
func TestPurge_KeepsConsentsInForceAndDeletesOldOnes(t *testing.T) {
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

	orgID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Purge "+orgID.String()[:8], "purge-"+orgID.String()[:8]); err != nil {
		t.Fatalf("organization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-Retention - 24*time.Hour)
	recent := now.Add(-Retention + 24*time.Hour)
	type row struct {
		name, status       string
		granted, confirmed time.Time
		withdrawn          *time.Time
		wantKept           bool
	}
	rows := []row{
		{"withdrawn-old", "withdrawn", old.Add(-time.Hour), old, &old, false},
		{"withdrawn-recent", "withdrawn", old, old, &recent, true},
		{"pending-old", "pending", old, time.Time{}, nil, false},
		{"pending-recent", "pending", recent, time.Time{}, nil, true},
		{"confirmed-old", "confirmed", old.Add(-time.Hour), old, nil, true},
	}
	ids := map[string]uuid.UUID{}
	for _, r := range rows {
		id := uuid.New()
		ids[r.name] = id
		var confirmed any
		if !r.confirmed.IsZero() {
			confirmed = r.confirmed
		}
		email := r.name + "@purge.test"
		if _, err := pool.Exec(ctx, `
			INSERT INTO marketing_subscriptions
				(id, org_id, email, email_normalized, status, text_version, source, granted_at,
				 confirmed_at, withdrawn_at, unsubscribe_token)
			VALUES ($1, $2, $3, $3, $4, 'v1', 'widget', $5, $6, $7, $8)`,
			id, orgID, email, r.status, r.granted, confirmed, r.withdrawn, "purge-"+id.String()); err != nil {
			t.Fatalf("subscription %s: %v", r.name, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO marketing_consents (subscription_id, org_id, action, text_version, source, created_at)
			VALUES ($1, $2, 'granted', 'v1', 'widget', $3)`, id, orgID, r.granted); err != nil {
			t.Fatalf("consent %s: %v", r.name, err)
		}
	}

	n, err := purge(ctx, pool, now, []uuid.UUID{orgID})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d subscriptions, want 2", n)
	}
	for _, r := range rows {
		var subs, journal int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_subscriptions WHERE id = $1`, ids[r.name]).Scan(&subs); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE subscription_id = $1`, ids[r.name]).Scan(&journal); err != nil {
			t.Fatal(err)
		}
		kept := subs == 1 && journal == 1
		gone := subs == 0 && journal == 0
		if r.wantKept && !kept {
			t.Errorf("%s: want kept, have %d subscriptions and %d journal rows", r.name, subs, journal)
		}
		if !r.wantKept && !gone {
			t.Errorf("%s: want deleted, have %d subscriptions and %d journal rows", r.name, subs, journal)
		}
	}
}
