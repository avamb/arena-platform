package scannerapp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNewCode_AlphabetAndLength(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 2000; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != CodeLen {
			t.Fatalf("code %q has length %d", c, len(c))
		}
		for j := 0; j < len(c); j++ {
			if strings.IndexByte(CodeAlphabet, c[j]) < 0 {
				t.Fatalf("code %q has %q outside the alphabet", c, c[j])
			}
			seen[c[j]] = true
		}
		if n, ok := NormalizeCode(c); !ok || n != c {
			t.Fatalf("NormalizeCode(%q) = %q, %v", c, n, ok)
		}
	}
	if len(seen) != len(CodeAlphabet) {
		t.Errorf("2000 codes used %d of %d characters", len(seen), len(CodeAlphabet))
	}
	for _, bad := range []byte("01OIL") {
		if strings.IndexByte(CodeAlphabet, bad) >= 0 {
			t.Errorf("alphabet holds the ambiguous %q", bad)
		}
	}
	if len(CodeAlphabet) != 31 {
		t.Errorf("alphabet has %d characters, want 31", len(CodeAlphabet))
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{
		"abcd-efgh":    "ABCDEFGH",
		" ab cd ef gh": "ABCDEFGH",
		"AB-CD EF-GH":  "ABCDEFGH",
		"23456789":     "23456789",
	} {
		got, ok := NormalizeCode(in)
		if !ok || got != want {
			t.Errorf("NormalizeCode(%q) = %q, %v, want %q", in, got, ok, want)
		}
		if HashCode(in) != HashCode(want) {
			t.Errorf("HashCode(%q) differs from HashCode(%q)", in, want)
		}
	}
	for _, in := range []string{"", "ABCDEFG", "ABCDEFGHJ", "ABCDEFG0", "ABCDEFGI", "ABCDEFGL", "ABCDEFGO", "ABCDEFG1", "ABCDÉFGH"} {
		if _, ok := NormalizeCode(in); ok {
			t.Errorf("NormalizeCode(%q) accepted", in)
		}
		if h := HashCode(in); len(h) != 64 {
			t.Errorf("HashCode(%q) = %q, want 64 hex", in, h)
		}
	}
}

func TestPairLink(t *testing.T) {
	for _, base := range []string{"https://api.example.com", "https://api.example.com/"} {
		if got := PairLink(base, "ABCDEFGH"); got != "https://api.example.com/scan/pair#ABCDEFGH" {
			t.Errorf("PairLink(%q) = %q", base, got)
		}
	}
}

func TestIPLimitKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":                   "203.0.113.7",
		" 203.0.113.7 ":                 "203.0.113.7",
		"::ffff:203.0.113.7":            "203.0.113.7",
		"2001:db8:1:2:aaaa:bbbb:cccc:1": "2001:db8:1:2::/64",
		"2001:db8:1:2::ffff":            "2001:db8:1:2::/64",
		"fe80::1%eth0":                  "fe80::/64",
		"not an address":                "not an address",
		"":                              "",
	} {
		if got := IPLimitKey(in); got != want {
			t.Errorf("IPLimitKey(%q) = %q, want %q", in, got, want)
		}
	}
	// One /64 is one key; the next /64 is another; IPv4 is counted as before.
	if HashIP("2001:db8:1:2::1") != HashIP("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("two addresses of one /64 hash apart")
	}
	if HashIP("2001:db8:1:2::1") == HashIP("2001:db8:1:3::1") {
		t.Error("two /64 prefixes hash the same")
	}
	if HashIP("::ffff:198.51.100.1") != HashIP("198.51.100.1") || HashIP("198.51.100.1") == HashIP("198.51.100.2") {
		t.Error("IPv4 is not hashed by the address")
	}
	sum := sha256.Sum256([]byte("198.51.100.1"))
	if HashIP("198.51.100.1") != hex.EncodeToString(sum[:]) {
		t.Error("IPv4 hash changed")
	}
}

func TestRefreshToken_Shape(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, RefreshTokenWirePrefix) || hash != HashToken(raw) {
		t.Fatalf("raw %q hash %q", raw, hash)
	}
	if !LooksLikeRefreshToken(raw) {
		t.Error("a refresh token does not look like one")
	}
	if LooksLikeDeviceToken(raw) {
		t.Error("a refresh token looks like a device access token")
	}
	access, _, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if LooksLikeRefreshToken(access) {
		t.Error("an access token looks like a refresh token")
	}
	if LooksLikeRefreshToken(raw[:len(raw)-1]) || LooksLikeRefreshToken(raw+"A") {
		t.Error("a refresh token of the wrong length was accepted")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	for _, c := range []struct {
		oldest *time.Time
		want   time.Duration
	}{
		{nil, time.Second},
		{at(-PairAttemptWindow), time.Second},
		{at(-PairAttemptWindow + 300*time.Millisecond), time.Second},
		{at(-PairAttemptWindow + 1500*time.Millisecond), 2 * time.Second},
		{at(-10 * time.Minute), 5 * time.Minute},
		{at(0), PairAttemptWindow},
	} {
		if got := retryAfter(c.oldest, now); got != c.want {
			t.Errorf("retryAfter(%v) = %v, want %v", c.oldest, got, c.want)
		}
	}
}

func TestPairOutcome(t *testing.T) {
	for _, c := range []struct {
		err     error
		outcome string
		counted bool
	}{
		{nil, OutcomeOK, true},
		{ErrCodeNotFound, OutcomeNotFound, true},
		{fmt.Errorf("wrapped: %w", ErrPassFull), OutcomePassFull, true},
		{ErrInvalidDevice, "", false},
		{errors.New("db down"), "", false},
	} {
		o, counted := PairOutcome(c.err)
		if o != c.outcome || counted != c.counted {
			t.Errorf("PairOutcome(%v) = %q, %v, want %q, %v", c.err, o, counted, c.outcome, c.counted)
		}
	}
}

func TestTypedErrors(t *testing.T) {
	var err error = &RevokedError{Reason: ReasonPassRevoked, JustRevoked: true}
	if !errors.Is(fmt.Errorf("x: %w", err), ErrRevoked) {
		t.Error("RevokedError is not ErrRevoked")
	}
	var re *RevokedError
	if !errors.As(fmt.Errorf("x: %w", err), &re) || re.Reason != ReasonPassRevoked {
		t.Error("errors.As RevokedError failed")
	}
	err = &TooManyAttemptsError{RetryAfter: time.Minute}
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Error("TooManyAttemptsError is not ErrTooManyAttempts")
	}
}

func TestWindowEnded(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Second)
	if !windowEnded(PassKindStandard, &past, now) || !windowEnded(PassKindStandard, &now, now) || !windowEnded(PassKindStandard, nil, now) {
		t.Error("an ended standard window reads as open")
	}
	if windowEnded(PassKindStandard, &future, now) || windowEnded(PassKindDemo, nil, now) {
		t.Error("an open window reads as ended")
	}
}
