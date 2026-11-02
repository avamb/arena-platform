package disputes

// pdf.go — the evidence files. One PDF per Stripe evidence field, plus the
// full package for the organizer. gofpdf with the e-ticket's embedded DejaVu
// family, so Cyrillic and Latin diacritics print; output is deterministic
// (no compression, pinned dates) so a rebuild of the same facts hashes the
// same.

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/jung-kurt/gofpdf"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
)

const (
	pageW, pageH   = 595.28, 841.89 // A4, points
	marginX        = 42.0
	marginTop      = 44.0
	marginBottom   = 48.0
	contentW       = pageW - 2*marginX
	bodySize       = 9.0
	lineH          = 12.0
	pdfFamily      = "dejavu"
	pinnedYear     = 2026
	truncateMarker = "…"
)

// report is one PDF being written with a page cap.
type report struct {
	doc      *gofpdf.Fpdf
	maxPages int // 0 = no cap
	full     bool
	lbl      labels
	cut      bool
}

func newReport(title string, lbl labels, maxPages int, subtitle string) *report {
	doc := gofpdf.NewCustom(&gofpdf.InitType{UnitStr: "pt", Size: gofpdf.SizeType{Wd: pageW, Ht: pageH}})
	fonts := pdf.DejaVuFonts()
	doc.AddUTF8FontFromBytes(pdfFamily, "", fonts.Regular)
	doc.AddUTF8FontFromBytes(pdfFamily, "B", fonts.Bold)
	doc.SetCompression(false)
	doc.SetCatalogSort(true)
	pinned := time.Date(pinnedYear, 1, 1, 0, 0, 0, 0, time.UTC)
	doc.SetCreationDate(pinned)
	doc.SetModificationDate(pinned)
	doc.SetTitle(title, true)
	doc.SetAutoPageBreak(false, marginBottom)
	doc.SetMargins(marginX, marginTop, marginX)
	doc.SetCellMargin(0)
	r := &report{doc: doc, maxPages: maxPages, lbl: lbl}
	r.page()
	doc.SetFont(pdfFamily, "B", 14)
	doc.MultiCell(contentW, 18, title, "", "L", false)
	if subtitle != "" {
		doc.SetFont(pdfFamily, "", bodySize)
		doc.SetTextColor(90, 90, 90)
		doc.MultiCell(contentW, lineH, subtitle, "", "L", false)
		doc.SetTextColor(0, 0, 0)
	}
	doc.Ln(6)
	return r
}

func (r *report) page() {
	r.doc.AddPage()
	r.doc.SetY(marginTop)
}

// room makes sure h points fit; it opens a page when allowed and reports
// false when the cap is reached (the caller stops writing).
func (r *report) room(h float64) bool {
	if r.cut {
		return false
	}
	if r.doc.GetY()+h <= pageH-marginBottom {
		return true
	}
	if r.maxPages > 0 && r.doc.PageNo() >= r.maxPages {
		return false
	}
	r.page()
	return true
}

// truncated notes that the rest is only in the full package.
func (r *report) truncated(left int) {
	if r.cut {
		return
	}
	r.cut = true
	if r.doc.GetY()+lineH > pageH-marginBottom+lineH {
		return
	}
	r.doc.SetFont(pdfFamily, "", bodySize)
	r.doc.SetTextColor(120, 60, 0)
	msg := r.lbl.get("truncated")
	if left > 0 {
		msg = fmt.Sprintf(r.lbl.get("truncated_n"), left)
	}
	r.doc.MultiCell(contentW, lineH, msg, "", "L", false)
	r.doc.SetTextColor(0, 0, 0)
}

func (r *report) heading(text string) bool {
	if !r.room(lineH * 3) {
		r.truncated(0)
		return false
	}
	r.doc.Ln(4)
	r.doc.SetFont(pdfFamily, "B", 11)
	r.doc.MultiCell(contentW, 15, text, "", "L", false)
	r.doc.SetDrawColor(200, 200, 200)
	y := r.doc.GetY()
	r.doc.Line(marginX, y, pageW-marginX, y)
	r.doc.Ln(3)
	return true
}

func (r *report) para(text string) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	r.doc.SetFont(pdfFamily, "", bodySize)
	lines := r.doc.SplitText(text, contentW)
	for i, ln := range lines {
		if !r.room(lineH) {
			r.truncated(len(lines) - i)
			return false
		}
		r.doc.CellFormat(contentW, lineH, ln, "", 1, "L", false, 0, "")
	}
	return true
}

func (r *report) gap(text string) bool {
	r.doc.SetTextColor(150, 70, 0)
	ok := r.para(text)
	r.doc.SetTextColor(0, 0, 0)
	return ok
}

// kv writes label: value rows.
func (r *report) kv(rows [][2]string) bool {
	labelW := 150.0
	for i, row := range rows {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		r.doc.SetFont(pdfFamily, "", bodySize)
		lines := r.doc.SplitText(row[1], contentW-labelW)
		// A long label wraps inside its column instead of running into the
		// value (staging run 2026-11-02: "Earlier undisputed orders...").
		r.doc.SetFont(pdfFamily, "B", bodySize)
		labels := r.doc.SplitText(row[0], labelW-8)
		n := len(lines)
		if len(labels) > n {
			n = len(labels)
		}
		h := float64(n) * lineH
		if !r.room(h) {
			r.truncated(len(rows) - i)
			return false
		}
		y := r.doc.GetY()
		for j, ln := range labels {
			r.doc.SetXY(marginX, y+float64(j)*lineH)
			r.doc.CellFormat(labelW, lineH, ln, "", 0, "L", false, 0, "")
		}
		r.doc.SetFont(pdfFamily, "", bodySize)
		for j, ln := range lines {
			r.doc.SetXY(marginX+labelW, y+float64(j)*lineH)
			r.doc.CellFormat(contentW-labelW, lineH, ln, "", 0, "L", false, 0, "")
		}
		r.doc.SetXY(marginX, y+h)
	}
	return true
}

// table writes a table; widths are fractions of the content width.
func (r *report) table(headers []string, widths []float64, rows [][]string) bool {
	if len(rows) == 0 {
		return true
	}
	cols := make([]float64, len(widths))
	for i, w := range widths {
		cols[i] = w * contentW
	}
	drawHeader := func() {
		r.doc.SetFont(pdfFamily, "B", 8)
		r.doc.SetFillColor(238, 238, 238)
		x := marginX
		y := r.doc.GetY()
		for i, h := range headers {
			r.doc.SetXY(x, y)
			r.doc.CellFormat(cols[i], lineH, h, "", 0, "L", true, 0, "")
			x += cols[i]
		}
		r.doc.SetXY(marginX, y+lineH)
	}
	if !r.room(lineH * 2) {
		r.truncated(len(rows))
		return false
	}
	drawHeader()
	for i, row := range rows {
		r.doc.SetFont(pdfFamily, "", 8)
		cellLines := make([][]string, len(row))
		maxL := 1
		for c, v := range row {
			cellLines[c] = r.doc.SplitText(v, cols[c]-3)
			if len(cellLines[c]) == 0 {
				cellLines[c] = []string{""}
			}
			if len(cellLines[c]) > maxL {
				maxL = len(cellLines[c])
			}
		}
		h := float64(maxL) * 10.5
		pageBefore := r.doc.PageNo()
		if !r.room(h) {
			r.truncated(len(rows) - i)
			return false
		}
		if r.doc.PageNo() != pageBefore {
			drawHeader()
			r.doc.SetFont(pdfFamily, "", 8)
		}
		y := r.doc.GetY()
		x := marginX
		for c := range row {
			for j, ln := range cellLines[c] {
				r.doc.SetXY(x, y+float64(j)*10.5)
				r.doc.CellFormat(cols[c]-3, 10.5, ln, "", 0, "L", false, 0, "")
			}
			x += cols[c]
		}
		r.doc.SetXY(marginX, y+h)
	}
	return true
}

func (r *report) bytes() ([]byte, int, error) {
	// Footer with page numbers.
	n := r.doc.PageNo()
	for p := 1; p <= n; p++ {
		r.doc.SetPage(p)
		r.doc.SetFont(pdfFamily, "", 7)
		r.doc.SetTextColor(140, 140, 140)
		r.doc.SetXY(marginX, pageH-marginBottom+14)
		r.doc.CellFormat(contentW, 10, fmt.Sprintf("%s · %d / %d", r.lbl.get("footer"), p, n), "", 0, "R", false, 0, "")
	}
	var buf bytes.Buffer
	if err := r.doc.Output(&buf); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), n, nil
}
