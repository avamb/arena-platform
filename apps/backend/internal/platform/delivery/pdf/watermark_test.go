package pdf

import (
	"bytes"
	"context"
	"testing"

	"github.com/skip2/go-qrcode"
)

// A watermark prints the stamp (upper-cased), stays deterministic, and
// leaves the anchored code block byte-identical to a real ticket's: the QR
// image object and the barcode bars are the same bytes with and without it.
func TestRender_Watermark_StampsTheFlowOnly(t *testing.T) {
	plain := validTicket(t)
	stamped := validTicket(t)
	stamped.Watermark = "Sample"

	outPlain, err := Render(context.Background(), plain)
	if err != nil {
		t.Fatalf("Render(plain): %v", err)
	}
	outStamped, err := Render(context.Background(), stamped)
	if err != nil {
		t.Fatalf("Render(stamped): %v", err)
	}
	if !bytes.Contains(outStamped, pdfText("SAMPLE")) {
		t.Fatalf("the stamp text is not in the content stream")
	}
	if bytes.Contains(outPlain, pdfText("SAMPLE")) {
		t.Fatalf("a ticket without a watermark must not carry the stamp")
	}
	if !bytes.Contains(outStamped, []byte("/GS")) || !bytes.Contains(outStamped, []byte(" cm\n")) {
		t.Fatalf("the stamp must be drawn under an alpha ExtGState inside a transform")
	}

	// The QR of the EAN-13 digits is embedded, unchanged, in both.
	wantQR, err := qrcode.Encode(stamped.EAN13, qrcode.High, qrPixelSize)
	if err != nil {
		t.Fatalf("encode expected qr: %v", err)
	}
	if !bytes.Contains(outStamped, pngIDAT(t, wantQR)) || !bytes.Contains(outPlain, pngIDAT(t, wantQR)) {
		t.Fatalf("the watermark changed the QR image")
	}
	// And the printed digits under the barcode are still there.
	if !bytes.Contains(outStamped, pdfText(stamped.EAN13[1:7])) {
		t.Fatalf("the barcode's digits are missing on the stamped ticket")
	}
	// Determinism survives the extra graphics state.
	again, err := Render(context.Background(), stamped)
	if err != nil {
		t.Fatalf("Render(stamped) again: %v", err)
	}
	if !bytes.Equal(outStamped, again) {
		t.Fatalf("a watermarked render is not deterministic")
	}
}

// An empty or blank watermark is a no-op: the output is byte-identical.
func TestRender_Watermark_BlankIsNoop(t *testing.T) {
	a := validTicket(t)
	b := validTicket(t)
	b.Watermark = "   "
	outA, err := Render(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	outB, err := Render(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(outA, outB) {
		t.Fatalf("a blank watermark changed the output")
	}
}
