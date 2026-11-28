package scannerapp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"
)

// CodeAlphabet is the alphabet of a pairing code: no 0, O, 1, I or L, so a
// code read aloud or typed by hand is not misread.
const CodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// CodeLen is the length of a pairing code.
const CodeLen = 8

// OrganizerCodeTTL is how long an organizer's one-time pairing code works.
const OrganizerCodeTTL = 15 * time.Minute

// PassCodeTTL is the stored term of a pass's pairing code. The code does not
// end by itself: the window of the pass decides.
const PassCodeTTL = 10 * 365 * 24 * time.Hour

// NewCode returns a random pairing code of CodeLen characters of
// CodeAlphabet, every character equally likely (bytes past the last whole
// multiple of the alphabet size are thrown away).
func NewCode() (string, error) {
	const n = len(CodeAlphabet)
	const limit = 256 - 256%n
	out := make([]byte, 0, CodeLen)
	var buf [16]byte
	for len(out) < CodeLen {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, CodeAlphabet[int(b)%n])
			if len(out) == CodeLen {
				break
			}
		}
	}
	return string(out), nil
}

// normalizeCodeInput upper-cases a typed code and drops spaces and hyphens.
func normalizeCodeInput(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range strings.ToUpper(raw) {
		switch r {
		case ' ', '-', '\t', '\n', '\r':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeCode returns the normalized form of a typed code and whether it
// has the shape of a pairing code (CodeLen characters of CodeAlphabet).
func NormalizeCode(raw string) (string, bool) {
	c := normalizeCodeInput(raw)
	if len(c) != CodeLen {
		return c, false
	}
	for i := 0; i < len(c); i++ {
		if strings.IndexByte(CodeAlphabet, c[i]) < 0 {
			return c, false
		}
	}
	return c, true
}

// HashCode is the SHA-256 hex of the normalized form of a typed code, as
// stored in scanner_pairing_codes.code_hash and scanner_pair_attempts. A
// code of the wrong shape still has a hash, so the attempt is counted.
func HashCode(raw string) string {
	c, _ := NormalizeCode(raw)
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

// HashIP is the SHA-256 hex of the limit key of a client address
// (IPLimitKey), as stored in scanner_pair_attempts.ip_hash; the address
// itself is never stored.
func HashIP(ip string) string {
	sum := sha256.Sum256([]byte(IPLimitKey(ip)))
	return hex.EncodeToString(sum[:])
}

// IPLimitKey is the key the pairing attempt limit counts a client address
// by. An IPv4 address (an IPv4-mapped IPv6 one too) is the address itself;
// an IPv6 address is its /64 prefix, since one subscriber gets a whole /64
// and could otherwise step around the limit by changing the low bits. A
// string that is not an address is kept as it is.
func IPLimitKey(ip string) string {
	s := strings.TrimSpace(ip)
	a, err := netip.ParseAddr(s)
	if err != nil {
		return s
	}
	a = a.WithZone("").Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return s
	}
	return p.String()
}

// PairLink is the link a phone opens to pair: <base>/scan/pair#<code>. The
// code travels in the fragment, so it never reaches a server log.
func PairLink(apiPublicBaseURL, code string) string {
	return strings.TrimRight(apiPublicBaseURL, "/") + "/scan/pair#" + code
}
