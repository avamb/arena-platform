package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseMinor(t *testing.T) {
	cases := []struct {
		in, cur string
		want    int64
		wantErr bool
	}{
		{"650.00", "ILS", 65000, false},
		{"12.5", "ILS", 1250, false},
		{"7", "ILS", 700, false},
		{"0.00", "ILS", 0, false},
		{"0.07", "ILS", 7, false},
		{".5", "ILS", 50, false},
		{"18.90", "EUR", 1890, false},
		{"5.000", "ILS", 500, false}, // trailing zeros past the decimals are harmless
		{"1500", "JPY", 1500, false},
		{"1.234", "KWD", 1234, false},
		{"12.345", "ILS", 0, true}, // would need rounding: refused
		{"-1.00", "ILS", 0, true},
		{"1,000.00", "ILS", 0, true},
		{"abc", "ILS", 0, true},
		{"", "ILS", 0, true},
		{"1.5", "JPY", 0, true},
	}
	for _, c := range cases {
		got, err := ParseMinor(c.in, c.cur)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseMinor(%q,%s) error = %v, wantErr %v", c.in, c.cur, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("ParseMinor(%q,%s) = %d, want %d", c.in, c.cur, got, c.want)
		}
	}
	if _, err := ParseMinor("  ", "ILS"); !errors.Is(err, errEmptyMoney) {
		t.Errorf("blank amount: want errEmptyMoney, got %v", err)
	}
}

func TestFormatMinor(t *testing.T) {
	for in, want := range map[int64]string{65000: "650.00", 7: "0.07", 0: "0.00", 123456: "1234.56"} {
		if got := FormatMinor(in, "ILS"); got != want {
			t.Errorf("FormatMinor(%d) = %q, want %q", in, got, want)
		}
	}
	if got := FormatMinor(1500, "JPY"); got != "1500" {
		t.Errorf("JPY: %q", got)
	}
}

func parseSample(t *testing.T) *ProductsFile {
	t.Helper()
	pf, err := ParseProductsFile("testdata/products_sample.csv", "ILS")
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func byHandle(pf *ProductsFile, handle string) *ImportProduct {
	for i := range pf.Products {
		if pf.Products[i].Handle == handle {
			return &pf.Products[i]
		}
	}
	return nil
}

func TestParseProducts_GroupsRowsByHandle(t *testing.T) {
	pf := parseSample(t)
	if len(pf.Problems) != 0 {
		t.Fatalf("problems: %v", pf.Problems)
	}
	if pf.Rows != 9 || len(pf.Products) != 5 {
		t.Fatalf("rows=%d products=%d, want 9 and 5", pf.Rows, len(pf.Products))
	}

	// Simple product: one default variant, one picture, price in minor units.
	cat := byHandle(pf, "lonely-cat-cabfr")
	if cat == nil || cat.Status != "active" || len(cat.Variants) != 1 || len(cat.Images) != 1 {
		t.Fatalf("lonely-cat-cabfr: %+v", cat)
	}
	if v := cat.Variants[0]; v.PriceMinor != 20000 || v.Title != "Default Title" || v.Stock != 87 || !v.TrackStock {
		t.Errorf("variant: %+v", v)
	}
	if !strings.Contains(cat.BodyHTML, "<h4>") || !strings.Contains(cat.BodyHTML, `"Lonely Cat"`) {
		t.Errorf("quoted HTML body was damaged: %.80q", cat.BodyHTML)
	}
	if cat.Vendor != "Einat Winery" || cat.Type != "Wine" {
		t.Errorf("vendor/type: %q %q", cat.Vendor, cat.Type)
	}

	// Variants on later rows, extra pictures in their own rows.
	set := byHandle(pf, "wineandglassesset")
	if set == nil || len(set.Variants) != 2 || len(set.Images) != 3 {
		t.Fatalf("wineandglassesset: variants=%d images=%d", len(set.Variants), len(set.Images))
	}
	if set.Variants[0].Option1 == nil || *set.Variants[0].Option1 != "Lonely Cat Cabernet Franc 2024" || set.Variants[0].Title != *set.Variants[0].Option1 {
		t.Errorf("variant 0: %+v", set.Variants[0])
	}
	if set.Variants[1].Position != 1 || set.Variants[1].Stock != 13 || set.Variants[1].PriceMinor != 29000 {
		t.Errorf("variant 1: %+v", set.Variants[1])
	}
	for i, im := range set.Images {
		if im.Position != int32(i+1) || !strings.HasPrefix(im.Src, "https://") {
			t.Errorf("image %d: %+v", i, im)
		}
	}

	// Cyrillic handle becomes the slug as is (lower-cased), image-only row
	// adds a picture and not a variant.
	cy := byHandle(pf, "набор-красных-вин-собери-кота")
	if cy == nil || cy.Slug != "набор-красных-вин-собери-кота" || len(cy.Variants) != 1 || len(cy.Images) != 2 {
		t.Fatalf("cyrillic product: %+v", cy)
	}
	if cy.Title != `Набор красных вин "Собери кота"` {
		t.Errorf("title: %q", cy.Title)
	}
}

func TestParseProducts_StatusesAndSelection(t *testing.T) {
	pf := parseSample(t)
	if p := byHandle(pf, "a-duma"); p == nil || p.ShopifyStatus != "unlisted" || p.Status != "draft" {
		t.Errorf("unlisted must import as a draft: %+v", p)
	}
	if p := byHandle(pf, "5-6wine"); p == nil || p.Status != "archived" || len(p.Variants) != 2 {
		t.Errorf("archived: %+v", p)
	}
	keep, skipped := SelectProducts(pf.Products, false)
	if len(keep) != 3 || skipped["unlisted"] != 1 || skipped["archived"] != 1 {
		t.Errorf("default selection: keep=%d skipped=%v", len(keep), skipped)
	}
	keep, skipped = SelectProducts(pf.Products, true)
	if len(keep) != 5 || len(skipped) != 0 {
		t.Errorf("with drafts: keep=%d skipped=%v", len(keep), skipped)
	}
}

func TestParseProducts_InventoryPolicy(t *testing.T) {
	csv := "Handle,Title,Variant Price,Variant Compare At Price,Variant Inventory Tracker,Variant Inventory Qty,Variant Inventory Policy,Status\n" +
		"a,A,10.00,15.00,shopify,5,deny,active\n" +
		"b,B,10.00,10.00,shopify,-3,continue,active\n" +
		"c,C,10.00,,,,deny,active\n"
	pf, err := ParseProducts(strings.NewReader(csv), "ILS")
	if err != nil || len(pf.Problems) != 0 {
		t.Fatalf("%v %v", err, pf.Problems)
	}
	a, b, c := pf.Products[0].Variants[0], pf.Products[1].Variants[0], pf.Products[2].Variants[0]
	if !a.TrackStock || a.Stock != 5 || a.CompareAtMinor == nil || *a.CompareAtMinor != 1500 {
		t.Errorf("a: %+v", a)
	}
	if b.TrackStock || b.Stock != 0 || b.CompareAtMinor != nil {
		t.Errorf("b (continue, negative stock, compare-at equal to price): %+v", b)
	}
	if c.TrackStock || c.Stock != 0 {
		t.Errorf("c (no tracker): %+v", c)
	}
}

func TestParseProducts_ProblemsAreIsolated(t *testing.T) {
	csv := "Handle,Title,Variant Price,Status\n" +
		"good,Good,10.00,active\n" +
		"bad-price,Bad,12.345,active\n" +
		"no title,,5.00,active\n" +
		"also-good,Also good,1,active\n"
	pf, err := ParseProducts(strings.NewReader(csv), "ILS")
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Products) != 2 || len(pf.Problems) != 2 {
		t.Fatalf("products=%d problems=%v", len(pf.Products), pf.Problems)
	}
}

func TestParseProducts_RejectsForeignCSV(t *testing.T) {
	if _, err := ParseProducts(strings.NewReader("a,b\n1,2\n"), "ILS"); err == nil {
		t.Fatal("a CSV without Shopify columns must be refused")
	}
}

func TestParseOptionNames(t *testing.T) {
	const csvText = "Handle,Title,Variant Price,Option1 Name,Option1 Value,Option2 Name,Option2 Value,Option3 Name,Option3 Value\n" +
		"plain,Plain,10.00,Title,Default Title,,,,\n" +
		"sized,Sized,10.00,Size,0.75,Colour,Red,,\n" +
		"sized,,12.00,,1.5,,Red,,\n" +
		"titled,Titled,10.00,Title,Special,,,,\n"
	pf, err := ParseProducts(strings.NewReader(csvText), "ILS")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, p := range pf.Products {
		got[p.Handle] = p.OptionNames
	}
	if len(got["plain"]) != 0 {
		t.Errorf("the Title/Default Title placeholder is not an option: %v", got["plain"])
	}
	if strings.Join(got["sized"], "|") != "Size|Colour" {
		t.Errorf("option names: %v", got["sized"])
	}
	if strings.Join(got["titled"], "|") != "Title" {
		t.Errorf("a real option called Title is kept: %v", got["titled"])
	}
}

func TestParseProducts_SingleDefaultTitleVariantCarriesNoOptionValue(t *testing.T) {
	const csvText = "Handle,Title,Variant Price,Option1 Name,Option1 Value,Option2 Name,Option2 Value,Option3 Name,Option3 Value\n" +
		"plain,Plain,10.00,Title,Default Title,,,,\n" +
		"sized,Sized,10.00,Size,0.75,,,,\n" +
		"sized,,12.00,,1.5,,,,\n"
	pf, err := ParseProducts(strings.NewReader(csvText), "ILS")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pf.Products {
		switch p.Handle {
		case "plain":
			v := p.Variants[0]
			if v.Option1 != nil || v.Title != "Default Title" {
				t.Errorf("a simple product keeps no option value: %+v", v)
			}
		case "sized":
			if p.Variants[0].Option1 == nil || *p.Variants[0].Option1 != "0.75" {
				t.Errorf("real options stay: %+v", p.Variants[0])
			}
		}
	}
}

func TestParseProducts_SEOTagsWeightAndBarcode(t *testing.T) {
	pf, err := ParseProductsFile("testdata/products_seo.csv", "ILS")
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Problems) != 0 || len(pf.Products) != 2 {
		t.Fatalf("problems=%v products=%d", pf.Problems, len(pf.Products))
	}
	p := byHandle(pf, "seo-wine")
	if p.SEOTitle != "Buy Seo Wine" || p.SEODescription != "A dry red from the Galilee" {
		t.Errorf("seo: %q / %q", p.SEOTitle, p.SEODescription)
	}
	// Trimmed, empty cells dropped, "RED" is the same tag as "Red".
	if want := []string{"Red", "wine", "Gift"}; !sameStrings(p.Tags, want) {
		t.Errorf("tags: %q, want %q", p.Tags, want)
	}
	v0, v1 := p.Variants[0], p.Variants[1]
	if v0.WeightGrams == nil || *v0.WeightGrams != 1250 || v0.Barcode != "7290001234567" {
		t.Errorf("variant 0: %+v", v0)
	}
	if v1.WeightGrams == nil || *v1.WeightGrams != 2600 || v1.Barcode != "" {
		t.Errorf("variant 1: %+v", v1)
	}
	// A zero weight is "not set", and a product without SEO cells has none.
	plain := byHandle(pf, "plain")
	if plain.Variants[0].WeightGrams != nil || plain.SEOTitle != "" || len(plain.Tags) != 0 {
		t.Errorf("plain: %+v", plain)
	}
}

func TestParseProducts_RealExportColumns(t *testing.T) {
	// The real export has "Variant Barcodes", "Variant Grams", "SEO *".
	pf := parseSample(t)
	if len(pf.Products) == 0 {
		t.Fatal("no products")
	}
	for _, p := range pf.Products {
		for _, v := range p.Variants {
			if v.WeightGrams != nil && *v.WeightGrams <= 0 {
				t.Errorf("%s: weight %d", p.Handle, *v.WeightGrams)
			}
		}
	}
}

func TestParseProducts_RefusesANegativeWeight(t *testing.T) {
	csv := "Handle,Title,Variant Price,Variant Grams\nbad,Bad,1.00,-5\nok,Ok,1.00,10\n"
	pf, err := ParseProducts(strings.NewReader(csv), "ILS")
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Products) != 1 || pf.Products[0].Handle != "ok" || len(pf.Problems) != 1 {
		t.Errorf("products=%d problems=%v", len(pf.Products), pf.Problems)
	}
}

func TestClampSEO(t *testing.T) {
	p := ImportProduct{SEOTitle: strings.Repeat("я", 300), SEODescription: strings.Repeat("d", 1001)}
	for i := 0; i < 60; i++ {
		p.Tags = append(p.Tags, "t"+strings.Repeat("x", i))
	}
	clampSEO(&p)
	if n := len([]rune(p.SEOTitle)); n != maxSEOTitleRunes {
		t.Errorf("title runes: %d", n)
	}
	if n := len(p.SEODescription); n != maxSEODescriptionRunes {
		t.Errorf("description: %d", n)
	}
	if len(p.Tags) != maxTags || len(p.Warnings) != 3 {
		t.Errorf("tags=%d warnings=%v", len(p.Tags), p.Warnings)
	}
}
