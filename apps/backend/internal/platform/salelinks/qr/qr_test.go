package qr

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"testing"
)

const sample = "https://go.example.test/k7Hq2mXp"

func isWhite(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r == 0xffff && g == 0xffff && b == 0xffff
}

func TestPNG_ExactSizeSquareWithQuietZone(t *testing.T) {
	m, err := Matrix(sample)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ size, margin int }{{256, 2}, {512, 4}, {1024, 4}, {2048, 8}, {777, 3}} {
		raw, err := PNG(sample, c.size, c.margin)
		if err != nil {
			t.Fatalf("PNG %v: %v", c, err)
		}
		if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("PNG %v: not a PNG", c)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("PNG %v: decode: %v", c, err)
		}
		b := img.Bounds()
		if b.Dx() != c.size || b.Dy() != c.size {
			t.Fatalf("PNG %v: got %dx%d", c, b.Dx(), b.Dy())
		}
		total := len(m) + 2*c.margin
		px := c.size / total
		offset := (c.size - px*total) / 2
		// Every pixel outside the symbol area is white: the quiet zone is at least margin modules.
		border := offset + c.margin*px
		for y := 0; y < c.size; y++ {
			for x := 0; x < c.size; x++ {
				inside := x >= border && x < c.size-border && y >= border && y < c.size-border
				if !inside && !isWhite(img, x, y) {
					t.Fatalf("PNG %v: dark pixel in the quiet zone at %d,%d", c, x, y)
				}
			}
		}
		// Every module is drawn as one solid block of the right colour.
		for my, row := range m {
			for mx, dark := range row {
				x := offset + (c.margin+mx)*px
				y := offset + (c.margin+my)*px
				for _, p := range [][2]int{{x, y}, {x + px - 1, y + px - 1}, {x + px/2, y + px/2}} {
					if isWhite(img, p[0], p[1]) == dark {
						t.Fatalf("PNG %v: module %d,%d (dark=%v) drawn wrong", c, mx, my, dark)
					}
				}
			}
		}
	}
}

func TestPNG_Deterministic(t *testing.T) {
	a, _ := PNG(sample, 512, 4)
	b, _ := PNG(sample, 512, 4)
	if !bytes.Equal(a, b) {
		t.Fatal("PNG bytes differ between calls")
	}
}

func TestMatrix_HasNoLibraryBorder(t *testing.T) {
	m, err := Matrix(sample)
	if err != nil {
		t.Fatal(err)
	}
	// A QR symbol starts with the 7x7 finder pattern: the corner module is dark.
	if !m[0][0] || !m[0][6] || !m[6][0] {
		t.Fatal("finder pattern not at the corner: the quiet zone was not removed")
	}
	for _, row := range m {
		if len(row) != len(m) {
			t.Fatal("matrix is not square")
		}
	}
}

func TestSVG_StructureAndRuns(t *testing.T) {
	for _, margin := range []int{2, 4, 8} {
		raw, err := SVG(sample, margin)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := Matrix(sample)
		n := len(m) + 2*margin
		var doc struct {
			XMLName xml.Name `xml:"svg"`
			ViewBox string   `xml:"viewBox,attr"`
			Width   string   `xml:"width,attr"`
			Height  string   `xml:"height,attr"`
			Rects   []struct {
				Fill string `xml:"fill,attr"`
			} `xml:"rect"`
		}
		if err := xml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("margin %d: not XML: %v", margin, err)
		}
		if want := fmt.Sprintf("0 0 %d %d", n, n); doc.ViewBox != want {
			t.Errorf("viewBox = %q, want %q", doc.ViewBox, want)
		}
		if doc.Width != "" || doc.Height != "" {
			t.Errorf("width/height must be absent, got %q %q", doc.Width, doc.Height)
		}
		runs := 0
		for _, row := range m {
			for x := 0; x < len(row); x++ {
				if row[x] && (x == 0 || !row[x-1]) {
					runs++
				}
			}
		}
		if len(doc.Rects) != runs+1 {
			t.Errorf("margin %d: %d rects, want %d runs + background", margin, len(doc.Rects), runs)
		}
		for i, r := range doc.Rects {
			want := "#000"
			if i == 0 {
				want = "#fff"
			}
			if r.Fill != want {
				t.Errorf("rect %d fill = %q, want %q", i, r.Fill, want)
			}
		}
		again, _ := SVG(sample, margin)
		if !bytes.Equal(raw, again) {
			t.Error("SVG bytes differ between calls")
		}
	}
}

// TestSVG_Golden keeps the exact bytes of one SVG. ARENA_REGEN_QR_GOLDEN=1 rewrites it.
func TestSVG_Golden(t *testing.T) {
	got, err := SVG(sample, 4)
	if err != nil {
		t.Fatal(err)
	}
	const path = "testdata/sample.svg"
	if os.Getenv("ARENA_REGEN_QR_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with ARENA_REGEN_QR_GOLDEN=1)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Error("SVG differs from testdata/sample.svg (ARENA_REGEN_QR_GOLDEN=1 rewrites it)")
	}
}

func TestParameterValidation(t *testing.T) {
	for _, size := range []int{0, 255, 2049} {
		if _, err := PNG(sample, size, 4); !errors.Is(err, ErrBadSize) {
			t.Errorf("size %d: %v", size, err)
		}
	}
	for _, margin := range []int{0, 1, 9} {
		if _, err := PNG(sample, 512, margin); !errors.Is(err, ErrBadMargin) {
			t.Errorf("png margin %d: %v", margin, err)
		}
		if _, err := SVG(sample, margin); !errors.Is(err, ErrBadMargin) {
			t.Errorf("svg margin %d: %v", margin, err)
		}
	}
	if _, err := PNG("", 512, 4); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty content: %v", err)
	}
}
