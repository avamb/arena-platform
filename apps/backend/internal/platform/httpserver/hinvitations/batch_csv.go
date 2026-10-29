package hinvitations

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Limits of one CSV upload (spec 46, section 3.5).
const (
	// MaxBatchPlaces is the most places one file may hand out.
	MaxBatchPlaces = 1000
	// MaxBatchFileBytes is the largest file the route reads.
	MaxBatchFileBytes = 2 << 20
	// maxBatchRows bounds the data rows before any place is counted, so a
	// file of a million blank-looking rows is refused early.
	maxBatchRows = 5000
)

// csvRow is one data row of the upload. Line is the 1-based line of the file
// the row starts on (the header is line 1), which is the number a person
// finds in a spreadsheet.
type csvRow struct {
	Line     int
	Guest    string
	Name     string
	Email    string
	Phone    string
	Locale   string
	Note     string
	SeatKey  string
	Sector   string
	Row      string
	Seat     string
	Category string
	Qty      string
}

// parsedCSV is a parsed upload.
type parsedCSV struct {
	Rows []csvRow
}

// headerAliases folds the spellings people type onto the canonical column
// names (all compared lower-case with spaces and dashes as underscores).
var headerAliases = map[string]string{
	"e_mail":       "email",
	"mail":         "email",
	"seatkey":      "seat_key",
	"seat_number":  "seat",
	"place":        "seat",
	"quantity":     "qty",
	"count":        "qty",
	"guest_number": "guest",
	"guest_no":     "guest",
	"language":     "locale",
	"lang":         "locale",
	"full_name":    "name",
	"guest_name":   "name",
	"comment":      "note",
}

var knownColumns = map[string]bool{
	"guest": true, "name": true, "email": true, "phone": true, "locale": true,
	"note": true, "seat_key": true, "sector": true, "row": true, "seat": true,
	"category": true, "qty": true,
}

func batchErr(status int, code, msg string, details map[string]any) *IssueError {
	return refuse(status, code, msg, details)
}

// ParseCSV reads an upload: UTF-8 with or without a BOM, `,` or `;` (a tab
// too) as the separator, the header row mandatory and in any column order.
// A file-level problem is an *IssueError; problems of single rows are found
// later, against the session's plan.
func ParseCSV(data []byte) (*parsedCSV, *IssueError) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		return nil, batchErr(http.StatusBadRequest, "invitation_batch.invalid_encoding",
			"the file is not UTF-8, save it as CSV UTF-8", nil)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, batchErr(http.StatusBadRequest, "invitation_batch.empty_file", "the file is empty", nil)
	}

	rd := csv.NewReader(bytes.NewReader(data))
	rd.Comma = detectDelimiter(data)
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	rd.TrimLeadingSpace = true

	header, err := rd.Read()
	if err != nil {
		return nil, csvReadErr(err)
	}
	cols := map[string]int{}
	for i, h := range header {
		name := normHeader(h)
		if name == "" || !knownColumns[name] {
			continue // unknown columns are ignored, so a spreadsheet may carry its own
		}
		if _, dup := cols[name]; dup {
			return nil, batchErr(http.StatusUnprocessableEntity, "invitation_batch.invalid_header",
				"a column is named twice", map[string]any{"column": name})
		}
		cols[name] = i
	}
	if _, ok := cols["name"]; !ok {
		return nil, batchErr(http.StatusUnprocessableEntity, "invitation_batch.invalid_header",
			"the header has no name column", map[string]any{"missing": []string{"name"}})
	}
	_, hasKey := cols["seat_key"]
	_, hasRow := cols["row"]
	_, hasSeat := cols["seat"]
	_, hasCat := cols["category"]
	_, hasQty := cols["qty"]
	if !hasKey && !(hasRow && hasSeat) && !(hasCat && hasQty) {
		return nil, batchErr(http.StatusUnprocessableEntity, "invitation_batch.invalid_header",
			"the header names no places: use seat_key, or sector,row,seat, or category,qty",
			map[string]any{"missing": []string{"seat_key | row,seat | category,qty"}})
	}

	out := &parsedCSV{}
	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, csvReadErr(err)
		}
		line, _ := rd.FieldPos(0)
		get := func(col string) string {
			i, ok := cols[col]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		row := csvRow{
			Line: line, Guest: get("guest"), Name: get("name"), Email: get("email"),
			Phone: get("phone"), Locale: get("locale"), Note: get("note"),
			SeatKey: get("seat_key"), Sector: get("sector"), Row: get("row"), Seat: get("seat"),
			Category: get("category"), Qty: get("qty"),
		}
		if row == (csvRow{Line: line}) {
			continue // a blank row
		}
		out.Rows = append(out.Rows, row)
		if len(out.Rows) > maxBatchRows {
			return nil, batchErr(http.StatusUnprocessableEntity, "invitation_batch.too_many_rows",
				"the file has too many rows", map[string]any{"max_rows": maxBatchRows})
		}
	}
	if len(out.Rows) == 0 {
		return nil, batchErr(http.StatusUnprocessableEntity, "invitation_batch.empty_file", "the file has a header but no rows", nil)
	}
	return out, nil
}

func csvReadErr(err error) *IssueError {
	if errors.Is(err, io.EOF) {
		return batchErr(http.StatusBadRequest, "invitation_batch.empty_file", "the file is empty", nil)
	}
	details := map[string]any{}
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		details["line"] = pe.StartLine
	}
	return batchErr(http.StatusBadRequest, "invitation_batch.invalid_csv", "the file is not a valid CSV", details)
}

func normHeader(h string) string {
	n := strings.ToLower(strings.TrimSpace(h))
	n = strings.NewReplacer(" ", "_", "-", "_").Replace(n)
	if a, ok := headerAliases[n]; ok {
		return a
	}
	return n
}

// detectDelimiter picks the separator of the header line: the one of `;`,
// `,` and a tab that occurs most outside quotes (a tie keeps the comma).
func detectDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	var comma, semi, tab int
	inQuotes := false
	for _, b := range line {
		switch {
		case b == '"':
			inQuotes = !inQuotes
		case inQuotes:
		case b == ',':
			comma++
		case b == ';':
			semi++
		case b == '\t':
			tab++
		}
	}
	switch {
	case semi > comma && semi >= tab:
		return ';'
	case tab > comma && tab > semi:
		return '\t'
	default:
		return ','
	}
}
