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

// TestImportSVG_PalacAkropolisGAAcceptance is the AB-40 B4 second
// acceptance fixture: the *combined* Palác Akropolis plan — side/balcony
// seats plus a 500-capacity ground floor authored per the "#GA <name>"
// convention. It is NOT a damaged copy of the seated plan; both must
// import. Total imported capacity must equal the real venue capacity.
func TestImportSVG_PalacAkropolisGAAcceptance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "Palac_Akropolis_GA.svg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, _, errs := ImportSVG(raw)
	if len(errs) != 0 {
		t.Fatalf("expected zero validation errors on GA fixture, got: %v", errs)
	}

	// Combined plan: balcony seats only (the ground floor carries no
	// coordinate seats), 90 of them.
	if got := g.SeatCount(); got != 90 {
		t.Fatalf("GA fixture seat count = %d, want 90 balcony seats", got)
	}

	// Exactly one GA category, bound to the Fifteenth swatch by fill,
	// renamed by the area label, capacity 500 — the whole dance floor.
	ga := g.GACategories()
	if len(ga) != 1 {
		t.Fatalf("GA fixture must yield exactly 1 GA category, got %d (%v)",
			len(ga), categoryNames(g))
	}
	if ga[0].Name != "General admission" {
		t.Errorf("GA category name = %q, want %q (area label wins over swatch)",
			ga[0].Name, "General admission")
	}
	if ga[0].Capacity != 500 {
		t.Errorf("GA category capacity = %d, want 500", ga[0].Capacity)
	}
	if len(ga[0].Polygon) != 4 {
		t.Errorf("GA rect must yield a 4-point polygon, got %d points", len(ga[0].Polygon))
	}
	if got := g.GACapacity(); got != 500 {
		t.Errorf("GACapacity() = %d, want 500", got)
	}

	// Imported total capacity equals the real venue: 90 + 500 = 590.
	if total := g.SeatCount() + g.GACapacity(); total != 590 {
		t.Errorf("total imported capacity = %d, want 590", total)
	}

	// Valid as a mixed plan; invalid as assigned_seats (GA present).
	if verrs := ValidateForPlanType(g, "mixed"); len(verrs) != 0 {
		t.Errorf("ValidateForPlanType(mixed) = %v, want none", verrs)
	}
	if verrs := ValidateForPlanType(g, "assigned_seats"); !verrs.HasCode(ErrGAAreaNotAllowed) {
		t.Errorf("ValidateForPlanType(assigned_seats) = %v, want %q", verrs, ErrGAAreaNotAllowed)
	}

	// Checksum stability across two imports.
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

// TestImportSVG_GAAreas covers the AB-40 "#GA <name>" authoring rules.
func TestImportSVG_GAAreas(t *testing.T) {
	t.Parallel()
	canvas := `viewBox="0 0 100 100"`
	row := rowGroupOpen("Sector A", "Row 1") +
		`<circle cx="1" cy="1" r="1" style="fill:#ff0000"><title>1</title></circle>` +
		rowGroupClose()

	cases := []struct {
		name     string
		inner    string
		wantCode string
	}{
		{
			name: "valid rect GA area",
			inner: priceCategoryFragment() + row +
				`<rect inkscape:label="#GA Floor" x="0" y="0" width="10" height="10" style="fill:#ff0000"><title>50</title></rect>`,
			wantCode: "", // fill matches the First swatch — but seats also use it; validated separately
		},
		{
			name: "fill matches no swatch",
			inner: priceCategoryFragment() + row +
				`<rect inkscape:label="#GA Floor" x="0" y="0" width="10" height="10" style="fill:#123456"><title>50</title></rect>`,
			wantCode: ErrGAColorUnmatched,
		},
		{
			name: "missing capacity title",
			inner: priceCategoryFragment() + row +
				`<rect inkscape:label="#GA Floor" x="0" y="0" width="10" height="10" style="fill:#ff0000"/>`,
			wantCode: ErrGACapacityInvalid,
		},
		{
			name: "non-numeric capacity",
			inner: priceCategoryFragment() + row +
				`<rect inkscape:label="#GA Floor" x="0" y="0" width="10" height="10" style="fill:#ff0000"><title>lots</title></rect>`,
			wantCode: ErrGACapacityInvalid,
		},
		{
			name: "unsupported shape",
			inner: priceCategoryFragment() + row +
				`<path inkscape:label="#GA Floor" d="M0 0 L10 10" style="fill:#ff0000"><title>50</title></path>`,
			wantCode: ErrGAShapeUnsupported,
		},
		{
			name: "two areas binding one swatch",
			inner: priceCategoryFragment() + row +
				`<rect inkscape:label="#GA A" x="0" y="0" width="5" height="5" style="fill:#ff0000"><title>10</title></rect>` +
				`<rect inkscape:label="#GA B" x="6" y="0" width="5" height="5" style="fill:#ff0000"><title>10</title></rect>`,
			wantCode: ErrGADuplicateCategory,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, _, errs := ImportSVG([]byte(wrapSVG(canvas, tc.inner)))
			if tc.wantCode == "" {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got %v", errs)
				}
				if len(g.GACategories()) != 1 {
					t.Fatalf("expected one GA category, got %+v", g.Categories)
				}
				return
			}
			if !errs.HasCode(tc.wantCode) {
				t.Fatalf("expected code %q, got %v", tc.wantCode, errs)
			}
		})
	}
}

// TestImportSVG_GAPolygonShape checks the polygon points path.
func TestImportSVG_GAPolygonShape(t *testing.T) {
	t.Parallel()
	inner := priceCategoryFragment() +
		`<polygon inkscape:label="#GA Pit" points="0,0 10,0 10,10" style="fill:#ff0000"><title>25</title></polygon>`
	g, _, errs := ImportSVG([]byte(wrapSVG(`viewBox="0 0 100 100"`, inner)))
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	ga := g.GACategories()
	if len(ga) != 1 || len(ga[0].Polygon) != 3 || ga[0].Capacity != 25 {
		t.Fatalf("unexpected GA parse result: %+v", ga)
	}
}

// TestValidateForPlanType covers the AB-40 B3 plan-type gate.
func TestValidateForPlanType(t *testing.T) {
	t.Parallel()
	seat := Seat{Key: "a|1|1", Number: "1", CategoryIndex: 1}
	seated := Geometry{
		Categories: []Category{{Index: 1, Name: "First", Color: "#ff0000"}},
		Sections:   []Section{{Key: "a", Name: "A", Rows: []Row{{Key: "1", Name: "1", Seats: []Seat{seat}}}}},
	}
	gaOnly := Geometry{
		Categories: []Category{{Index: 1, Name: "R1", Color: "#ff0000", Kind: KindGeneralAdmission, Capacity: 10}},
	}
	combined := Geometry{
		Categories: []Category{
			{Index: 1, Name: "First", Color: "#ff0000"},
			{Index: 2, Name: "GA", Color: "#00d455", Kind: KindGeneralAdmission, Capacity: 500},
		},
		Sections: seated.Sections,
	}

	cases := []struct {
		name     string
		g        Geometry
		planType string
		want     string // "" = valid
	}{
		{"assigned ok", seated, "assigned_seats", ""},
		{"assigned with GA", combined, "assigned_seats", ErrGAAreaNotAllowed},
		{"assigned without seats", gaOnly, "assigned_seats", ErrSeatsMissing},
		{"ga ok", gaOnly, "general_admission", ""},
		{"ga without GA category", seated, "general_admission", ErrGAAreaMissing},
		{"mixed ok", combined, "mixed", ""},
		{"mixed without GA", seated, "mixed", ErrGAAreaMissing},
		{"mixed without seats", gaOnly, "mixed", ErrSeatsMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs := ValidateForPlanType(tc.g, tc.planType)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatalf("expected valid, got %v", errs)
				}
				return
			}
			if !errs.HasCode(tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, errs)
			}
		})
	}

	t.Run("seat bound to GA category", func(t *testing.T) {
		t.Parallel()
		bad := Geometry{
			Categories: []Category{{Index: 1, Name: "GA", Color: "#ff0000", Kind: KindGeneralAdmission, Capacity: 5}},
			Sections:   seated.Sections, // seat has CategoryIndex 1 → the GA category
		}
		if errs := ValidateForPlanType(bad, "mixed"); !errs.HasCode(ErrSeatInGACategory) {
			t.Fatalf("expected %q, got %v", ErrSeatInGACategory, errs)
		}
	})
	t.Run("sixteen categories", func(t *testing.T) {
		t.Parallel()
		var cats []Category
		for i := 1; i <= 16; i++ {
			cats = append(cats, Category{Index: i, Name: "C", Color: "#000000"})
		}
		g := seated
		g.Categories = cats
		if errs := ValidateForPlanType(g, "assigned_seats"); !errs.HasCode(ErrTooManyCategories) {
			t.Fatalf("expected %q, got %v", ErrTooManyCategories, errs)
		}
	})
	t.Run("nonpositive GA capacity", func(t *testing.T) {
		t.Parallel()
		g := Geometry{
			Categories: []Category{{Index: 1, Name: "R1", Color: "#ff0000", Kind: KindGeneralAdmission, Capacity: 0}},
		}
		if errs := ValidateForPlanType(g, "general_admission"); !errs.HasCode(ErrGACapacityInvalid) {
			t.Fatalf("expected %q, got %v", ErrGACapacityInvalid, errs)
		}
	})
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
