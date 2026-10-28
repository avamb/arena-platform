package mediastore

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestImageDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBuf, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, img, nil); err != nil {
		t.Fatal(err)
	}
	riff := func(chunk string, body []byte) []byte {
		b := append([]byte("RIFF\x00\x00\x00\x00WEBP"+chunk+"\x00\x00\x00\x00"), body...)
		for len(b) < 40 {
			b = append(b, 0)
		}
		return b
	}
	// VP8X: flags (4 bytes), then canvas width-1 and height-1, 24 bits each.
	vp8x := riff("VP8X", []byte{0, 0, 0, 0, 0x7f, 0x02, 0x00, 0xdf, 0x01, 0x00}) // 640x480
	// VP8: frame tag (3), start code 9d 01 2a, width and height 14 bits LE.
	vp8 := riff("VP8 ", []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 0x80, 0x02, 0xe0, 0x01})
	// VP8L: signature 2f, then width-1 (14 bits) and height-1 (14 bits).
	bits := uint32(639) | uint32(479)<<14
	vp8l := riff("VP8L", []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24)})

	for name, b := range map[string][]byte{
		"png": pngBuf.Bytes(), "jpeg": jpgBuf.Bytes(), "gif": gifBuf.Bytes(),
		"webp vp8x": vp8x, "webp vp8": vp8, "webp vp8l": vp8l,
	} {
		w, h := ImageDimensions(bytes.NewReader(b))
		if w == nil || h == nil || *w != 640 || *h != 480 {
			t.Errorf("%s: %v x %v; want 640 x 480", name, w, h)
		}
	}
	for name, b := range map[string][]byte{
		"pdf":   []byte("%PDF-1.7\n1 0 obj"),
		"empty": nil,
		"short": []byte("RIFF"),
	} {
		if w, h := ImageDimensions(bytes.NewReader(b)); w != nil || h != nil {
			t.Errorf("%s: %v x %v; want none", name, w, h)
		}
	}
}
