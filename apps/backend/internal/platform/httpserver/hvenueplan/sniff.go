package hvenueplan

// sniff.go — what a sent file is, decided by its CONTENT (spec 64 section
// 2.1); the file name and the declared Content-Type prove nothing.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"regexp"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

const mb = 1 << 20

// Limits of spec 64 section 2.1 (revision 2).
const (
	maxFiles       = 10
	maxTotalBytes  = 80 * mb
	maxFileBytes   = 20 * mb // the largest per-kind limit
	maxPDFPages    = 20
	maxPixels      = 40_000_000
	maxXLSXUnzip   = 200 * mb
	multipartSlack = 1 * mb // headers and boundaries around the files
)

// kindLimit is the biggest file of a kind, in bytes.
func kindLimit(k venueplan.SourceKind) int64 {
	switch k {
	case venueplan.SourceXLSX, venueplan.SourceXLS:
		return 10 * mb
	case venueplan.SourceCSV:
		return 5 * mb
	case venueplan.SourceSVG:
		return 3 * mb
	}
	return 20 * mb // pdf and every picture
}

// contentTypeOf is the stored media type of a kind.
func contentTypeOf(k venueplan.SourceKind) string {
	switch k {
	case venueplan.SourcePDF:
		return "application/pdf"
	case venueplan.SourceXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case venueplan.SourceXLS:
		return "application/vnd.ms-excel"
	case venueplan.SourceCSV:
		return "text/csv"
	case venueplan.SourceSVG:
		return "image/svg+xml"
	}
	return "image/" + string(k)
}

// isPicture reports whether a kind is a raster picture with a pixel size.
func isPicture(k venueplan.SourceKind) bool {
	switch k {
	case venueplan.SourceJPEG, venueplan.SourcePNG, venueplan.SourceWEBP, venueplan.SourceTIFF,
		venueplan.SourceBMP, venueplan.SourceGIF:
		return true
	}
	return false
}

// needsPNGCopy: pictures the generator does not read directly get a PNG copy.
func needsPNGCopy(k venueplan.SourceKind) bool {
	switch k {
	case venueplan.SourceWEBP, venueplan.SourceTIFF, venueplan.SourceBMP, venueplan.SourceGIF:
		return true
	}
	return false
}

// needsConversion: formats stored as received and converted later (no Go
// decoder for HEIC, old Excel is turned into XLSX by the generator side).
func needsConversion(k venueplan.SourceKind) bool {
	return k == venueplan.SourceHEIC || k == venueplan.SourceXLS
}

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// heicBrands are the ISO BMFF major brands of HEIF/HEIC pictures.
var heicBrands = map[string]bool{
	"heic": true, "heix": true, "hevc": true, "hevx": true, "heim": true, "heis": true,
	"hevm": true, "hevs": true, "mif1": true, "msf1": true, "avif": false,
}

// sniffKind names the kind of data, or "" when it is none of the accepted
// formats.
func sniffKind(data []byte) venueplan.SourceKind {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return venueplan.SourceJPEG
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return venueplan.SourcePNG
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return venueplan.SourceGIF
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return venueplan.SourceWEBP
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return venueplan.SourceTIFF
	case bytes.HasPrefix(data, []byte("BM")) && len(data) >= 26:
		return venueplan.SourceBMP
	case len(data) >= 12 && string(data[4:8]) == "ftyp" && heicBrands[string(data[8:12])]:
		return venueplan.SourceHEIC
	case bytes.HasPrefix(data, oleMagic):
		return venueplan.SourceXLS
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		if isXLSX(data) {
			return venueplan.SourceXLSX
		}
		return ""
	}
	if bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")) {
		return venueplan.SourcePDF
	}
	if looksLikeSVG(data) {
		return venueplan.SourceSVG
	}
	if looksLikeCSV(data) {
		return venueplan.SourceCSV
	}
	return ""
}

// isXLSX reports whether a zip holds an Excel workbook (xl/workbook.xml).
func isXLSX(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == "xl/workbook.xml" {
			return true
		}
	}
	return false
}

// xlsxUnpackedSize is the declared unpacked size of every entry of the zip.
func xlsxUnpackedSize(data []byte) uint64 {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}
	var n uint64
	for _, f := range zr.File {
		n += f.UncompressedSize64
	}
	return n
}

// pdfPagePattern counts page objects (/Type /Page, not /Pages). A PDF whose
// page objects sit in compressed object streams counts 0: the count is then
// unknown, not refused.
var pdfPagePattern = regexp.MustCompile(`/Type\s*/Page[^s]`)

func pdfPages(data []byte) int {
	return len(pdfPagePattern.FindAllIndex(data, maxPDFPages*50))
}

// looksLikeSVG reports whether the bytes are an XML document whose root
// element is <svg> (the same rule as the Telegram bot's plan upload).
func looksLikeSVG(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 || !utf8.Valid(data[:min(len(data), 4096)]) {
		return false
	}
	//nolint:gosec // G709: only the first 64 tokens are read to find the root element, nothing is decoded into a value
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for i := 0; i < 64; i++ {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "svg"
		}
	}
	return false
}

// looksLikeCSV reports whether the bytes are UTF-8 text without NUL bytes
// whose first line holds a separator (comma, semicolon or tab).
func looksLikeCSV(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	head := data[:min(len(data), 64<<10)]
	if len(bytes.TrimSpace(head)) == 0 || bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	// A cut in the middle of a rune at the end of head is not a reason to refuse.
	for i := 0; i < 3 && !utf8.Valid(head) && len(head) > 0; i++ {
		head = head[:len(head)-1]
	}
	if !utf8.Valid(head) {
		return false
	}
	first := head
	if i := bytes.IndexAny(head, "\r\n"); i >= 0 {
		first = head[:i]
	}
	if bytes.HasPrefix(bytes.TrimSpace(first), []byte("<")) {
		return false
	}
	return bytes.ContainsAny(first, ",;\t")
}
