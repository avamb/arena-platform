package mediastore

// variants.go — resized copies of a stored picture, served by
// GET /v1/media-files/{id}?w=<width>. A shop card shows a picture 111 px wide
// while the original is 3648x5472 and over a megabyte, so the download route
// can hand out a smaller copy of the same picture.
//
// Contract:
//   - only the widths in VariantWidths exist; any other request is rounded UP
//     to the next one, and a request above the largest means "the original";
//   - a picture is never upscaled: a width at or above the original's is
//     served as the original;
//   - the aspect ratio and the format are kept (JPEG stays JPEG at quality
//     82, PNG stays PNG); GIF, WebP and every other format are served
//     unchanged;
//   - a variant is rendered once and stored beside the original under
//     "_variants/<id>_<w>.<ext>" (a local file or a sibling S3 key), so the
//     next request is a plain read.
//
// The URL signature covers the id and the expiry only (computeHMAC), so the
// width is not part of what is signed: a client may append w to any signed
// URL it already holds.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sort"

	"github.com/google/uuid"
	"golang.org/x/image/draw"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// VariantWidths are the only widths a variant is rendered at.
var VariantWidths = []int{165, 240, 360, 480, 533, 720, 990, 1200}

// maxVariantPixels bounds the decode work: a picture over 40 megapixels is
// served as the original instead of being decoded into memory.
const maxVariantPixels = 40_000_000

// maxVariantSourceBytes bounds the bytes read for a decode (the original is
// held in memory once).
const maxVariantSourceBytes = 64 << 20

const jpegVariantQuality = 82

// RoundVariantWidth maps a requested width to the allowed one: the smallest
// allowed width that is >= w. original is true when w is above the largest
// allowed width (or not positive), meaning "serve the original".
func RoundVariantWidth(w int) (width int, original bool) {
	if w <= 0 {
		return 0, true
	}
	i := sort.SearchInts(VariantWidths, w)
	if i == len(VariantWidths) {
		return 0, true
	}
	return VariantWidths[i], false
}

// variantExt returns the file extension of a variant for a content type, or
// "" when the format is not resized.
func variantExt(contentType string) string {
	switch contentType {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	}
	return ""
}

// VariantKey is the storage key of the w-wide variant of media id.
func VariantKey(id uuid.UUID, w int, ext string) string {
	return fmt.Sprintf("_variants/%s_%d.%s", id.String(), w, ext)
}

// OpenVariant returns the w-wide copy of the object, rendering and storing it
// on the first request. ok is false when the original must be served instead
// (unsupported format, width not smaller than the original, a picture too
// large to decode, or any failure to build the copy: a resize problem must
// never cost the buyer the picture). The caller closes res.Body.
func (r *Repo) OpenVariant(ctx context.Context, obj Object, requested int) (res *storage.GetResult, ok bool) {
	w, original := RoundVariantWidth(requested)
	if original {
		return nil, false
	}
	ext := variantExt(obj.ContentType)
	if ext == "" {
		return nil, false
	}
	// A recorded width at or below the target needs no resize at all.
	if obj.Width != nil && int(*obj.Width) <= w {
		return nil, false
	}
	key := VariantKey(obj.ID, w, ext)
	if got, err := r.storage.Get(ctx, key); err == nil {
		return got, true
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, false
	}

	src, err := r.storage.Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(src.Body, maxVariantSourceBytes+1))
	_ = src.Body.Close()
	if err != nil || len(raw) > maxVariantSourceBytes {
		return nil, false
	}
	out, resized := ResizeToWidth(raw, ext, w)
	if !resized {
		return nil, false
	}
	ct := "image/jpeg"
	if ext == "png" {
		ct = "image/png"
	}
	if _, err := r.storage.Put(ctx, storage.PutInput{Key: key, ContentType: ct, Body: bytes.NewReader(out)}); err != nil {
		// Could not cache it: the copy cannot be handed out without the
		// store, so fall back to the original.
		return nil, false
	}
	got, err := r.storage.Get(ctx, key)
	if err != nil {
		return nil, false
	}
	return got, true
}

// ResizeToWidth decodes raw (ext "jpg" or "png"), scales it to width w with
// Catmull-Rom keeping the aspect ratio and encodes it in the same format.
// resized is false when the picture is not larger than w, is over the pixel
// bound, or cannot be decoded; the caller then serves the original.
func ResizeToWidth(raw []byte, ext string, w int) (out []byte, resized bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return nil, false
	}
	if cfg.Width <= w || cfg.Width*cfg.Height > maxVariantPixels {
		return nil, false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, false
	}
	h := (cfg.Height*w + cfg.Width/2) / cfg.Width
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	switch ext {
	case "jpg":
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegVariantQuality})
	case "png":
		err = png.Encode(&buf, dst)
	default:
		return nil, false
	}
	if err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// DeleteVariants removes every stored variant of a media id (best effort:
// most ids never had one, and a missing copy is not an error).
func (r *Repo) DeleteVariants(ctx context.Context, id uuid.UUID, contentType string) {
	ext := variantExt(contentType)
	if ext == "" {
		return
	}
	for _, w := range VariantWidths {
		_ = r.storage.Delete(ctx, VariantKey(id, w, ext))
	}
}
