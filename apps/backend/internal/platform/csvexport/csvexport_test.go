package csvexport

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// parseLikeExcel reads a file back the way a spreadsheet does: BOM dropped,
// CRLF records, `;` fields, quotes unwrapped and un-doubled, then the two
// presentation rules undone — `="…"` evaluates to its text and a leading
// `'` is the text-marker a spreadsheet hides.
func parseLikeExcel(t *testing.T, data []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(data, []byte("\uFEFF")) {
		t.Fatalf("file does not start with a UTF-8 BOM: %q", data[:3])
	}
	body := strings.TrimPrefix(string(data), "\uFEFF")
	if !strings.HasSuffix(body, "\r\n") {
		t.Fatalf("file does not end with CRLF: %q", body[len(body)-4:])
	}
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
		t.Fatalf("a bare LF inside the file: %q", body)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		rows = append(rows, parseLine(t, line))
	}
	return rows
}

func parseLine(t *testing.T, line string) []string {
	t.Helper()
	var fields []string
	i := 0
	for {
		if i < len(line) && line[i] == '"' {
			// quoted field
			var sb strings.Builder
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
			fields = append(fields, sb.String())
		} else {
			end := strings.IndexByte(line[i:], Separator)
			if end < 0 {
				end = len(line) - i
			}
			raw := line[i : i+end]
			if strings.HasPrefix(raw, `="`) && strings.HasSuffix(raw, `"`) {
				raw = raw[2 : len(raw)-1]
			}
			fields = append(fields, raw)
			i += end
		}
		if i >= len(line) {
			return fields
		}
		if line[i] != Separator {
			t.Fatalf("expected %q at %d in %q", string(Separator), i, line)
		}
		i++
	}
}

func excelValue(field string) string {
	return strings.TrimPrefix(field, "'")
}

func TestWriter_RoundTripLikeExcel(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteHeader(Header("ru", ColOrder, ColBuyer, ColPhone, ColBarcode, ColPrice, ColDate)); err != nil {
		t.Fatal(err)
	}
	prague, _ := time.LoadLocation("Europe/Prague")
	when := time.Date(2026, 10, 9, 17, 30, 0, 0, time.UTC) // 19:30 in Prague
	rows := []struct {
		order   int64
		buyer   string
		phone   string
		barcode string
	}{
		{1000000500, `=HYPERLINK("http://evil.test")`, "+34600111222", "4600051000001"},
		{7, `Anna "Nova" O'Neil; Ltd`, "0612345678", "0123456789012"},
		{8, "-weird @name", "+34 600-111 (222)", "123456789"},
	}
	for _, r := range rows {
		if err := w.WriteRow(Number(r.order), Text(r.buyer), Text(r.phone), Text(r.barcode), Money(1890), DateTime(when, prague)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()

	// The raw shape: Excel-text formulas for the barcode and the phone, the
	// injection prefix on the formula-looking name, quotes doubled.
	for _, want := range []string{
		`="1000000500"`, `="4600051000001"`, `="0123456789012"`, `="+34600111222"`, `="0612345678"`,
		`"'=HYPERLINK(""http://evil.test"")"`, `"Anna ""Nova"" O'Neil; Ltd"`, `"'-weird @name"`,
		`;18.90;`, `"2026-10-09 19:30"`, `"Заказ";"Покупатель";"Телефон";"Штрихкод";"Цена";"Дата"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("file lacks %s:\n%s", want, raw)
		}
	}
	if !strings.Contains(raw, "\r\n7;") {
		t.Errorf("a short number is written bare so a column sums: %s", raw)
	}

	parsed := parseLikeExcel(t, buf.Bytes())
	if len(parsed) != 4 {
		t.Fatalf("rows: %d", len(parsed))
	}
	if got := parsed[0]; strings.Join(got, "|") != "Заказ|Покупатель|Телефон|Штрихкод|Цена|Дата" {
		t.Errorf("header: %q", got)
	}
	for i, r := range rows {
		got := parsed[i+1]
		if excelValue(got[1]) != r.buyer {
			t.Errorf("row %d buyer: %q", i, got[1])
		}
		if got[2] != r.phone {
			t.Errorf("row %d phone: %q", i, got[2])
		}
		if got[3] != r.barcode {
			t.Errorf("row %d barcode: %q", i, got[3])
		}
		if got[4] != "18.90" {
			t.Errorf("row %d price: %q", i, got[4])
		}
		if got[5] != "2026-10-09 19:30" {
			t.Errorf("row %d date: %q", i, got[5])
		}
	}
	if parsed[1][0] != "1000000500" || parsed[2][0] != "7" {
		t.Errorf("order numbers: %q %q", parsed[1][0], parsed[2][0])
	}
}

func TestIsExcelText(t *testing.T) {
	cases := map[string]bool{
		"4600051000001": true,  // barcode
		"1234567890":    true,  // exactly ExcelTextDigits
		"123456789":     false, // short number stays a number
		"0123":          true,  // leading zero
		"0":             false, // a lone zero is a number, not a padded one
		"00":            true,
		"+34600111222":  true,
		"+34 600 111":   true,
		"+":             false, // no digit: not a phone, gets the prefix
		"+HYPERLINK":    false,
		"12a":           false,
		"":              false,
		"-5":            false,
	}
	for v, want := range cases {
		if got := IsExcelText(v); got != want {
			t.Errorf("IsExcelText(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestEncode_Rules(t *testing.T) {
	cases := []struct {
		cell Cell
		want string
	}{
		{Text(""), `""`},
		{Cell{}, `""`},
		{Text("plain"), `"plain"`},
		{Text("=1+1"), `"'=1+1"`},
		{Text("+HYPERLINK(1)"), `"'+HYPERLINK(1)"`},
		{Text("-1"), `"'-1"`},
		{Text("@cmd"), `"'@cmd"`},
		{Text("\tx"), `"'` + "\tx" + `"`},
		{Text("\rx"), `"'` + "\rx" + `"`},
		{Text("  =late"), `"'  =late"`},
		{Text(`a"b`), `"a""b"`},
		{Text("multi\nline"), "\"multi\nline\""},
		{Money(0), "0.00"},
		{Money(5), "0.05"},
		{Money(-1234), "-12.34"},
		{Money(100000), "1000.00"},
		{YesNo(true), `"yes"`},
		{YesNo(false), `"no"`},
		{Number(42), `42`},
		{Number(0), `0`},
		{Number(-3), `-3`},
		{Number(1000000500), `="1000000500"`},
		{DateTime(time.Time{}, nil), `""`},
		{DateTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), nil), `"2026-01-02 03:04"`},
	}
	for _, c := range cases {
		if got := encode(c.cell); got != c.want {
			t.Errorf("encode(%+v) = %s, want %s", c.cell, got, c.want)
		}
	}
	s := "x"
	if got := encode(TextPtr(&s)); got != `"x"` {
		t.Errorf("TextPtr: %s", got)
	}
	if got := encode(TextPtr(nil)); got != `""` {
		t.Errorf("TextPtr(nil): %s", got)
	}
	n := int64(250)
	if got := encode(MoneyPtr(&n)); got != "2.50" {
		t.Errorf("MoneyPtr: %s", got)
	}
	if got := encode(MoneyPtr(nil)); got != `""` {
		t.Errorf("MoneyPtr(nil): %s", got)
	}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := encode(DateTimePtr(&when, nil)); got != `"2026-01-02 03:04"` {
		t.Errorf("DateTimePtr: %s", got)
	}
	if got := encode(DateTimePtr(nil, nil)); got != `""` {
		t.Errorf("DateTimePtr(nil): %s", got)
	}
}

func TestWriter_BOMOnceAndStreaming(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteRow(Text("a")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	first := buf.Len()
	if first == 0 {
		t.Fatal("Flush wrote nothing: the writer does not stream")
	}
	if err := w.WriteRow(Text("b")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "\uFEFF\"a\"\r\n\"b\"\r\n" {
		t.Errorf("file: %q", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errBoom }

var errBoom = &writeError{}

type writeError struct{}

func (*writeError) Error() string { return "boom" }

func TestWriter_StickyError(t *testing.T) {
	w := NewWriter(failingWriter{})
	big := strings.Repeat("x", 1<<16) // larger than the bufio buffer
	if err := w.WriteRow(Text(big)); err == nil {
		t.Fatal("expected an error from the underlying writer")
	}
	if err := w.WriteRow(Text("more")); err == nil {
		t.Fatal("a failed writer must keep failing")
	}
	if err := w.Flush(); err == nil {
		t.Fatal("Flush after a failure must report it")
	}
}

func TestHeader_Locales(t *testing.T) {
	if got := Header("ru", ColOrder, ColOrderStatus, ColDate, ColBuyer, ColEmail, ColPhone, ColCategory,
		ColPrice, ColCurrency, ColBarcode, ColTicketStatus, ColEntered, ColChannel, ColPromoCode); strings.Join(got, ";") !=
		"Заказ;Статус заказа;Дата;Покупатель;E-mail;Телефон;Категория;Цена;Валюта;Штрихкод;Статус билета;Вошёл;Канал;Промокод" {
		t.Errorf("ru header: %q", got)
	}
	if got := Label("es", ColBarcode); got != "Código de barras" {
		t.Errorf("es barcode: %q", got)
	}
	if got := Label("xx", ColBarcode); got != "Barcode" {
		t.Errorf("unknown locale falls back to English: %q", got)
	}
	// Every locale labels every column.
	for loc, m := range labels {
		for c := ColOrder; c <= ColDiscount; c++ {
			if m[c] == "" {
				t.Errorf("locale %s lacks a label for column %d", loc, c)
			}
		}
	}
}

func TestLocaleFromRequest(t *testing.T) {
	cases := []struct {
		url, accept, want string
	}{
		{"/x", "", "en"},
		{"/x?locale=ru", "en", "ru"},
		{"/x?lang=es", "", "es"},
		{"/x?locale=klingon", "ru-RU,ru;q=0.9", "ru"},
		{"/x", "es-ES,es;q=0.8,en;q=0.5", "es"},
		{"/x", "cs", "en"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.url, nil)
		if c.accept != "" {
			r.Header.Set("Accept-Language", c.accept)
		}
		if got := LocaleFromRequest(r); got != c.want {
			t.Errorf("%s / %q: got %s, want %s", c.url, c.accept, got, c.want)
		}
	}
}
