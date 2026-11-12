package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

const samplePath = "../../tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg"

func loadSample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return raw
}

func TestRestyle_HouseStyleRoundTrip(t *testing.T) {
	src := loadSample(t)
	house, rep := Restyle(src, nil)
	if house == nil {
		t.Fatalf("no house.svg, report: %+v", rep)
	}
	if rep.Status == StatusFailed {
		t.Fatalf("status failed: %v", rep.Reasons)
	}
	if rep.Scale != 1 {
		t.Fatalf("scale = %v", rep.Scale)
	}
	if !rep.StageFound {
		t.Fatalf("stage label not found in the sample")
	}
	if rep.RemovedRowNumbers == 0 {
		t.Fatalf("expected the sample's row numbers to be removed")
	}

	plan, _, errs := seating.ImportSBTSVG(src)
	if len(errs) > 0 {
		t.Fatalf("source import: %v", errs)
	}
	sg := plan.Geometry

	hg, _, herrs := seating.ImportSVG(house)
	if len(herrs) > 0 {
		t.Fatalf("ImportSVG errors: %v", herrs)
	}
	if verrs := seating.ValidateForPlanType(hg, "assigned_seats"); len(verrs) > 0 {
		t.Fatalf("ValidateForPlanType errors: %v", verrs)
	}
	if hg.SeatCount() != sg.SeatCount() || rep.SourceSeats != rep.HouseSeats {
		t.Fatalf("seat count: source %d house %d", sg.SeatCount(), hg.SeatCount())
	}

	// Per-row counts.
	counts := func(g seating.Geometry) map[string]int {
		m := map[string]int{}
		for _, s := range g.Sections {
			for _, r := range s.Rows {
				m[s.Key+"|"+r.Key] = len(r.Seats)
			}
		}
		return m
	}
	sc, hc := counts(sg), counts(hg)
	if len(sc) != len(hc) {
		t.Fatalf("row count: source %d house %d", len(sc), len(hc))
	}
	for k, n := range sc {
		if hc[k] != n {
			t.Fatalf("row %s: source %d house %d", k, n, hc[k])
		}
	}

	// Positions.
	pos := map[string][2]float64{}
	for _, s := range sg.Sections {
		for _, r := range s.Rows {
			for _, seat := range r.Seats {
				pos[seat.Key] = [2]float64{seat.X, seat.Y}
			}
		}
	}
	for _, s := range hg.Sections {
		for _, r := range s.Rows {
			for _, seat := range r.Seats {
				p, ok := pos[seat.Key]
				if !ok {
					t.Fatalf("seat %s is not in the source", seat.Key)
				}
				if math.Abs(p[0]-seat.X) > 0.01 || math.Abs(p[1]-seat.Y) > 0.01 {
					t.Fatalf("seat %s moved: %v -> %v,%v", seat.Key, p, seat.X, seat.Y)
				}
			}
		}
	}

	// Categories: only those with seats, no prices.
	if len(hg.Categories) != rep.Categories || len(hg.Categories) == 0 || len(hg.Categories) > len(sg.Categories) {
		t.Fatalf("categories: house %d report %d source %d", len(hg.Categories), rep.Categories, len(sg.Categories))
	}
	for _, c := range hg.Categories {
		if c.PriceHint != "" {
			t.Fatalf("category %q carries a price hint %q", c.Name, c.PriceHint)
		}
	}
	text := string(house)
	for _, bad := range []string{"1250", "sbt:price", "%price%", "PriceCategoryText"} {
		if strings.Contains(text, bad) {
			t.Fatalf("house.svg contains price text %q", bad)
		}
	}

	// data-* attributes and house groups.
	circles := regexp.MustCompile(`<circle [^>]*data-sector="[^"]+" data-row="[^"]+" data-seat="[^"]+"`)
	if n := len(circles.FindAllString(text, -1)); n != sg.SeatCount() {
		t.Fatalf("seats with data-* attributes: %d, want %d", n, sg.SeatCount())
	}
	for _, want := range []string{`id="PriceCategory"`, `id="Legend"`, `id="HouseDecor"`, `id="HouseLabels"`, `pointer-events:none`} {
		if !strings.Contains(text, want) {
			t.Fatalf("house.svg lacks %s", want)
		}
	}
	if strings.Contains(text, `id="Legend"><`) {
		t.Fatalf("legend group must be empty")
	}
}

func TestRestyle_Deterministic(t *testing.T) {
	src := loadSample(t)
	a, _ := Restyle(src, nil)
	b, _ := Restyle(src, nil)
	if !bytes.Equal(a, b) {
		t.Fatalf("two runs differ")
	}
}

func TestRestyle_BrokenSourceFails(t *testing.T) {
	src := loadSample(t)
	re := regexp.MustCompile(` sbt:cat="\d+"`)
	broken := re.ReplaceAll(src, nil) // every seat loses its category
	house, rep := Restyle(broken, nil)
	if house != nil {
		t.Fatalf("house.svg must not be written for a broken source")
	}
	if rep.Status != StatusFailed || len(rep.Errors) == 0 {
		t.Fatalf("status %q errors %v", rep.Status, rep.Errors)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.svg"), broken, 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale house.svg must go.
	if err := os.WriteFile(filepath.Join(dir, "house.svg"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := ConvertPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusFailed {
		t.Fatalf("status %q", r.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "house.svg")); !os.IsNotExist(err) {
		t.Fatalf("house.svg exists after a failed conversion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "convert.json")); err != nil {
		t.Fatalf("convert.json missing: %v", err)
	}
}

func TestRestyle_NoStageNeedsReview(t *testing.T) {
	src := loadSample(t)
	re := regexp.MustCompile(`(?i)stage`)
	noStage := re.ReplaceAll(src, []byte("xxxxx"))
	_, rep := Restyle(noStage, nil)
	if rep.Status != StatusNeedsReview {
		t.Fatalf("status %q reasons %v", rep.Status, rep.Reasons)
	}
	found := false
	for _, r := range rep.Reasons {
		if r == "stage label not found" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons %v", rep.Reasons)
	}
}

func TestSchemaCounts(t *testing.T) {
	raw := []byte(`{"resultCode":0,"seats":[{"seatId":1,"sector":"A","row":"1","number":"1"},{"seatId":2,"sector":"A","row":"1","number":"2"}]}`)
	counts, rows, ok := schemaCounts(raw)
	if !ok || counts["a|1"] != 2 || len(rows) != 1 {
		t.Fatalf("counts %v rows %v ok %v", counts, rows, ok)
	}
	if _, _, ok := schemaCounts([]byte(`{"x":[1,2]}`)); ok {
		t.Fatalf("unknown shape must not be understood")
	}
}

const authoringPath = "../../internal/domain/seating/testdata/Palac_Akropolis.svg"
const authoringGAPath = "../../internal/domain/seating/testdata/Palac_Akropolis_GA.svg"

func TestRestyle_AuthoringDialect(t *testing.T) {
	src, err := os.ReadFile(authoringPath)
	if err != nil {
		t.Fatal(err)
	}
	house, rep := Restyle(src, nil)
	if house == nil {
		t.Fatalf("no house.svg, report: %+v", rep)
	}
	if rep.Dialect != "authoring" {
		t.Fatalf("dialect %q", rep.Dialect)
	}
	if rep.Status == StatusFailed {
		t.Fatalf("status failed: %v", rep.Reasons)
	}
	sg, _, serrs := seating.ImportSVG(src)
	if len(serrs) > 0 {
		t.Fatalf("source import: %v", serrs)
	}
	hg, _, herrs := seating.ImportSVG(house)
	if len(herrs) > 0 {
		t.Fatalf("ImportSVG errors: %v", herrs)
	}
	if verrs := seating.ValidateForPlanType(hg, "assigned_seats"); len(verrs) > 0 {
		t.Fatalf("ValidateForPlanType errors: %v", verrs)
	}
	sc, hc := rowKeyCounts(sg), rowKeyCounts(hg)
	if len(sc) == 0 || len(sc) != len(hc) {
		t.Fatalf("rows: source %d house %d", len(sc), len(hc))
	}
	for k, n := range sc {
		if hc[k] != n {
			t.Fatalf("row %s: source %d house %d", k, n, hc[k])
		}
	}
	pos := map[string][2]float64{}
	for _, s := range sg.Sections {
		for _, r := range s.Rows {
			for _, seat := range r.Seats {
				pos[seat.Key] = [2]float64{seat.X, seat.Y}
			}
		}
	}
	for _, s := range hg.Sections {
		for _, r := range s.Rows {
			for _, seat := range r.Seats {
				p, ok := pos[seat.Key]
				if !ok {
					t.Fatalf("seat %s is not in the source", seat.Key)
				}
				if math.Abs(p[0]-seat.X) > 0.01 || math.Abs(p[1]-seat.Y) > 0.01 {
					t.Fatalf("seat %s moved", seat.Key)
				}
			}
		}
	}
}

func TestRestyle_GeneralAdmissionFails(t *testing.T) {
	src, err := os.ReadFile(authoringGAPath)
	if err != nil {
		t.Fatal(err)
	}
	house, rep := Restyle(src, nil)
	if house != nil || rep.Status != StatusFailed {
		t.Fatalf("status %q house %v", rep.Status, house != nil)
	}
	if len(rep.Reasons) == 0 || rep.Reasons[0] != "general-admission area: not converted" {
		t.Fatalf("reasons %v", rep.Reasons)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.svg"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ConvertPlan(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "house.svg")); !os.IsNotExist(err) {
		t.Fatalf("house.svg exists: %v", err)
	}
}

func inlineSBT(rowName, decor string) []byte {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 200 100">`)
	b.WriteString(`<metadata><sbt:category sbt:id="1000000010" sbt:index="1" sbt:name="First" sbt:color="#ff0000" sbt:price="0" sbt:class="cat-1"/></metadata>`)
	b.WriteString(`<g id="Decor">` + decor + `</g>`)
	b.WriteString(`<g sbt:sect="Sec">`)
	id := 1
	for _, row := range []struct {
		name string
		y    int
	}{{rowName, 50}, {"1", 60}} {
		b.WriteString(`<g sbt:row="` + row.name + `">`)
		for i := 0; i < 3; i++ {
			b.WriteString(`<circle sbt:id="` + strconv.Itoa(id) + `" sbt:state="1" sbt:cat="1" sbt:seat="` + strconv.Itoa(i+1) +
				`" cx="` + strconv.Itoa(80+10*i) + `" cy="` + strconv.Itoa(row.y) + `" r="3" fill="#ff0000"/>`)
			id++
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</g></svg>`)
	return []byte(b.String())
}

func labelTexts(house []byte) []string {
	i := strings.Index(string(house), `id="HouseLabels"`)
	re := regexp.MustCompile(`<text [^>]*>([^<]*)</text>`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(house)[i:], -1) {
		out = append(out, m[1])
	}
	return out
}

func TestRestyle_PlaceholderRowGetsNoLabel(t *testing.T) {
	house, rep := Restyle(inlineSBT("no", ""), nil)
	if house == nil {
		t.Fatalf("no house: %+v", rep)
	}
	if rep.Dialect != "sbt" {
		t.Fatalf("dialect %q", rep.Dialect)
	}
	for _, s := range labelTexts(house) {
		if s == "no" {
			t.Fatalf("placeholder row got a label")
		}
	}
	if !strings.Contains(string(house), "<title>no</title>") {
		t.Fatalf("row title lost")
	}
	hasRow1 := 0
	for _, s := range labelTexts(house) {
		if s == "1" {
			hasRow1++
		}
	}
	if hasRow1 < 2 {
		t.Fatalf("real row labels missing")
	}
}

func TestRestyle_TitleAvoidsDecorText(t *testing.T) {
	decor := `<text x="90" y="42" style="text-anchor:middle">Hall</text>`
	house, rep := Restyle(inlineSBT("2", decor), nil)
	if house == nil {
		t.Fatalf("no house: %+v", rep)
	}
	for _, r := range rep.Reasons {
		if strings.Contains(r, "sector title overlaps") {
			t.Fatalf("reasons %v", rep.Reasons)
		}
	}
	m := regexp.MustCompile(`<text x="([^"]+)" y="([^"]+)"[^>]*>Sec</text>`).FindStringSubmatch(string(house))
	if m == nil {
		t.Fatalf("no sector title")
	}
	y, _ := strconv.ParseFloat(m[2], 64)
	if y < 60 {
		t.Fatalf("title y=%v still above the sector", y)
	}
	// Same name in the decor: no generated title.
	house2, _ := Restyle(inlineSBT("2", `<text x="10" y="10">  SEC </text>`), nil)
	for _, s := range labelTexts(house2) {
		if s == "Sec" {
			t.Fatalf("title drawn although the decor has it")
		}
	}
}

func TestRestyle_CentredDecorTextByClassIsAvoided(t *testing.T) {
	// No text-anchor attribute: the class may centre the text, so the box
	// covers both readings (x-w/2 .. x+w), w = 0.6 * 6 * 5 = 18.
	decor := `<text class="c" x="90" y="44">STAGE</text>`
	house, rep := Restyle(inlineSBT("2", decor), nil)
	if house == nil {
		t.Fatalf("no house: %+v", rep)
	}
	m := regexp.MustCompile(`<text x="([^"]+)" y="([^"]+)" style="font-family:sans-serif;font-size:([^p]+)px[^>]*>Sec</text>`).FindStringSubmatch(string(house))
	if m == nil {
		t.Fatalf("no sector title")
	}
	x, _ := strconv.ParseFloat(m[1], 64)
	y, _ := strconv.ParseFloat(m[2], 64)
	size, _ := strconv.ParseFloat(m[3], 64)
	if size != 6 {
		t.Fatalf("title font %v, want 2 x seat radius = 6", size)
	}
	title := textBox(x, y, size, "Sec")
	stage := box{90 - 9, 44 - 6, 90 + 18, 44 + 1.2}
	if title.overlaps(stage) {
		t.Fatalf("title %+v intersects STAGE %+v", title, stage)
	}
}

func TestRestyle_TwoLineDecorEqualsSectorName(t *testing.T) {
	src := strings.Replace(string(inlineSBT("2", `<text x="10" y="10"><tspan x="10" y="10">Side  Boxes</tspan><tspan x="10" y="20">RIGHT</tspan></text>`)),
		`sbt:sect="Sec"`, `sbt:sect="Side Boxes right"`, 1)
	house, rep := Restyle([]byte(src), nil)
	if house == nil {
		t.Fatalf("no house: %+v", rep)
	}
	for _, s := range labelTexts(house) {
		if s == "Side Boxes right" {
			t.Fatalf("title drawn although the decor has it")
		}
	}
}

func TestRestyle_ScreenIsAStage(t *testing.T) {
	_, rep := Restyle(inlineSBT("2", `<text x="10" y="10">SCREEN</text>`), nil)
	if !rep.StageFound || rep.StageLabel != "SCREEN" {
		t.Fatalf("stage_found %v label %q", rep.StageFound, rep.StageLabel)
	}
}

func TestConvertPlan_SkipsNotDownloaded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.svg"), inlineSBT("1", ""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(`{"status":"skipped"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := ConvertPlan(dir)
	if err != nil || rep.Status != StatusSkipped {
		t.Fatalf("status %q err %v", rep.Status, err)
	}
	for _, f := range []string{"house.svg", "convert.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Fatalf("%s written: %v", f, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(`{"status":"downloaded"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err = ConvertPlan(dir)
	if err != nil || rep.Status == StatusSkipped {
		t.Fatalf("status %q err %v", rep.Status, err)
	}
}

func sbtPlan(pairs [][2]string) []byte {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 200 100">`)
	b.WriteString(`<metadata><sbt:category sbt:id="1000000010" sbt:index="1" sbt:name="First" sbt:color="#ff0000" sbt:price="0" sbt:class="cat-1"/></metadata>`)
	id := 1
	for i, p := range pairs {
		b.WriteString(`<g sbt:sect="` + p[0] + `"><g sbt:row="` + p[1] + `">`)
		for s := 0; s < 3; s++ {
			b.WriteString(`<circle sbt:id="` + strconv.Itoa(id) + `" sbt:state="1" sbt:cat="1" sbt:seat="` + strconv.Itoa(s+1) +
				`" cx="` + strconv.Itoa(20+10*s) + `" cy="` + strconv.Itoa(20+15*i) + `" r="3" fill="#ff0000"/>`)
			id++
		}
		b.WriteString(`</g></g>`)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func authoringPlan(pairs [][2]string) []byte {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 200 100">`)
	b.WriteString(`<g id="PriceCategory"><circle inkscape:label="#First" cx="-10" cy="-10" r="4" style="fill:#ff0000"/></g><g id="Legend"/>`)
	for i, p := range pairs {
		b.WriteString(`<g inkscape:label="#Сектор ` + p[0] + `"><title>` + p[1] + `</title>`)
		for s := 0; s < 3; s++ {
			b.WriteString(`<circle cx="` + strconv.Itoa(20+10*s) + `" cy="` + strconv.Itoa(20+15*i) + `" r="3" style="fill:#ff0000"><title>` + strconv.Itoa(s+1) + `</title></circle>`)
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func TestRestyle_MergeGuard(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{"sbt sectors", sbtPlan([][2]string{{"Партер", "1"}, {"Балкон", "1"}})},
		{"authoring sectors", authoringPlan([][2]string{{"Партер", "1"}, {"Балкон", "1"}})},
		{"sbt rows", sbtPlan([][2]string{{"Parter", "А"}, {"Parter", "Б"}})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			house, rep := Restyle(c.src, nil)
			if house != nil || rep.Status != StatusFailed {
				t.Fatalf("status %q house %v errors %v", rep.Status, house != nil, rep.Errors)
			}
			if len(rep.Reasons) == 0 || !strings.HasPrefix(rep.Reasons[0], "importer merged sectors or rows:") {
				t.Fatalf("reasons %v", rep.Reasons)
			}
			if len(rep.MergedNames) == 0 {
				t.Fatalf("merged_names empty")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source.svg"), c.src, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "house.svg"), []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if r, err := ConvertPlan(dir); err != nil || r.Status != StatusFailed {
				t.Fatalf("status %q err %v", r.Status, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "house.svg")); !os.IsNotExist(err) {
				t.Fatalf("house.svg exists: %v", err)
			}
		})
	}
}

func TestRestyle_MergeGuardLatinPlansStayOK(t *testing.T) {
	for name, src := range map[string][]byte{
		"sbt":       sbtPlan([][2]string{{"Parter", "A"}, {"Balcony", "B"}}),
		"authoring": authoringPlan([][2]string{{"Parter", "A"}, {"Balcony", "B"}}),
	} {
		house, rep := Restyle(src, nil)
		if house == nil || rep.Status == StatusFailed {
			t.Fatalf("%s: status %q reasons %v", name, rep.Status, rep.Reasons)
		}
	}
}
