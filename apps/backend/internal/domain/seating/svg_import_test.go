package seating

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Table-driven §6 rule violations. Each fixture is deliberately minimal so a
// failure points at exactly one class.
// ---------------------------------------------------------------------------

func TestImportSVG_Rules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		svg     string
		wantErr string // ValidationError.Code that MUST appear in the batch
	}{
		{
			name: "canvas too large (rule 1)",
			svg: wrapSVG(`viewBox="0 0 3000 500"`,
				priceCategoryFragment()),
			wantErr: ErrCanvasTooLarge,
		},
		{
			name: "canvas missing (rule 1)",
			svg: `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">` +
				priceCategoryFragment() + `</svg>`,
			wantErr: ErrCanvasMissing,
		},
		{
			name: "seat is not a circle (rule 2)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				priceCategoryFragment()+
					rowGroupOpen("Parter", "1")+
					`<rect x="1" y="1" width="2" height="2" style="fill:#ff0000"><title>1</title></rect>`+
					rowGroupClose(),
			),
			wantErr: ErrSeatNotCircle,
		},
		{
			name: "row missing <title> (rule 3)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				priceCategoryFragment()+
					`<g inkscape:label="#Parter" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape">`+
					`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle>`+
					`</g>`),
			wantErr: ErrRowMissingTitle,
		},
		{
			name: "seat missing number (rule 4)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				priceCategoryFragment()+
					rowGroupOpen("Parter", "1")+
					`<circle cx="1" cy="1" r="1" style="fill:#ff0000"/>`+
					rowGroupClose(),
			),
			wantErr: ErrSeatMissingNumber,
		},
		{
			name: "PriceCategory group missing (rule 5)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				rowGroupOpen("Parter", "1")+
					`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle>`+
					rowGroupClose(),
			),
			wantErr: ErrPriceCategoryMissing,
		},
		{
			name: "seat colour unmatched (rule 7)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				priceCategoryFragment()+
					rowGroupOpen("Parter", "1")+
					`<circle cx="1" cy="1" r="1" style="fill:#123456"><title>1</title></circle>`+
					rowGroupClose(),
			),
			wantErr: ErrSeatColorUnmatched,
		},
		{
			name: "duplicate (sector,row,number) (rule 8)",
			svg: wrapSVG(`viewBox="0 0 100 100"`,
				priceCategoryFragment()+
					rowGroupOpen("Parter", "1")+
					`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>7</title></circle>`+
					`<circle cx="2" cy="2" r="1" style="fill:#ff0000"><title>7</title></circle>`+
					rowGroupClose(),
			),
			wantErr: ErrDuplicateSeat,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, errs := ImportSVG([]byte(tc.svg))
			if !errs.HasCode(tc.wantErr) {
				t.Fatalf("want error %q in batch, got: %v", tc.wantErr, errs)
			}
		})
	}
}

func TestImportSVG_LegendMissingIsWarning(t *testing.T) {
	t.Parallel()
	// Deliberately DO NOT use wrapSVG (which injects a Legend group).
	svg := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<svg xmlns="http://www.w3.org/2000/svg" ` +
		`xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" ` +
		`viewBox="0 0 100 100">` +
		priceCategoryFragment() +
		rowGroupOpen("Parter", "1") +
		`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle>` +
		rowGroupClose() +
		`</svg>`
	_, warnings, errs := ImportSVG([]byte(svg))
	if len(errs) != 0 {
		t.Fatalf("expected no hard errors, got %v", errs)
	}
	found := false
	for _, w := range warnings {
		if w.Code == WarnLegendMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning %q, got %v", WarnLegendMissing, warnings)
	}
}

func TestImportSVG_SectorPrefixStripped(t *testing.T) {
	t.Parallel()
	svg := wrapSVG(`viewBox="0 0 100 100"`,
		priceCategoryFragment()+
			`<g id="Legend"/>`+
			rowGroupOpen("Sector Balcony", "1")+
			`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle>`+
			rowGroupClose(),
	)
	g, _, errs := ImportSVG([]byte(svg))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(g.Sections) != 1 || g.Sections[0].Name != "Balcony" {
		t.Fatalf("expected sector 'Balcony', got sections=%+v", g.Sections)
	}
}

func TestImportSVG_MalformedXML(t *testing.T) {
	t.Parallel()
	_, _, errs := ImportSVG([]byte(`<svg><unterminated`))
	if !errs.HasCode(ErrInvalidSVG) {
		t.Fatalf("expected %q, got %v", ErrInvalidSVG, errs)
	}
}

// ---------------------------------------------------------------------------
// Palác Akropolis acceptance fixture (§7 SEAT-A2).
// ---------------------------------------------------------------------------

func TestImportSVG_PalacAkropolisAcceptance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "Palac_Akropolis.svg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, _, errs := ImportSVG(raw)
	if len(errs) != 0 {
		t.Fatalf("expected zero validation errors on Akropolis fixture, got: %v", errs)
	}

	// Section identity (Parter + three Balcony sectors).
	wantSections := map[string]bool{
		"Parter":         false,
		"Balcony left":   false,
		"Balcony center": false,
		"Balcony right":  false,
	}
	for _, s := range g.Sections {
		if _, ok := wantSections[s.Name]; ok {
			wantSections[s.Name] = true
		}
	}
	for name, present := range wantSections {
		if !present {
			t.Errorf("Akropolis fixture missing section %q; got sections %v",
				name, sectionNames(g))
		}
	}

	// Category count: 15 (First..Fifteenth).
	if got := len(g.Categories); got != 15 {
		t.Fatalf("Akropolis fixture must yield 15 categories, got %d (%v)",
			got, categoryNames(g))
	}

	// Seat count: the fixture contains 260 authoring-format seat circles
	// (279 total <circle> elements minus 15 PriceCategory swatches and
	// 4 Legend swatches, per §6 rules 5+6). The seating_backlog "279"
	// figure counts every <circle> element in the source SVG; the
	// importer, following the §6 rule that swatches are NOT seats,
	// yields the 260 authoring seats.
	if got := g.SeatCount(); got != 260 {
		t.Fatalf("Akropolis fixture seat count = %d, want 260 (§6 rules 5+6 exclude swatches)",
			got)
	}

	// Stability: two consecutive imports MUST hash identically (§5.3
	// geometry_checksum contract).
	sum1, err := Checksum(g)
	if err != nil {
		t.Fatalf("Checksum #1: %v", err)
	}
	g2, _, errs2 := ImportSVG(raw)
	if len(errs2) != 0 {
		t.Fatalf("second import produced errors: %v", errs2)
	}
	sum2, err := Checksum(g2)
	if err != nil {
		t.Fatalf("Checksum #2: %v", err)
	}
	if sum1 != sum2 {
		t.Fatalf("checksum not stable across runs: %s vs %s", sum1, sum2)
	}
}

// ---------------------------------------------------------------------------
// Fixture helpers.
// ---------------------------------------------------------------------------

func wrapSVG(canvasAttrs, inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<svg xmlns="http://www.w3.org/2000/svg" ` +
		`xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" ` +
		canvasAttrs + `>` + `<g id="Legend"/>` + inner + `</svg>`
}

// priceCategoryFragment builds a minimal PriceCategory group with a
// single swatch (colour #ff0000, label "First"). This is enough to
// satisfy §6 rule 5 for the negative-path fixtures.
func priceCategoryFragment() string {
	return `<g id="PriceCategory">` +
		`<circle inkscape:label="#First" cx="1" cy="1" r="1" ` +
		`style="fill:#ff0000"/></g>`
}

func rowGroupOpen(sector, rowTitle string) string {
	return `<g inkscape:label="#` + sector + `"><title>` + rowTitle + `</title>`
}

func rowGroupClose() string { return `</g>` }

func sectionNames(g Geometry) []string {
	out := make([]string, 0, len(g.Sections))
	for _, s := range g.Sections {
		out = append(out, s.Name)
	}
	return out
}

func categoryNames(g Geometry) []string {
	out := make([]string, 0, len(g.Categories))
	for _, c := range g.Categories {
		out = append(out, c.Name)
	}
	return out
}

// sanity: ensure test helper filenames still resolve to the domain
// package (guards against a future rename breaking discovery).
func TestFixtureExists(t *testing.T) {
	if _, err := os.Stat(filepath.Join("testdata", "Palac_Akropolis.svg")); err != nil {
		t.Fatalf("Palac_Akropolis.svg missing from testdata: %v", err)
	}
	// Sanity: fixture is non-trivial.
	raw, _ := os.ReadFile(filepath.Join("testdata", "Palac_Akropolis.svg"))
	if !strings.Contains(string(raw), `id="PriceCategory"`) {
		t.Fatalf("fixture does not contain PriceCategory group")
	}
}
