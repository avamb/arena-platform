package hexport

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/csvexport"
)

func sp(s string) *string { return &s }

// render writes a header and rows through csvexport (the platform's only CSV
// writer) and reads the file back the way a spreadsheet does: BOM dropped,
// CRLF records, `;` fields in double quotes.
func render(t *testing.T, locale string, cols []csvexport.Column, rows [][]csvexport.Cell) [][]string {
	t.Helper()
	var buf bytes.Buffer
	w := csvexport.NewWriter(&buf)
	if err := w.WriteHeader(csvexport.Header(locale, cols...)); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := w.WriteRow(r...); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(buf.String(), "\uFEFF")
	var out [][]string
	for _, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		out = append(out, splitLine(t, line))
	}
	return out
}

// splitLine splits one record on `;`, unwrapping double-quoted fields and the
// `="..."` text form.
func splitLine(t *testing.T, line string) []string {
	t.Helper()
	var fields []string
	for i := 0; ; {
		var sb strings.Builder
		switch {
		case strings.HasPrefix(line[i:], `="`):
			i += 2
			for i < len(line) && line[i] != '"' {
				sb.WriteByte(line[i])
				i++
			}
			i++
		case i < len(line) && line[i] == '"':
			i++
			for {
				if i >= len(line) {
					t.Fatalf("unterminated quote in %q", line)
				}
				if line[i] == '"' {
					if i+1 < len(line) && line[i+1] == '"' {
						sb.WriteByte('"')
						i += 2
						continue
					}
					i++
					break
				}
				sb.WriteByte(line[i])
				i++
			}
		default:
			for i < len(line) && line[i] != ';' {
				sb.WriteByte(line[i])
				i++
			}
		}
		fields = append(fields, sb.String())
		if i >= len(line) {
			return fields
		}
		i++ // the separator
		if i >= len(line) {
			return append(fields, "")
		}
	}
}

func baseRow() gen.ExportSalesRow {
	return gen.ExportSalesRow{
		SystemTicketID: 349,
		TicketStatus:   "active",
		IssuedAt:       time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC),
		SessionStart:   time.Date(2026, 11, 1, 19, 0, 0, 0, time.UTC),
		VenueTimezone:  sp("Europe/Madrid"),
		TierName:       sp("Parterre"),
	}
}

func column(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("no column %q in %v", name, header)
	return -1
}

func TestSalesCells_EntryColumns(t *testing.T) {
	entered := time.Date(2026, 11, 1, 18, 32, 0, 0, time.UTC) // 19:32 in Madrid
	macs := baseRow()
	macs.EnteredAt = &entered
	macs.EnteredSource = sp("macs")
	macs.EnteredScanner = sp("Scanner 7")
	macs.EnteredGate = sp("Gate 2")

	arena := baseRow()
	arena.SystemTicketID = 350
	arena.UsedAt = &entered
	arena.EnteredAt = &entered
	arena.EnteredSource = sp("arena_scanner")
	arena.EnteredScanner = sp("")
	arena.EnteredGate = sp("North")

	// A legacy scan: used_at only, no scan row behind it.
	legacy := baseRow()
	legacy.SystemTicketID = 351
	legacy.UsedAt = &entered

	never := baseRow()
	never.SystemTicketID = 352

	var rows [][]csvexport.Cell
	for _, r := range []gen.ExportSalesRow{macs, arena, legacy, never} {
		rows = append(rows, salesCells(r, false))
	}
	got := render(t, "en", salesColumns, rows)
	h := got[0]
	iEntered, iAt := column(t, h, "Entered"), column(t, h, "Entry time")
	iSrc, iScan := column(t, h, "Entry source"), column(t, h, "Scanner / gate")
	if iAt != iEntered+1 || iSrc != iEntered+2 || iScan != iEntered+3 {
		t.Fatalf("entry columns are not grouped after Entered: %v", h)
	}
	want := [][4]string{
		{"yes", "2026-11-01 19:32", "macs", "Scanner 7 / Gate 2"},
		{"yes", "2026-11-01 19:32", "arena_scanner", "North"},
		{"yes", "2026-11-01 19:32", "", ""},
		{"no", "", "", ""},
	}
	for i, w := range want {
		row := got[i+1]
		g := [4]string{row[iEntered], row[iAt], row[iSrc], row[iScan]}
		if g != w {
			t.Errorf("ticket %d entry cells = %v, want %v", i, g, w)
		}
	}
}

func TestSalesCells_EnteredByAdmittedScanAlone(t *testing.T) {
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	r := baseRow()
	r.EnteredAt = &at // used_at never stamped
	got := render(t, "ru", salesColumns, [][]csvexport.Cell{salesCells(r, false)})
	if v := got[1][column(t, got[0], "Вошёл")]; v != "yes" {
		t.Errorf("a ticket with an admitted scan but no used_at reads %q, want yes", v)
	}
	if got[0][column(t, got[0], "Время входа")] == "" {
		t.Errorf("ru header lost the entry time label: %v", got[0])
	}
}

func TestSalesCells_EventFileAppendsSessionLast(t *testing.T) {
	cols := append(append([]csvexport.Column{}, salesColumns...), csvexport.ColSession)
	got := render(t, "es", cols, [][]csvexport.Cell{salesCells(baseRow(), true)})
	if len(got[0]) != len(got[1]) || got[0][len(got[0])-1] != "Sesión" {
		t.Errorf("event file header/row = %v / %v", got[0], got[1])
	}
}

func TestScannerGate(t *testing.T) {
	cases := []struct {
		scanner, gate *string
		want          string
	}{
		{nil, nil, ""},
		{sp(""), sp("  "), ""},
		{sp("S1"), nil, "S1"},
		{nil, sp("Gate 2"), "Gate 2"},
		{sp(" S1 "), sp("Gate 2"), "S1 / Gate 2"},
	}
	for _, c := range cases {
		if got := scannerGate(c.scanner, c.gate); got != c.want {
			t.Errorf("scannerGate(%v, %v) = %q, want %q", c.scanner, c.gate, got, c.want)
		}
	}
}
