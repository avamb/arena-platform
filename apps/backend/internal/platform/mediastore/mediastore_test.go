package mediastore

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAllowedOwnerTypesCanonical(t *testing.T) {
	for _, want := range []string{"org_logo", "event_poster", "artist_photo"} {
		if _, ok := AllowedOwnerTypes[want]; !ok {
			t.Errorf("AllowedOwnerTypes missing %q", want)
		}
	}
	if _, ok := AllowedOwnerTypes["video_clip"]; ok {
		t.Error("AllowedOwnerTypes should not contain unsupported owner types")
	}
}

func TestNewStorageKey_PrefixAndUniqueness(t *testing.T) {
	a, err := NewStorageKey("org_logo")
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}
	b, err := NewStorageKey("org_logo")
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}
	if a == b {
		t.Error("NewStorageKey produced duplicate keys")
	}
	if !strings.HasPrefix(a, "org_logo/") {
		t.Errorf("NewStorageKey result %q is not prefixed by owner_type", a)
	}
}

func TestLocalSignatureRoundTrip(t *testing.T) {
	r := &Repo{
		signingSecret:   []byte("test-secret-aa"),
		downloadURLBase: "",
	}
	id := uuid.New()
	url := r.localSignedURL(id, 5*time.Minute)
	if !strings.Contains(url, "expires=") || !strings.Contains(url, "sig=") {
		t.Fatalf("signed url missing expected query params: %q", url)
	}

	// Parse expires/sig from the URL.
	q := strings.SplitN(url, "?", 2)[1]
	parts := map[string]string{}
	for _, kv := range strings.Split(q, "&") {
		if i := strings.IndexByte(kv, '='); i > 0 {
			parts[kv[:i]] = kv[i+1:]
		}
	}
	if err := r.VerifyLocalSignature(id, parts["expires"], parts["sig"]); err != nil {
		t.Errorf("VerifyLocalSignature roundtrip failed: %v", err)
	}

	// Tampering with the signature must fail.
	if err := r.VerifyLocalSignature(id, parts["expires"], parts["sig"]+"00"); err == nil {
		t.Error("VerifyLocalSignature accepted a tampered signature")
	}

	// Expired URL must fail.
	if err := r.VerifyLocalSignature(id, "1", parts["sig"]); err == nil {
		t.Error("VerifyLocalSignature accepted an expired URL")
	}

	// Empty signing secret skips signature verification entirely (dev mode).
	r2 := &Repo{}
	if err := r2.VerifyLocalSignature(uuid.New(), parts["expires"], "anything"); err != nil {
		t.Errorf("dev-mode VerifyLocalSignature should accept any signature: %v", err)
	}
}

func TestComputeHMAC_Stable(t *testing.T) {
	secret := []byte("k")
	id := "abc"
	want := computeHMAC(secret, id, 1000)
	if want != computeHMAC(secret, id, 1000) {
		t.Error("computeHMAC is not deterministic for identical inputs")
	}
	if want == computeHMAC(secret, id, 1001) {
		t.Error("computeHMAC collides across distinct expires values")
	}
}
