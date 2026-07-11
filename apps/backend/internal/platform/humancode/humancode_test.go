// humancode_test.go — SEAT-C4 property + normalization tests.
//
// The core acceptance property: 10 000 generated codes, none of which
// can be parsed as a number by Excel/CSV tooling — no code matches
// ^[0-9]+$ (plain integer) nor ^[0-9]+[eE][0-9]+$ (scientific notation).
// Both are guaranteed by construction (first character is always a
// letter, and E-notation would additionally require a digit-leading
// prefix), but the property test pins the guarantee against future
// generator edits.
package humancode

import (
	"regexp"
	"strings"
	"testing"
)

var (
	rePlainNumber  = regexp.MustCompile(`^[0-9]+$`)
	reSciNotation  = regexp.MustCompile(`^[0-9]+[eE][0-9]+$`)
	reCanonical    = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{8}$`)
	reFirstLetter  = regexp.MustCompile(`^[ABCDEFGHJKMNPQRSTVWXYZ]`)
	reDisplayShape = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{4}-[0-9ABCDEFGHJKMNPQRSTVWXYZ]{4}$`)
)

// TestGenerate_Property10k pins the SEAT-C4 acceptance property over
// 10 000 generated codes: Excel-safe (never numeric, never scientific
// notation), canonical Crockford alphabet, leading letter.
func TestGenerate_Property10k(t *testing.T) {
	const n = 10_000
	for i := 0; i < n; i++ {
		code, err := Generate()
		if err != nil {
			t.Fatalf("Generate() #%d: %v", i, err)
		}
		if rePlainNumber.MatchString(code) {
			t.Fatalf("code %q parses as a plain number", code)
		}
		if reSciNotation.MatchString(code) {
			t.Fatalf("code %q parses as scientific notation", code)
		}
		if !reCanonical.MatchString(code) {
			t.Fatalf("code %q is not 8 chars of the Crockford alphabet", code)
		}
		if !reFirstLetter.MatchString(code) {
			t.Fatalf("code %q does not start with a letter", code)
		}
		for _, banned := range "ILOU" {
			if strings.ContainsRune(code, banned) {
				t.Fatalf("code %q contains banned look-alike %q", code, banned)
			}
		}
	}
}

// TestGenerate_ReasonablyUnique is a sanity check that the generator
// draws from crypto/rand rather than a constant: 1 000 codes must be
// pairwise distinct (collision odds at 32^8 ≈ 1.1e12 are negligible).
func TestGenerate_ReasonablyUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		code, err := Generate()
		if err != nil {
			t.Fatalf("Generate(): %v", err)
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("duplicate code %q within 1000 draws", code)
		}
		seen[code] = struct{}{}
	}
}

func TestFormat_GroupsFourFour(t *testing.T) {
	if got := Format("M7KT2QV9"); got != "M7KT-2QV9" {
		t.Fatalf("Format = %q, want M7KT-2QV9", got)
	}
	// Non-canonical input passes through untouched.
	if got := Format("SHORT"); got != "SHORT" {
		t.Fatalf("Format(SHORT) = %q, want passthrough", got)
	}
}

func TestFormat_OfGeneratedCodeMatchesDisplayShape(t *testing.T) {
	code, err := Generate()
	if err != nil {
		t.Fatalf("Generate(): %v", err)
	}
	if !reDisplayShape.MatchString(Format(code)) {
		t.Fatalf("Format(%q) = %q does not match XXXX-XXXX", code, Format(code))
	}
}

func TestNormalize_RoundTripsGeneratedCodes(t *testing.T) {
	for i := 0; i < 100; i++ {
		code, err := Generate()
		if err != nil {
			t.Fatalf("Generate(): %v", err)
		}
		for _, variant := range []string{
			code,
			Format(code),
			strings.ToLower(Format(code)),
			" " + code[:4] + " " + code[4:] + " ",
		} {
			got, ok := Normalize(variant)
			if !ok || got != code {
				t.Fatalf("Normalize(%q) = (%q, %v), want (%q, true)", variant, got, ok, code)
			}
		}
	}
}

func TestNormalize_MapsCrockfordAliases(t *testing.T) {
	cases := map[string]string{
		"m7kt-2qvi": "M7KT2QV1", // i → 1
		"M7KT-2QVL": "M7KT2QV1", // L → 1
		"M7KT-2QVO": "M7KT2QV0", // O → 0
		"m7kt 2qv9": "M7KT2QV9", // space grouping
	}
	for in, want := range cases {
		got, ok := Normalize(in)
		if !ok || got != want {
			t.Errorf("Normalize(%q) = (%q, %v), want (%q, true)", in, got, ok, want)
		}
	}
}

func TestNormalize_RejectsNonCodes(t *testing.T) {
	for _, in := range []string{
		"",
		"M7KT",      // too short
		"M7KT2QV9X", // too long
		"M7KT-2QVU", // U has no alias and is not in the alphabet
		"M7KT_2QV9", // underscore is not grouping noise
		"8d3b1c9a4f7e6a2c5d8b3f1e9c7a4d6b2f8e1c5a7d9b3e6f2a4c8d1b5e7f9a3c", // raw QR token
	} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = (%q, true), want rejection", in, got)
		}
	}
}
