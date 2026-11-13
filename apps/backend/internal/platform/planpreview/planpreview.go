// Package planpreview draws a PNG picture of a seating plan geometry. The
// Telegram bot sends it to an organizer who checks a draft of a seating plan
// (spec 08_architecture/64, section 2.3): a filled circle per seat in the
// colour of its category, the outline of a general admission area, and the row
// names. It is pure Go (standard library and the basic bitmap font of
// golang.org/x/image) and reads nothing but the geometry it is given.
//
// The picture is cropped to its content: the frame is the box of the seats,
// the areas and the stage plus a border, whatever the canvas of the plan is.
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
	// padShare is the empty border around the content as a share of the longest side.
	padShare = 0.07
	// Size of a label of the basic 7x13 font, in pixels.
	labelHeight = 13
	labelAscent = 11
	// defaultSeatRadius is the seat radius in canvas units for a seat without one.
	defaultSeatRadius = 4.0
	// minSeatPx and maxSeatPx bound the drawn seat radius, in pixels. A hall of
	// very many seats may go down to denseSeatPx so that it stays readable as a whole.
	minSeatPx   = 3
	denseSeatPx = 2
	maxSeatPx   = 12
	denseSeats  = 3000
	// bigFontSide is the side from which labels are drawn twice as big.
	bigFontSide = 900
	// minShare is the shortest side of the picture as a share of the longest one.
	minShare = 1.0 / 3
)

// ErrEmpty is returned for a geometry with no seat and no area to draw.
var ErrEmpty = errors.New("planpreview: nothing to draw")

var (
	background = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	fallback   = color.RGBA{R: 128, G: 128, B: 128, A: 255}
	labelInk   = color.RGBA{R: 60, G: 60, B: 60, A: 255}
)

type dot struct {
	x, y, r int
	col     color.RGBA
}

type placedLabel struct {
	text string
	x, y int // top left corner of the text box, in pixels
	kind string
}

// frame is the layout of the picture: its size and the mapping of canvas units to pixels.
type frame struct {
	w, h       int
	scale      float64
	minX, minY float64
	offX, offY float64
	padX, padY int
	font       int // label magnification, 1 or 2
	seatMin    int
	hasStage   bool
	stage      stageRect
}

func (f frame) px(x, y float64) (int, int) {
	return clampPx(f.offX+math.Round((x-f.minX)*f.scale), f.w), clampPx(f.offY+math.Round((y-f.minY)*f.scale), f.h)
}

// plan is everything Render draws.
type plan struct {
	f      frame
	dots   []dot
	labels []placedLabel
}

// Render draws the geometry as a PNG whose longest side is at most maxSide
// pixels (the value is kept between MinSide and MaxSide).
func Render(g seating.Geometry, maxSide int) ([]byte, error) {
	p, err := build(g, maxSide)
	if err != nil {
		return nil, err
	}
	f := p.f
	img := image.NewRGBA(image.Rect(0, 0, f.w, f.h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
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
			x0, y0 := f.px(c.Polygon[i].X, c.Polygon[i].Y)
			x1, y1 := f.px(next.X, next.Y)
			line(img, x0, y0, x1, y1, col)
		}
	}
	if f.hasStage {
		drawStage(img, f.stage, f.px, f.font)
	}
	for _, d := range p.dots {
		disc(img, d.x, d.y, d.r, d.col)
	}
	for _, l := range p.labels {
		label(img, l.text, l.x, l.y, f.font)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// build lays the picture out: the frame fitted to the content, the seats and
// the labels that have room. It draws nothing.
func build(g seating.Geometry, maxSide int) (*plan, error) {
	maxSide = min(max(maxSide, MinSide), MaxSide)
	st, hasStage := findStage(g.DecorSVG)
	b, seatCount, ok := contentBounds(g)
	if !ok {
		return nil, ErrEmpty
	}
	if hasStage && usable(st.x, st.y) && usable(st.x+st.w, st.y+st.h) {
		b.add(st.x, st.y)
		b.add(st.x+st.w, st.y+st.h)
	} else {
		hasStage = false
	}
	spanX, spanY := b.maxX-b.minX, b.maxY-b.minY
	if spanX <= 0 {
		spanX = 1
	}
	if spanY <= 0 {
		spanY = 1
	}
	f := frame{font: 1, seatMin: minSeatPx, minX: b.minX, minY: b.minY, hasStage: hasStage, stage: st}
	if maxSide >= bigFontSide {
		f.font = 2
	}
	if seatCount > denseSeats {
		f.seatMin = denseSeatPx
	}
	// The room a label needs: the widest drawable row name beside a seat, and
	// a section name above the first row.
	rowRoom := 0
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			if n := strings.TrimSpace(row.Name); rowLabelOK(n) {
				rowRoom = max(rowRoom, labelWidth(n)*f.font)
			}
		}
	}
	if rowRoom > 0 {
		rowRoom += maxSeatPx + 6
	}
	share := int(math.Round(padShare * float64(maxSide)))
	limit := maxSide / 4
	f.padX = max(min(max(share, rowRoom), limit), 1)
	f.padY = max(min(max(share, labelHeight*f.font+maxSeatPx+8), limit), 1)
	f.scale = math.Min(float64(maxSide-2*f.padX)/spanX, float64(maxSide-2*f.padY)/spanY)
	if math.IsInf(f.scale, 0) || math.IsNaN(f.scale) || f.scale <= 0 {
		f.scale = 1
	}
	cw, ch := spanX*f.scale, spanY*f.scale
	minDim := int(float64(maxSide) * minShare)
	f.w = min(max(int(math.Ceil(cw))+2*f.padX, minDim), maxSide)
	f.h = min(max(int(math.Ceil(ch))+2*f.padY, minDim), maxSide)
	f.offX, f.offY = (float64(f.w)-cw)/2, (float64(f.h)-ch)/2
	p := &plan{f: f}

	colors := map[int]color.RGBA{}
	for _, c := range g.Categories {
		colors[c.Index] = parseColor(c.Color)
	}
	w, h := f.w, f.h
	// occupied marks every pixel a seat or a placed label covers, so a label
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
				rp := min(max(int(math.Round(r*f.scale)), f.seatMin), maxSeatPx)
				x, y := f.px(s.X, s.Y)
				p.dots = append(p.dots, dot{x, y, rp, col})
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
	lh := labelHeight * f.font
	put := func(text string, x0, y0 int, kind string) {
		p.labels = append(p.labels, placedLabel{text: text, x: x0, y: y0, kind: kind})
		mark(x0, y0, x0+labelWidth(text)*f.font, y0+lh)
	}
	// Names the basic font cannot draw are skipped: it has the printable ASCII
	// range only, so a name like "Партер" is not drawn.
	for _, si := range secs {
		if !sectionLabelOK(si.name) {
			continue
		}
		x, y := f.px(si.minX, si.minY)
		tw := labelWidth(si.name) * f.font
		x0 := x - si.topR
		for try := 0; try < 4; try++ {
			y0 := y - si.topR - 4 - lh - try*(lh+2)
			if free(x0, y0, x0+tw, y0+lh) {
				put(si.name, x0, y0, "section")
				break
			}
		}
	}
	for _, ri := range rows {
		if !rowLabelOK(ri.name) {
			continue
		}
		tw := labelWidth(ri.name) * f.font
		x, y := f.px(ri.leftX, ri.leftY)
		if x0, y0 := x-ri.leftR-3-tw, y-lh/2; free(x0, y0, x0+tw, y0+lh) {
			put(ri.name, x0, y0, "row-left")
		}
		x, y = f.px(ri.rightX, ri.rightY)
		if x0, y0 := x+ri.rightR+3, y-lh/2; free(x0, y0, x0+tw, y0+lh) {
			put(ri.name, x0, y0, "row-right")
		}
	}
	return p, nil
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

// clampPx turns a pixel value into an int between -40 and side+40.
func clampPx(v float64, side int) int {
	if math.IsNaN(v) {
		return 0
	}
	return int(math.Min(math.Max(v, -40), float64(side+40)))
}

// contentBounds is the box of the seats and the areas (not the canvas of the
// plan: a plan whose canvas is far bigger than its content is shown by its
// content), and the number of usable seats.
func contentBounds(g seating.Geometry) (bounds, int, bool) {
	b := bounds{minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
	found := false
	seats := 0
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				if !usable(s.X, s.Y) {
					continue
				}
				b.add(s.X, s.Y)
				found = true
				seats++
			}
		}
	}
	for _, c := range g.Categories {
		for _, p := range c.Polygon {
			if !usable(p.X, p.Y) {
				continue
			}
			b.add(p.X, p.Y)
			found = true
		}
	}
	return b, seats, found
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
func drawStage(img *image.RGBA, st stageRect, px func(x, y float64) (int, int), fs int) {
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
	tw := labelWidth(word) * fs
	if tw > x1-x0 {
		return
	}
	label(img, word, (x0+x1-tw)/2, (y0+y1)/2-labelHeight*fs/2, fs)
}

func labelWidth(s string) int { return len(s) * basicfont.Face7x13.Advance }

// label draws s with its text box starting at (x, y), magnified fs times.
func label(img *image.RGBA, s string, x, y, fs int) {
	tw := labelWidth(s)
	small := image.NewRGBA(image.Rect(0, 0, tw, labelHeight))
	d := font.Drawer{
		Dst:  small,
		Src:  image.NewUniform(labelInk),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(0, labelAscent),
	}
	d.DrawString(s)
	for sy := 0; sy < labelHeight; sy++ {
		for sx := 0; sx < tw; sx++ {
			if small.RGBAAt(sx, sy).A == 0 {
				continue
			}
			for dy := 0; dy < fs; dy++ {
				for dx := 0; dx < fs; dx++ {
					set(img, x+sx*fs+dx, y+sy*fs+dy, labelInk)
				}
			}
		}
	}
}
