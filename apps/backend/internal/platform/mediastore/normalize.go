package mediastore

// normalize.go — a PNG copy of a picture in a format other readers do not take
// (WebP, TIFF, BMP, GIF), for the hall plan intake (spec 64 section 2.1: the
// server turns every common picture format into PNG before the generator).
// Decoding is the expensive part, so it runs under the same render slots as
// the resized copies (variants.go), and the pixel count is checked from the
// header BEFORE anything is decoded.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder
	"image/png"

	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/bmp"  // registers the BMP decoder
	_ "golang.org/x/image/tiff" // registers the TIFF decoder
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

// ErrTooManyPixels: the picture's header names more pixels than allowed; it
// was not decoded.
var ErrTooManyPixels = errors.New("mediastore: the picture has too many pixels")

// ErrNotAPicture: the bytes are not a picture any registered decoder reads.
var ErrNotAPicture = errors.New("mediastore: the bytes are not a readable picture")

// PictureConfig reads the format name and the pixel size of a picture from its
// header only (JPEG, PNG, GIF, WebP, TIFF, BMP). It never decodes the pixels.
func PictureConfig(raw []byte) (format string, width, height int, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return "", 0, 0, ErrNotAPicture
	}
	return format, cfg.Width, cfg.Height, nil
}

// NormalizeToPNG decodes a picture (the first frame of a GIF) and encodes it as
// PNG. The pixel count is read from the header first: a picture over
// maxPixels answers ErrTooManyPixels without being decoded. The decode runs
// under one of the Repo's render slots, so a burst of uploads cannot decode
// many large pictures at once. It returns the PNG bytes and the pixel size.
func (r *Repo) NormalizeToPNG(ctx context.Context, raw []byte, maxPixels int) (out []byte, width, height int, err error) {
	_, w, h, err := PictureConfig(raw)
	if err != nil {
		return nil, 0, 0, err
	}
	if maxPixels > 0 && w*h > maxPixels {
		return nil, w, h, ErrTooManyPixels
	}
	release, err := r.acquireRender(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	defer release()
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: %v", ErrNotAPicture, err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, fmt.Errorf("mediastore: encode png: %w", err)
	}
	return buf.Bytes(), w, h, nil
}

// RowQueryer is a pool or a transaction: InsertTx writes through it.
type RowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// InsertTx is Insert inside the caller's transaction, so the media row and the
// rows that point at it commit or roll back together. It queues no resized
// copies (the hall plan intake's files are never served through the public
// download route).
func (r *Repo) InsertTx(ctx context.Context, db RowQueryer, in InsertInput) (Object, error) {
	var obj Object
	err := db.QueryRow(ctx, insertObjectSQL,
		in.OrgID, in.OwnerType, in.OwnerID, in.StorageBackend, in.StorageKey,
		in.ContentType, in.ByteSize, in.ChecksumSHA256, in.Width, in.Height,
	).Scan(
		&obj.ID, &obj.OrgID, &obj.OwnerType, &obj.OwnerID,
		&obj.StorageBackend, &obj.StorageKey, &obj.ContentType, &obj.ByteSize,
		&obj.ChecksumSHA256, &obj.Width, &obj.Height,
		&obj.CreatedAt, &obj.DeletedAt,
	)
	if err != nil {
		return Object{}, fmt.Errorf("mediastore: insert: %w", err)
	}
	return obj, nil
}
