package pdf

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
)

// makePNG returns a minimal WxH solid-colour PNG for use as a fake logo or
// poster in tests.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func money(v int64) *int64 { return &v }

// validTicket is the shared fixture: a paid, seated ticket for an organizer
// that has a logo and a poster. Individual tests strip whatever they are
// about (the poster, the logo, the price, the seat) — the live clients run
// with most of it absent, which is exactly what those tests cover.
func validTicket(t *testing.T) Ticket {
	t.Helper()
	return Ticket{
		TicketID:     "11111111-2222-3333-4444-555555555555",
		TicketNumber: "1042",
		OrderNumber:  "9096",
		EventName:    "Spring Symphony Gala",
		SessionStart: time.Date(2026, 5, 12, 18, 30, 0, 0, time.UTC),
		SessionTZ:    "Europe/Moscow",
		VenueName:    "Tchaikovsky Hall",
		VenueAddress: "Triumfalnaya Square 4/31",
		VenueCity:    "Moscow",
		TierName:     "Stalls",
		HolderName:   "Ivan Petrov",
		PriceMinor:   money(60000),
		Currency:     "CZK",
		EAN13:        "2100000000302",
		OrgName:      "Lampyris",
		// Deliberately different pixel WIDTHS. gofpdf orders image objects by
		// width and breaks a tie with Go's randomized map iteration, so two
		// same-width images make the output non-byte-deterministic — see
		// RenderFormat's doc comment. Real logos and posters rarely collide,
		// and the fixture must not make the determinism tests flaky.
		OrgLogo:     makePNG(t, 320, 100),
		PosterImage: makePNG(t, 300, 314),
	}
}

func TestRender_ReturnsPDFBytes(t *testing.T) {
	out, err := Render(context.Background(), validTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) < 200 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(out))
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF (missing %%PDF- header): %q", out[:8])
	}
	if !bytes.Contains(out, []byte("%%EOF")) {
		t.Fatalf("PDF missing %%EOF trailer")
	}
	if got := bytes.Count(out, []byte("/Type /Page\n")); got > 1 {
		t.Errorf("expected a single-page PDF, found %d /Type /Page markers", got)
	}
}

// TestRender_PageIsHalfA4Lengthwise pins the page size. 105x297 mm is the
// whole reason the design works: two consecutive tickets' codes stay a full
// page apart (so an entrance scanner cannot grab the neighbouring one) and
// the sheet still prints on A4 at exactly 100%. A5 would break both.
func TestRender_PageIsHalfA4Lengthwise(t *testing.T) {
	out, err := Render(context.Background(), validTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	w, h := mediaBox(t, out)
	wantW, wantH := mm(105), mm(297)
	if diff := w - wantW; diff > 0.5 || diff < -0.5 {
		t.Errorf("page width = %.2fpt, want %.2fpt (105 mm)", w, wantW)
	}
	if diff := h - wantH; diff > 0.5 || diff < -0.5 {
		t.Errorf("page height = %.2fpt, want %.2fpt (297 mm)", h, wantH)
	}
	// A4's height, to the point — "prints at exactly 100%" depends on it.
	if diff := h - 841.89; diff > 0.5 || diff < -0.5 {
		t.Errorf("page height %.2fpt is not A4's 841.89pt", h)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	tk := validTicket(t)
	a, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render a: %v", err)
	}
	b, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Render is not deterministic: produced %d vs %d bytes that differ",
			len(a), len(b))
	}
}

func TestRender_RejectsMissingRequired(t *testing.T) {
	cases := map[string]func(*Ticket){
		"ticket_id":     func(t *Ticket) { t.TicketID = "" },
		"event_name":    func(t *Ticket) { t.EventName = "" },
		"session_start": func(t *Ticket) { t.SessionStart = time.Time{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			tk := validTicket(t)
			mut(&tk)
			_, err := Render(context.Background(), tk)
			if err == nil {
				t.Fatalf("expected ErrInvalidTicket when %s is missing", name)
			}
			if !errors.Is(err, ErrInvalidTicket) {
				t.Fatalf("expected ErrInvalidTicket, got %v", err)
			}
		})
	}
}

func TestRender_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Render(ctx, validTicket(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestRenderFormat_BothFormatsAreTheSamePage documents the deliberate
// collapse of the old two-layout API: the ported page already prints on A4
// at 100%, so "a4" is an alias rather than a second geometry. An unknown
// value is still rejected.
func TestRenderFormat_BothFormatsAreTheSamePage(t *testing.T) {
	tk := validTicket(t)
	mobile, err := RenderFormat(context.Background(), tk, FormatMobile)
	if err != nil {
		t.Fatalf("mobile: %v", err)
	}
	a4, err := RenderFormat(context.Background(), tk, FormatA4Print)
	if err != nil {
		t.Fatalf("a4: %v", err)
	}
	if !bytes.Equal(mobile, a4) {
		t.Error("the two formats should render the identical page")
	}
	if _, err := RenderFormat(context.Background(), tk, Format("letter")); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("expected ErrUnknownFormat, got %v", err)
	}
}

// ── Pure helpers ────────────────────────────────────────────────────────

func TestBuildLegalLines_AssemblesAddressBlock(t *testing.T) {
	cases := []struct {
		name string
		in   Ticket
		want []string
	}{
		{name: "empty input", in: Ticket{}, want: []string{}},
		{name: "legal name only", in: Ticket{LegalName: "X Ltd"}, want: []string{"X Ltd"}},
		{
			name: "postal + city combined",
			in: Ticket{
				LegalName:              "X Ltd",
				LegalAddressPostalCode: "SE1 9DT",
				LegalAddressCity:       "London",
			},
			want: []string{"X Ltd", "SE1 9DT London"},
		},
		{name: "postal only", in: Ticket{LegalAddressPostalCode: "10115"}, want: []string{"10115"}},
		{name: "city only", in: Ticket{LegalAddressCity: "Berlin"}, want: []string{"Berlin"}},
		{
			name: "all fields",
			in: Ticket{
				LegalName:              "X Ltd",
				LegalAddressLine1:      "1 Road",
				LegalAddressLine2:      "Floor 2",
				LegalAddressPostalCode: "10115",
				LegalAddressCity:       "Berlin",
				LegalAddressCountry:    "DE",
				ContactEmail:           "hi@x.example",
			},
			want: []string{
				"X Ltd", "1 Road", "Floor 2", "10115 Berlin", "DE",
				"Contact: hi@x.example",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildLegalLines(tc.in, "Contact")
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestJoinNonEmpty(t *testing.T) {
	if got := joinNonEmpty(", ", "A", "", "B"); got != "A, B" {
		t.Fatalf("got %q", got)
	}
	if got := joinNonEmpty(", ", "", "  ", ""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := joinNonEmpty(", ", "only"); got != "only" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeImageInfo(t *testing.T) {
	t.Run("png", func(t *testing.T) {
		got, ok := decodeImageInfo(makePNG(t, 40, 10))
		if !ok || got.typ != "PNG" || got.w != 40 || got.h != 10 {
			t.Fatalf("got (%+v, %v)", got, ok)
		}
	})
	t.Run("jpeg", func(t *testing.T) {
		got, ok := decodeImageInfo(makeJPEG(t, 16, 16))
		if !ok || got.typ != "JPEG" {
			t.Fatalf("got (%+v, %v)", got, ok)
		}
	})
	t.Run("junk rejected", func(t *testing.T) {
		if _, ok := decodeImageInfo([]byte("definitely not an image")); ok {
			t.Fatal("expected not-ok for junk bytes")
		}
	})
	t.Run("too short rejected", func(t *testing.T) {
		if _, ok := decodeImageInfo([]byte{0x01, 0x02}); ok {
			t.Fatal("expected not-ok for tiny input")
		}
	})
}

func TestFitBox_PreservesAspectAndNeverOverflows(t *testing.T) {
	cases := []struct {
		name                 string
		imgW, imgH           int
		boxW, boxH           float64
		wantW, wantH         float64
		aspectMustBePreserve bool
	}{
		{name: "wide plate fits by width", imgW: 320, imgH: 100, boxW: 160, boxH: 60, wantW: 160, wantH: 50},
		{name: "tall logo fits by height", imgW: 100, imgH: 400, boxW: 160, boxH: 60, wantW: 15, wantH: 60},
		{name: "exact ratio", imgW: 320, imgH: 335, boxW: 130, boxH: 136.09, wantW: 130, wantH: 136.09},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := fitBox(tc.imgW, tc.imgH, tc.boxW, tc.boxH)
			if w > tc.boxW+0.01 || h > tc.boxH+0.01 {
				t.Fatalf("fitBox overflowed its box: got %vx%v in %vx%v", w, h, tc.boxW, tc.boxH)
			}
			if diff := w - tc.wantW; diff > 0.1 || diff < -0.1 {
				t.Errorf("w = %v, want ~%v", w, tc.wantW)
			}
			if diff := h - tc.wantH; diff > 0.1 || diff < -0.1 {
				t.Errorf("h = %v, want ~%v", h, tc.wantH)
			}
		})
	}
	if w, h := fitBox(0, 0, 100, 100); w != 0 || h != 0 {
		t.Errorf("a zero-sized image should fit to nothing, got %vx%v", w, h)
	}
}

func TestParseHexColor(t *testing.T) {
	fb := rgb{r: 1, g: 2, b: 3}
	cases := []struct {
		in   string
		want rgb
	}{
		{"#4f46e5", rgb{79, 70, 229}},
		{"4f46e5", rgb{79, 70, 229}},
		{"#FCDC54", rgb{252, 220, 84}},
		{"#abc", rgb{170, 187, 204}},
		{"", fb},
		{"nope", fb},
		{"#12345", fb},
		{"#zzzzzz", fb},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := parseHexColor(tc.in, fb); got != tc.want {
				t.Errorf("parseHexColor(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestAccentColor_DefaultsToArenaIndigo pins the platform default and the
// per-ticket override the Lampyris migration will need.
func TestAccentColor_DefaultsToArenaIndigo(t *testing.T) {
	if got := accentColor(Ticket{}); got != (rgb{79, 70, 229}) {
		t.Errorf("default accent = %+v, want the Arena Sold Out indigo #4f46e5", got)
	}
	if got := accentColor(Ticket{AccentColor: "#FCDC54"}); got != (rgb{252, 220, 84}) {
		t.Errorf("override accent = %+v, want Lampyris yellow", got)
	}
	if got := accentColor(Ticket{AccentColor: "not a colour"}); got != (rgb{79, 70, 229}) {
		t.Errorf("an unparseable accent must fall back to the default, got %+v", got)
	}
}

func TestFormatMoneyMinor(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{60000, "CZK", "600 CZK"},
		{1895, "EUR", "18.95 EUR"},
		{150000, "CZK", "1 500 CZK"},
		{123456789, "CZK", "1 234 567.89 CZK"},
		{2050, "", "20.50"},
		{-500, "EUR", "-5 EUR"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatMoneyMinor(tc.minor, tc.currency); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestPriceValue_DropsUnknownAndFree(t *testing.T) {
	if got := priceValue(Ticket{Currency: "EUR"}); got != "" {
		t.Errorf("a nil price must drop the cell, got %q", got)
	}
	if got := priceValue(Ticket{PriceMinor: money(0), Currency: "EUR"}); got != "" {
		t.Errorf("a free ticket must drop the cell rather than print 0, got %q", got)
	}
	if got := priceValue(Ticket{PriceMinor: money(1895), Currency: "EUR"}); got != "18.95 EUR" {
		t.Errorf("got %q", got)
	}
	// The complimentary override replaces the amount outright.
	if got := priceValue(Ticket{PriceMinor: money(1895), Currency: "EUR", PriceLabel: "Invitation"}); got != "Invitation" {
		t.Errorf("got %q", got)
	}
}

func TestSeatValue_UsesLocalizedWordsAndSkipsMissingParts(t *testing.T) {
	en := stringsFor("en")
	cases := []struct {
		name string
		in   Ticket
		want string
	}{
		{"full", Ticket{SeatSector: "A", SeatRow: "3", SeatNumber: "12"}, "A, row 3, seat 12"},
		{"no sector", Ticket{SeatRow: "3", SeatNumber: "12"}, "row 3, seat 12"},
		{"row only", Ticket{SeatRow: "3"}, "row 3"},
		{"general admission", Ticket{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seatValue(tc.in, en); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	ru := stringsFor("ru")
	if got := seatValue(Ticket{SeatSector: "A", SeatRow: "3", SeatNumber: "12"}, ru); got != "A, ряд 3, место 12" {
		t.Errorf("ru seat value = %q", got)
	}
}

func TestInfoCells_SkipsEveryEmptyValue(t *testing.T) {
	en := stringsFor("en")
	full := infoCells(seatedTicket(t), en)
	if len(full) != 4 {
		t.Fatalf("a fully populated ticket should have 4 cells, got %d (%+v)", len(full), full)
	}
	// A free general-admission ticket with no holder name — the shape both
	// of the first live clients actually sell.
	bare := infoCells(Ticket{TierName: "General Admission"}, en)
	if len(bare) != 1 || bare[0].value != "General Admission" {
		t.Fatalf("expected only the category cell, got %+v", bare)
	}
	if got := infoCells(Ticket{}, en); len(got) != 0 {
		t.Fatalf("a ticket with nothing to show must produce no cells, got %+v", got)
	}
}

// TestInfoRows_HolderGetsItsOwnRow pins the one structural deviation from
// the ported design: Category / Seat / Price share the original's single row
// of equal columns, and the arena-added holder name gets the full width
// beneath rather than squeezing the row to four 22 mm columns.
func TestInfoRows_HolderGetsItsOwnRow(t *testing.T) {
	en := stringsFor("en")

	rows := infoRows(seatedTicket(t), en)
	if len(rows) != 2 {
		t.Fatalf("expected a top row and a holder row, got %d rows (%+v)", len(rows), rows)
	}
	if len(rows[0]) != 3 {
		t.Errorf("top row = %+v, want category/seat/price", rows[0])
	}
	if len(rows[1]) != 1 || rows[1][0].label != en.Holder {
		t.Errorf("second row = %+v, want the holder alone", rows[1])
	}

	noHolder := seatedTicket(t)
	noHolder.HolderName = ""
	if got := infoRows(noHolder, en); len(got) != 1 {
		t.Errorf("without a holder there should be one row, got %+v", got)
	}

	onlyHolder := Ticket{HolderName: "Ivan Petrov"}
	if got := infoRows(onlyHolder, en); len(got) != 1 || len(got[0]) != 1 {
		t.Errorf("a holder-only ticket should produce exactly its own row, got %+v", got)
	}

	if got := infoRows(Ticket{}, en); len(got) != 0 {
		t.Errorf("a ticket with nothing to show must produce no rows, got %+v", got)
	}
}

func TestRuneLen_CountsCharactersNotBytes(t *testing.T) {
	// The shrink thresholds are in characters: a byte budget would shrink
	// every Cyrillic title for no reason.
	if got := runeLen("Концерт"); got != 7 {
		t.Errorf("runeLen(Концерт) = %d, want 7 (its byte length is 14)", got)
	}
}

func TestDisplayNumber_NeverTheUUID(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	if got := DisplayNumber("1042", id); got != "1042" {
		t.Errorf("got %q", got)
	}
	got := DisplayNumber("", id)
	if got != "11111111" {
		t.Errorf("fallback = %q, want the short reference", got)
	}
	if strings.Contains(got, "-") {
		t.Error("the fallback must not look like a UUID")
	}
}
