// Package planpreview draws a PNG picture of a seating plan geometry. The
// Telegram bot sends it to an organizer who checks a draft of a seating plan
// (spec 08_architecture/64, section 2.3): a filled circle per seat in the
// colour of its category, the outline of a general admission area, and the row
// names. It is pure Go (standard library and the basic bitmap font of
// golang.org/x/image) and reads nothing but the geometry it is given.
package planpreview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// Limits of the picture, in pixels.
const (
	MinSide = 64
	MaxSide = 2000
	// margin is the empty border around the plan, in pixels.
	// It leaves room for a row name left of the first seat and a sector name
	// above the first row, so the labels stay inside the picture.
	margin = 40
	// longRow is the seat count from which a row name is also put at its right end.
	longRow = 12
	// Size of a label of the basic 7x13 font, in pixels.
	labelHeight = 13
	labelAscent = 11
	// defaultSeatRadius is the seat radius in canvas units for a seat without one.
	defaultSeatRadius = 4.0
	// minSeatPx and maxSeatPx bound the drawn seat radius, in pixels.
	minSeatPx = 2
	maxSeatPx = 10
)

// ErrEmpty is returned for a geometry with no seat and no area to draw.
var ErrEmpty = errors.New("planpreview: nothing to draw")

var (
	background = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	fallback   = color.RGBA{R: 128, G: 128, B: 128, A: 255}
	labelInk   = color.RGBA{R: 60, G: 60, B: 60, A: 255}
)

// Render draws the geometry as a PNG whose longest side is at most maxSide
// pixels (the value is kept between MinSide and MaxSide).
func Render(g seating.Geometry, maxSide int) ([]byte, error) {
	maxSide = min(max(maxSide, MinSide), MaxSide)
	st, hasStage := findStage(g.DecorSVG)
	b, ok := boundsOf(g)
	if ok && hasStage {
		b.add(st.x, st.y)
		b.add(st.x+st.w, st.y+st.h)
	}
	if !ok {
		return nil, ErrEmpty
	}
	inner := float64(maxSide - 2*margin)
	scale := inner / math.Max(b.maxX-b.minX, b.maxY-b.minY)
	if math.IsInf(scale, 0) || math.IsNaN(scale) || scale <= 0 {
		scale = 1
	}
	w := int(math.Ceil((b.maxX-b.minX)*scale)) + 2*margin
	h := int(math.Ceil((b.maxY-b.minY)*scale)) + 2*margin
	w, h = min(max(w, 2*margin+1), maxSide), min(max(h, 2*margin+1), maxSide)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)

	// px maps a canvas point to pixels, clamped to the image so that no
	// drawing loop can run longer than the image is wide.
	px := func(x, y float64) (int, int) {
		return clampPx(margin+math.Round((x-b.minX)*scale), w), clampPx(margin+math.Round((y-b.minY)*scale), h)
	}
	colors := map[int]color.RGBA{}
	for _, c := range g.Categories {
		colors[c.Index] = parseColor(c.Color)
	}
	// Areas first, so a seat is never covered by an outline.
	for _, c := range g.Categories {
		if len(c.Polygon) < 2 {
			continue
		}
		col := colors[c.Index]
		for i := range c.Polygon {
			next := c.Polygon[(i+1)%len(c.Polygon)]
			if !usable(c.Polygon[i].X, c.Polygon[i].Y) || !usable(next.X, next.Y) {
				continue
			}
			x0, y0 := px(c.Polygon[i].X, c.Polygon[i].Y)
			x1, y1 := px(next.X, next.Y)
			line(img, x0, y0, x1, y1, col)
		}
	}
	// occupied marks every pixel a seat or a drawn label covers, so a label
	// is never put over a seat or over another label.
	occupied := make([]bool, w*h)
	mark := func(x0, y0, x1, y1 int) {
		for y := max(y0, 0); y < min(y1, h); y++ {
			for x := max(x0, 0); x < min(x1, w); x++ {
				occupied[y*w+x] = true
			}
		}
	}
	free := func(x0, y0, x1, y1 int) bool {
		if x0 < 0 || y0 < 0 || x1 > w || y1 > h {
			return false
		}
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if occupied[y*w+x] {
					return false
				}
			}
		}
		return true
	}
	// Seats are collected first (they fill the occupancy map), the stage is
	// drawn under them, then the seats, then the names.
	type dot struct {
		x, y, r int
		col     color.RGBA
	}
	type rowInfo struct {
		name                         string
		leftX, leftY, rightX, rightY float64
		leftR, rightR                int
		seats                        int
	}
	type secInfo struct {
		name string
		minX float64
		minY float64
		topR int
		set  bool
	}
	var dots []dot
	var rows []rowInfo
	var secs []secInfo
	for _, sec := range g.Sections {
		si := secInfo{name: strings.TrimSpace(sec.Name), minX: math.Inf(1), minY: math.Inf(1)}
		for _, row := range sec.Rows {
			ri := rowInfo{name: strings.TrimSpace(row.Name), leftX: math.Inf(1), rightX: math.Inf(-1)}
			for _, s := range row.Seats {
				if !usable(s.X, s.Y) {
					continue
				}
				col, known := colors[s.CategoryIndex]
				if !known {
					col = fallback
				}
				r := s.Radius
				if !usable(r, 0) || r <= 0 {
					r = defaultSeatRadius
				}
				rp := min(max(int(math.Round(r*scale)), minSeatPx), maxSeatPx)
				x, y := px(s.X, s.Y)
				dots = append(dots, dot{x, y, rp, col})
				mark(x-rp, y-rp, x+rp+1, y+rp+1)
				ri.seats++
				if s.X < ri.leftX {
					ri.leftX, ri.leftY, ri.leftR = s.X, s.Y, rp
				}
				if s.X > ri.rightX {
					ri.rightX, ri.rightY, ri.rightR = s.X, s.Y, rp
				}
				if s.X < si.minX {
					si.minX = s.X
				}
				if s.Y < si.minY {
					si.minY, si.topR = s.Y, rp
				}
				si.set = true
			}
			if ri.seats > 0 {
				rows = append(rows, ri)
			}
		}
		if si.set {
			secs = append(secs, si)
		}
	}
	if hasStage {
		drawStage(img, st, px)
	}
	for _, d := range dots {
		disc(img, d.x, d.y, d.r, d.col)
	}
	// Names the basic font cannot draw are skipped (labelOK): it has the
	// printable ASCII range only, so a name like "Партер" is not drawn.
	for _, si := range secs {
		if !sectionLabelOK(si.name) {
			continue
		}
		x, y := px(si.minX, si.minY)
		tw := labelWidth(si.name)
		x0, y0 := x-si.topR, y-si.topR-4-labelHeight
		if free(x0, y0, x0+tw, y0+labelHeight) {
			label(img, si.name, x0, y0+labelAscent)
			mark(x0, y0, x0+tw, y0+labelHeight)
		}
	}
	for _, ri := range rows {
		if !rowLabelOK(ri.name) {
			continue
		}
		tw := labelWidth(ri.name)
		x, y := px(ri.leftX, ri.leftY)
		x0, y0 := x-ri.leftR-3-tw, y-labelHeight/2
		if free(x0, y0, x0+tw, y0+labelHeight) {
			label(img, ri.name, x0, y0+labelAscent)
			mark(x0, y0, x0+tw, y0+labelHeight)
		}
		if ri.seats >= longRow {
			x, y = px(ri.rightX, ri.rightY)
			x0, y0 = x+ri.rightR+3, y-labelHeight/2
			if free(x0, y0, x0+tw, y0+labelHeight) {
				label(img, ri.name, x0, y0+labelAscent)
				mark(x0, y0, x0+tw, y0+labelHeight)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type bounds struct{ minX, minY, maxX, maxY float64 }

func (b *bounds) add(x, y float64) {
	b.minX, b.maxX = math.Min(b.minX, x), math.Max(b.maxX, x)
	b.minY, b.maxY = math.Min(b.minY, y), math.Max(b.maxY, y)
}

// maxCoord bounds a coordinate the picture accepts.
const maxCoord = 1e9

// usable reports whether both values are finite and within maxCoord.
func usable(x, y float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0) && math.Abs(x) <= maxCoord &&
		!math.IsNaN(y) && !math.IsInf(y, 0) && math.Abs(y) <= maxCoord
}

// clampPx turns a pixel value into an int between -margin and side+margin.
func clampPx(v float64, side int) int {
	if math.IsNaN(v) {
		return 0
	}
	return int(math.Min(math.Max(v, -margin), float64(side+margin)))
}

// boundsOf is the box the picture shows: the seats and the areas, widened by
// the seat radius and the room a row name needs on the left. The canvas of the
// plan is used when it is bigger than that box, so the picture keeps the
// proportions of the plan.
func boundsOf(g seating.Geometry) (bounds, bool) {
	b := bounds{minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
	add := b.add
	found := false
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				if !usable(s.X, s.Y) {
					continue
				}
				add(s.X, s.Y)
				found = true
			}
		}
	}
	for _, c := range g.Categories {
		for _, p := range c.Polygon {
			if !usable(p.X, p.Y) {
				continue
			}
			add(p.X, p.Y)
			found = true
		}
	}
	if !found {
		return bounds{}, false
	}
	if usable(g.Canvas.Width, g.Canvas.Height) && g.Canvas.Width > 0 && g.Canvas.Height > 0 {
		add(0, 0)
		add(g.Canvas.Width, g.Canvas.Height)
	}
	if b.maxX-b.minX <= 0 {
		b.maxX = b.minX + 1
	}
	if b.maxY-b.minY <= 0 {
		b.maxY = b.minY + 1
	}
	return b, true
}

// parseColor reads #rrggbb (or #rgb); anything else is grey.
func parseColor(s string) color.RGBA {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return fallback
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255} //nolint:gosec // G115: six hex digits, each byte is taken on purpose
}

// disc fills a circle of radius r around (cx, cy).
func disc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				set(img, cx+x, cy+y, c)
			}
		}
	}
}

func set(img *image.RGBA, x, y int, c color.RGBA) {
	if image.Pt(x, y).In(img.Rect) {
		img.SetRGBA(x, y, c)
	}
}

// line draws a one pixel line (Bresenham).
func line(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		set(img, x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// printable reports whether the basic font has every character of s: it has
// the printable ASCII range only (Cyrillic, accents and so on are missing).
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// rowLabelOK reports whether a row name is drawn: non-empty, ASCII, short.
func rowLabelOK(s string) bool { return s != "" && len(s) <= 6 && printable(s) }

// sectionLabelOK is the same for a sector name, which may be longer.
func sectionLabelOK(s string) bool { return s != "" && len(s) <= 24 && printable(s) }

var (
	stageFill = color.RGBA{R: 217, G: 217, B: 217, A: 255}
	stageEdge = color.RGBA{R: 90, G: 90, B: 90, A: 255}
)

// drawStage fills the stage rectangle and writes its word in the middle when
// the word is ASCII and the stage is wider than tall (the basic font cannot
// be rotated). The word may be taller than a thin bar; it is then written
// over the bar's middle, on the white around it.
func drawStage(img *image.RGBA, st stageRect, px func(x, y float64) (int, int)) {
	x0, y0 := px(st.x, st.y)
	x1, y1 := px(st.x+st.w, st.y+st.h)
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if x == x0 || x == x1 || y == y0 || y == y1 {
				set(img, x, y, stageEdge)
			} else {
				set(img, x, y, stageFill)
			}
		}
	}
	word := strings.TrimSpace(st.word)
	if word == "" || !printable(word) || len(word) > 24 || st.w < st.h {
		return
	}
	tw := labelWidth(word)
	if tw > x1-x0 {
		return
	}
	label(img, word, (x0+x1-tw)/2, (y0+y1)/2+labelAscent/2-1)
}

func labelWidth(s string) int { return len(s) * basicfont.Face7x13.Advance }

func label(img *image.RGBA, s string, x, y int) {
	d := font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(labelInk),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}
