//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A page imported before the old address existed gets it on the next run, a
// third run changes nothing, and a policy page stays without one (finding
// H-13). The page's link survives the shop's sanitizer (finding H-01).
func TestShopImport_PageLegacySlugIsFilledOnRerun(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	orgID := uuid.New()
	nonce := strings.ReplaceAll(orgID.String(), "-", "")[:12]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Page alias "+nonce, "page-alias-"+nonce); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupOrg(pool, orgID, t) })

	dir := t.TempDir()
	t.Setenv("MEDIA_BACKEND", "local")
	t.Setenv("MEDIA_LOCAL_ROOT", filepath.Join(dir, "media"))
	t.Setenv("MEDIA_SIGNING_SECRET", "integration-test-secret")
	t.Setenv("JWT_SIGNING_SECRET", "integration-test-secret")

	pagesDir := filepath.Join(dir, "pages")
	if err := os.MkdirAll(pagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	partners := `<html><body><main id="MainContent"><div class="rte"><h1>Наши партнеры</h1><p>Станьте партнером.</p>` +
		`<a class="button" href="/pages/contact">Напишите нам</a></div></main></body></html>`
	mustWrite(t, filepath.Join(pagesDir, "pages__%D0%BD%D0%B0%D1%88%D0%B8-%D0%BF%D0%B0%D1%80%D1%82%D0%BD%D0%B5%D1%80%D1%8B.html"), []byte(partners))
	mustWrite(t, filepath.Join(pagesDir, "policies__refund-policy.html"), []byte(policyPage))
	mapFile := filepath.Join(pagesDir, "map.json")
	mustWrite(t, mapFile, []byte(`{"pages":[{"file":"pages__наши-партнеры.html","slug":"partners","position":40},{"file":"policies__refund-policy.html","slug":"refund-policy","position":70}]}`))

	do := func() string {
		t.Helper()
		var out bytes.Buffer
		args := []string{"--org", orgID.String(), "--db-url", dsn, "--pages", pagesDir, "--pages-map", mapFile, "--skip-settings"}
		if err := run(args, &out); err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
		return out.String()
	}
	legacy := func(slug string) (string, bool) {
		t.Helper()
		var v *string
		if err := pool.QueryRow(ctx, `SELECT legacy_slug FROM shop_pages WHERE org_id = $1 AND slug = $2`, orgID, slug).Scan(&v); err != nil {
			t.Fatalf("page %s: %v", slug, err)
		}
		if v == nil {
			return "", false
		}
		return *v, true
	}

	do()
	if v, ok := legacy("partners"); !ok || v != "наши-партнеры" {
		t.Fatalf("first run: legacy_slug = %q %v", v, ok)
	}
	if v, ok := legacy("refund-policy"); ok {
		t.Errorf("a policy page got an old address: %q", v)
	}
	var body string
	if err := pool.QueryRow(ctx, `SELECT body_html FROM shop_pages WHERE org_id = $1 AND slug = 'partners'`, orgID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `<a href="/pages/contact"`) {
		t.Errorf("the button lost its link: %s", body)
	}

	// A page stored by an earlier import has no alias: the re-run fills it.
	if _, err := pool.Exec(ctx, `UPDATE shop_pages SET legacy_slug = NULL WHERE org_id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	out2 := do()
	if v, ok := legacy("partners"); !ok || v != "наши-партнеры" {
		t.Fatalf("re-run: legacy_slug = %q %v\n%s", v, ok, out2)
	}
	if !strings.Contains(out2, "1 updated") {
		t.Errorf("the re-run should report one updated page:\n%s", out2)
	}
	if out3 := do(); strings.Contains(out3, "updated") && !strings.Contains(out3, "0 updated") {
		t.Errorf("a third run must change nothing:\n%s", out3)
	}
}
