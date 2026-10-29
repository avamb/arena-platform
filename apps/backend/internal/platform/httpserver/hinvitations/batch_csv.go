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

// Limits of one CSV upload (spec 46, section 3.5). The whole file is ONE
// transaction that holds the session's row lock, so a file is small by
// design: a longer guest list is split into several files.
const (
	// MaxBatchPlaces is the most places one file may hand out.
	MaxBatchPlaces = 250
	// MaxBatchGuests is the most guests (orders and letters) one file may make.
	MaxBatchGuests = 100
	// MaxBatchFileBytes is the largest file the route reads. It stays well
	// under the router's global 1 MiB body limit, which also covers a
	// multipart form and answers http.payload_too_large above it.
	MaxBatchFileBytes = 512 << 10
	// maxBatchRows bounds the data rows before any place is counted, so a
	// file of a million blank-looking rows is refused early. Every row
	// names at least one place, so more rows than places can never pass.
	maxBatchRows = 1000
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

	// Excel writes a "sep=;" first line into a CSV it exports for a given
	// separator: it names the separator and is not a header.
	comma, lineOffset := detectDelimiter(data), 0
	if sep, rest, ok := excelSepLine(data); ok {
		comma, data, lineOffset = sep, rest, 1
	}
	// Quotes are strict: with lazy quotes an unclosed quote swallows every
	// row after it into one field, and the file would look shorter instead
	// of broken.
	rd := csv.NewReader(bytes.NewReader(data))
	rd.Comma = comma
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true

	header, err := rd.Read()
	if err != nil {
		return nil, csvReadErr(err, lineOffset)
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
	namesPlaces := hasKey || (hasRow && hasSeat) || (hasCat && hasQty)
	if !namesPlaces {
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
			return nil, csvReadErr(err, lineOffset)
		}
		line, _ := rd.FieldPos(0)
		line += lineOffset
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

func csvReadErr(err error, lineOffset int) *IssueError {
	if errors.Is(err, io.EOF) {
		return batchErr(http.StatusBadRequest, "invitation_batch.empty_file", "the file is empty", nil)
	}
	details := map[string]any{}
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		details["line"] = pe.StartLine + lineOffset
	}
	return batchErr(http.StatusBadRequest, "invitation_batch.invalid_csv",
		"the file is not a valid CSV: check the quotes around the line named in details", details)
}

// excelSepLine recognises Excel's "sep=<char>" first line and answers the
// separator and the file without that line.
func excelSepLine(data []byte) (sep rune, rest []byte, ok bool) {
	if !bytes.HasPrefix(data, []byte("sep=")) {
		return 0, data, false
	}
	line, after, found := bytes.Cut(data, []byte("\n"))
	if !found {
		return 0, data, false
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	val := string(line[len("sep="):])
	if len([]rune(val)) != 1 {
		return 0, data, false
	}
	return []rune(val)[0], after, true
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
