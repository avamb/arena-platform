package planpreview

import (
	"bytes"
	"image/color"
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
