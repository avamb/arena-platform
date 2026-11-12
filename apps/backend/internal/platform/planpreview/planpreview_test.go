package planpreview

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
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
	// Same mapping as Render: the box is the canvas (0..400 x 0..200).
	scale := float64(800-2*margin) / 400
	at := func(x, y float64) color.Color {
		return img.At(margin+int(x*scale+0.5), margin+int(y*scale+0.5))
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

func TestRender_LongRowGetsRightLabelToo(t *testing.T) {
	mk := func(seats int) seating.Geometry {
		g := sample()
		g.Sections[0].Rows = []seating.Row{{Key: "1", Name: "A"}}
		for i := 0; i < seats; i++ {
			g.Sections[0].Rows[0].Seats = append(g.Sections[0].Rows[0].Seats,
				seating.Seat{Key: "k", Number: "1", X: 20 + float64(i)*20, Y: 100, Radius: 5, CategoryIndex: 1})
		}
		return g
	}
	long, noName := mk(15), mk(15)
	noName.Sections[0].Rows[0].Name = ""
	short, shortNoName := mk(4), mk(4)
	shortNoName.Sections[0].Rows[0].Name = ""
	nLong := diffPixels(t, decode(t, long, 800), decode(t, noName, 800))
	nShort := diffPixels(t, decode(t, short, 800), decode(t, shortNoName, 800))
	if nLong <= nShort {
		t.Fatalf("long row diff %d is not bigger than short row diff %d", nLong, nShort)
	}
}

func TestRender_LabelNeverCoversASeat(t *testing.T) {
	// A seat of another row stands exactly where the left label would go.
	g := sample()
	g.Sections[0].Rows = []seating.Row{
		{Key: "1", Name: "ABCDE", Seats: []seating.Seat{{Key: "a", Number: "1", X: 100, Y: 100, Radius: 8, CategoryIndex: 1}}},
		{Key: "2", Name: "", Seats: []seating.Seat{{Key: "b", Number: "1", X: 80, Y: 100, Radius: 8, CategoryIndex: 2}}},
	}
	noLabel := sample()
	noLabel.Sections[0].Rows = []seating.Row{g.Sections[0].Rows[0], g.Sections[0].Rows[1]}
	noLabel.Sections[0].Rows[0].Name = ""
	if n := diffPixels(t, decode(t, g, 800), decode(t, noLabel, 800)); n != 0 {
		t.Fatalf("label drawn over a seat: %d bytes differ", n)
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
	if n := diffPixels(t, img, decode(t, sample(), 800)); n == 0 {
		t.Fatal("stage changed nothing")
	}
	scale := float64(800-2*margin) / 400
	c := img.RGBAAt(margin+int(150*scale), margin+int(12*scale))
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
