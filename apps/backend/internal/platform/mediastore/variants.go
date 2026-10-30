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
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"golang.org/x/image/draw"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
)

// VariantWidths are the only widths a variant is rendered at.
var VariantWidths = []int{165, 240, 360, 480, 533, 720, 990, 1200}

// maxVariantPixels bounds the decode work: a larger picture is served as the
// original instead of being decoded into memory.
//
// One render must fit ~250 MB (arena-api runs with a 512-768 MB limit).
// Measured on a progressive 4:2:0 JPEG, 3648x5472 = 20 MP (the product photos
// of the first shop), total Go memory classes at the peak:
//
//	decode alone                    165 MB   (~8.25 bytes per pixel: the
//	                                          progressive decoder keeps 4 bytes
//	                                          per coefficient of every component
//	                                          = 6 bytes/px, plus 1.5 bytes/px
//	                                          for the YCbCr image)
//	+ one-shot CatmullRom, 1200 px  369 MB   (scratch dstW x srcH x 32 bytes)
//	+ banded scale (scaleBanded)    230 MB   (scratch fixed at 16 MB)
//
// So the cost is ~8.25 bytes/px + ~35 MB; 250 MB leaves room for 26 MP, which
// still covers 24 MP camera photos (6000x4000). The old 40 MP bound would have
// taken ~370 MB for the decode alone.
const maxVariantPixels = 26_000_000

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

// DefaultVariantWorkers is how many resized copies one process renders at the
// same time unless MEDIA_VARIANT_WORKERS says otherwise.
const DefaultVariantWorkers = 1

// variantRenderTimeout bounds one render, queue wait included. The render runs
// detached from the request that started it (other requests wait on the same
// result), so it needs a limit of its own.
const variantRenderTimeout = 2 * time.Minute

// errNoVariant says the original must be served: not resizable, or the render
// failed. It is never shown to a buyer.
var errNoVariant = errors.New("mediastore: no variant")

// OpenVariant returns the w-wide copy of the object, rendering and storing it
// on the first request. ok is false when the original must be served instead
// (unsupported format, width not smaller than the original, a picture too
// large to decode, the request gave up while waiting for a render slot, or any
// failure to build the copy: a resize problem must never cost the buyer the
// picture). The caller closes res.Body.
//
// Renders are expensive in memory, so concurrent requests for the same copy
// share ONE render (singleflight on the storage key) and at most
// VariantWorkers renders run at once in the process; every other request
// waits for its copy, or gives up with ctx and gets the original.
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

	// The render outlives this request (others may be waiting on it), so it
	// runs on a context detached from ctx; this request only stops waiting.
	ch := r.variantSF.DoChan(key, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), variantRenderTimeout)
		defer cancel()
		return nil, r.renderVariant(rctx, obj, w, ext, key)
	})
	select {
	case <-ctx.Done():
		return nil, false
	case out := <-ch:
		if out.Err != nil {
			return nil, false
		}
	}
	got, err := r.storage.Get(ctx, key)
	if err != nil {
		return nil, false
	}
	return got, true
}

// acquireRender takes a render slot, waiting for one while the process is at
// its limit. release is a no-op for a Repo built without New (tests).
func (r *Repo) acquireRender(ctx context.Context) (release func(), err error) {
	if r.renderSem == nil {
		return func() {}, nil
	}
	select {
	case r.renderSem <- struct{}{}:
		return func() { <-r.renderSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// renderVariant builds and stores one copy under a render slot. The copy may
// have been stored by another process or an earlier render while this one
// queued, so it looks again once it holds the slot.
func (r *Repo) renderVariant(ctx context.Context, obj Object, w int, ext, key string) error {
	release, err := r.acquireRender(ctx)
	if err != nil {
		return err
	}
	defer release()
	if _, err := r.storage.Stat(ctx, key); err == nil {
		return nil
	}
	raw, err := r.readOriginal(ctx, obj)
	if err != nil {
		return err
	}
	if r.renderHook != nil {
		r.renderHook(key)
	}
	out, resized := ResizeToWidth(raw, ext, w)
	if !resized {
		return errNoVariant
	}
	return r.storeVariant(ctx, key, ext, out)
}

func (r *Repo) readOriginal(ctx context.Context, obj Object) ([]byte, error) {
	src, err := r.storage.Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(src.Body, maxVariantSourceBytes+1))
	_ = src.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > maxVariantSourceBytes {
		return nil, errNoVariant
	}
	return raw, nil
}

func (r *Repo) storeVariant(ctx context.Context, key, ext string, out []byte) error {
	ct := "image/jpeg"
	if ext == "png" {
		ct = "image/png"
	}
	// Could not cache it: the copy cannot be handed out without the store, so
	// the caller falls back to the original.
	_, err := r.storage.Put(ctx, storage.PutInput{Key: key, ContentType: ct, Body: bytes.NewReader(out)})
	return err
}

// ResizeToWidth decodes raw (ext "jpg" or "png"), scales it to width w with
// Catmull-Rom keeping the aspect ratio and encodes it in the same format.
// resized is false when the picture is not larger than w, is over the pixel
// bound, or cannot be decoded; the caller then serves the original.
func ResizeToWidth(raw []byte, ext string, w int) (out []byte, resized bool) {
	pic, ok := decodeForResize(raw, w)
	if !ok {
		return nil, false
	}
	return scaleAndEncode(pic, ext, w)
}

// decodedPicture is a source decoded once and scaled to several widths.
type decodedPicture struct {
	img           image.Image
	width, height int
}

// decodeForResize decodes raw unless the picture cannot be shrunk to w (not
// wider than w), is over the pixel bound, or is not a picture at all.
func decodeForResize(raw []byte, w int) (decodedPicture, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return decodedPicture{}, false
	}
	if cfg.Width <= w || cfg.Width*cfg.Height > maxVariantPixels {
		return decodedPicture{}, false
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return decodedPicture{}, false
	}
	return decodedPicture{img: src, width: cfg.Width, height: cfg.Height}, true
}

// scaleAndEncode renders the decoded picture at width w and encodes it.
func scaleAndEncode(pic decodedPicture, ext string, w int) ([]byte, bool) {
	if pic.width <= w {
		return nil, false
	}
	h := (pic.height*w + pic.width/2) / pic.width
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	scaleBanded(dst, pic.img)

	var buf bytes.Buffer
	var err error
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

// scaleTmpEntries bounds the scaler's scratch buffer. draw.CatmullRom.Scale
// keeps dstWidth x srcHeight [4]float64 values (32 bytes each) while it
// works, which for a 20 MP photo scaled to 1200 px is 210 MB on top of the
// decoded picture. Scaling the picture in horizontal bands keeps that scratch
// at 16 MB (512Ki entries) whatever the source size.
const scaleTmpEntries = 512 << 10

// bandMargin is how many destination rows each band computes beyond the rows
// it keeps. The Catmull-Rom support is 2 source-scale units, so 3 rows leave
// the kept rows untouched by the (rounded) band edges.
const bandMargin = 3

// scaleBanded scales src into dst (Catmull-Rom, whole picture) one band of
// destination rows at a time. The result matches a single Scale call to well
// under one level per channel: a band's source window is rounded to a whole
// row, a shift of at most half a source pixel at the margin rows, which are
// discarded.
func scaleBanded(dst *image.RGBA, src image.Image) {
	sb := src.Bounds()
	dw, dh := dst.Bounds().Dx(), dst.Bounds().Dy()
	sh := sb.Dy()
	if dw*sh <= scaleTmpEntries {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Src, nil)
		return
	}
	f := float64(sh) / float64(dh)
	bandSrc := scaleTmpEntries / dw
	if bandSrc < 16 {
		bandSrc = 16
	}
	inner := int(float64(bandSrc)/f) - 2*bandMargin
	if inner < 1 {
		inner = 1
	}
	var buf []uint8
	for y0 := 0; y0 < dh; y0 += inner {
		y1 := y0 + inner
		if y1 > dh {
			y1 = dh
		}
		a, b := y0-bandMargin, y1+bandMargin
		if a < 0 {
			a = 0
		}
		if b > dh {
			b = dh
		}
		sy0, sy1 := sb.Min.Y+int(math.Round(float64(a)*f)), sb.Min.Y+int(math.Round(float64(b)*f))
		if a == 0 {
			sy0 = sb.Min.Y
		}
		if b == dh {
			sy1 = sb.Max.Y
		}
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		rows := b - a
		if need := rows * dw * 4; cap(buf) < need {
			buf = make([]uint8, need)
		}
		band := &image.RGBA{Pix: buf[:rows*dw*4], Stride: dw * 4, Rect: image.Rect(0, 0, dw, rows)}
		draw.CatmullRom.Scale(band, band.Bounds(), src, image.Rect(sb.Min.X, sy0, sb.Max.X, sy1), draw.Src, nil)
		for y := y0; y < y1; y++ {
			copy(dst.Pix[y*dst.Stride:y*dst.Stride+dw*4], band.Pix[(y-a)*band.Stride:(y-a)*band.Stride+dw*4])
		}
	}
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
