package holdpolicy

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func TestPageLifetime_FloorAndSum(t *testing.T) {
	// Defaults: 900 + 300 x 1 + 600 = 30 min, below the floor with margin.
	if got := Default().PageLifetime(); got != MinProviderPageLifetime {
		t.Fatalf("default page lifetime = %v, want %v", got, MinProviderPageLifetime)
	}
	p := Default()
	p.Payment = 1800 * time.Second
	p.Extend = 300 * time.Second
	p.ExtendCount = 2
	p.Paying = 600 * time.Second
	if got, want := p.PageLifetime(), 50*time.Minute; got != want {
		t.Fatalf("page lifetime = %v, want %v", got, want)
	}
}

func TestHashHoldToken_IsSHA256OfTheString(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	if got := HashHoldToken("abc"); string(got) != string(sum[:]) {
		t.Fatalf("hash = %x, want %x", got, sum)
	}
}

func TestFromSnapshot_DefaultsAndExtraKeys(t *testing.T) {
	if got := FromSnapshot(nil); got != Default() {
		t.Fatalf("nil snapshot = %+v", got)
	}
	if got := FromSnapshot([]byte("not json")); got != Default() {
		t.Fatalf("bad snapshot = %+v", got)
	}
	p := Default()
	p.Payment = 600 * time.Second
	raw, _ := json.Marshal(p)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["seat_keys"] = []string{"A-1"}
	raw, _ = json.Marshal(m)
	if got := FromSnapshot(raw); got.Payment != 600*time.Second {
		t.Fatalf("snapshot with extra keys = %+v", got)
	}
}
