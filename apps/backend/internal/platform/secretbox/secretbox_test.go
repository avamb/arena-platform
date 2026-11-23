package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func std(k []byte) string { return base64.StdEncoding.EncodeToString(k) }

func mustBox(t *testing.T, spec string) *Box {
	t.Helper()
	b, err := ParseKeys(spec)
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustBox(t, "v1:"+std(key(1)))
	if b.CurrentKeyID() != "v1" {
		t.Fatalf("CurrentKeyID = %q", b.CurrentKeyID())
	}
	aad := []byte("org-1|meta_connection")
	s1, err := b.Seal([]byte("token-value"), aad)
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := b.Seal([]byte("token-value"), aad)
	if s1 == s2 {
		t.Error("two seals of the same value must differ (fresh nonce)")
	}
	if !strings.HasPrefix(s1, "v1:") {
		t.Errorf("sealed value %q must start with the key id", s1)
	}
	if strings.Contains(s1, "token-value") {
		t.Error("sealed value carries the plaintext")
	}
	got, err := b.Open(s1, aad)
	if err != nil || string(got) != "token-value" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestAADMismatchFails(t *testing.T) {
	b := mustBox(t, "v1:"+std(key(1)))
	s, _ := b.Seal([]byte("x"), []byte("org-1"))
	if _, err := b.Open(s, []byte("org-2")); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong AAD: err = %v, want ErrOpen", err)
	}
}

func TestTamperFails(t *testing.T) {
	b := mustBox(t, "v1:"+std(key(1)))
	s, _ := b.Seal([]byte("secret"), nil)
	id, enc, _ := strings.Cut(s, ":")
	raw, _ := base64.RawURLEncoding.DecodeString(enc)
	raw[len(raw)-1] ^= 0x01
	tampered := id + ":" + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := b.Open(tampered, nil); !errors.Is(err, ErrOpen) {
		t.Fatalf("tampered: err = %v, want ErrOpen", err)
	}
	if _, err := b.Open(id+":"+enc[:10], nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated: err = %v, want ErrMalformed", err)
	}
	if _, err := b.Open("no-colon", nil); !errors.Is(err, ErrMalformed) {
		t.Fatalf("no id: err = %v, want ErrMalformed", err)
	}
}

func TestRotation(t *testing.T) {
	old := mustBox(t, "v1:"+std(key(1)))
	sealedOld, _ := old.Seal([]byte("old"), nil)

	rotated := mustBox(t, "v2:"+base64.RawURLEncoding.EncodeToString(key(2))+", v1:"+std(key(1)))
	if rotated.CurrentKeyID() != "v2" {
		t.Fatalf("CurrentKeyID = %q, want v2", rotated.CurrentKeyID())
	}
	got, err := rotated.Open(sealedOld, nil)
	if err != nil || string(got) != "old" {
		t.Fatalf("old key must still open: %q, %v", got, err)
	}
	sealedNew, _ := rotated.Seal([]byte("new"), nil)
	if !strings.HasPrefix(sealedNew, "v2:") {
		t.Errorf("new seal must use v2, got %q", sealedNew)
	}
}

func TestUnknownKeyID(t *testing.T) {
	b := mustBox(t, "v1:"+std(key(1)))
	other := mustBox(t, "v9:"+std(key(9)))
	s, _ := other.Seal([]byte("x"), nil)
	if _, err := b.Open(s, nil); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestDisabled(t *testing.T) {
	var nilBox *Box
	if _, err := nilBox.Seal([]byte("x"), nil); !errors.Is(err, ErrDisabled) {
		t.Errorf("nil Seal: %v", err)
	}
	if _, err := nilBox.Open("v1:abc", nil); !errors.Is(err, ErrDisabled) {
		t.Errorf("nil Open: %v", err)
	}
	if nilBox.CurrentKeyID() != "" {
		t.Error("nil CurrentKeyID must be empty")
	}
	if _, err := (&Box{}).Seal([]byte("x"), nil); !errors.Is(err, ErrDisabled) {
		t.Errorf("empty Seal: %v", err)
	}
}

func TestBadSpec(t *testing.T) {
	secret := std(key(7))
	cases := map[string]string{
		"empty":          "",
		"no colon":       secret,
		"bad id":         "k1:" + secret,
		"bare v":         "v:" + secret,
		"short key":      "v1:" + std(bytes.Repeat([]byte{1}, 16)),
		"long key":       "v1:" + std(bytes.Repeat([]byte{1}, 33)),
		"not base64":     "v1:!!!not-base64!!!",
		"duplicate id":   "v1:" + secret + ",v1:" + std(key(8)),
		"trailing comma": "v1:" + secret + ",",
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseKeys(spec)
			if err == nil {
				t.Fatalf("ParseKeys(%q) must fail", spec)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks key material: %v", err)
			}
		})
	}
}
