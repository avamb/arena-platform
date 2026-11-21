// Package xlsxtest reads back a workbook written by xlsxexport, for tests:
// the cells with their type and style, the column styles, the frozen pane
// and the autofilter. It parses the XML parts itself, so a test sees what
// a spreadsheet would read, not what the writer meant to write.
package xlsxtest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"testing"
)

// Parsed is a workbook read back from its bytes, the way a test looks at it.
type Parsed struct {
	SheetName string
	Sheet     Worksheet
	Styles    StyleSheet
	Parts     map[string][]byte
}

// Worksheet is the part of sheet1.xml the tests check.
type Worksheet struct {
	Views struct {
		View []struct {
			Pane *struct {
				YSplit      string `xml:"ySplit,attr"`
				TopLeftCell string `xml:"topLeftCell,attr"`
				State       string `xml:"state,attr"`
			} `xml:"pane"`
		} `xml:"sheetView"`
	} `xml:"sheetViews"`
	Cols struct {
		Col []struct {
			Min   int     `xml:"min,attr"`
			Max   int     `xml:"max,attr"`
			Width float64 `xml:"width,attr"`
			Style *int    `xml:"style,attr"`
		} `xml:"col"`
	} `xml:"cols"`
	Data struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				R  string  `xml:"r,attr"`
				S  int     `xml:"s,attr"`
				T  string  `xml:"t,attr"`
				V  *string `xml:"v"`
				Is *struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
	AutoFilter *struct {
		Ref string `xml:"ref,attr"`
	} `xml:"autoFilter"`
}

// StyleSheet is the part of styles.xml the tests check.
type StyleSheet struct {
	Fonts struct {
		Font []struct {
			B *struct{} `xml:"b"`
		} `xml:"font"`
	} `xml:"fonts"`
	CellXfs struct {
		Xf []struct {
			NumFmtID int `xml:"numFmtId,attr"`
			FontID   int `xml:"fontId,attr"`
		} `xml:"xf"`
	} `xml:"cellXfs"`
}

// Open reads a one-sheet workbook built by this package.
func Open(t testing.TB, body []byte) Parsed {
	t.Helper()
	sheets := OpenBook(t, body)
	if len(sheets) != 1 {
		t.Fatalf("sheets = %d", len(sheets))
	}
	return sheets[0]
}

// OpenBook reads a workbook of any number of sheets, one Parsed per sheet in
// tab order (SheetName and Sheet are that sheet's; Styles and Parts are the
// workbook's).
func OpenBook(t testing.TB, body []byte) []Parsed {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	p := Parsed{Parts: map[string][]byte{}}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		p.Parts[f.Name] = b
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml"} {
		b, ok := p.Parts[name]
		if !ok {
			t.Fatalf("part %s is missing", name)
		}
		// Every part must be well-formed XML.
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("part %s is not well-formed: %v", name, err)
			}
		}
	}
	if err := xml.Unmarshal(p.Parts["xl/styles.xml"], &p.Styles); err != nil {
		t.Fatal(err)
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(p.Parts["xl/workbook.xml"], &wb); err != nil {
		t.Fatal(err)
	}
	var out []Parsed
	for i, sh := range wb.Sheets {
		q := p
		q.SheetName = sh.Name
		part := "xl/worksheets/sheet" + strconv.Itoa(i+1) + ".xml"
		b, ok := p.Parts[part]
		if !ok {
			t.Fatalf("part %s is missing", part)
		}
		if err := xml.Unmarshal(b, &q.Sheet); err != nil {
			t.Fatal(err)
		}
		out = append(out, q)
	}
	return out
}

// Cell is one cell read back: its type, style and text.
type Cell struct {
	Type, Value string
	Style       int
}

// CellAt finds a cell by reference ("E2"); ok is false for an empty cell.
func (p Parsed) CellAt(ref string) (Cell, bool) {
	for _, row := range p.Sheet.Data.Rows {
		for _, c := range row.Cells {
			if c.R != ref {
				continue
			}
			out := Cell{Type: c.T, Style: c.S}
			switch {
			case c.Is != nil:
				out.Value = c.Is.T
			case c.V != nil:
				out.Value = *c.V
			}
			return out, true
		}
	}
	return Cell{}, false
}

// TextFormat reports whether a style index is the "@" text format.
func (p Parsed) TextFormat(style int) bool {
	xfs := p.Styles.CellXfs.Xf
	return style >= 0 && style < len(xfs) && xfs[style].NumFmtID == 49
}

// Bold reports whether a style index uses a bold font.
func (p Parsed) Bold(style int) bool {
	xfs := p.Styles.CellXfs.Xf
	if style < 0 || style >= len(xfs) {
		return false
	}
	f := xfs[style].FontID
	return f < len(p.Styles.Fonts.Font) && p.Styles.Fonts.Font[f].B != nil
}

// ColumnStyle is the style of a 1-based column, -1 when it has none.
func (p Parsed) ColumnStyle(col int) int {
	for _, c := range p.Sheet.Cols.Col {
		if c.Min <= col && col <= c.Max && c.Style != nil {
			return *c.Style
		}
	}
	return -1
}

// Values is the text of every row, the header first; an empty cell is "".
func (p Parsed) Values() [][]string {
	var out [][]string
	for _, row := range p.Sheet.Data.Rows {
		var vals []string
		for _, c := range row.Cells {
			col := colIndex(c.R)
			for len(vals) < col {
				vals = append(vals, "")
			}
			v := ""
			switch {
			case c.Is != nil:
				v = c.Is.T
			case c.V != nil:
				v = *c.V
			}
			vals = append(vals, v)
		}
		out = append(out, vals)
	}
	return out
}

// colIndex is the zero-based column of a cell reference ("C7" -> 2).
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}
