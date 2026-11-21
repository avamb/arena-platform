package hvenueplan

import (
	"archive/zip"
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

func zipWith(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("<x/>"))
	}
	_ = zw.Close()
	return buf.Bytes()
}

func encoded(t *testing.T, enc func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSniffKind_ByContentOnly(t *testing.T) {
	jpg := encoded(t, func(b *bytes.Buffer, i image.Image) error { return jpeg.Encode(b, i, nil) })
	pngb := encoded(t, func(b *bytes.Buffer, i image.Image) error { return png.Encode(b, i) })
	gifb := encoded(t, func(b *bytes.Buffer, i image.Image) error { return gif.Encode(b, i, nil) })
	cases := []struct {
		name string
		data []byte
		want venueplan.SourceKind
	}{
		{"jpeg", jpg, venueplan.SourceJPEG},
		{"png", pngb, venueplan.SourcePNG},
		{"gif", gifb, venueplan.SourceGIF},
		{"webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), venueplan.SourceWEBP},
		{"tiff le", []byte("II*\x00\x08\x00\x00\x00"), venueplan.SourceTIFF},
		{"tiff be", []byte("MM\x00*\x00\x00\x00\x08"), venueplan.SourceTIFF},
		{"bmp", append([]byte("BM"), make([]byte, 40)...), venueplan.SourceBMP},
		{"heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), venueplan.SourceHEIC},
		{"xls", append(append([]byte{}, oleMagic...), 0, 0, 0), venueplan.SourceXLS},
		{"xlsx", zipWith(t, "[Content_Types].xml", "xl/workbook.xml"), venueplan.SourceXLSX},
		{"zip that is not xlsx", zipWith(t, "word/document.xml"), ""},
		{"pdf", []byte("%PDF-1.7\n..."), venueplan.SourcePDF},
		{"svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), venueplan.SourceSVG},
		{"csv", []byte("row;seat;price\n1;1;20\n"), venueplan.SourceCSV},
		{"html is not csv", []byte("<html><body>a,b</body></html>"), ""},
		{"plain text", []byte("hello world"), ""},
		{"binary", []byte{0x00, 0x01, 0x02, 0x03}, ""},
	}
	for _, c := range cases {
		if got := sniffKind(c.data); got != c.want {
			t.Errorf("%s: kind %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPDFPages_CountsPagesNotPagesTree(t *testing.T) {
	doc := []byte("<< /Type /Pages /Count 2 >> << /Type /Page >> << /Type/Page /Parent 1 0 R >>")
	if n := pdfPages(doc); n != 2 {
		t.Errorf("pages %d, want 2", n)
	}
}

func TestStripJPEGMetadata_DropsEXIFKeepsPictureAndOrientation(t *testing.T) {
	plain := encoded(t, func(b *bytes.Buffer, i image.Image) error { return jpeg.Encode(b, i, nil) })
	tiff := []byte("II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x08\x00\x00\x00\x00\x00\x00\x00GPS-SECRET")
	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1, 0, byte(len(payload) + 2)}, payload...)
	xmpBody := []byte("http://ns.a")
	xmp := append([]byte{0xFF, 0xE1, 0, byte(len(xmpBody) + 2)}, xmpBody...)
	com := []byte{0xFF, 0xFE, 0, 6, 'h', 'i', 'd', 'e'}
	in := append(append(append(append([]byte{}, plain[:2]...), app1...), xmp...), com...)
	in = append(in, plain[2:]...)

	out, orientation, err := stripJPEGMetadata(in)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if orientation != 8 {
		t.Errorf("orientation %d, want 8", orientation)
	}
	for _, s := range []string{"GPS-SECRET", "http://ns.a", "hide"} {
		if bytes.Contains(out, []byte(s)) {
			t.Errorf("%q survived", s)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("decode: %v", err)
	}
	if o := exifOrientation(out[bytes.Index(out, []byte("Exif\x00\x00"))+6:]); o != 8 {
		t.Errorf("kept orientation %d, want 8", o)
	}
	// A JPEG without metadata comes out byte for byte.
	same, o, err := stripJPEGMetadata(plain)
	if err != nil || o != 0 || !bytes.Equal(same, plain) {
		t.Errorf("plain JPEG changed: %v %d", err, o)
	}
	if _, _, err := stripJPEGMetadata([]byte{0xFF, 0xD8, 0x00}); err == nil {
		t.Error("a broken JPEG was accepted")
	}
}

// A phone appends more pictures (MPF) with their own EXIF after the EOI of the
// main picture: the stored JPEG ends at the first EOI after the scan data.
func TestStripJPEGMetadata_EndsAtFirstEOIAfterScan(t *testing.T) {
	plain := encoded(t, func(b *bytes.Buffer, i image.Image) error { return jpeg.Encode(b, i, nil) })
	if !bytes.HasSuffix(plain, []byte{0xFF, 0xD9}) {
		t.Fatal("the test JPEG does not end with EOI")
	}
	second := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x12}, []byte("Exif\x00\x00GPS-TRAILER")...)
	second = append(second, 0xFF, 0xD9)
	in := append(append(append([]byte{}, plain...), second...), []byte("MPF-TRAILING-BYTES")...)

	out, _, err := stripJPEGMetadata(in)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("stored %d bytes, want the %d bytes up to the first EOI", len(out), len(plain))
	}
	for _, s := range []string{"GPS-TRAILER", "MPF-TRAILING-BYTES"} {
		if bytes.Contains(out, []byte(s)) {
			t.Errorf("%q survived after the EOI", s)
		}
	}
	// Stuffed bytes and restart markers inside the scan do not end it.
	scan := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD3, 0x56, 0xFF, 0xD9, 0x99}
	if got := scanEnd(scan, 0); got != 7 {
		t.Errorf("scanEnd = %d, want 7", got)
	}
}

// The request that lost the Idempotency-Key insert race answers like a
// replay: an intake of another venue is 409 idempotency_key_reused.
func TestAnswerExisting_OtherVenueIs409(t *testing.T) {
	h := New(nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	in := gen.VenuePlanIntakeRow{ID: uuid.New(), VenueID: uuid.New()}
	h.answerExisting(rec, httptest.NewRequest(http.MethodPost, "/", nil), in, uuid.New())
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "venue_plan_intake.idempotency_key_reused") {
		t.Errorf("answer %d %s", rec.Code, rec.Body.String())
	}
}

// Every administrator write but claim needs the caller's own claim.
func TestHoldsClaim(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(auth.WithActor(req.Context(), auth.Actor{ID: me.String()}))
	cases := []struct {
		name     string
		operator *uuid.UUID
		ok       bool
		code     string
	}{
		{"unclaimed", nil, false, "venue_plan_intake.not_claimed"},
		{"claimed by another", &other, false, "venue_plan_intake.claimed_by_other"},
		{"claimed by the caller", &me, true, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		got := holdsClaim(rec, req, gen.VenuePlanIntakeRow{OperatorID: c.operator})
		if got != c.ok {
			t.Errorf("%s: %v, want %v", c.name, got, c.ok)
		}
		if !c.ok && (rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), c.code)) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

func TestCleanFileName(t *testing.T) {
	cases := map[string]string{
		`C:\fakepath\plan.pdf`: "plan.pdf",
		"../../etc/passwd":     "passwd",
		"":                     "file",
		"a\x00b.png":           "ab.png",
	}
	for in, want := range cases {
		if got := cleanFileName(in); got != want {
			t.Errorf("cleanFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandlersAnswer503WithoutDatabase(t *testing.T) {
	h := New(nil, nil, nil, nil)
	for _, fn := range []http.HandlerFunc{h.HandleCreate, h.HandleList, h.HandleGet, h.HandleAdminList, h.HandleAdminDraft} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", rec.Code)
		}
	}
}
