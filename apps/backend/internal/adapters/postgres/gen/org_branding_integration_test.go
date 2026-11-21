//go:build integration

package gen_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestOrganizationBranding_LiveDB exercises migration 0203 and the wrappers
// in org_branding.sql.go: a new organization reads an empty theme, a write
// replaces the whole object, nil returns to the platform palette, and an
// unknown organization is pgx.ErrNoRows.
func TestOrganizationBranding_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)

	slug := "brand-" + uuid.NewString()[:8]
	org, err := q.InsertOrganization(ctx, "Branding "+slug, slug, "IL", "en", 1200)
	if err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID) }()

	got, err := q.GetOrganizationBranding(ctx, org.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("fresh organization: %v %v", got, err)
	}
	vino := map[string]string{"primary": "#822121", "on_primary_muted": "rgba(255, 255, 255, .72)", "font": "Assistant"}
	if ok, err := q.SetOrganizationBranding(ctx, org.ID, vino); err != nil || !ok {
		t.Fatalf("set: %v %v", ok, err)
	}
	got, err = q.GetOrganizationBranding(ctx, org.ID)
	if err != nil || got["primary"] != "#822121" || got["font"] != "Assistant" || len(got) != 3 {
		t.Fatalf("read back: %v %v", got, err)
	}
	if ok, err := q.SetOrganizationBranding(ctx, org.ID, map[string]string{"primary": "#001489"}); err != nil || !ok {
		t.Fatalf("replace: %v %v", ok, err)
	}
	got, _ = q.GetOrganizationBranding(ctx, org.ID)
	if len(got) != 1 || got["primary"] != "#001489" {
		t.Fatalf("a write must replace the whole object: %v", got)
	}
	if ok, err := q.SetOrganizationBranding(ctx, org.ID, nil); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	got, _ = q.GetOrganizationBranding(ctx, org.ID)
	if len(got) != 0 {
		t.Fatalf("nil must clear: %v", got)
	}
	if _, err := q.GetOrganizationBranding(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown organization: %v", err)
	}
	if _, err := q.GetOrganizationAccentForTicket(ctx, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown ticket: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET branding = '[]'::jsonb WHERE id = $1`, org.ID); err == nil {
		t.Fatal("the CHECK must refuse a non-object branding")
	}
}
