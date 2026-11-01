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
		t.Fatal("the hash must depend on the signing secret")
	}
}

// Pinned value (checked against an independent Python HMAC): the rollup (stage A5) and any later glue recompute the same
// hash, so a change of the scheme must be a deliberate, visible edit.
func TestTGHasher_PinnedValue(t *testing.T) {
	const want = "f90fca5baf8b201c1baa4f516b1c891d23e3a72c39f246ee18cef7a7c361ca6d"
	got := NewTGHasher("test-secret").Hash(1)
	if got != want {
		t.Fatalf("hash changed: %s", got)
	}
}
