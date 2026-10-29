package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

func sp(s string) *string { return &s }

func stored(title, sku string, opt1 *string, price int64, stock, pos int32) gen.ShopVariantRow {
	return gen.ShopVariantRow{
		ID: uuid.New(), Title: title, SKU: strPtr(sku), PriceMinor: price, Stock: stock, TrackStock: true,
		Position: pos, Option1: opt1,
	}
}

func TestPlanVariants_MatchesBySkuOptionsTitle(t *testing.T) {
	bySKU := stored("Old title", "SKU-1", nil, 1000, 7, 0)
	byOpt := stored("Red", "", sp("Red"), 2000, 3, 1)
	byTitle := stored("Default Title", "", nil, 3000, 9, 2)
	extra := stored("Organizer added", "X", nil, 500, 1, 3)
	existing := []gen.ShopVariantRow{bySKU, byOpt, byTitle, extra}

	want := []ImportVariant{
		{Title: "New title", SKU: "SKU-1", PriceMinor: 1100, TrackStock: true, Position: 0, Stock: 50},
		{Title: "Red", Option1: sp("Red"), PriceMinor: 2000, TrackStock: true, Position: 1, Stock: 3},
		{Title: "Default Title", PriceMinor: 3000, TrackStock: true, Position: 2, Stock: 9},
		{Title: "Blue", Option1: sp("Blue"), PriceMinor: 2000, TrackStock: true, Position: 3, Stock: 4},
	}
	plan := planVariants(existing, want, false)
	if plan.Created != 1 || plan.Updated != 1 || plan.Same != 2 || plan.Kept != 1 {
		t.Fatalf("plan: created=%d updated=%d same=%d kept=%d", plan.Created, plan.Updated, plan.Same, plan.Kept)
	}
	if !plan.Dirty() {
		t.Error("plan must be dirty")
	}
	// Matched variants keep their id and, without --sync-stock, their stock.
	if plan.Inputs[0].ID == nil || *plan.Inputs[0].ID != bySKU.ID || plan.Inputs[0].Stock != 7 || plan.Inputs[0].PriceMinor != 1100 {
		t.Errorf("sku match: %+v", plan.Inputs[0])
	}
	if plan.Inputs[1].ID == nil || *plan.Inputs[1].ID != byOpt.ID || plan.Inputs[2].ID == nil || *plan.Inputs[2].ID != byTitle.ID {
		t.Errorf("option/title matches lost their ids")
	}
	if plan.Inputs[3].ID != nil || plan.Inputs[3].Stock != 4 {
		t.Errorf("a new variant takes the CSV stock: %+v", plan.Inputs[3])
	}
	// The organizer's variant is carried through untouched, so ReplaceVariants keeps it.
	last := plan.Inputs[len(plan.Inputs)-1]
	if last.ID == nil || *last.ID != extra.ID || last.Title != "Organizer added" || last.PriceMinor != 500 {
		t.Errorf("kept variant: %+v", last)
	}

	synced := planVariants(existing, want, true)
	if synced.Inputs[0].Stock != 50 {
		t.Errorf("--sync-stock must take the CSV stock, got %d", synced.Inputs[0].Stock)
	}
}

func TestPlanVariants_SecondRunIsClean(t *testing.T) {
	want := []ImportVariant{
		{Title: "A", SKU: "S", PriceMinor: 100, TrackStock: true, Position: 0, Stock: 5},
		{Title: "B", Option1: sp("B"), PriceMinor: 200, TrackStock: true, Position: 1, Stock: 6},
	}
	first := planVariants(nil, want, true)
	if first.Created != 2 || first.Updated != 0 {
		t.Fatalf("first: %+v", first)
	}
	// What the first run stored.
	var rows []gen.ShopVariantRow
	for i, in := range first.Inputs {
		rows = append(rows, gen.ShopVariantRow{
			ID: uuid.New(), Title: in.Title, SKU: in.SKU, PriceMinor: in.PriceMinor, Stock: in.Stock,
			TrackStock: *in.TrackStock, Position: int32(i), Option1: in.Option1,
		})
	}
	second := planVariants(rows, want, false)
	if second.Dirty() || second.Same != 2 || second.Kept != 0 {
		t.Errorf("a re-run must change nothing: %+v", second)
	}
}

func TestImageSource_LocalCopyWinsAndTypeIsSniffed(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	mustWrite(t, dir+"/GN3A8431.jpg", png) // the name lies, the bytes are a PNG
	mustWrite(t, dir+"/notes.jpg", []byte("just text"))
	src := newImageSource(dir)

	got, err := src.load(t.Context(), "https://cdn.shopify.com/s/files/1/0836/files/GN3A8431.jpg?v=1711115096")
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/png" || len(got.Checksum) != 64 || !bytes.Equal(got.Body, png) {
		t.Errorf("got %s %s", got.ContentType, got.Checksum)
	}
	if _, err := src.load(t.Context(), "https://cdn.shopify.com/files/notes.jpg"); err == nil || !strings.Contains(err.Error(), "not a jpeg") {
		t.Errorf("a text file must be refused as a picture, got %v", err)
	}
	if src.available("https://cdn.shopify.com/files/absent.jpg") != true {
		t.Error("a remote http(s) picture is available to try")
	}
	if src.available("files/absent.jpg") {
		t.Error("a relative path with no local copy is not available")
	}
	if fileName("https://x.com/s/files/%D1%84%D0%BE%D1%82%D0%BE.jpg?v=1") != "фото.jpg" {
		t.Errorf("file name decoding: %q", fileName("https://x.com/s/files/%D1%84%D0%BE%D1%82%D0%BE.jpg?v=1"))
	}
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeBody_UsesTheShopSanitizerAndReports(t *testing.T) {
	im := NewImporter(Options{DryRun: true}, nil, nil, nil, nil)
	got, err := im.sanitizeBody("description", `<h1>Title</h1><p onclick="x()">Dry <b>red</b></p><img src="https://cdn.example.com/a.jpg"><script>alert(1)</script>`, shop.ProductBodyProfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "script") || strings.Contains(got, "onclick") || strings.Contains(got, "<img") {
		t.Errorf("sanitizer let something through: %q", got)
	}
	if !strings.Contains(got, "Dry") {
		t.Errorf("text lost: %q", got)
	}
	if im.rep.BodyImagesDropped != 1 {
		t.Errorf("the dropped picture must be counted, got %d", im.rep.BodyImagesDropped)
	}
	// A text longer than the profile allows is refused with an error, never cut.
	huge := "<p>" + strings.Repeat("x", shop.MaxBodyHTMLLen+10) + "</p>"
	if _, err := im.sanitizeBody("description", huge, shop.ProductBodyProfile); err == nil {
		t.Error("an oversized text must be refused")
	}
}

// A product imported before the placeholder was dropped stored "Default Title"
// as the value of option 1. A re-run clears it on the same variant instead of
// adding a second one.
func TestPlanVariants_RerunClearsTheDefaultTitleOptionValue(t *testing.T) {
	old := stored("Default Title", "", sp("Default Title"), 20000, 82, 0)
	want := []ImportVariant{{Title: "Default Title", PriceMinor: 20000, TrackStock: true, Position: 0, Stock: 87}}
	plan := planVariants([]gen.ShopVariantRow{old}, want, false)
	if plan.Created != 0 || plan.Updated != 1 || plan.Kept != 0 || len(plan.Inputs) != 1 {
		t.Fatalf("plan: created=%d updated=%d kept=%d inputs=%d", plan.Created, plan.Updated, plan.Kept, len(plan.Inputs))
	}
	in := plan.Inputs[0]
	if in.ID == nil || *in.ID != old.ID || in.Option1 != nil || in.Stock != 82 {
		t.Errorf("input: %+v", in)
	}
	// Once cleared, the next run changes nothing.
	cleared := stored("Default Title", "", nil, 20000, 82, 0)
	if again := planVariants([]gen.ShopVariantRow{cleared}, want, false); again.Dirty() {
		t.Errorf("a cleared variant must be left alone: %+v", again)
	}
}
