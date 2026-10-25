package pdfinsert

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks/qr"
)

const testContent = "https://go.arenasoldout.com/x7k2qpm"

func baseInsert() Insert {
	return Insert{
		Content:  testContent,
		Title:    "Весенний концерт",
		Subtitle: "25.10.2026, 19:00",
		Tagline:  "Scan and buy tickets",
		OrgName:  "Arena Test Promotions",
		Locale:   "en",
		Paper:    PaperA6,
	}
}

func render(t *testing.T, in Insert) []byte {
	t.Helper()
	out, err := Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	return out
}

// logoPNG is a small deterministic PNG: a grey 120x40 band.
func logoPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 120, 40))
	for i := range img.Pix {
		img.Pix[i] = 0x60
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ── the pdfDrawnText approach: read every text run back out of the stream ─

// pdfDrawnText joins every "Td (<utf-16be>)Tj" run of the content stream.
func pdfDrawnText(doc []byte) string {
	var parts []string
	marker := []byte("Td (")
	for rest := doc; ; {
		i := bytes.Index(rest, marker)
		if i < 0 {
			break
		}
		rest = rest[i+len(marker):]
		raw, n, ok := scanPDFStringLiteral(rest)
		if !ok {
			break
		}
		parts = append(parts, decodeUTF16BE(raw))
		rest = rest[n:]
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func scanPDFStringLiteral(b []byte) (raw []byte, n int, ok bool) {
	for i := 0; i < len(b); {
		if b[i] == '\\' && i+1 < len(b) {
			c := b[i+1]
			if c == 'r' {
				c = '\r'
			}
			raw = append(raw, c)
			i += 2
			continue
		}
		if bytes.HasPrefix(b[i:], []byte(")Tj")) {
			return raw, i + len(")Tj"), true
		}
		raw = append(raw, b[i])
		i++
	}
	return nil, 0, false
}

func decodeUTF16BE(b []byte) string {
	var sb strings.Builder
	for i := 0; i+1 < len(b); i += 2 {
		sb.WriteRune(rune(binary.BigEndian.Uint16(b[i:])))
	}
	return sb.String()
}

var (
	mediaBoxRe = regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`)
	rectOpRe   = regexp.MustCompile(`(-?[0-9.]+) (-?[0-9.]+) (-?[0-9.]+) (-?[0-9.]+) re f`)
	imageOpRe  = regexp.MustCompile(`q ([0-9.]+) 0 0 ([0-9.]+) ([0-9.]+) ([0-9.]+) cm /I[0-9a-f]+ Do Q`)
)

type rect struct{ x, top, w, h float64 }

// filledRects returns every filled rectangle in a top-down frame.
func filledRects(t *testing.T, out []byte, pageH float64) []rect {
	t.Helper()
	var got []rect
	for _, m := range rectOpRe.FindAllSubmatch(out, -1) {
		x, _ := strconv.ParseFloat(string(m[1]), 64)
		y, _ := strconv.ParseFloat(string(m[2]), 64)
		w, _ := strconv.ParseFloat(string(m[3]), 64)
		h, _ := strconv.ParseFloat(string(m[4]), 64)
		got = append(got, rect{x: x, top: pageH - y, w: w, h: -h})
	}
	return got
}

func mediaBox(t *testing.T, out []byte) (w, h float64) {
	t.Helper()
	m := mediaBoxRe.FindSubmatch(out)
	if m == nil {
		t.Fatal("no MediaBox")
	}
	w, _ = strconv.ParseFloat(string(m[1]), 64)
	h, _ = strconv.ParseFloat(string(m[2]), 64)
	return w, h
}

// ── tests ────────────────────────────────────────────────────────────────

func TestRender_IsByteDeterministic(t *testing.T) {
	in := baseInsert()
	in.Logo = logoPNG(t)
	a, b := render(t, in), render(t, in)
	if !bytes.Equal(a, b) {
		t.Fatal("two renders of the same insert differ")
	}
	in.Paper = PaperA5
	if !bytes.Equal(render(t, in), render(t, in)) {
		t.Fatal("A5 renders differ")
	}
}

func TestRender_BothPapersHaveTheirSize(t *testing.T) {
	for _, tc := range []struct {
		paper Paper
		w, h  float64
	}{{PaperA6, 105, 148}, {PaperA5, 148, 210}} {
		in := baseInsert()
		in.Paper = tc.paper
		out := render(t, in)
		w, h := mediaBox(t, out)
		if d := w - mm(tc.w); d > 0.02 || d < -0.02 {
			t.Errorf("%s width %.2f, want %.2f", tc.paper, w, mm(tc.w))
		}
		if d := h - mm(tc.h); d > 0.02 || d < -0.02 {
			t.Errorf("%s height %.2f, want %.2f", tc.paper, h, mm(tc.h))
		}
		text := pdfDrawnText(out)
		for _, want := range []string{"Весенний концерт", "25.10.2026, 19:00", "Scan and buy tickets", "go.arenasoldout.com/x7k2qpm", "Arena Test Promotions"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: drawn text lacks %q:\n%s", tc.paper, want, text)
			}
		}
	}
}

func TestRender_EveryLanguageOfTheTagline(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		in := baseInsert()
		in.Locale = loc
		in.Tagline = Tagline(bundle, loc)
		if in.Tagline == "" || (loc != "en" && in.Tagline == taglineFallback) {
			t.Errorf("%s: tagline %q is not localized", loc, in.Tagline)
		}
		out := render(t, in)
		text := pdfDrawnText(out)
		want := in.Tagline
		if loc == "he" {
			// Hebrew is drawn in visual order: the line reversed, as on the ticket.
			want = visualLine(in.Tagline)
			if want == in.Tagline {
				t.Errorf("he: visualLine changed nothing in %q", in.Tagline)
			}
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s: drawn text lacks the tagline %q:\n%s", loc, want, text)
		}
		// The address is never reordered, whatever the language.
		if !strings.Contains(text, "go.arenasoldout.com/x7k2qpm") {
			t.Errorf("%s: the address is missing or reordered:\n%s", loc, text)
		}
	}
}

func TestTagline_FallsBackToEnglish(t *testing.T) {
	if got := Tagline(nil, "ru"); got != taglineFallback {
		t.Errorf("nil bundle: %q", got)
	}
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	if got := Tagline(bundle, "de"); got != taglineFallback {
		t.Errorf("unsupported locale: %q, want the English line", got)
	}
	if got := Tagline(bundle, "RU-ru"); got != Tagline(bundle, "ru") {
		t.Errorf("region and case must fold: %q", got)
	}
}

func TestRender_QRIsBlackModulesInsideTheQuietZone(t *testing.T) {
	in := baseInsert()
	out := render(t, in)
	_, pageH := mediaBox(t, out)
	matrix, err := qr.Matrix(in.Content)
	if err != nil {
		t.Fatal(err)
	}
	// One rectangle per run of dark modules in a row.
	wantRuns := 0
	for _, row := range matrix {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			wantRuns++
			for x < len(row) && row[x] {
				x++
			}
		}
	}
	rects := filledRects(t, out, pageH)
	if len(rects) != wantRuns {
		t.Fatalf("filled rectangles = %d, want %d runs of dark modules", len(rects), wantRuns)
	}
	// All of them sit inside the code square minus the quiet zone.
	sp, _ := specFor(PaperA6)
	module := sp.qrSide / float64(len(matrix)+2*quietModules)
	left := (sp.pageW - sp.qrSide) / 2
	minX, maxX := left+quietModules*module-0.01, left+sp.qrSide-quietModules*module+0.01
	var top, bottom float64
	for i, r := range rects {
		if r.x < minX || r.x+r.w > maxX {
			t.Fatalf("rect %d spans x %.2f..%.2f, outside the module area %.2f..%.2f (the quiet zone must stay empty)", i, r.x, r.x+r.w, minX, maxX)
		}
		if i == 0 || r.top < top {
			top = r.top
		}
		if b := r.top + r.h; b > bottom {
			bottom = b
		}
	}
	if span := bottom - top; span > float64(len(matrix))*module+0.05 || span < float64(len(matrix))*module-0.05 {
		t.Errorf("vertical span of the modules %.2f, want %.2f", span, float64(len(matrix))*module)
	}
	// Pure black fill, never grey, never an image.
	if !bytes.Contains(out, []byte("0.000 g")) {
		t.Error("no black fill colour operator")
	}
	if imageOpRe.Match(out) {
		t.Error("the code must be vector rectangles, not an image")
	}
}

func TestRender_LogoReplacesTheOrganizationName(t *testing.T) {
	in := baseInsert()
	without := render(t, in)
	if imageOpRe.Match(without) {
		t.Error("no logo, yet an image is drawn")
	}
	if !strings.Contains(pdfDrawnText(without), in.OrgName) {
		t.Error("without a logo the organization name must be printed")
	}
	in.Logo = logoPNG(t)
	with := render(t, in)
	if !imageOpRe.Match(with) {
		t.Error("with a logo no image is drawn")
	}
	if strings.Contains(pdfDrawnText(with), in.OrgName) {
		t.Error("with a logo the organization name must not be printed on top")
	}
	// Garbage is not a logo: the name comes back.
	in.Logo = []byte("not an image at all, just bytes")
	if !strings.Contains(pdfDrawnText(render(t, in)), in.OrgName) {
		t.Error("an undecodable logo must fall back to the name")
	}
}

func TestRender_LongTitleShrinksThenIsCut(t *testing.T) {
	in := baseInsert()
	in.Title = strings.Repeat("Very long festival name with many words ", 8)
	out := render(t, in)
	text := pdfDrawnText(out)
	if !strings.Contains(text, "…") {
		t.Errorf("a title that cannot fit three lines must end with an ellipsis:\n%s", text)
	}
	// The code and the address still made it onto the page.
	for _, want := range []string{"Scan and buy tickets", "go.arenasoldout.com/x7k2qpm"} {
		if !strings.Contains(text, want) {
			t.Errorf("drawn text lacks %q", want)
		}
	}
}

func TestRender_Validation(t *testing.T) {
	if _, err := Render(Insert{}); err != ErrEmptyContent {
		t.Errorf("empty content: %v", err)
	}
	if _, err := Render(Insert{Content: "x", Paper: "letter"}); err != ErrBadPaper {
		t.Errorf("bad paper: %v", err)
	}
	// Only the content is required.
	if _, err := Render(Insert{Content: testContent}); err != nil {
		t.Errorf("content only: %v", err)
	}
}

func TestParsePaperAndFileName(t *testing.T) {
	for in, want := range map[string]Paper{"": PaperA6, "a6": PaperA6, " A5 ": PaperA5} {
		if got, ok := ParsePaper(in); !ok || got != want {
			t.Errorf("ParsePaper(%q) = %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"a4", "letter", "a 6"} {
		if _, ok := ParsePaper(bad); ok {
			t.Errorf("ParsePaper(%q) accepted", bad)
		}
	}
	if got := FileName("x7k2qpm", PaperA6); got != "x7k2qpm-a6.pdf" {
		t.Errorf("FileName = %q", got)
	}
}

func TestSessionLine_UsesTheVenueClock(t *testing.T) {
	start := time.Date(2026, 10, 25, 17, 0, 0, 0, time.UTC)
	if got := SessionLine(start, "Europe/Madrid"); got != "25.10.2026, 18:00" {
		t.Errorf("Madrid: %q", got)
	}
	if got := SessionLine(start, ""); got != "25.10.2026, 17:00" {
		t.Errorf("no zone falls back to UTC: %q", got)
	}
	if got := SessionLine(start, "Mars/Olympus"); got != "25.10.2026, 17:00" {
		t.Errorf("unknown zone falls back to UTC: %q", got)
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{"": "en", "ru": "ru", "cs-CZ": "cs", "HE": "he", "es_ES": "es", "de": "en", "klingon": "en"} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVisualLine(t *testing.T) {
	cases := []struct{ in, want string }{
		// no Hebrew: untouched
		{"Scan and buy tickets", "Scan and buy tickets"},
		// pure Hebrew: reversed
		{"אבג", "גבא"},
		{"סרקו וקנו כרטיסים", "םיסיטרכ ונקו וקרס"},
		// digits keep their order and move to the left
		{"קונצרט 2026", "2026 טרצנוק"},
		// a Latin run stays, brackets mirror
		{"הופעה (Live) בחיפה", "הפיחב (Live) העפוה"},
		// the bidi order: the date on the right, the hall on the left
		{"25.10.2026, 19:00 אולם", "םלוא 19:00 ,25.10.2026"},
	}
	for _, c := range cases {
		if got := visualLine(c.in); got != c.want {
			t.Errorf("visualLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripScheme(t *testing.T) {
	if got := stripScheme("https://go.example.com/abc"); got != "go.example.com/abc" {
		t.Errorf("%q", got)
	}
	if got := stripScheme("go.example.com/abc"); got != "go.example.com/abc" {
		t.Errorf("%q", got)
	}
}

func TestLogoColourDoesNotMatter(t *testing.T) {
	// A colour JPEG-free check: a coloured PNG is still a logo.
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeLogo(buf.Bytes()); !ok {
		t.Error("a coloured PNG must decode as a logo")
	}
}
