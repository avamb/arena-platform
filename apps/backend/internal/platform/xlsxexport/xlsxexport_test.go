package xlsxexport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/xlsxexport/xlsxtest"
)

func sample() Sheet {
	return Sheet{
		Name: "Guests: list/1",
		Columns: []Column{
			{Header: "Guest"}, {Header: "Barcode", Text: true}, {Header: "Order", Text: true},
			{Header: "Phone", Text: true}, {Header: "Amount"}, {Header: "Entered at"},
		},
		Rows: [][]csvexport.Cell{
			{csvexport.Text("=HYPERLINK(\"x\")"), csvexport.Text("4600051000001"), csvexport.Number(42),
				csvexport.Text("+34600111222"), csvexport.Money(1890), csvexport.Text("2026-11-20 19:31")},
			{csvexport.Text("Анна <b>&\"\x01"), csvexport.Text("0012"), {}, csvexport.Text("0612345678"), {}, {}},
		},
	}
}

func TestBuild_TextColumnsAreStringsInTheTextFormat(t *testing.T) {
	body, err := Build(sample())
	if err != nil {
		t.Fatal(err)
	}
	p := xlsxtest.Open(t, body)
	for _, ref := range []string{"B2", "C2", "D2", "B3", "D3"} {
		c, ok := p.CellAt(ref)
		if !ok {
			t.Fatalf("%s is empty", ref)
		}
		if c.Type != "inlineStr" && c.Type != "s" {
			t.Errorf("%s type = %q, want a string", ref, c.Type)
		}
		if !p.TextFormat(c.Style) {
			t.Errorf("%s style %d is not the text format", ref, c.Style)
		}
	}
	if c, _ := p.CellAt("B2"); c.Value != "4600051000001" {
		t.Errorf("barcode = %q", c.Value)
	}
	if c, _ := p.CellAt("B3"); c.Value != "0012" {
		t.Errorf("leading zero lost: %q", c.Value)
	}
	if c, _ := p.CellAt("C2"); c.Value != "42" {
		t.Errorf("order = %q", c.Value)
	}
	// The columns themselves carry the text format, so a new row stays text.
	for col := 2; col <= 4; col++ {
		if st := p.ColumnStyle(col); !p.TextFormat(st) {
			t.Errorf("column %d style = %d, want the text format", col, st)
		}
	}
	if st := p.ColumnStyle(1); st != -1 {
		t.Errorf("guest column has a style %d", st)
	}
	if !strings.Contains(string(p.Parts["xl/worksheets/sheet1.xml"]), `numberStoredAsText="1"`) {
		t.Error("text columns are not excluded from the number-as-text warning")
	}
}

func TestBuild_NumbersStayNumbersAndTextIsLiteral(t *testing.T) {
	p := xlsxtest.Open(t, mustBuild(t, sample()))
	if c, _ := p.CellAt("E2"); c.Type != "" || c.Value != "18.90" {
		t.Errorf("money = %+v, want a number", c)
	}
	// An inline string is never evaluated: no CSV injection prefix needed.
	if c, _ := p.CellAt("A2"); c.Type != "inlineStr" || c.Value != `=HYPERLINK("x")` {
		t.Errorf("A2 = %+v", c)
	}
	// Entry time is readable text.
	if c, _ := p.CellAt("F2"); c.Type != "inlineStr" || c.Value != "2026-11-20 19:31" {
		t.Errorf("F2 = %+v", c)
	}
	// XML specials are escaped, a control character is dropped.
	if c, _ := p.CellAt("A3"); c.Value != `Анна <b>&"` {
		t.Errorf("A3 = %q", c.Value)
	}
	if _, ok := p.CellAt("C3"); ok {
		t.Error("an empty cell was written")
	}
}

func TestBuild_HeaderBoldFrozenFilteredAndSized(t *testing.T) {
	p := xlsxtest.Open(t, mustBuild(t, sample()))
	for i, want := range []string{"Guest", "Barcode", "Order", "Phone", "Amount", "Entered at"} {
		c, _ := p.CellAt(colName(i) + "1")
		if c.Value != want || !p.Bold(c.Style) {
			t.Errorf("header %d = %+v (bold %v)", i, c, p.Bold(c.Style))
		}
	}
	views := p.Sheet.Views.View
	if len(views) != 1 || views[0].Pane == nil || views[0].Pane.YSplit != "1" || views[0].Pane.State != "frozen" || views[0].Pane.TopLeftCell != "A2" {
		t.Errorf("header row is not frozen: %+v", views)
	}
	if p.Sheet.AutoFilter == nil || p.Sheet.AutoFilter.Ref != "A1:F3" {
		t.Errorf("autofilter = %+v", p.Sheet.AutoFilter)
	}
	if !strings.Contains(string(p.Parts["xl/workbook.xml"]), "_xlnm._FilterDatabase") {
		t.Error("the filter range is not named in the workbook")
	}
	if len(p.Sheet.Cols.Col) != 6 {
		t.Fatalf("cols = %d", len(p.Sheet.Cols.Col))
	}
	// The barcode column fits 13 digits; every width is within the limits.
	if w := p.Sheet.Cols.Col[1].Width; w < 14 {
		t.Errorf("barcode width = %v", w)
	}
	for _, c := range p.Sheet.Cols.Col {
		if c.Width < minWidth || c.Width > maxWidth {
			t.Errorf("width %v out of range", c.Width)
		}
	}
	if p.SheetName != "Guests list1" {
		t.Errorf("sheet name = %q", p.SheetName)
	}
}

func TestBuild_Deterministic(t *testing.T) {
	a, b := mustBuild(t, sample()), mustBuild(t, sample())
	if !bytes.Equal(a, b) {
		t.Error("two builds of the same sheet differ")
	}
}

func TestBuild_HeaderOnlyAndLongValues(t *testing.T) {
	long := strings.Repeat("я", maxCellUnits+10)
	p := xlsxtest.Open(t, mustBuild(t, Sheet{Columns: []Column{{Header: "A"}}, Rows: [][]csvexport.Cell{{csvexport.Text(long)}}}))
	c, _ := p.CellAt("A2")
	if n := len([]rune(c.Value)); n != maxCellUnits {
		t.Errorf("long value has %d characters", n)
	}
	if p.Sheet.Cols.Col[0].Width != maxWidth {
		t.Errorf("width = %v", p.Sheet.Cols.Col[0].Width)
	}
	if p.SheetName != "Sheet1" {
		t.Errorf("default sheet name = %q", p.SheetName)
	}
	p = xlsxtest.Open(t, mustBuild(t, Sheet{Name: "x", Columns: []Column{{Header: "A", Text: true}}}))
	if p.Sheet.AutoFilter == nil || p.Sheet.AutoFilter.Ref != "A1:A1" {
		t.Errorf("empty sheet filter = %+v", p.Sheet.AutoFilter)
	}
}

func TestColName(t *testing.T) {
	for i, want := range map[int]string{0: "A", 11: "L", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA"} {
		if got := colName(i); got != want {
			t.Errorf("colName(%d) = %q, want %q", i, got, want)
		}
	}
}

func mustBuild(t *testing.T, s Sheet) []byte {
	t.Helper()
	b, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
