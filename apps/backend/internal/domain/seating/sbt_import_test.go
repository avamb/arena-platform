package seating

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sbtGoldenPath is the real sbt/1.0 plan the WordPress seat picker
// consumes, generated from the Palac Akropolis geometry by the writer in
// hseating (feature #500). Reading the harness fixture rather than a
// private copy is deliberate: writer and reader must agree on ONE file,
// so a change to the wire shape cannot pass one side and break the other.
const sbtGoldenPath = "../../../tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg"

func loadSBTGolden(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(sbtGoldenPath))
	if err != nil {
		t.Fatalf("read sbt golden: %v", err)
	}
	return raw
}

func TestImportSBTSVG_PalacAkropolis(t *testing.T) {
	t.Parallel()
	plan, warnings, errs := ImportSBTSVG(loadSBTGolden(t))
	if len(errs) != 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	// ── canvas comes from viewBox verbatim ──────────────────────────────
	if got, want := plan.Geometry.Canvas.Width, 317.49999; got != want {
		t.Errorf("canvas width = %v, want %v", got, want)
	}
	if got, want := plan.Geometry.Canvas.Height, 423.33334; got != want {
		t.Errorf("canvas height = %v, want %v", got, want)
	}
	if plan.StatusVersion != 42 {
		t.Errorf("status version = %d, want 42", plan.StatusVersion)
	}

	// ── categories: external ids and the one priced category ────────────
	if len(plan.Categories) != 15 {
		t.Fatalf("categories = %d, want 15", len(plan.Categories))
	}
	for i, c := range plan.Categories {
		if c.Index != i+1 {
			t.Fatalf("category %d has index %d, want ascending order", i, c.Index)
		}
		wantExternal := int64(1000000000 + 10*(i+1))
		if c.ExternalID != wantExternal {
			t.Errorf("category %d external id = %d, want %d", c.Index, c.ExternalID, wantExternal)
		}
	}
	third := plan.Categories[2]
	if third.Name != "Third" {
		t.Errorf("category 3 name = %q, want %q", third.Name, "Third")
	}
	if third.Price != "1250" || third.PriceMinor != 125000 {
		t.Errorf("category 3 price = %q/%d, want \"1250\"/125000", third.Price, third.PriceMinor)
	}
	if plan.Categories[0].PriceMinor != 0 {
		t.Errorf("category 1 has no tier, want price 0, got %d", plan.Categories[0].PriceMinor)
	}
	// Geometry mirrors the metadata list, external ids included.
	if len(plan.Geometry.Categories) != 15 {
		t.Fatalf("geometry categories = %d, want 15", len(plan.Geometry.Categories))
	}
	if plan.Geometry.Categories[2].ExternalID != third.ExternalID {
		t.Errorf("geometry category 3 external id = %d, want %d",
			plan.Geometry.Categories[2].ExternalID, third.ExternalID)
	}
	if plan.Geometry.Categories[2].PriceHint != "1250" {
		t.Errorf("geometry category 3 price hint = %q, want %q",
			plan.Geometry.Categories[2].PriceHint, "1250")
	}

	// ── seats: every one carries a unique external id ───────────────────
	seatCount := plan.Geometry.SeatCount()
	if wantCircles := strings.Count(string(loadSBTGolden(t)), "<circle sbt:id="); seatCount != wantCircles {
		t.Errorf("seat count = %d, want %d (one per <circle sbt:id=…>)", seatCount, wantCircles)
	}
	seen := map[int64]string{}
	var byExternal10000 *Seat
	for si := range plan.Geometry.Sections {
		sec := plan.Geometry.Sections[si]
		for ri := range sec.Rows {
			row := sec.Rows[ri]
			for i := range row.Seats {
				s := row.Seats[i]
				if s.ExternalID == 0 {
					t.Fatalf("seat %q has no external id", s.Key)
				}
				if prev, dup := seen[s.ExternalID]; dup {
					t.Fatalf("external id %d used by both %q and %q", s.ExternalID, prev, s.Key)
				}
				seen[s.ExternalID] = s.Key
				if s.CategoryIndex <= 0 || s.CategoryIndex > 15 {
					t.Fatalf("seat %q category index %d out of range", s.Key, s.CategoryIndex)
				}
				if s.ExternalID == 10000 {
					seat := row.Seats[i]
					byExternal10000 = &seat
				}
			}
		}
	}
	if byExternal10000 == nil {
		t.Fatalf("seat with sbt:id=10000 not imported")
	}
	// The document's first circle: <g sbt:sect="Balcony center"><g sbt:row="1">
	// <circle sbt:id="10000" sbt:cat="3" sbt:seat="1" cx="-69.301842" …/>
	if got, want := byExternal10000.Key, SeatKey("balcony-center", "1", "1"); got != want {
		t.Errorf("seat 10000 key = %q, want %q", got, want)
	}
	if byExternal10000.CategoryIndex != 3 {
		t.Errorf("seat 10000 category = %d, want 3", byExternal10000.CategoryIndex)
	}
	if byExternal10000.X != -69.301842 {
		t.Errorf("seat 10000 cx = %v, want -69.301842", byExternal10000.X)
	}

	// ── decor keeps the backdrop and drops the sector subtrees ──────────
	decor := plan.Geometry.DecorSVG
	if !strings.Contains(decor, `id="Decor"`) {
		t.Errorf("decor lost the <g id=\"Decor\"> backdrop")
	}
	if strings.Contains(decor, "sbt:sect") || strings.Contains(decor, "sbt:state") {
		t.Errorf("decor still carries seat groups: %.200s", decor)
	}
	if strings.Contains(decor, "sbt:category") {
		t.Errorf("decor still carries <metadata>: %.200s", decor)
	}
}

// TestImportSBTSVG_Deterministic pins the promise Checksum rests on: the
// same bytes import to the same canonical geometry, external ids and all.
func TestImportSBTSVG_Deterministic(t *testing.T) {
	t.Parallel()
	raw := loadSBTGolden(t)
	a, _, errA := ImportSBTSVG(raw)
	b, _, errB := ImportSBTSVG(raw)
	if len(errA) != 0 || len(errB) != 0 {
		t.Fatalf("validation errors: %v / %v", errA, errB)
	}
	sumA, err := Checksum(a.Geometry)
	if err != nil {
		t.Fatalf("checksum a: %v", err)
	}
	sumB, err := Checksum(b.Geometry)
	if err != nil {
		t.Fatalf("checksum b: %v", err)
	}
	if sumA != sumB {
		t.Fatalf("checksum not deterministic: %s vs %s", sumA, sumB)
	}
}

// TestImportSBTSVG_ExternalIDsAffectChecksum proves external ids are part
// of the canonical form (feature #515): a plan re-imported after upstream
// re-issued its seat ids must produce a NEW geometry version, not silently
// reuse the old one.
func TestImportSBTSVG_ExternalIDsAffectChecksum(t *testing.T) {
	t.Parallel()
	base, _, errs := ImportSBTSVG([]byte(sbtDoc(`viewBox="0 0 100 100"`,
		sbtMetadata(`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`),
		sbtSector("Parter", "1", `<circle sbt:id="4242" sbt:state="1" sbt:cat="1" sbt:seat="12" cx="10" cy="20" r="3" fill="#ff0000"/>`),
	)))
	if len(errs) != 0 {
		t.Fatalf("base import errors: %v", errs)
	}
	shifted, _, errs := ImportSBTSVG([]byte(sbtDoc(`viewBox="0 0 100 100"`,
		sbtMetadata(`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`),
		sbtSector("Parter", "1", `<circle sbt:id="9999" sbt:state="1" sbt:cat="1" sbt:seat="12" cx="10" cy="20" r="3" fill="#ff0000"/>`),
	)))
	if len(errs) != 0 {
		t.Fatalf("shifted import errors: %v", errs)
	}
	sumBase, _ := Checksum(base.Geometry)
	sumShifted, _ := Checksum(shifted.Geometry)
	if sumBase == sumShifted {
		t.Fatalf("changing sbt:id did not change the geometry checksum (%s)", sumBase)
	}

	// A geometry with no external ids at all must hash exactly as it did
	// before the field existed — omitempty guards every stored checksum.
	stripped := Canonicalize(base.Geometry)
	stripped.Categories[0].ExternalID = 0
	stripped.Sections[0].Rows[0].Seats[0].ExternalID = 0
	js, err := CanonicalJSON(stripped)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if strings.Contains(string(js), "external_id") {
		t.Fatalf("zero external ids must not be serialised: %s", js)
	}
}

// TestImportSBTSVG_Rules is the synthetic per-rule fixture set from
// §13.3. Each document violates exactly one rule.
func TestImportSBTSVG_Rules(t *testing.T) {
	t.Parallel()
	oneCategory := sbtMetadata(
		`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`)
	seat := func(id, cat, num string) string {
		attrs := `sbt:id="` + id + `" sbt:state="1"`
		if cat != "" {
			attrs += ` sbt:cat="` + cat + `"`
		}
		if num != "" {
			attrs += ` sbt:seat="` + num + `"`
		}
		return `<circle ` + attrs + ` cx="1" cy="2" r="3" fill="#ff0000"/>`
	}

	cases := []struct {
		name    string
		svg     string
		wantErr string
	}{
		{
			name:    "viewBox missing",
			svg:     sbtDoc(``, oneCategory, sbtSector("Parter", "1", seat("11", "1", "1"))),
			wantErr: ErrCanvasMissing,
		},
		{
			name:    "viewBox malformed",
			svg:     sbtDoc(`viewBox="0 0 100"`, oneCategory, sbtSector("Parter", "1", seat("11", "1", "1"))),
			wantErr: ErrCanvasMissing,
		},
		{
			name:    "metadata missing",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, "", sbtSector("Parter", "1", seat("11", "1", "1"))),
			wantErr: ErrSBTCategoriesMissing,
		},
		{
			name:    "category index not a number",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, sbtMetadata(`<sbt:category sbt:id="7001" sbt:index="x" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`), sbtSector("Parter", "1", seat("11", "1", "1"))),
			wantErr: ErrSBTCategoryInvalid,
		},
		{
			name: "two categories share an index",
			svg: sbtDoc(`viewBox="0 0 100 100"`, sbtMetadata(
				`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`+
					`<sbt:category sbt:id="7002" sbt:index="1" sbt:name="Balcony" sbt:color="#00ff00" sbt:price="500"/>`),
				sbtSector("Parter", "1", seat("11", "1", "1"))),
			wantErr: ErrSBTDuplicateCategory,
		},
		{
			name:    "seat without sbt:cat",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, oneCategory, sbtSector("Parter", "1", seat("11", "", "1"))),
			wantErr: ErrSBTSeatCategoryMissing,
		},
		{
			name:    "seat referencing an unknown category index",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, oneCategory, sbtSector("Parter", "1", seat("11", "9", "1"))),
			wantErr: ErrSBTSeatCategoryMissing,
		},
		{
			name: "duplicate sbt:id",
			svg: sbtDoc(`viewBox="0 0 100 100"`, oneCategory,
				sbtSector("Parter", "1", seat("11", "1", "1")+seat("11", "1", "2"))),
			wantErr: ErrSBTDuplicateSeatID,
		},
		{
			name: "non-positive sbt:id",
			svg: sbtDoc(`viewBox="0 0 100 100"`, oneCategory,
				sbtSector("Parter", "1", seat("0", "1", "1"))),
			wantErr: ErrSBTSeatIDInvalid,
		},
		{
			name: "duplicate (sector,row,number)",
			svg: sbtDoc(`viewBox="0 0 100 100"`, oneCategory,
				sbtSector("Parter", "1", seat("11", "1", "7")+seat("12", "1", "7"))),
			wantErr: ErrDuplicateSeat,
		},
		{
			name:    "seat without a number",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, oneCategory, sbtSector("Parter", "1", seat("11", "1", ""))),
			wantErr: ErrSeatMissingNumber,
		},
		{
			name: "seat with no sbt:sect ancestor",
			svg: sbtDoc(`viewBox="0 0 100 100"`, oneCategory,
				`<g sbt:row="1">`+seat("11", "1", "1")+`</g>`),
			wantErr: ErrRowMissingSectorLabel,
		},
		{
			name: "seat with no row ancestor",
			svg: sbtDoc(`viewBox="0 0 100 100"`, oneCategory,
				`<g sbt:sect="Parter">`+seat("11", "1", "1")+`</g>`),
			wantErr: ErrRowMissingTitle,
		},
		{
			name:    "no seats at all",
			svg:     sbtDoc(`viewBox="0 0 100 100"`, oneCategory, `<g id="Decor"><rect x="0" y="0" width="5" height="5"/></g>`),
			wantErr: ErrSBTSeatsMissing,
		},
		{
			name:    "not XML",
			svg:     `<svg><g`,
			wantErr: ErrInvalidSVG,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, errs := ImportSBTSVG([]byte(tc.svg))
			if !errs.HasCode(tc.wantErr) {
				t.Fatalf("want code %q, got %v", tc.wantErr, errs)
			}
		})
	}
}

// TestImportSBTSVG_RowFromTitle covers the §13.3 fallback: a row group
// that names itself with <title> instead of sbt:row.
func TestImportSBTSVG_RowFromTitle(t *testing.T) {
	t.Parallel()
	svg := sbtDoc(`viewBox="0 0 100 100"`,
		sbtMetadata(`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="12.5"/>`),
		`<g sbt:sect="Parter"><g><title>B</title>`+
			`<circle sbt:id="11" sbt:state="1" sbt:cat="1" sbt:seat="4" cx="1" cy="2" r="3" fill="#ff0000"/>`+
			`</g></g>`)
	plan, _, errs := ImportSBTSVG([]byte(svg))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(plan.Geometry.Sections) != 1 || len(plan.Geometry.Sections[0].Rows) != 1 {
		t.Fatalf("want one section with one row, got %+v", plan.Geometry.Sections)
	}
	row := plan.Geometry.Sections[0].Rows[0]
	if row.Name != "B" || row.Key != "b" {
		t.Errorf("row = %q/%q, want B/b", row.Name, row.Key)
	}
	if row.Seats[0].Key != SeatKey("parter", "b", "4") {
		t.Errorf("seat key = %q", row.Seats[0].Key)
	}
	// Fractional major-unit price converts to minor units exactly.
	if plan.Categories[0].PriceMinor != 1250 {
		t.Errorf("price minor = %d, want 1250", plan.Categories[0].PriceMinor)
	}
}

// TestImportSBTSVG_DecorCircleIsNotASeat: a plain <circle> without the
// sbt attributes belongs to the backdrop, never to a row.
func TestImportSBTSVG_DecorCircleIsNotASeat(t *testing.T) {
	t.Parallel()
	svg := sbtDoc(`viewBox="0 0 100 100"`,
		sbtMetadata(`<sbt:category sbt:id="7001" sbt:index="1" sbt:name="Parter" sbt:color="#ff0000" sbt:price="900"/>`),
		`<g id="Decor"><circle id="stage-dot" cx="9" cy="9" r="2" fill="#000000"/></g>`+
			sbtSector("Parter", "1", `<circle sbt:id="11" sbt:state="4" sbt:cat="1" sbt:seat="1" cx="1" cy="2" r="3" fill="#ff0000"/>`))
	plan, _, errs := ImportSBTSVG([]byte(svg))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if got := plan.Geometry.SeatCount(); got != 1 {
		t.Fatalf("seat count = %d, want 1 (the decor circle must not be a seat)", got)
	}
	if !strings.Contains(plan.Geometry.DecorSVG, `stage-dot`) {
		t.Errorf("decor circle dropped: %q", plan.Geometry.DecorSVG)
	}
}

// --- synthetic fixture builders -------------------------------------------

func sbtDoc(rootAttrs, metadata, body string) string {
	root := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="` + SBTNamespaceURI + `"`
	if rootAttrs != "" {
		root += " " + rootAttrs
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + root + `>` + metadata + body + `</svg>`
}

func sbtMetadata(inner string) string {
	if inner == "" {
		return ""
	}
	return `<metadata>` + inner + `</metadata>`
}

func sbtSector(sector, row, seats string) string {
	return `<g sbt:sect="` + sector + `"><g sbt:row="` + row + `">` + seats + `</g></g>`
}
