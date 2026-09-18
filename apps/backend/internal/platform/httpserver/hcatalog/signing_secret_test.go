package hcatalog

import (
	"encoding/hex"
	"testing"
)

func TestResolveSigningSecret_KeepsSuppliedValue(t *testing.T) {
	got, err := resolveSigningSecret("  operator-chosen-secret  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "operator-chosen-secret" {
		t.Fatalf("supplied secret must be kept (trimmed), got %q", got)
	}
}

func TestResolveSigningSecret_GeneratesWhenBlank(t *testing.T) {
	for _, in := range []string{"", "   "} {
		got, err := resolveSigningSecret(in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2*signingSecretBytes {
			t.Fatalf("generated secret length = %d, want %d", len(got), 2*signingSecretBytes)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Fatalf("generated secret must be hex: %v", err)
		}
	}
	a, _ := resolveSigningSecret("")
	b, _ := resolveSigningSecret("")
	if a == b {
		t.Fatal("two generated secrets must differ")
	}
}
