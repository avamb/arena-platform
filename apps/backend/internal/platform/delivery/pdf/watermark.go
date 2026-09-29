package pdf

// watermark.go — the diagonal "SAMPLE" stamp of a preview ticket.
//
// An organizer who just published an event through the event center gets a
// PDF of "their" ticket to check the name, the date, the venue and the price
// before the first sale. It must be the real layout — the same renderer, the
// same fonts, a real EAN-13 the gate recognises as a sample — and it must be
// impossible to mistake for a sold ticket, which is what this stamp is for.
//
// The stamp covers only the FLOWING block above the anchored code block
// (see the package doc in layout.go): the QR, the barcode and its quiet
// zones stay untouched, because a half-transparent glyph across a bar is a
// scan failure, and a sample that does not scan proves nothing.

import (
	"math"
	"strings"

	"github.com/jung-kurt/gofpdf"
)

const (
	// watermarkAngle is the counter-clockwise tilt in degrees; a diagonal
	// reads as a stamp, not as a headline.
	watermarkAngle = 30.0
	// watermarkAlpha keeps the text under it legible: the stamp says what
	// the page is, it must not hide what is on it.
	watermarkAlpha = 0.16
	// watermarkStartFS is the size the text is measured at before it is
	// scaled to fit the block; watermarkMinFS is the floor below which the
	// stamp is skipped rather than printed unreadably small.
	watermarkStartFS = 60.0
	watermarkMinFS   = 18.0
	// watermarkFill is how much of the block's width and height the rotated
	// text may span.
	watermarkFill = 0.86
)

// drawWatermark stamps t.Watermark across the flowing block. It resets every
// graphics state it touches (alpha, transform, font, colour) so the code
// block drawn afterwards is byte-for-byte what it is on a real ticket.
func drawWatermark(doc *gofpdf.Fpdf, t Ticket, spec layoutSpec, accent rgb) {
	text := strings.ToUpper(strings.TrimSpace(t.Watermark))
	if text == "" {
		return
	}
	blockW := spec.pageW
	blockH := spec.bottomTop
	if blockH <= 0 || blockW <= 0 {
		return
	}
	rad := watermarkAngle * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)

	doc.SetFont(fontFamily, "B", watermarkStartFS)
	w := doc.GetStringWidth(text)
	if w <= 0 {
		return
	}
	// The rotated run spans w·cos horizontally and w·sin vertically; scale
	// the font so both fit inside the block with a margin.
	scale := math.Min(blockW*watermarkFill/(w*cos), blockH*watermarkFill/(w*sin))
	fs := watermarkStartFS * scale
	if fs < watermarkMinFS {
		return
	}
	doc.SetFont(fontFamily, "B", fs)
	w = doc.GetStringWidth(text)

	cx, cy := blockW/2, blockH/2
	setText(doc, accent)
	doc.SetAlpha(watermarkAlpha, "Normal")
	doc.TransformBegin()
	doc.TransformRotate(watermarkAngle, cx, cy)
	// Text() places the BASELINE at y; lift it by roughly a third of the
	// font size so the glyphs' visual centre sits on the block's centre.
	doc.Text(cx-w/2, cy+fs*0.35, text)
	doc.TransformEnd()
	doc.SetAlpha(1, "Normal")
	setText(doc, colorBody)
}
