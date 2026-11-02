package analytics

import (
	"strings"
	"testing"
)

func TestTGHasher_StableKeyedAndNeverTheRawID(t *testing.T) {
	h := NewTGHasher("secret-one")
	a := h.Hash(446002546)
	if a != h.Hash(446002546) {
		t.Fatal("the hash must be stable")
	}
	if len(a) != 64 || !idValueRe.MatchString(a) {
		t.Fatalf("hash %q is not a 64-char identifier the writer accepts", a)
	}
	if strings.Contains(a, "446002546") {
		t.Fatal("the raw Telegram id leaks into the hash")
	}
	if a == h.Hash(446002547) {
		t.Fatal("different ids must hash differently")
	}
	if a == NewTGHasher("secret-two").Hash(446002546) {
		t.Fatal("the hash must depend on the secret")
	}
}

// Pinned value (checked against an independent Python HMAC): the rollup (stage A5) and any later glue recompute the same
// hash, so a change of the scheme must be a deliberate, visible edit.
func TestTGHasher_PinnedValue(t *testing.T) {
	const want = "8a2b97cc7e0c8bab46a1856b7d3539b3eafac61f2711f7156faa9e0a064e36d5"
	got := NewTGHasher("test-secret").Hash(1)
	if got != want {
		t.Fatalf("hash changed: %s", got)
	}
}
