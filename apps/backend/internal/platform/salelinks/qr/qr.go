// Package qr renders the QR code of a sales link (spec 43 section 4.2, QL-5)
// as a PNG or an SVG. It is pure: no HTTP, no database. The module matrix comes
// from go-qrcode (error correction level Medium, the library quiet zone
// switched off); the quiet zone and the scaling are done here, so the image is
// exactly size x size pixels with a whole number of pixels per module.
package qr

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Limits of the image parameters (spec 43 section 4.2).
const (
	MinSize   = 256
	MaxSize   = 2048
	MinMargin = 2
	MaxMargin = 8
)

var (
	// ErrBadSize is returned for a size outside MinSize..MaxSize.
	ErrBadSize = errors.New("qr: size must be between 256 and 2048 pixels")
	// ErrBadMargin is returned for a margin outside MinMargin..MaxMargin.
	ErrBadMargin = errors.New("qr: margin must be between 2 and 8 modules")
	// ErrEmpty is returned for empty content.
	ErrEmpty = errors.New("qr: content is empty")
)

// Matrix returns the module matrix of content without any quiet zone:
// m[y][x] is true for a dark module.
func Matrix(content string) ([][]bool, error) {
	if content == "" {
		return nil, ErrEmpty
	}
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("qr: encode: %w", err)
	}
	q.DisableBorder = true
	return q.Bitmap(), nil
}

func checkMargin(margin int) error {
	if margin < MinMargin || margin > MaxMargin {
		return ErrBadMargin
	}
	return nil
}

// PNG renders content as a square PNG of size x size pixels with a quiet zone
// of margin modules. Dark modules are black on white. The module size is the
// whole number size / (modules + 2*margin); the pixels left over become extra
// white border, split evenly around the code.
func PNG(content string, size, margin int) ([]byte, error) {
	if size < MinSize || size > MaxSize {
		return nil, ErrBadSize
	}
	if err := checkMargin(margin); err != nil {
		return nil, err
	}
	m, err := Matrix(content)
	if err != nil {
		return nil, err
	}
	total := len(m) + 2*margin
	px := size / total
	if px < 1 {
		// Cannot happen within the limits (177+16 < 256): refuse rather than draw garbage.
		return nil, ErrBadSize
	}
	offset := (size - px*total) / 2
	img := image.NewGray(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	black := color.Gray{Y: 0}
	for my, row := range m {
		for mx, dark := range row {
			if !dark {
				continue
			}
			x0 := offset + (margin+mx)*px
			y0 := offset + (margin+my)*px
			for y := y0; y < y0+px; y++ {
				for x := x0; x < x0+px; x++ {
					img.SetGray(x, y, black)
				}
			}
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("qr: png: %w", err)
	}
	return buf.Bytes(), nil
}

// SVG renders content as a scalable SVG: a viewBox of (modules + 2*margin)
// units a side, a white background and one rectangle per run of dark modules in
// a row. There are no width and height attributes, so the image scales to its
// container. The output is deterministic.
func SVG(content string, margin int) ([]byte, error) {
	if err := checkMargin(margin); err != nil {
		return nil, err
	}
	m, err := Matrix(content)
	if err != nil {
		return nil, err
	}
	n := len(m) + 2*margin
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, n, n)
	for y, row := range m {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1" fill="#000"/>`, margin+start, margin+y, x-start)
		}
	}
	b.WriteString(`</svg>`)
	return []byte(b.String()), nil
}
