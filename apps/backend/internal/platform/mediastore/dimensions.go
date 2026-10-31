package mediastore

// dimensions.go — the pixel size of an uploaded picture, read from its
// header only (image.DecodeConfig never decodes the pixels). The storefront
// needs it to reserve a card's aspect before the picture loads.

import (
	"bufio"
	"context"
	"encoding/binary"
	"image"
	_ "image/gif"  // registers the GIF header reader for image.DecodeConfig
	_ "image/jpeg" // registers the JPEG header reader
	_ "image/png"  // registers the PNG header reader
	"io"

	"github.com/google/uuid"
)

// maxDimension bounds a believable picture side; anything larger is not
// recorded (a corrupt header, not a picture).
const maxDimension = 100000

// ImageDimensions reads the width and height of a JPEG, PNG, GIF or WebP
// picture from r's header. Both are nil when r is not one of those or its
// header cannot be read: the dimensions are a hint, never a reason to refuse
// an upload.
func ImageDimensions(r io.Reader) (width, height *int32) {
	br := bufio.NewReaderSize(r, 64)
	if head, err := br.Peek(30); err == nil && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP" {
		return sized(webpDimensions(head))
	}
	cfg, _, err := image.DecodeConfig(br)
	if err != nil {
		return nil, nil
	}
	return sized(cfg.Width, cfg.Height)
}

func sized(w, h int) (*int32, *int32) {
	if w < 1 || h < 1 || w > maxDimension || h > maxDimension {
		return nil, nil
	}
	ww, hh := int32(w), int32(h) //nolint:gosec // bounded by maxDimension above
	return &ww, &hh
}

// webpDimensions reads the canvas size of the three WebP flavours from the
// first 30 bytes (RIFF header + the first chunk): VP8X (extended), VP8
// (lossy) and VP8L (lossless).
func webpDimensions(b []byte) (int, int) {
	switch string(b[12:16]) {
	case "VP8X":
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1
	case "VP8 ":
		// frame tag (3 bytes) + start code 9d 01 2a, then 14-bit sizes.
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0
		}
		w := int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return w, h
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1
	}
	return 0, 0
}

// SetDimensionsIfMissing records a picture's size on its media object when
// the object has none yet (objects uploaded before dimensions were read).
// It never overwrites a recorded size.
func (r *Repo) SetDimensionsIfMissing(ctx context.Context, id uuid.UUID, width, height int32) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE media_objects SET width = $2, height = $3
		WHERE id = $1 AND (width IS NULL OR height IS NULL)`, id, width, height)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
