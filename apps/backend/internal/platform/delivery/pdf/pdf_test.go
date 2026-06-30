package pdf

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// makePNG returns a minimal NxN solid-colour PNG suitable for use as a
// fake organisation logo in tests.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func validTicket(t *testing.T) Ticket {
	t.Helper()
	return Ticket{
		TicketID:     "11111111-2222-3333-4444-555555555555",
		EventName:    "Spring Symphony Gala",
		SessionStart: time.Date(2026, 5, 12, 18, 30, 0, 0, time.UTC),
		SessionTZ:    "Europe/Moscow",
		VenueName:    "Tchaikovsky Hall",
		VenueCity:    "Moscow",
		TierName:     "Stalls — Row C",
		HolderName:   "Ivan Petrov",
		OrgLogo:      makePNG(t, 200, 100),
		QRPayload:    "TKT-CRED-CODE-ABCDEF0123456789",
	}
}

func TestRender_ReturnsPDFBytes(t *testing.T) {
	out, err := Render(context.Background(), validTicket(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) < 200 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(out))
	}
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF (missing %%PDF- header): %q", out[:8])
	}
	if !bytes.Contains(out, []byte("%%EOF")) {
		t.Fatalf("PDF missing %%EOF trailer")
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	tk := validTicket(t)
	a, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render a: %v", err)
	}
	b, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Render is not deterministic: produced %d vs %d bytes that differ",
			len(a), len(b))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Feature #290 (T-3) Branding hooks on PDF e-tickets
// ──────────────────────────────────────────────────────────────────────────────

func TestRender_Branding_FooterLegalBlockEmittedWhenLegalNameSet(t *testing.T) {
	tk := validTicket(t)
	tk.OrgName = "Globe Theatre"
	tk.OrgWebsiteURL = "https://globe.example.com"
	tk.LegalName = "Globe Theatre Ltd"
	tk.LegalAddressLine1 = "21 New Globe Walk"
	tk.LegalAddressCity = "London"
	tk.LegalAddressCountry = "GB"
	tk.ContactEmail = "hello@globe.example.com"
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) < 200 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(out))
	}
	// The PDF text-content stream is uncompressed (SetCompression(false))
	// so the legal-block lines must appear verbatim in the bytes.
	for _, want := range []string{
		"Globe Theatre", "Globe Theatre Ltd", "21 New Globe Walk",
		"London", "GB", "Contact: hello@globe.example.com",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("PDF missing branding text %q", want)
		}
	}
}

func TestRender_Branding_NoLegalNameOmitsFooterBlock(t *testing.T) {
	tk := validTicket(t)
	// No branding fields set. Expectation: PDF still renders (fine-print
	// disclaimer prints) but no legal block.
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if bytes.Contains(out, []byte("Contact:")) {
		t.Errorf("PDF should not contain Contact: line when ContactEmail is empty")
	}
}

func TestRender_Branding_OrgNameOverridesDefaultWordmark(t *testing.T) {
	tk := validTicket(t)
	tk.OrgName = "Acme Productions"
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Contains(out, []byte("Acme Productions")) {
		t.Error("PDF missing OrgName wordmark")
	}
	if bytes.Contains(out, []byte("Arena E-Ticket")) {
		t.Error("PDF should not contain default wordmark when OrgName is set")
	}
}

func TestRender_Branding_NoOrgNameFallsBackToDefaultWordmark(t *testing.T) {
	tk := validTicket(t)
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Contains(out, []byte("Arena E-Ticket")) {
		t.Error("PDF missing default wordmark when OrgName empty")
	}
}

func TestBuildLegalLines_AssemblesAddressBlock(t *testing.T) {
	cases := []struct {
		name string
		in   Ticket
		want []string
	}{
		{
			name: "empty input",
			in:   Ticket{},
			want: []string{},
		},
		{
			name: "legal name only",
			in:   Ticket{LegalName: "X Ltd"},
			want: []string{"X Ltd"},
		},
		{
			name: "postal + city combined",
			in: Ticket{
				LegalName:              "X Ltd",
				LegalAddressPostalCode: "SE1 9DT",
				LegalAddressCity:       "London",
			},
			want: []string{"X Ltd", "SE1 9DT London"},
		},
		{
			name: "postal only",
			in:   Ticket{LegalAddressPostalCode: "10115"},
			want: []string{"10115"},
		},
		{
			name: "city only",
			in:   Ticket{LegalAddressCity: "Berlin"},
			want: []string{"Berlin"},
		},
		{
			name: "all fields",
			in: Ticket{
				LegalName:              "X Ltd",
				LegalAddressLine1:      "1 Road",
				LegalAddressLine2:      "Floor 2",
				LegalAddressPostalCode: "10115",
				LegalAddressCity:       "Berlin",
				LegalAddressCountry:    "DE",
				ContactEmail:           "hi@x.example",
			},
			want: []string{
				"X Ltd", "1 Road", "Floor 2", "10115 Berlin", "DE",
				"Contact: hi@x.example",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildLegalLines(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestRender_NoLogoOK(t *testing.T) {
	tk := validTicket(t)
	tk.OrgLogo = nil
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty PDF without a logo")
	}
}

func TestRender_BadLogoIsSkippedNotFatal(t *testing.T) {
	tk := validTicket(t)
	tk.OrgLogo = []byte("this is not an image at all")
	// Should NOT error — the renderer treats a corrupt logo as "no logo".
	out, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render unexpectedly errored on bad logo bytes: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty output on bad logo")
	}
}

func TestRender_RejectsMissingRequired(t *testing.T) {
	cases := map[string]func(*Ticket){
		"ticket_id":     func(t *Ticket) { t.TicketID = "" },
		"event_name":    func(t *Ticket) { t.EventName = "" },
		"qr_payload":    func(t *Ticket) { t.QRPayload = "" },
		"session_start": func(t *Ticket) { t.SessionStart = time.Time{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			tk := validTicket(t)
			mut(&tk)
			_, err := Render(context.Background(), tk)
			if err == nil {
				t.Fatalf("expected ErrInvalidTicket when %s is missing", name)
			}
			if !errors.Is(err, ErrInvalidTicket) {
				t.Fatalf("expected ErrInvalidTicket, got %v", err)
			}
		})
	}
}

func TestRender_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Render(ctx, validTicket(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFormatSessionInVenueTZ(t *testing.T) {
	utcSession := time.Date(2026, 5, 12, 18, 30, 0, 0, time.UTC)

	t.Run("known zone shifts time", func(t *testing.T) {
		got := formatSessionInVenueTZ(utcSession, "Europe/Moscow")
		// Moscow is UTC+3 year-round.
		want := "2026-05-12 21:30 (Europe/Moscow)"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("empty tz falls back to UTC", func(t *testing.T) {
		got := formatSessionInVenueTZ(utcSession, "")
		want := "2026-05-12 18:30 (UTC)"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("invalid tz falls back to UTC, never panics", func(t *testing.T) {
		got := formatSessionInVenueTZ(utcSession, "Not/A/Real/Zone")
		want := "2026-05-12 18:30 (UTC)"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

func TestJoinNonEmpty(t *testing.T) {
	if got := joinNonEmpty(", ", "A", "", "B"); got != "A, B" {
		t.Fatalf("got %q", got)
	}
	if got := joinNonEmpty(", ", "", "  ", ""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := joinNonEmpty(", ", "only"); got != "only" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectImageType(t *testing.T) {
	t.Run("png detected", func(t *testing.T) {
		got, ok := detectImageType(makePNG(t, 4, 4))
		if !ok || got != "PNG" {
			t.Fatalf("got (%q, %v)", got, ok)
		}
	})
	t.Run("junk rejected", func(t *testing.T) {
		_, ok := detectImageType([]byte("definitely not an image"))
		if ok {
			t.Fatal("expected not-ok for junk bytes")
		}
	})
	t.Run("too short rejected", func(t *testing.T) {
		_, ok := detectImageType([]byte{0x01, 0x02})
		if ok {
			t.Fatal("expected not-ok for tiny input")
		}
	})
}

func TestDefaultFinePrint_NoFiscalReceiptLanguage(t *testing.T) {
	// Guardrail: the default fine print must explicitly mention that the
	// PDF is not a fiscal receipt and must not accidentally include
	// fiscal-receipt boilerplate.
	low := DefaultFinePrint
	for _, banned := range []string{"кассовый чек", "fiscal receipt valid", "tax invoice"} {
		if bytes.Contains([]byte(low), []byte(banned)) {
			t.Fatalf("default fine print contains banned fragment %q", banned)
		}
	}
	if !bytes.Contains([]byte(low), []byte("not a fiscal receipt")) {
		t.Fatalf("default fine print must disclose that the document is not a fiscal receipt")
	}
}
