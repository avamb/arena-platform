package seating

import (
	"strings"
	"testing"
)

const dialectNS = `xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" xmlns:sbt="` + SBTNamespaceURI + `"`

func dialectDoc(body string) string {
	return `<?xml version="1.0"?><svg ` + dialectNS + ` viewBox="0 0 200 100">` + body + `</svg>`
}

// A wire plan: seats carry sbt:id and sbt:state.
func wireFixture() string {
	return dialectDoc(`<metadata><sbt:category sbt:index="1" sbt:name="Standard" sbt:color="#ff0000" sbt:price="10" sbt:id="500"/></metadata>` +
		`<g sbt:sect="Hall"><g sbt:row="3">` +
		`<circle cx="10" cy="10" r="2" sbt:id="9001" sbt:state="1" sbt:seat="1" sbt:cat="1"/>` +
		`<circle cx="20" cy="10" r="2" sbt:id="9002" sbt:state="1" sbt:seat="2" sbt:cat="1"/>` +
		`</g></g>`)
}

// An editor template: no sbt:id / sbt:state, no category name or price,
// and the row group carries the row only through its title.
func templateFixture() string {
	return dialectDoc(`<metadata><sbt:category sbt:class="c" sbt:color="#00ff00" sbt:id="77" sbt:index="1"/>` +
		`<sbt:category sbt:class="d" sbt:color="#0000ff" sbt:id="78" sbt:index="2"/></metadata>` +
		`<g sbt:sect="Balcony"><g sbt:row="5">` +
		`<circle cx="10" cy="10" r="2" sbt:seat="1" sbt:cat="1"/>` +
		`<circle cx="20" cy="10" r="2" sbt:seat="2" sbt:cat="2"/>` +
		`</g><g sbt:row="6"><circle cx="10" cy="20" r="2" sbt:seat="1" sbt:cat="1"/></g></g>`)
}

func authoringFixture(rowTitle, extraSeatAttrs, outside string) string {
	return dialectDoc(`<g id="Legend"/><g id="PriceCategory"><circle inkscape:label="#First" cx="1" cy="1" r="1" style="fill:#ff0000"/></g>` +
		`<g inkscape:label="#Stalls" sbt:sect="Old"><title>` + rowTitle + `</title>` +
		`<circle cx="10" cy="10" r="2" style="fill:#ff0000" ` + extraSeatAttrs + `><title>4</title></circle>` +
		`<circle cx="20" cy="10" r="2" style="fill:#ff0000" ` + extraSeatAttrs + `><title>5</title></circle>` +
		`</g>` + outside)
}

func seatsOf(g Geometry) []Seat {
	var out []Seat
	for _, s := range g.Sections {
		for _, r := range s.Rows {
			out = append(out, r.Seats...)
		}
	}
	return out
}

func TestImportAnySVG_WireUnchanged(t *testing.T) {
	raw := []byte(wireFixture())
	if !IsSBTSVG(raw) {
		t.Fatal("wire plan not detected as sbt")
	}
	g, _, errs := ImportAnySVG(raw)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	plan, _, _ := ImportSBTSVG(raw)
	if len(seatsOf(g)) != 2 || seatsOf(g)[0].ExternalID != 9001 || seatsOf(g)[1].ExternalID != 9002 {
		t.Fatalf("seats %+v", seatsOf(g))
	}
	if a, b := mustChecksum(t, g), mustChecksum(t, plan.Geometry); a != b {
		t.Fatalf("checksum differs from ImportSBTSVG")
	}
	if g.Categories[0].ExternalID != 500 {
		t.Fatalf("category external id %d", g.Categories[0].ExternalID)
	}
}

func mustChecksum(t *testing.T, g Geometry) string {
	t.Helper()
	c, err := Checksum(g)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestImportAnySVG_EditorTemplate(t *testing.T) {
	raw := []byte(templateFixture())
	if !IsSBTSVG(raw) {
		t.Fatal("template not detected as sbt")
	}
	g, _, errs := ImportAnySVG(raw)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	seats := seatsOf(g)
	if len(seats) != 3 {
		t.Fatalf("seats = %d, want 3", len(seats))
	}
	for _, s := range seats {
		if s.ExternalID != 0 {
			t.Fatalf("seat %s carries a synthetic external id %d", s.Key, s.ExternalID)
		}
	}
	if len(g.Sections) != 1 || g.Sections[0].Name != "Balcony" || len(g.Sections[0].Rows) != 2 {
		t.Fatalf("sections %+v", g.Sections)
	}
	rows := g.Sections[0].Rows
	if rows[0].Name != "5" || rows[1].Name != "6" || len(rows[0].Seats) != 2 || rows[0].Seats[1].Number != "2" {
		t.Fatalf("rows %+v", rows)
	}
	for _, c := range g.Categories {
		if c.ExternalID != 0 || c.PriceHint != "" {
			t.Fatalf("category %+v presents an invented fact", c)
		}
		if !strings.HasPrefix(c.Name, "Category ") {
			t.Fatalf("category name %q", c.Name)
		}
	}
}

func TestImportAnySVG_StaleSBTAttributesLoseToTitles(t *testing.T) {
	stale := `sbt:sect="Old" sbt:row="99" sbt:seat="77" sbt:cat="1"`
	raw := []byte(authoringFixture("Row 8", stale, ""))
	if IsSBTSVG(raw) {
		t.Fatal("authoring file with stale sbt attributes taken for sbt")
	}
	g, _, errs := ImportAnySVG(raw)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	seats := seatsOf(g)
	if len(seats) != 2 || g.Sections[0].Name != "Stalls" || g.Sections[0].Rows[0].Name != "Row 8" {
		t.Fatalf("sections %+v", g.Sections)
	}
	if seats[0].Number != "4" || seats[1].Number != "5" {
		t.Fatalf("seat numbers %q %q, want the titles", seats[0].Number, seats[1].Number)
	}
}

func TestImportAnySVG_HalfConvertedIsRefused(t *testing.T) {
	stale := `sbt:seat="1" sbt:cat="1"`
	stray := `<g sbt:sect="Loose"><circle cx="50" cy="50" r="2" sbt:seat="9" sbt:cat="1"/></g>`
	g, _, errs := ImportAnySVG([]byte(authoringFixture("Row 8", stale, stray)))
	if !errs.HasCode(ErrMixedDialect) {
		t.Fatalf("errs = %v, want %s", errs, ErrMixedDialect)
	}
	if len(seatsOf(g)) != 0 {
		t.Fatal("a half-converted file was partly imported")
	}
	var detail string
	for _, e := range errs {
		if e.Code == ErrMixedDialect {
			detail = e.Detail
		}
	}
	if !strings.Contains(detail, "2 seats") || !strings.Contains(detail, "1 are outside") {
		t.Fatalf("detail %q lacks the counts", detail)
	}
}

func TestImportAnySVG_NoSBTGoesToImportSVG(t *testing.T) {
	raw := []byte(authoringFixture("Row 8", "", ""))
	g, _, errs := ImportAnySVG(raw)
	if len(errs) > 0 || len(seatsOf(g)) != 2 {
		t.Fatalf("seats %d errs %v", len(seatsOf(g)), errs)
	}
}

func TestNormalizeRowTitle(t *testing.T) {
	cases := map[string]string{
		"Ряд 7":     "7",
		"ряд 7А":    "7А",
		"РЯД  12":   "12",
		"  Ряд 3  ": "3",
		"Ряд":       "Ряд",
		"Ряд ":      "Ряд",
		"Ряды 7":    "Ряды 7",
		"Row 7":     "Row 7",
	}
	for in, want := range cases {
		if got := NormalizeRowTitle(in); got != want {
			t.Errorf("NormalizeRowTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRowWordIsDroppedByBothImporters(t *testing.T) {
	// Authoring importer.
	g, _, errs := ImportSVG([]byte(authoringFixture("Ряд 7", "", "")))
	if len(errs) > 0 || g.Sections[0].Rows[0].Name != "7" {
		t.Fatalf("ImportSVG: errs %v rows %+v", errs, g.Sections)
	}
	g, _, errs = ImportSVG([]byte(authoringFixture("Ряд", "", "")))
	if len(errs) > 0 || g.Sections[0].Rows[0].Name != "Ряд" {
		t.Fatalf("ImportSVG alone: errs %v rows %+v", errs, g.Sections)
	}
	// sbt importer: the row comes from the ancestor's <title>.
	wire := dialectDoc(`<metadata><sbt:category sbt:index="1" sbt:name="S" sbt:color="#ff0000" sbt:price="1" sbt:id="1"/></metadata>` +
		`<g sbt:sect="Hall"><g><title>Ряд 7А</title>` +
		`<circle cx="10" cy="10" r="2" sbt:id="1" sbt:state="1" sbt:seat="1" sbt:cat="1"/></g></g>`)
	plan, _, errs := ImportSBTSVG([]byte(wire))
	if len(errs) > 0 || plan.Geometry.Sections[0].Rows[0].Name != "7А" {
		t.Fatalf("ImportSBTSVG: errs %v rows %+v", errs, plan.Geometry.Sections)
	}
}

func TestImportAnySVG_StaleSeatsInPriceCategoryAndLegendAreNotMixed(t *testing.T) {
	raw := dialectDoc(`<g id="Legend"><circle cx="1" cy="1" r="1" sbt:seat="1" sbt:cat="1"/></g>` +
		`<g id="PriceCategory"><circle inkscape:label="#First" cx="1" cy="1" r="1" style="fill:#ff0000" sbt:seat="1" sbt:cat="1"/></g>` +
		`<g inkscape:label="#Stalls"><title>Row 1</title>` +
		`<circle cx="10" cy="10" r="2" style="fill:#ff0000"><title>1</title></circle></g>`)
	g, _, errs := ImportAnySVG([]byte(raw))
	if errs.HasCode(ErrMixedDialect) || len(errs) > 0 || len(seatsOf(g)) != 1 {
		t.Fatalf("seats %d errs %v", len(seatsOf(g)), errs)
	}
}

func TestImportAnySVG_UnpricedTemplateReportsMissingCategory(t *testing.T) {
	raw := dialectDoc(`<metadata><sbt:category sbt:class="c" sbt:color="#00ff00" sbt:id="77" sbt:index="1"/></metadata>` +
		`<g sbt:sect="Hall"><g sbt:row="1">` +
		`<circle class="cat0" cx="10" cy="10" r="2" sbt:seat="1"/>` +
		`<circle class="cat0" cx="20" cy="10" r="2" sbt:seat="2"/>` +
		`<circle class="cat0" cx="30" cy="10" r="2" sbt:seat="3"/></g></g>`)
	_, _, errs := ImportAnySVG([]byte(raw))
	if len(errs) != 1 || errs[0].Code != ErrSBTSeatCategoryMissing || !strings.Contains(errs[0].Detail, "3 seats") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestImportAnySVG_ForeignNamespacePrefixIsAuthoring(t *testing.T) {
	raw := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" ` +
		`xmlns:sbtt="http://www.w3.org/2015/sbtt/1.0" viewBox="0 0 100 100">` +
		`<g id="Legend"/><g id="PriceCategory"><circle inkscape:label="#First" cx="1" cy="1" r="1" style="fill:#ff0000"/></g>` +
		`<g inkscape:label="#Stalls" sbtt:sect="Old"><title>Row 2</title>` +
		`<circle cx="10" cy="10" r="2" style="fill:#ff0000" sbtt:seat="55" sbtt:cat="1"><title>6</title></circle></g></svg>`
	g, _, errs := ImportAnySVG([]byte(raw))
	if len(errs) > 0 || len(seatsOf(g)) != 1 || seatsOf(g)[0].Number != "6" || g.Sections[0].Rows[0].Name != "Row 2" {
		t.Fatalf("errs %v sections %+v", errs, g.Sections)
	}
}
