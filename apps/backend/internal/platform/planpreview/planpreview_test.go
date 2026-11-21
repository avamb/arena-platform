package planpreview

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func sample() seating.Geometry {
	return seating.Geometry{
		Canvas: seating.Canvas{Width: 400, Height: 200},
		Categories: []seating.Category{
			{Index: 1, Name: "Parterre", Color: "#ff0000"},
			{Index: 2, Name: "Balcony", Color: "#0000ff"},
			{Index: 3, Name: "Standing", Color: "#00aa00", Kind: seating.KindGeneralAdmission, Capacity: 50,
				Polygon: []seating.Point{{X: 300, Y: 20}, {X: 380, Y: 20}, {X: 380, Y: 100}, {X: 300, Y: 100}}},
		},
		Sections: []seating.Section{{Key: "p", Name: "Parterre", Rows: []seating.Row{
			{Key: "1", Name: "A", Seats: []seating.Seat{
				{Key: "p|1|1", Number: "1", X: 100, Y: 100, Radius: 8, CategoryIndex: 1},
				{Key: "p|1|2", Number: "2", X: 130, Y: 100, Radius: 8, CategoryIndex: 1},
			}},
			{Key: "2", Name: "B", Seats: []seating.Seat{
				{Key: "p|2|1", Number: "1", X: 100, Y: 160, Radius: 8, CategoryIndex: 2},
			}},
		}}},
	}
}

func TestRender_DecodesAsPNGWithinTheSide(t *testing.T) {
	out, err := Render(sample(), 600)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() > 600 || b.Dy() > 600 || b.Dx() < 100 || b.Dy() < 50 {
		t.Fatalf("size = %v", b)
	}
}

func TestRender_SeatPixelHasItsCategoryColour(t *testing.T) {
	g := sample()
	out, err := Render(g, 800)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	// Same mapping as Render: the frame is fitted to the content.
	p, err := build(g, 800)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y float64) color.Color {
		px, py := p.f.px(x, y)
		return img.At(px, py)
	}
	if r, gg, bb, _ := at(100, 100).RGBA(); r>>8 != 255 || gg != 0 || bb != 0 {
		t.Fatalf("parterre seat is %v %v %v", r>>8, gg>>8, bb>>8)
	}
	if r, gg, bb, _ := at(100, 160).RGBA(); r != 0 || gg != 0 || bb>>8 != 255 {
		t.Fatalf("balcony seat is %v %v %v", r>>8, gg>>8, bb>>8)
	}
	// A point far from any seat is background.
	if r, gg, bb, _ := at(200, 20).RGBA(); r>>8 != 255 || gg>>8 != 255 || bb>>8 != 255 {
		t.Fatal("empty area is not white")
	}
}

func TestRender_SideIsBoundedAndEmptyIsRefused(t *testing.T) {
	out, err := Render(sample(), 100000)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() > MaxSide || img.Bounds().Dy() > MaxSide {
		t.Fatalf("size = %v", img.Bounds())
	}
	if _, err := Render(seating.Geometry{}, 600); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
}

func TestParseColor(t *testing.T) {
	if c := parseColor("#0f8"); c.G != 0xff || c.B != 0x88 {
		t.Fatalf("short = %+v", c)
	}
	if parseColor("nope") != fallback {
		t.Fatal("bad colour is not grey")
	}
}

func TestRender_NonFiniteAndHugeCoordinatesReturnQuickly(t *testing.T) {
	inf := math.Inf(1)
	g := sample()
	g.Categories[2].Polygon = []seating.Point{{X: math.NaN(), Y: 0}, {X: inf, Y: 5}, {X: 1e300, Y: -1e300}, {X: 10, Y: 10}, {X: 50, Y: 60}}
	g.Sections[0].Rows[0].Seats = append(g.Sections[0].Rows[0].Seats,
		seating.Seat{Key: "x1", Number: "9", X: math.NaN(), Y: 3, Radius: math.NaN()},
		seating.Seat{Key: "x2", Number: "10", X: -inf, Y: inf},
		seating.Seat{Key: "x3", Number: "11", X: 1e300, Y: 1e300, Radius: 1e300})
	g.Canvas = seating.Canvas{Width: inf, Height: math.NaN()}
	done := make(chan error, 1)
	go func() {
		_, err := Render(g, 600)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Render did not return within a second")
	}
}

func decode(t *testing.T, g seating.Geometry, side int) *image.RGBA {
	t.Helper()
	out, err := Render(g, side)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, image.Point{}, draw.Src)
	return rgba
}

// diffPixels counts the pixels that differ between two pictures of one size.
func diffPixels(t *testing.T, a, b *image.RGBA) int {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("sizes differ: %v %v", a.Bounds(), b.Bounds())
	}
	n := 0
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			n++
		}
	}
	return n
}

func TestRender_RowNamesAreDrawn(t *testing.T) {
	with := sample()
	without := sample()
	for i := range without.Sections[0].Rows {
		without.Sections[0].Rows[i].Name = ""
	}
	if n := diffPixels(t, decode(t, with, 800), decode(t, without, 800)); n == 0 {
		t.Fatal("row names changed nothing")
	}
}

func labelsOf(p *plan, kind string) []placedLabel {
	var out []placedLabel
	for _, l := range p.labels {
		if l.kind == kind {
			out = append(out, l)
		}
	}
	return out
}

func TestBuild_EveryRowHasLeftAndRightLabel(t *testing.T) {
	g := sample()
	p, err := build(g, 800)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A", "B"} {
		var left, right bool
		for _, l := range p.labels {
			if l.text == want && l.kind == "row-left" {
				left = true
			}
			if l.text == want && l.kind == "row-right" {
				right = true
			}
		}
		if !left || !right {
			t.Fatalf("row %s: left=%v right=%v", want, left, right)
		}
	}
	if len(labelsOf(p, "section")) != 1 {
		t.Fatalf("section labels = %d", len(labelsOf(p, "section")))
	}
}

func loadSVG(t *testing.T, name string) seating.Geometry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "venue-svg-generator", "arena_export", "testdata", name))
	if err != nil {
		t.Skipf("no sample %s: %v", name, err)
	}
	g, _, verrs := seating.ImportAnySVG(raw)
	if len(verrs) > 0 {
		t.Fatalf("import %s: %v", name, verrs)
	}
	return g
}

func seatBox(p *plan) (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for _, d := range p.dots {
		x0, y0, x1, y1 = min(x0, d.x), min(y0, d.y), max(x1, d.x), max(y1, d.y)
	}
	return
}

func TestBuild_SyntheticAuthoringFillsTheFrame(t *testing.T) {
	g := loadSVG(t, "synthetic_authoring.svg")
	for _, side := range []int{600, 1280} {
		p, err := build(g, side)
		if err != nil {
			t.Fatal(err)
		}
		x0, y0, x1, y1 := seatBox(p)
		fw, fh := float64(x1-x0)/float64(p.f.w), float64(y1-y0)/float64(p.f.h)
		if fw < 0.6 && fh < 0.6 {
			t.Fatalf("side %d: seats take %.2f x %.2f of the frame", side, fw, fh)
		}
		pad := min(p.f.padX, p.f.padY)
		for _, d := range p.dots {
			if d.x < pad || d.y < pad || d.x > p.f.w-pad || d.y > p.f.h-pad {
				t.Fatalf("side %d: seat centre (%d,%d) is closer than %d to the edge of %dx%d", side, d.x, d.y, pad, p.f.w, p.f.h)
			}
			if d.r < minSeatPx {
				t.Fatalf("seat radius %d", d.r)
			}
		}
		if len(labelsOf(p, "row-left")) == 0 || len(labelsOf(p, "row-right")) == 0 {
			t.Fatal("no row labels")
		}
	}
}

func TestBuild_HugeCanvasGivesTheSameFrame(t *testing.T) {
	small := sample()
	big := sample()
	big.Canvas = seating.Canvas{Width: 100000, Height: 80000}
	a, err := build(small, 800)
	if err != nil {
		t.Fatal(err)
	}
	b, err := build(big, 800)
	if err != nil {
		t.Fatal(err)
	}
	if a.f != b.f || len(a.dots) != len(b.dots) || len(a.labels) != len(b.labels) {
		t.Fatalf("frames differ: %+v vs %+v", a.f, b.f)
	}
}

func TestRender_EmptyGeometryDoesNotPanic(t *testing.T) {
	if _, err := Render(seating.Geometry{Canvas: seating.Canvas{Width: 100, Height: 100}}, 600); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
	g := seating.Geometry{Sections: []seating.Section{{Key: "s", Rows: []seating.Row{{Key: "r"}}}}}
	if _, err := Render(g, 600); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
}

func TestRender_SinglePointDoesNotFail(t *testing.T) {
	g := seating.Geometry{Sections: []seating.Section{{Key: "s", Name: "S", Rows: []seating.Row{
		{Key: "r", Name: "1", Seats: []seating.Seat{{Key: "k", Number: "1", X: 5, Y: 5}}}}}}}
	if _, err := Render(g, 600); err != nil {
		t.Fatal(err)
	}
}

func TestRender_ManySeatsStayWithinTheSide(t *testing.T) {
	g := sample()
	g.Sections[0].Rows = nil
	for r := 0; r < 60; r++ {
		row := seating.Row{Key: strconv.Itoa(r), Name: strconv.Itoa(r + 1)}
		for s := 0; s < 100; s++ {
			row.Seats = append(row.Seats, seating.Seat{Key: "k", Number: "1", X: float64(s) * 10, Y: float64(r) * 12, Radius: 4, CategoryIndex: 1})
		}
		g.Sections[0].Rows = append(g.Sections[0].Rows, row)
	}
	img := decode(t, g, MaxSide)
	if img.Bounds().Dx() > MaxSide || img.Bounds().Dy() > MaxSide {
		t.Fatalf("size = %v", img.Bounds())
	}
}

// TestSavePreviews writes the pictures of the sample plans for a look by eye
// when ARENA_PLANPREVIEW_OUT names a folder.
func TestSavePreviews(t *testing.T) {
	dir := os.Getenv("ARENA_PLANPREVIEW_OUT")
	if dir == "" {
		t.Skip("ARENA_PLANPREVIEW_OUT is not set")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"synthetic_authoring", "v2_render"} {
		out, err := Render(loadSVG(t, name+".svg"), 1280)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".png"), out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRender_LabelNeverCoversASeat(t *testing.T) {
	// A seat of another row stands exactly where the left label would go.
	g := sample()
	g.Sections[0].Rows = []seating.Row{
		{Key: "1", Name: "ABCDE", Seats: []seating.Seat{{Key: "a", Number: "1", X: 100, Y: 100, Radius: 8, CategoryIndex: 1}}},
		{Key: "2", Name: "", Seats: []seating.Seat{{Key: "b", Number: "1", X: 80, Y: 100, Radius: 8, CategoryIndex: 2}}},
	}
	p, err := build(g, 800)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range p.labels {
		x1, y1 := l.x+labelWidth(l.text)*p.f.font, l.y+labelHeight*p.f.font
		for _, d := range p.dots {
			if d.x+d.r >= l.x && d.x-d.r < x1 && d.y+d.r >= l.y && d.y-d.r < y1 {
				t.Fatalf("label %q (%s) covers a seat at (%d,%d)", l.text, l.kind, d.x, d.y)
			}
		}
	}
}

const stageDecor = `<svg xmlns="http://www.w3.org/2000/svg"><g id="decor"><rect id="decor-stage" x="100" y="10" width="200" height="20"/><text id="decor-stage-text" x="200" y="24">STAGE</text></g></svg>`

func TestFindStage(t *testing.T) {
	st, ok := findStage(stageDecor)
	if !ok || st.x != 100 || st.w != 200 || st.word != "STAGE" {
		t.Fatalf("generator stage = %+v %v", st, ok)
	}
	hand := `<svg><g transform="translate(-3,-36)"><rect x="79" y="56" width="150" height="17"/><text><tspan>STAGE</tspan></text></g></svg>`
	st, ok = findStage(hand)
	if !ok || st.x != 76 || st.y != 20 || st.word != "STAGE" {
		t.Fatalf("hand-drawn stage = %+v %v", st, ok)
	}
	if _, ok := findStage(`<svg><g transform="rotate(30)"><rect x="1" y="1" width="5" height="5"/><text>STAGE</text></g></svg>`); ok {
		t.Fatal("a rotated stage must not be guessed")
	}
	if _, ok := findStage(`<svg><rect x="1" y="1" width="5" height="5"/></svg>`); ok {
		t.Fatal("a rectangle without a stage word is not a stage")
	}
	if _, ok := findStage(""); ok {
		t.Fatal("empty decor has no stage")
	}
}

func TestRender_StageIsDrawn(t *testing.T) {
	with := sample()
	with.DecorSVG = stageDecor
	img := decode(t, with, 800)
	p, err := build(with, 800)
	if err != nil {
		t.Fatal(err)
	}
	sx, sy := p.f.px(150, 20)
	c := img.RGBAAt(sx, sy)
	if c != stageFill {
		t.Fatalf("stage fill pixel = %v", c)
	}
}

func TestRender_NonLatinNamesDoNotFail(t *testing.T) {
	g := sample()
	g.Sections[0].Name = "Партер"
	g.Sections[0].Rows[0].Name = "Ряд"
	g.DecorSVG = `<svg><rect id="decor-stage" x="100" y="10" width="200" height="20"/><text id="decor-stage-text">Сцена</text></svg>`
	if _, err := Render(g, 600); err != nil {
		t.Fatal(err)
	}
	// The unreadable sector name is skipped: nothing above the section is drawn.
	plain := sample()
	plain.Sections[0].Name = ""
	plain.Sections[0].Rows[0].Name = ""
	plain.DecorSVG = g.DecorSVG
	if n := diffPixels(t, decode(t, g, 600), decode(t, plain, 600)); n != 0 {
		t.Fatalf("non-Latin labels were drawn: %d", n)
	}
}

func TestRender_SectorNameIsDrawnAboveSection(t *testing.T) {
	g := sample()
	g.Sections[0].Name = "Parterre"
	plain := sample()
	plain.Sections[0].Name = ""
	if n := diffPixels(t, decode(t, g, 800), decode(t, plain, 800)); n == 0 {
		t.Fatal("sector name changed nothing")
	}
}

func TestRender_LabelsKeepTheSizeLimit(t *testing.T) {
	g := sample()
	g.DecorSVG = stageDecor
	for _, side := range []int{MinSide, 200, 1280, MaxSide, 100000} {
		img := decode(t, g, side)
		if img.Bounds().Dx() > MaxSide || img.Bounds().Dy() > MaxSide || img.Bounds().Dx() > max(side, MinSide) || img.Bounds().Dy() > max(side, MinSide) {
			t.Fatalf("side %d: size = %v", side, img.Bounds())
		}
	}
}
