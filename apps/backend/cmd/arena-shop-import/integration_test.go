//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pngBytes is a PNG signature plus a tail: the importer sniffs the signature,
// it never decodes the picture.
func pngBytes(tail string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89"), []byte(tail)...)
}

func writeCSV(t *testing.T, path string, rows [][]string) {
	t.Helper()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

var productHeader = []string{
	"Handle", "Title", "Body (HTML)", "Vendor", "Type", "Tags", "Option1 Name", "Option1 Value", "Variant SKU",
	"Variant Inventory Tracker", "Variant Inventory Qty", "Variant Inventory Policy", "Variant Price",
	"Variant Compare At Price", "Image Src", "Image Position", "Image Alt Text", "Status",
}

// TestShopImport_IsIdempotent drives the real CLI (run) against a scratch
// database: the same export twice gives the same rows, a changed price
// updates one product and keeps the organizer's stock and settings, and a dry
// run writes nothing.
func TestShopImport_IsIdempotent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // registered first, so it runs after the cleanup below

	orgID := uuid.New()
	nonce := strings.ReplaceAll(orgID.String(), "-", "")[:12]
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Shop import "+nonce, "shop-import-"+nonce); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupOrg(pool, orgID, t) })

	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%q: %v", sql, err)
		}
		return n
	}

	// Pictures served by a local stub, three different files.
	imgs := map[string][]byte{"/img/a.png": pngBytes("A"), "/img/b.png": pngBytes("BB"), "/img/c.png": pngBytes("CCC")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := imgs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream") // the bytes decide
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("MEDIA_BACKEND", "local")
	t.Setenv("MEDIA_LOCAL_ROOT", filepath.Join(dir, "media"))
	t.Setenv("MEDIA_SIGNING_SECRET", "integration-test-secret")
	t.Setenv("JWT_SIGNING_SECRET", "integration-test-secret")

	productsCSV := filepath.Join(dir, "products.csv")
	writeProducts := func(price string, extra ...[]string) {
		rows := [][]string{
			productHeader,
			{"red-wine-" + nonce, `Red "Wine"`, `<h4 class="x">Dry red</h4><script>alert(1)</script>`, "Winery", "Wine", "a, b", "Size", "0.75", "W-1",
				"shopify", "12", "deny", price, "250.00", srv.URL + "/img/a.png", "1", "front", "active"},
			{"red-wine-" + nonce, "", "", "", "", "", "", "1.5", "W-2", "shopify", "4", "deny", "400.00", "", srv.URL + "/img/b.png", "2", "", ""},
			{"набор-" + nonce, "Набор «Собери кота»", "<p>Подарочный набор</p>", "Winery", "", "", "Title", "Default Title", "",
				"shopify", "40", "deny", "550.00", "", srv.URL + "/img/c.png", "1", "", "active"},
			{"draft-" + nonce, "Draft", "", "Winery", "", "", "Title", "Default Title", "", "", "", "", "10.00", "", "", "", "", "draft"},
		}
		writeCSV(t, productsCSV, append(rows, extra...))
	}
	writeProducts("200.00")

	customersCSV := filepath.Join(dir, "customers.csv")
	writeCSV(t, customersCSV, [][]string{
		{"Customer ID", "First Name", "Last Name", "Email", "Accepts Email Marketing", "Default Address Country Code", "Default Address Phone", "Phone"},
		{"'1", "Dina", "Levi", "dina." + nonce + "@example.com", "yes", "IL", "", ""},
		{"'2", "Ivan", "", "ivan." + nonce + "@example.com", "no", "IL", "", ""},
		{"'3", "Marina", "K", "marina." + nonce + "@example.com", "yes", "IL", "", ""},
		{"'4", "Dup", "", "DINA." + nonce + "@example.com", "yes", "IL", "", ""},
		{"'5", "", "", "", "no", "IL", "", ""},
	})

	pagesDir := filepath.Join(dir, "pages")
	if err := os.MkdirAll(pagesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(pagesDir, "about.html"), []byte(themePage))
	mustWrite(t, filepath.Join(pagesDir, "refund.html"), []byte(policyPage))
	mapFile := filepath.Join(pagesDir, "map.json")
	mustWrite(t, mapFile, []byte(`{"pages":[{"file":"about.html","slug":"about","position":10},{"file":"refund.html","slug":"refund-policy","position":50}]}`))

	args := func(extra ...string) []string {
		a := []string{"--org", orgID.String(), "--db-url", dsn, "--products", productsCSV, "--customers", customersCSV,
			"--pages", pagesDir, "--pages-map", mapFile}
		return append(a, extra...)
	}
	do := func(extra ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(args(extra...), &out); err != nil {
			t.Fatalf("run %v: %v\n%s", extra, err, out.String())
		}
		return out.String()
	}

	state := func() string {
		return fmt.Sprintf("products=%d variants=%d images=%d media=%d pages=%d settings=%d links=%d consents=%d moves=%d",
			count(`SELECT count(*) FROM shop_products WHERE org_id = $1 AND deleted_at IS NULL`, orgID),
			count(`SELECT count(*) FROM shop_variants v JOIN shop_products p ON p.id = v.product_id WHERE p.org_id = $1 AND v.deleted_at IS NULL`, orgID),
			count(`SELECT count(*) FROM shop_product_images i JOIN shop_products p ON p.id = i.product_id WHERE p.org_id = $1`, orgID),
			count(`SELECT count(*) FROM media_objects WHERE org_id = $1 AND owner_type = 'shop_product' AND deleted_at IS NULL`, orgID),
			count(`SELECT count(*) FROM shop_pages WHERE org_id = $1`, orgID),
			count(`SELECT count(*) FROM shop_settings WHERE org_id = $1`, orgID),
			count(`SELECT count(*) FROM customer_org_links WHERE org_id = $1`, orgID),
			count(`SELECT count(*) FROM customer_consents WHERE org_id = $1 AND kind = 'marketing'`, orgID),
			count(`SELECT count(*) FROM shop_stock_moves WHERE org_id = $1`, orgID))
	}

	// ── First run.
	out1 := do()
	want := "products=2 variants=3 images=3 media=3 pages=2 settings=1 links=3 consents=2 moves=3"
	if got := state(); got != want {
		t.Fatalf("after the first run:\n got %s\nwant %s\n%s", got, want, out1)
	}
	for _, frag := range []string{"2 created", "3 created", "organization: Shop import"} {
		if !strings.Contains(out1, frag) {
			t.Errorf("first report lacks %q:\n%s", frag, out1)
		}
	}
	if n := count(`SELECT count(*) FROM shop_products WHERE org_id = $1 AND status = 'draft'`, orgID); n != 0 {
		t.Errorf("a draft was imported without --include-drafts")
	}
	var fee, age int
	var cur, announcement, contact string
	if err := pool.QueryRow(ctx, `SELECT shipping_fee_minor, age_gate_min_age, currency, announcement_text, contact_email FROM shop_settings WHERE org_id = $1`, orgID).
		Scan(&fee, &age, &cur, &announcement, &contact); err != nil {
		t.Fatal(err)
	}
	if fee != 5000 || age != 18 || cur != "ILS" || !strings.Contains(announcement, "15%") || contact != "sales@einatwinery.com" {
		t.Errorf("settings: %d %d %s %q %s", fee, age, cur, announcement, contact)
	}
	var title, body string
	var price, compare, stock int
	if err := pool.QueryRow(ctx, `SELECT p.title, p.body_html, v.price_minor, v.compare_at_minor, v.stock FROM shop_products p
		JOIN shop_variants v ON v.product_id = p.id WHERE p.org_id = $1 AND p.slug = $2 AND v.sku = 'W-1'`, orgID, "red-wine-"+nonce).
		Scan(&title, &body, &price, &compare, &stock); err != nil {
		t.Fatal(err)
	}
	if title != `Red "Wine"` || price != 20000 || compare != 25000 || stock != 12 || body != "<h4>Dry red</h4>" {
		t.Errorf("product: %q body=%q price=%d compare=%d stock=%d", title, body, price, compare, stock)
	}
	if n := count(`SELECT count(*) FROM shop_products WHERE org_id = $1 AND slug = $2`, orgID, "набор-"+nonce); n != 1 {
		t.Errorf("the Cyrillic handle must be the slug")
	}

	// ── Second run: nothing changes.
	out2 := do()
	if got := state(); got != want {
		t.Fatalf("a re-run changed the data:\n got %s\nwant %s\n%s", got, want, out2)
	}
	if strings.Contains(out2, "created") || strings.Contains(out2, "updated") {
		t.Errorf("a re-run must report nothing created or updated:\n%s", out2)
	}

	// ── The organizer edits stock and settings, the export gets a new price.
	if _, err := pool.Exec(ctx, `UPDATE shop_variants SET stock = 3 WHERE sku = 'W-1' AND product_id IN (SELECT id FROM shop_products WHERE org_id = $1)`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE shop_settings SET shipping_fee_minor = 777 WHERE org_id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	writeProducts("210.00")
	out3 := do()
	if !strings.Contains(out3, "1 updated") {
		t.Errorf("one product must be updated:\n%s", out3)
	}
	if err := pool.QueryRow(ctx, `SELECT v.price_minor, v.stock FROM shop_variants v JOIN shop_products p ON p.id = v.product_id
		WHERE p.org_id = $1 AND v.sku = 'W-1'`, orgID).Scan(&price, &stock); err != nil {
		t.Fatal(err)
	}
	if price != 21000 || stock != 3 {
		t.Errorf("price must follow the export (21000) and stock stay the organizer's (3): %d %d", price, stock)
	}
	if n := count(`SELECT shipping_fee_minor FROM shop_settings WHERE org_id = $1`, orgID); n != 777 {
		t.Errorf("settings the organizer edited were overwritten: %d", n)
	}
	if got := state(); !strings.HasPrefix(got, "products=2 variants=3 images=3 media=3 pages=2 settings=1 links=3 consents=2") {
		t.Errorf("after the price change: %s", got)
	}

	// ── --sync-stock takes the CSV's stock back.
	do("--sync-stock")
	if n := count(`SELECT v.stock FROM shop_variants v JOIN shop_products p ON p.id = v.product_id WHERE p.org_id = $1 AND v.sku = 'W-1'`, orgID); n != 12 {
		t.Errorf("--sync-stock: stock %d", n)
	}

	// ── A stored variant the CSV does not list survives a re-run.
	if _, err := pool.Exec(ctx, `INSERT INTO shop_variants (product_id, title, sku, price_minor, stock, position)
		SELECT id, 'Organizer added', 'ORG-1', 100, 1, 9 FROM shop_products WHERE org_id = $1 AND slug = $2`, orgID, "red-wine-"+nonce); err != nil {
		t.Fatal(err)
	}
	do()
	if n := count(`SELECT count(*) FROM shop_variants v JOIN shop_products p ON p.id = v.product_id
		WHERE p.org_id = $1 AND v.sku = 'ORG-1' AND v.deleted_at IS NULL`, orgID); n != 1 {
		t.Errorf("an organizer's variant was deleted by a re-run")
	}

	// ── Dry run: reports, writes nothing.
	before := state()
	writeProducts("210.00", []string{"brand-new-" + nonce, "Brand new", "", "Winery", "", "", "Title", "Default Title", "", "", "", "", "9.99", "", "", "", "", "active"})
	outDry := do("--dry-run")
	if !strings.Contains(outDry, "DRY RUN") || !strings.Contains(outDry, "1 would be created") {
		t.Errorf("dry run report:\n%s", outDry)
	}
	if after := state(); after != before {
		t.Errorf("a dry run wrote:\n before %s\n after  %s", before, after)
	}
}

func cleanupOrg(pool *pgxpool.Pool, orgID uuid.UUID, t *testing.T) {
	ctx := context.Background()
	steps := []string{
		`DELETE FROM shop_stock_moves WHERE org_id = $1`,
		`DELETE FROM shop_product_images WHERE product_id IN (SELECT id FROM shop_products WHERE org_id = $1)`,
		`DELETE FROM shop_variants WHERE product_id IN (SELECT id FROM shop_products WHERE org_id = $1)`,
		`DELETE FROM shop_products WHERE org_id = $1`,
		`DELETE FROM shop_pages WHERE org_id = $1`,
		`DELETE FROM shop_settings WHERE org_id = $1`,
		`DELETE FROM media_objects WHERE org_id = $1`,
		`CREATE TEMP TABLE IF NOT EXISTS shop_import_cust AS SELECT customer_id FROM customer_org_links WHERE false`,
		`INSERT INTO shop_import_cust SELECT customer_id FROM customer_org_links WHERE org_id = $1`,
		`DELETE FROM customer_consents WHERE org_id = $1`,
		`DELETE FROM customer_org_links WHERE org_id = $1`,
		`DELETE FROM customer_attributes WHERE customer_id IN (SELECT customer_id FROM shop_import_cust)`,
		`DELETE FROM customer_identities WHERE customer_id IN (SELECT customer_id FROM shop_import_cust)`,
		`DELETE FROM customers WHERE id IN (SELECT customer_id FROM shop_import_cust)`,
		`DROP TABLE IF EXISTS shop_import_cust`,
		`DELETE FROM organizations WHERE id = $1`,
	}
	// A temp table lives on one connection: run the whole cleanup on one.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Logf("cleanup: %v", err)
		return
	}
	defer conn.Release()
	for _, s := range steps {
		var execErr error
		if strings.Contains(s, "$1") {
			_, execErr = conn.Exec(ctx, s, orgID)
		} else {
			_, execErr = conn.Exec(ctx, s)
		}
		if execErr != nil {
			t.Logf("cleanup %q: %v", s, execErr)
		}
	}
}
