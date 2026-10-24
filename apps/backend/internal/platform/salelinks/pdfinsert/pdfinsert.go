// Package pdfinsert renders the printable PDF insert of a sales link (spec 43
// section 6.5, task QL-13): the QR code of the short address, the name of
// what the code sells (an event, a date, a promoter or the organization), a
// "scan and buy tickets" line in the organizer's language, the short address
// in letters under the code, and the organization logo on top when it has
// one. It is pure: no HTTP, no database, no clock.
//
// The renderer is built on gofpdf like the e-ticket (delivery/pdf) and reuses
// that package's embedded DejaVu Sans Condensed family through
// pdf.DejaVuFonts, so Cyrillic, Czech diacritics and Hebrew print with real
// glyphs. Hebrew gets the same treatment as on the ticket: correct glyphs in
// right-to-left visual order per line (visualLine), no contextual shaping,
// because gofpdf has no bidi engine. Latin letters and digits inside a
// Hebrew line (a hall name, a date) keep their own left-to-right order.
//
// The QR is drawn as filled black rectangles on the white page, one per run
// of dark modules in a row, never as a raster image: a printer cannot blur a
// vector module and the quiet zone (quietModules modules on every side) is
// left completely empty. Every text block is wrapped and centred by this
// package itself, so a long title shrinks and is finally cut with an
// ellipsis rather than pushing the code off the page.
//
// Byte determinism: compression off, catalog sort on, timestamps pinned,
// exactly as delivery/pdf does it. Two renders of the same Insert are
// byte-identical; the HTTP handler builds its ETag from them.
package pdfinsert

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // logo decoding
	_ "image/png"  // logo decoding
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
)

// Paper is the page size of the insert.
type Paper string

// The two papers the route accepts (spec 43 section 6.5): A6 fits a flyer
// stand and a table tent, A5 a poster corner.
const (
	PaperA6 Paper = "a6"
	PaperA5 Paper = "a5"
)

// DefaultPaper is what the route uses when the request names none.
const DefaultPaper = PaperA6

// ParsePaper reads a query value; case and surrounding spaces do not matter.
// An empty value is the default paper.
func ParsePaper(s string) (Paper, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return DefaultPaper, true
	case string(PaperA6):
		return PaperA6, true
	case string(PaperA5):
		return PaperA5, true
	}
	return "", false
}

// FileName is the name a client saves the insert under: <code>-<paper>.pdf.
func FileName(code string, paper Paper) string {
	return code + "-" + string(paper) + ".pdf"
}

// Insert is everything the page shows. Content is what the QR encodes (the
// link's full short address); Address is what is printed under the code and
// defaults to Content without its scheme. Title names the target (event,
// promoter or organization), Subtitle is an optional second line (the date
// of a session), Tagline the localized "scan and buy tickets" line (see
// Tagline). OrgName is printed on top when Logo is empty or not a PNG/JPEG;
// Logo is drawn there otherwise. Locale drives only the Hebrew visual order.
type Insert struct {
	Content  string
	Address  string
	Title    string
	Subtitle string
	Tagline  string
	OrgName  string
	Logo     []byte
	Locale   string
	Paper    Paper
}

// ErrEmptyContent is returned when there is nothing to encode.
var ErrEmptyContent = errors.New("pdfinsert: content is empty")

// ErrBadPaper is returned for a paper that is neither a6 nor a5.
var ErrBadPaper = errors.New("pdfinsert: paper must be a6 or a5")

// quietModules is the quiet zone of the printed code, in modules: the
// standard's minimum is 4, and nothing is ever drawn inside it.
const quietModules = 4

// mm converts millimetres to points, the unit every geometry value is in.
func mm(v float64) float64 { return v * 72 / 25.4 }

// spec is the geometry of one paper. The A6 values are the design; A5 is the
// same design scaled by the width ratio, so both papers look alike.
type spec struct {
	pageW, pageH float64
	side         float64 // side margin
	logoH        float64 // height of the logo box
	gap          float64 // vertical gap between blocks
	qrSide       float64 // side of the code INCLUDING the quiet zone
	titleSize    float64 // base title font size in points
	subSize      float64
	tagSize      float64
	addrSize     float64
	orgSize      float64 // the organization name when there is no logo
	lineH        float64 // line height as a multiple of the font size
}

func specFor(paper Paper) (spec, error) {
	var w, h float64
	switch paper {
	case PaperA6:
		w, h = 105, 148
	case PaperA5:
		w, h = 148, 210
	default:
		return spec{}, ErrBadPaper
	}
	k := w / 105 // 1 for A6, 1.41 for A5
	return spec{
		pageW: mm(w), pageH: mm(h),
		side:      mm(9 * k),
		logoH:     mm(16 * k),
		gap:       mm(4 * k),
		qrSide:    mm(62 * k),
		titleSize: 14 * k,
		subSize:   10.5 * k,
		tagSize:   11.5 * k,
		addrSize:  11 * k,
		orgSize:   12 * k,
		lineH:     1.25,
	}, nil
}

// Render draws the insert and returns the PDF bytes.
func Render(in Insert) ([]byte, error) {
	if strings.TrimSpace(in.Content) == "" {
		return nil, ErrEmptyContent
	}
	paper := in.Paper
	if paper == "" {
		paper = DefaultPaper
	}
	sp, err := specFor(paper)
	if err != nil {
		return nil, err
	}
	matrix, err := qr.Matrix(in.Content)
	if err != nil {
		return nil, err
	}
	fonts := pdf.DejaVuFonts()
	doc := newDoc(sp, fonts.Family, fonts, in)
	doc.AddPage()

	rtl := isRTLLocale(in.Locale)
	contentW := sp.pageW - 2*sp.side
	var blocks []block

	// 1. Logo or the organization name.
	if info, ok := decodeLogo(in.Logo); ok {
		w, h := fitBox(info.w, info.h, contentW, sp.logoH)
		doc.RegisterImageOptionsReader("logo", gofpdf.ImageOptions{ImageType: info.typ, ReadDpi: false}, bytes.NewReader(in.Logo))
		blocks = append(blocks, block{h: h, draw: func(y float64) {
			doc.ImageOptions("logo", (sp.pageW-w)/2, y, w, h, false, gofpdf.ImageOptions{ImageType: info.typ}, 0, "")
		}})
	} else if name := strings.TrimSpace(in.OrgName); name != "" {
		blocks = append(blocks, textBlock(doc, fonts.Family, "B", sp.orgSize, sp.lineH, contentW, name, 2, rtl, sp))
	}

	// 2. Title and subtitle.
	if title := strings.TrimSpace(in.Title); title != "" {
		blocks = append(blocks, shrinkingTextBlock(doc, fonts.Family, "B", sp.titleSize, sp.lineH, contentW, title, 3, rtl, sp))
	}
	if sub := strings.TrimSpace(in.Subtitle); sub != "" {
		blocks = append(blocks, textBlock(doc, fonts.Family, "", sp.subSize, sp.lineH, contentW, sub, 2, rtl, sp))
	}

	// 3. The code.
	blocks = append(blocks, block{h: sp.qrSide, draw: func(y float64) {
		drawQR(doc, matrix, (sp.pageW-sp.qrSide)/2, y, sp.qrSide)
	}})

	// 4. The tagline and the address.
	if tag := strings.TrimSpace(in.Tagline); tag != "" {
		blocks = append(blocks, textBlock(doc, fonts.Family, "B", sp.tagSize, sp.lineH, contentW, tag, 2, rtl, sp))
	}
	addr := strings.TrimSpace(in.Address)
	if addr == "" {
		addr = stripScheme(in.Content)
	}
	// The address is never reordered: it is read left to right in every language.
	blocks = append(blocks, textBlock(doc, fonts.Family, "", sp.addrSize, sp.lineH, contentW, addr, 2, false, sp))

	// Stack the blocks and centre the stack on the page.
	total := 0.0
	for i, b := range blocks {
		total += b.h
		if i > 0 {
			total += sp.gap
		}
	}
	y := (sp.pageH - total) / 2
	if y < sp.side {
		y = sp.side
	}
	for i, b := range blocks {
		if i > 0 {
			y += sp.gap
		}
		b.draw(y)
		y += b.h
	}

	var out bytes.Buffer
	if err := doc.Output(&out); err != nil {
		return nil, fmt.Errorf("pdfinsert: output: %w", err)
	}
	return out.Bytes(), nil
}

// newDoc builds the page-less document: fonts registered, every source of
// non-determinism pinned (see the package comment).
func newDoc(sp spec, family string, fonts pdf.Fonts, in Insert) *gofpdf.Fpdf {
	doc := gofpdf.NewCustom(&gofpdf.InitType{
		OrientationStr: "P",
		UnitStr:        "pt",
		Size:           gofpdf.SizeType{Wd: sp.pageW, Ht: sp.pageH},
	})
	doc.AddUTF8FontFromBytes(family, "", fonts.Regular)
	doc.AddUTF8FontFromBytes(family, "B", fonts.Bold)
	doc.SetMargins(sp.side, sp.side, sp.side)
	doc.SetCellMargin(0)
	doc.SetAutoPageBreak(false, 0)
	doc.SetCompression(false)
	doc.SetCatalogSort(true)
	pinned := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	doc.SetCreationDate(pinned)
	doc.SetModificationDate(pinned)
	doc.SetProducer("arena-ticketing", true)
	doc.SetCreator("arena-ticketing", true)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = strings.TrimSpace(in.OrgName)
	}
	if title == "" {
		title = stripScheme(in.Content)
	}
	doc.SetTitle(title, true)
	doc.SetTextColor(0, 0, 0)
	doc.SetFillColor(0, 0, 0)
	return doc
}

// block is one vertical slice of the page: its height, and how to draw it at
// a given top.
type block struct {
	h    float64
	draw func(y float64)
}

// textBlock wraps text into at most maxLines centred lines at one font size
// (the last line is cut with an ellipsis when the text is longer).
func textBlock(doc *gofpdf.Fpdf, family, style string, size, lineH, w float64, text string, maxLines int, rtl bool, sp spec) block {
	doc.SetFont(family, style, size)
	lines := clipLines(doc, wrap(doc, text, w), w, maxLines)
	return linesBlock(doc, family, style, size, lineH, lines, rtl, sp)
}

// shrinkingTextBlock is textBlock for the title: the font shrinks in whole
// points, down to 70 % of the base size, until the text fits maxLines, and
// only then is it cut.
func shrinkingTextBlock(doc *gofpdf.Fpdf, family, style string, base, lineH, w float64, text string, maxLines int, rtl bool, sp spec) block {
	size := base
	minSize := base * 0.7
	var lines []string
	for {
		doc.SetFont(family, style, size)
		lines = wrap(doc, text, w)
		if len(lines) <= maxLines || size-1 < minSize {
			break
		}
		size--
	}
	lines = clipLines(doc, lines, w, maxLines)
	return linesBlock(doc, family, style, size, lineH, lines, rtl, sp)
}

func linesBlock(doc *gofpdf.Fpdf, family, style string, size, lineH float64, lines []string, rtl bool, sp spec) block {
	h := size * lineH
	return block{h: h * float64(len(lines)), draw: func(y float64) {
		doc.SetFont(family, style, size)
		for i, ln := range lines {
			if rtl {
				ln = visualLine(ln)
			}
			doc.SetXY(sp.side, y+h*float64(i))
			doc.CellFormat(sp.pageW-2*sp.side, h, ln, "", 0, "C", false, 0, "")
		}
	}}
}

// wrap breaks text into lines no wider than w with the font currently set on
// doc: greedy by words, and a single word wider than the line is broken by
// runes. Line breaks typed into the text are honoured.
func wrap(doc *gofpdf.Fpdf, text string, w float64) []string {
	var lines []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			continue
		}
		cur := ""
		for _, word := range words {
			if doc.GetStringWidth(word) > w {
				// Flush what is pending, then split the long word by runes.
				if cur != "" {
					lines = append(lines, cur)
					cur = ""
				}
				piece := ""
				for _, r := range word {
					if piece != "" && doc.GetStringWidth(piece+string(r)) > w {
						lines = append(lines, piece)
						piece = ""
					}
					piece += string(r)
				}
				cur = piece
				continue
			}
			if cur == "" {
				cur = word
			} else if doc.GetStringWidth(cur+" "+word) <= w {
				cur += " " + word
			} else {
				lines = append(lines, cur)
				cur = word
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}

// clipLines keeps at most maxLines lines; a cut last line ends with an
// ellipsis that still fits the width.
func clipLines(doc *gofpdf.Fpdf, lines []string, w float64, maxLines int) []string {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	kept := append([]string(nil), lines[:maxLines]...)
	last := []rune(kept[maxLines-1])
	const ell = "…"
	for len(last) > 0 && doc.GetStringWidth(strings.TrimRight(string(last), " ")+ell) > w {
		last = last[:len(last)-1]
	}
	kept[maxLines-1] = strings.TrimRight(string(last), " ") + ell
	return kept
}

// drawQR fills the dark modules of matrix as black rectangles inside a square
// of side points at (x, y), leaving quietModules empty modules on every side.
// Adjacent dark modules of one row are one rectangle, so a rasterizer never
// has to butt two shapes against each other inside a run.
func drawQR(doc *gofpdf.Fpdf, matrix [][]bool, x, y, side float64) {
	n := len(matrix)
	if n == 0 {
		return
	}
	module := side / float64(n+2*quietModules)
	origin := quietModules * module
	doc.SetFillColor(0, 0, 0)
	for row, cells := range matrix {
		for col := 0; col < len(cells); {
			if !cells[col] {
				col++
				continue
			}
			start := col
			for col < len(cells) && cells[col] {
				col++
			}
			doc.Rect(x+origin+float64(start)*module, y+origin+float64(row)*module, float64(col-start)*module, module, "F")
		}
	}
}

// imageInfo is what the layout needs of a logo before placing it.
type imageInfo struct {
	typ  string // "PNG" or "JPEG", as gofpdf wants it
	w, h int
}

// decodeLogo accepts a decodable PNG or JPEG; anything else means "no logo".
func decodeLogo(b []byte) (imageInfo, bool) {
	if len(b) < 8 {
		return imageInfo{}, false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return imageInfo{}, false
	}
	switch format {
	case "png":
		return imageInfo{typ: "PNG", w: cfg.Width, h: cfg.Height}, true
	case "jpeg":
		return imageInfo{typ: "JPEG", w: cfg.Width, h: cfg.Height}, true
	}
	return imageInfo{}, false
}

// fitBox scales a w x h image into boxW x boxH keeping its aspect ratio.
func fitBox(imgW, imgH int, boxW, boxH float64) (w, h float64) {
	scale := boxW / float64(imgW)
	if s := boxH / float64(imgH); s < scale {
		scale = s
	}
	return float64(imgW) * scale, float64(imgH) * scale
}

// stripScheme turns https://go.example.com/x7k2qpm into go.example.com/x7k2qpm:
// the address a person types, as the bot's card prints it.
func stripScheme(u string) string {
	u = strings.TrimSpace(u)
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(u), p) {
			return u[len(p):]
		}
	}
	return u
}

// SessionLine is the second line of a date's insert: the start of the session
// on the venue's wall clock. An unknown zone falls back to UTC rather than to
// no line at all.
func SessionLine(start time.Time, timezone string) string {
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil || timezone == "" {
		loc = time.UTC
	}
	return start.In(loc).Format("02.01.2006, 15:04") // allow:timeformat: a printed wall-clock date for people, not a wire timestamp
}
