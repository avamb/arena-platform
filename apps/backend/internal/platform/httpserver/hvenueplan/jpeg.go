package hvenueplan

// jpeg.go — a JPEG loses its metadata before it is stored (spec 64 section
// 2.1: EXIF, GPS position included). The picture bytes are kept as they are:
// only the metadata segments are left out. The EXIF orientation alone is kept,
// in a minimal EXIF block of its own, so a phone photo still shows upright.

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var errBadJPEG = errors.New("the JPEG structure cannot be read")

const (
	markerSOI  = 0xD8
	markerEOI  = 0xD9
	markerSOS  = 0xDA
	markerAPP1 = 0xE1 // EXIF, XMP
	markerAPPD = 0xED // Photoshop IRB, IPTC
	markerCOM  = 0xFE // free-text comment
)

// stripJPEGMetadata returns the JPEG without its APP1 (EXIF and XMP), APP13
// (IPTC) and comment segments, and the EXIF orientation it had (0 = none). A
// minimal EXIF block with the orientation only replaces the first EXIF block
// when the orientation is not 1.
func stripJPEGMetadata(in []byte) (out []byte, orientation int, err error) {
	if len(in) < 4 || in[0] != 0xFF || in[1] != markerSOI {
		return nil, 0, errBadJPEG
	}
	var buf bytes.Buffer
	buf.Grow(len(in))
	buf.Write(in[:2])
	exifSlot := -1
	i := 2
	for i < len(in) {
		if in[i] != 0xFF {
			return nil, 0, errBadJPEG
		}
		// Fill bytes: any number of 0xFF before a marker.
		for i < len(in) && in[i] == 0xFF {
			i++
		}
		if i >= len(in) {
			return nil, 0, errBadJPEG
		}
		marker := in[i]
		i++
		switch {
		case marker == markerEOI:
			buf.Write([]byte{0xFF, marker})
			return finishJPEG(buf.Bytes(), exifSlot, orientation), orientation, nil
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01:
			buf.Write([]byte{0xFF, marker})
			continue
		}
		if i+2 > len(in) {
			return nil, 0, errBadJPEG
		}
		segLen := int(binary.BigEndian.Uint16(in[i : i+2]))
		if segLen < 2 || i+segLen > len(in) {
			return nil, 0, errBadJPEG
		}
		payload := in[i+2 : i+segLen]
		switch marker {
		case markerAPP1:
			if bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				if o := exifOrientation(payload[6:]); o > 0 && orientation == 0 {
					orientation = o
				}
				if exifSlot < 0 {
					exifSlot = buf.Len()
				}
			}
		case markerAPPD, markerCOM:
		default:
			buf.Write([]byte{0xFF, marker})
			buf.Write(in[i : i+segLen])
		}
		i += segLen
		if marker == markerSOS {
			// The entropy-coded data of the scan is the picture. It ends at the
			// next marker that is not a stuffed byte or a restart: further
			// segments and scans (a progressive JPEG) go through this loop, and
			// the picture ends at the first EOI. Anything a phone appends after
			// it (MPF pictures with their own EXIF) is dropped.
			end := scanEnd(in, i)
			buf.Write(in[i:end])
			i = end
			if i >= len(in) {
				// No EOI: keep the picture as it was sent.
				return finishJPEG(buf.Bytes(), exifSlot, orientation), orientation, nil
			}
		}
	}
	return nil, 0, errBadJPEG
}

// scanEnd returns the index of the 0xFF that starts the first real marker
// after the entropy-coded data beginning at i, or len(in) when there is none.
// 0xFF 0x00 (a stuffed byte) and 0xFF 0xD0..0xD7 (restarts) belong to the data.
func scanEnd(in []byte, i int) int {
	for j := i; j+1 < len(in); j++ {
		if in[j] != 0xFF {
			continue
		}
		next := in[j+1]
		if next == 0x00 || (next >= 0xD0 && next <= 0xD7) {
			j++
			continue
		}
		return j
	}
	return len(in)
}

// finishJPEG puts the minimal orientation block where the first EXIF block was.
func finishJPEG(b []byte, slot, orientation int) []byte {
	if slot < 0 || orientation <= 1 || orientation > 8 {
		return b
	}
	seg := orientationSegment(orientation)
	out := make([]byte, 0, len(b)+len(seg))
	out = append(out, b[:slot]...)
	out = append(out, seg...)
	return append(out, b[slot:]...)
}

// orientationSegment is an APP1 EXIF block holding only IFD0 with the
// Orientation tag (big-endian TIFF).
func orientationSegment(orientation int) []byte {
	body := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08")
	body = append(body, 0x00, 0x01)                          // one entry
	body = append(body, 0x01, 0x12, 0x00, 0x03)              // Orientation, SHORT
	body = append(body, 0x00, 0x00, 0x00, 0x01)              // count 1
	body = append(body, 0x00, byte(orientation), 0x00, 0x00) //nolint:gosec // value, an EXIF orientation is 1..8
	body = append(body, 0x00, 0x00, 0x00, 0x00)              // no next IFD
	seg := []byte{0xFF, markerAPP1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(body)+2)) //nolint:gosec // a few dozen bytes
	return append(seg, body...)
}

// exifOrientation reads tag 0x0112 of IFD0 from a TIFF block, 0 when absent.
func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int(bo.Uint32(t[4:8])) //nolint:gosec // G602: len(t) >= 8 is checked above
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off : off+2])) //nolint:gosec // G602: off+2 <= len(t) is checked above
	for e := 0; e < n; e++ {
		p := off + 2 + e*12
		if p+12 > len(t) {
			return 0
		}
		//nolint:gosec // G602: p+12 <= len(t) is checked above
		if bo.Uint16(t[p:p+2]) == 0x0112 && bo.Uint16(t[p+2:p+4]) == 3 {
			v := int(bo.Uint16(t[p+8 : p+10])) //nolint:gosec // G602: same bound
			if v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}
