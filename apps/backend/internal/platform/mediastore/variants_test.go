package mediastore

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

func TestRoundVariantWidth(t *testing.T) {
	cases := []struct {
		in       int
		want     int
		original bool
	}{
		{-5, 0, true},
		{0, 0, true},
		{1, 165, false},
		{111, 165, false},
		{165, 165, false},
		{166, 240, false},
		{300, 360, false},
		{480, 480, false},
		{500, 533, false},
		{533, 533, false},
		{534, 720, false},
		{991, 1200, false},
		{1200, 1200, false},
		{1201, 0, true},
		{5472, 0, true},
	}
	for _, c := range cases {
		got, orig := RoundVariantWidth(c.in)
		if got != c.want || orig != c.original {
			t.Errorf("RoundVariantWidth(%d) = (%d, %v), want (%d, %v)", c.in, got, orig, c.want, c.original)
		}
	}
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 90, A: 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gradient(w, h), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, gradient(w, h)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestResizeToWidth_JPEGRoundTrip(t *testing.T) {
	raw := encodeJPEG(t, 1000, 1500)
	out, ok := ResizeToWidth(raw, "jpg", 360)
	if !ok {
		t.Fatal("expected a resize")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || cfg.Width != 360 || cfg.Height != 540 {
		t.Fatalf("got %s %dx%d, want jpeg 360x540", format, cfg.Width, cfg.Height)
	}
	if len(out) >= len(raw) {
		t.Fatalf("variant (%d bytes) is not smaller than the original (%d)", len(out), len(raw))
	}
}

func TestResizeToWidth_PNGRoundTrip(t *testing.T) {
	raw := encodePNG(t, 800, 400)
	out, ok := ResizeToWidth(raw, "png", 240)
	if !ok {
		t.Fatal("expected a resize")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || cfg.Width != 240 || cfg.Height != 120 {
		t.Fatalf("got %s %dx%d, want png 240x120", format, cfg.Width, cfg.Height)
	}
}

func TestResizeToWidth_NeverUpscales(t *testing.T) {
	small := encodeJPEG(t, 240, 100)
	for _, w := range []int{240, 360} {
		if _, ok := ResizeToWidth(small, "jpg", w); ok {
			t.Errorf("a 240 px picture must not be resized to %d", w)
		}
	}
	if _, ok := ResizeToWidth(small, "jpg", 165); !ok {
		t.Error("a 240 px picture can be shrunk to 165")
	}
}

func TestResizeToWidth_RefusesGarbageAndHugePictures(t *testing.T) {
	if _, ok := ResizeToWidth([]byte("not a picture"), "jpg", 240); ok {
		t.Error("garbage must be served unchanged")
	}
	// A 50 MP PNG header declares more pixels than the 40 MP bound; the
	// encoded body of a flat image is tiny so this stays cheap.
	big := image.NewGray(image.Rect(0, 0, 8000, 6250))
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResizeToWidth(buf.Bytes(), "png", 240); ok {
		t.Error("a picture over 40 MP must be served unchanged")
	}
}

func TestOpenVariant_RendersOnceThenReadsTheStoredCopy(t *testing.T) {
	ctx := context.Background()
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &Repo{storage: st}
	id := uuid.New()
	key := "shop_product/" + id.String()
	raw := encodeJPEG(t, 1000, 600)
	if _, err := st.Put(ctx, storage.PutInput{Key: key, ContentType: "image/jpeg", Body: bytes.NewReader(raw)}); err != nil {
		t.Fatal(err)
	}
	obj := Object{ID: id, StorageKey: key, ContentType: "image/jpeg", ByteSize: int64(len(raw))}

	read := func() (int, int, int64) {
		t.Helper()
		res, ok := repo.OpenVariant(ctx, obj, 111) // rounds up to 165
		if !ok {
			t.Fatal("expected a variant")
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Width, cfg.Height, res.Size
	}
	w1, h1, _ := read()
	if w1 != 165 || h1 != 99 {
		t.Fatalf("variant is %dx%d, want 165x99", w1, h1)
	}
	// The copy now sits under _variants/ and the second read serves it.
	stored, err := st.Stat(ctx, VariantKey(id, 165, "jpg"))
	if err != nil {
		t.Fatalf("variant not stored: %v", err)
	}
	w2, _, size2 := read()
	if w2 != 165 || size2 != stored.Size {
		t.Fatalf("second read: width %d size %d, stored size %d", w2, size2, stored.Size)
	}
	// Original-serving cases.
	for _, req := range []int{2000, 0, 1200} {
		if res, ok := repo.OpenVariant(ctx, obj, req); ok {
			_ = res.Body.Close()
			t.Errorf("w=%d on a 1000 px picture must serve the original", req)
		}
	}
	gif := obj
	gif.ContentType = "image/gif"
	if res, ok := repo.OpenVariant(ctx, gif, 240); ok {
		_ = res.Body.Close()
		t.Error("a GIF is served unchanged")
	}
	repo.DeleteVariants(ctx, id, "image/jpeg")
	if _, err := st.Stat(ctx, VariantKey(id, 165, "jpg")); err == nil {
		t.Error("DeleteVariants left the copy behind")
	}
}
