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

// TestShopImport_SEOTagsWeightAndBarcode (audit SA-29): the SEO title and
// description, the tags, and each variant's weight and barcode reach the
// database, a re-run changes nothing, and an empty cell never clears a value.
func TestShopImport_SEOTagsWeightAndBarcode(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Shop seo "+nonce, "shop-seo-"+nonce); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupOrg(pool, orgID, t) })
	t.Setenv("JWT_SIGNING_SECRET", "integration-test-secret")

	header := []string{"Handle", "Title", "Body (HTML)", "Vendor", "Type", "Tags", "Option1 Name", "Option1 Value", "Variant SKU",
		"Variant Grams", "Variant Inventory Tracker", "Variant Inventory Qty", "Variant Inventory Policy", "Variant Price",
		"Variant Barcodes", "SEO Title", "SEO Description", "Status"}
	handle := "seo-" + nonce
	csvPath := filepath.Join(t.TempDir(), "products.csv")
	write := func(seoTitle, tags, grams2 string) {
		writeCSV(t, csvPath, [][]string{
			header,
			{handle, "Seo wine", "<p>Dry</p>", "Winery", "Wine", tags, "Size", "0.75", "SEO-1", "1250", "shopify", "5", "deny", "100.00", "7290001234567", seoTitle, "A dry red", "active"},
			{handle, "", "", "", "", "", "", "1.5", "SEO-2", grams2, "shopify", "2", "deny", "190.00", "", "", "", ""},
		})
	}
	run1 := func() string {
		t.Helper()
		var out bytes.Buffer
		args := []string{"--org", orgID.String(), "--db-url", dsn, "--products", csvPath, "--skip-images", "--skip-customers", "--skip-settings"}
		if err := run(args, &out); err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
		return out.String()
	}
	type seo struct {
		title, desc *string
		tags        []string
	}
	readSEO := func() seo {
		t.Helper()
		var s seo
		if err := pool.QueryRow(ctx, `SELECT seo_title, seo_description, tags FROM shop_products WHERE org_id = $1 AND slug = $2`, orgID, handle).
			Scan(&s.title, &s.desc, &s.tags); err != nil {
			t.Fatal(err)
		}
		return s
	}
	readVariant := func(sku string) (*int32, *string) {
		t.Helper()
		var w *int32
		var b *string
		if err := pool.QueryRow(ctx, `SELECT v.weight_grams, v.barcode FROM shop_variants v JOIN shop_products p ON p.id = v.product_id
			WHERE p.org_id = $1 AND v.sku = $2 AND v.deleted_at IS NULL`, orgID, sku).Scan(&w, &b); err != nil {
			t.Fatal(err)
		}
		return w, b
	}

	write("Buy Seo Wine", "Red, wine, Gift", "2600")
	run1()
	s := readSEO()
	if s.title == nil || *s.title != "Buy Seo Wine" || s.desc == nil || *s.desc != "A dry red" || strings.Join(s.tags, "|") != "Red|wine|Gift" {
		t.Fatalf("product after the first run: %+v", s)
	}
	if w, b := readVariant("SEO-1"); w == nil || *w != 1250 || b == nil || *b != "7290001234567" {
		t.Errorf("variant SEO-1: %v %v", w, b)
	}
	if w, b := readVariant("SEO-2"); w == nil || *w != 2600 || b != nil {
		t.Errorf("variant SEO-2: %v %v", w, b)
	}

	// Same file: nothing changes.
	if out := run1(); !strings.Contains(out, "1 unchanged") {
		t.Errorf("the re-run did not leave the product unchanged:\n%s", out)
	}

	// A changed SEO title and weight are written, and an empty tags cell
	// keeps the stored tags.
	write("A better title", "", "2700")
	run1()
	s = readSEO()
	if s.title == nil || *s.title != "A better title" || strings.Join(s.tags, "|") != "Red|wine|Gift" {
		t.Errorf("product after the change: %+v", s)
	}
	if w, _ := readVariant("SEO-2"); w == nil || *w != 2700 {
		t.Errorf("weight after the change: %v", w)
	}

	// An empty cell never clears: the weight of SEO-2 stays.
	write("", "", "")
	run1()
	s = readSEO()
	if s.title == nil || *s.title != "A better title" {
		t.Errorf("an empty SEO title cleared the stored one: %+v", s)
	}
	if w, _ := readVariant("SEO-2"); w == nil || *w != 2700 {
		t.Errorf("an empty weight cleared the stored one: %v", w)
	}
}
