package ean13

import "testing"

// TestValid_RealBil24Code pins the checksum algorithm against a real-world
// EAN-13 code (a Bil24 ticket barcode, spec §11) so a weight/rounding
// regression in checkDigit is caught even though the platform never mints
// this exact number itself (Bil24 codes use the "24…" prefix range, not
// the platform's "21…").
func TestValid_RealBil24Code(t *testing.T) {
	const real = "2402604868419"
	if !Valid(real) {
		t.Fatalf("Valid(%q) = false, want true", real)
	}
	// Flipping the check digit must invalidate the code.
	tampered := real[:12] + "0"
	if tampered == real {
		tampered = real[:12] + "1"
	}
	if Valid(tampered) {
		t.Fatalf("Valid(%q) = true, want false (tampered check digit)", tampered)
	}
}

// TestEncode_ProducesValidCheckDigit generates 100 codes across a spread of
// ticket ids and asserts every one is both 13 digits long, prefixed with
// "21", and passes Valid — the property the ticket_credentials_ean13_shape
// CHECK (migration 0093) and IssueTicketsForCheckout both rely on.
func TestEncode_ProducesValidCheckDigit(t *testing.T) {
	for i := 0; i < 100; i++ {
		n := int64(i)*1_000_003 + 1_000_000_000 // spread across the compat id range too
		code := Encode("21", n)
		if len(code) != 13 {
			t.Fatalf("Encode(%q, %d) = %q, want length 13", "21", n, code)
		}
		if code[:2] != "21" {
			t.Fatalf("Encode(%q, %d) = %q, want prefix %q", "21", n, code, "21")
		}
		if !Valid(code) {
			t.Fatalf("Valid(Encode(%q, %d)) = false, want true (code=%q)", "21", n, code)
		}
	}
}

// TestEncode_ZeroPadsTo13Digits checks the small-id case explicitly: a
// freshly-minted ticket with system_ticket_id=1 must still produce a full
// 13-digit code (the 10-digit body is zero-padded, not left short).
func TestEncode_ZeroPadsTo13Digits(t *testing.T) {
	code := Encode("21", 1)
	const want = "210000000001"
	if code[:12] != want {
		t.Fatalf("Encode(%q, 1) = %q, want body %q", "21", code, want)
	}
	if len(code) != 13 {
		t.Fatalf("Encode(%q, 1) = %q, want length 13", "21", code)
	}
	if !Valid(code) {
		t.Fatalf("Valid(%q) = false, want true", code)
	}
}

// TestValid_RejectsWrongLength and non-digit input guard the shape check
// that mirrors the DB-side CHECK (migration 0093).
func TestValid_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"123",
		"24026048684199", // too long
		"240260486841",   // too short (12 digits)
		"240260486841a",  // non-digit
		"24026048684-9",  // non-digit
	}
	for _, c := range cases {
		if Valid(c) {
			t.Errorf("Valid(%q) = true, want false", c)
		}
	}
}
