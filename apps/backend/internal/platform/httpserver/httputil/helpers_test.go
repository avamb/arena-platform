// Package httputil — unit tests for TrustedClientIP (feature #358, PR2-02).
//
// These tests verify that the rate-limit IP derivation is resistant to
// X-Forwarded-For spoofing regardless of the number of trusted proxy hops.
package httputil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// TrustedClientIP — zero trusted proxies (default, most secure)
// ---------------------------------------------------------------------------

func TestTrustedClientIP_ZeroProxies_IgnoresXFF(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Attacker sends a spoofed X-Forwarded-For header.
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	// RemoteAddr is what the TCP stack set — cannot be forged by the client.
	r.RemoteAddr = "10.0.0.5:55555"

	got := TrustedClientIP(r, 0)
	if got == "1.2.3.4" {
		t.Errorf("TrustedClientIP(0) returned spoofed XFF IP %q; want RemoteAddr IP 10.0.0.5", got)
	}
	if got != "10.0.0.5" {
		t.Errorf("TrustedClientIP(0) = %q; want 10.0.0.5", got)
	}
}

func TestTrustedClientIP_ZeroProxies_NoXFF_UsesRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:12345"

	got := TrustedClientIP(r, 0)
	if got != "203.0.113.7" {
		t.Errorf("TrustedClientIP(0) = %q; want 203.0.113.7", got)
	}
}

func TestTrustedClientIP_ZeroProxies_MultiValueXFF_StillIgnored(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2, 3.3.3.3")
	r.RemoteAddr = "10.10.10.1:8080"

	got := TrustedClientIP(r, 0)
	// With 0 trusted proxies, XFF is always ignored.
	if got != "10.10.10.1" {
		t.Errorf("TrustedClientIP(0) with multi-value XFF = %q; want 10.10.10.1", got)
	}
}

func TestTrustedClientIP_ZeroProxies_NegativeCount_TreatedAsZero(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "5.5.5.5")
	r.RemoteAddr = "10.10.10.2:9999"

	got := TrustedClientIP(r, -1)
	if got != "10.10.10.2" {
		t.Errorf("TrustedClientIP(-1) = %q; want 10.10.10.2 (negative treated as zero)", got)
	}
}

// ---------------------------------------------------------------------------
// TrustedClientIP — one trusted proxy hop
// ---------------------------------------------------------------------------

func TestTrustedClientIP_OneProxy_ReturnsSecondFromRight(t *testing.T) {
	// Layout: "<real-client>, <proxy-added>"
	// With trustedProxies=1, the rightmost entry is the trusted proxy; the
	// real client is at index len-1-1 = 0.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:40000"

	got := TrustedClientIP(r, 1)
	if got != "203.0.113.42" {
		t.Errorf("TrustedClientIP(1) = %q; want 203.0.113.42 (real client)", got)
	}
}

func TestTrustedClientIP_OneProxy_SpoofedLeftEntry_RealClientStillFromRight(t *testing.T) {
	// Attacker injects a fake IP at the left; the trusted proxy appended the real one.
	// XFF: "<attacker-injected>, <real-client>, <trusted-proxy-added>"
	// trustedProxies=1 → real client is at index len-1-1 = 1 → real-client value.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "evil-fake-ip, 203.0.113.99, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:40001"

	got := TrustedClientIP(r, 1)
	// With 1 trusted proxy the rightmost hop is trusted; the real client is the
	// second-from-right entry.
	if got != "203.0.113.99" {
		t.Errorf("TrustedClientIP(1) = %q; want 203.0.113.99", got)
	}
}

func TestTrustedClientIP_OneProxy_EmptyXFF_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No XFF header even though trustedProxies=1 is set.
	r.RemoteAddr = "172.16.0.5:443"

	got := TrustedClientIP(r, 1)
	if got != "172.16.0.5" {
		t.Errorf("TrustedClientIP(1) with empty XFF = %q; want 172.16.0.5", got)
	}
}

func TestTrustedClientIP_OneProxy_TooFewXFFEntries_FallsBackToRemoteAddr(t *testing.T) {
	// Only 1 entry in XFF but trustedProxies=1 requires 2 entries (one for the
	// real client, one for the trusted proxy). The single entry is from the
	// trusted proxy itself, so there is no real-client entry to read.
	// The function should fall back to RemoteAddr to avoid returning an
	// attacker-controlled value.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4") // only 1 entry
	r.RemoteAddr = "192.168.1.1:9090"

	got := TrustedClientIP(r, 1)
	if got != "192.168.1.1" {
		t.Errorf("TrustedClientIP(1) with single XFF entry = %q; want 192.168.1.1 (fallback)", got)
	}
}

// ---------------------------------------------------------------------------
// TrustedClientIP — two trusted proxy hops
// ---------------------------------------------------------------------------

func TestTrustedClientIP_TwoProxies_ReturnsThirdFromRight(t *testing.T) {
	// Layout: "<real-client>, <inner-proxy>, <outer-proxy>"
	// With trustedProxies=2, index = len-2-1 = 0 → real-client.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.5, 10.0.0.6")
	r.RemoteAddr = "10.0.0.6:12345"

	got := TrustedClientIP(r, 2)
	if got != "1.2.3.4" {
		t.Errorf("TrustedClientIP(2) = %q; want 1.2.3.4", got)
	}
}

func TestTrustedClientIP_TwoProxies_ClientCannotSpoofWithOneInjectedEntry(t *testing.T) {
	// Attacker injects one extra IP. With trustedProxies=2, the function still
	// picks the correct entry: index = len-2-1.
	// XFF: "<fake>, <real-client>, <inner-proxy>, <outer-proxy>" → index 4-2-1=1 → real-client.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4, 10.0.0.5, 10.0.0.6")
	r.RemoteAddr = "10.0.0.6:12345"

	got := TrustedClientIP(r, 2)
	if got != "1.2.3.4" {
		t.Errorf("TrustedClientIP(2) with injected entry = %q; want 1.2.3.4", got)
	}
}

// ---------------------------------------------------------------------------
// TrustedClientIP — invalid / edge-case IP values
// ---------------------------------------------------------------------------

func TestTrustedClientIP_InvalidIPInXFF_FallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// A non-IP string at the expected position.
	r.Header.Set("X-Forwarded-For", "not-an-ip, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:9000"

	got := TrustedClientIP(r, 1)
	// "not-an-ip" is at index 0 = len-1-1, which fails net.ParseIP → fallback.
	if got != "10.0.0.1" {
		t.Errorf("TrustedClientIP(1) with invalid XFF = %q; want 10.0.0.1 (fallback)", got)
	}
}

func TestTrustedClientIP_IPv6_ParsedCorrectly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "2001:db8::1, 10.0.0.1")
	r.RemoteAddr = "10.0.0.1:7777"

	got := TrustedClientIP(r, 1)
	// net.ParseIP normalises IPv6 addresses.
	if got != "2001:db8::1" {
		t.Errorf("TrustedClientIP(1) IPv6 = %q; want 2001:db8::1", got)
	}
}

func TestTrustedClientIP_RemoteAddrWithoutPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// In some edge cases RemoteAddr has no port component.
	r.RemoteAddr = "10.0.0.9"

	got := TrustedClientIP(r, 0)
	// SplitHostPort fails; the raw RemoteAddr is returned.
	if got != "10.0.0.9" {
		t.Errorf("TrustedClientIP(0) with bare IP RemoteAddr = %q; want 10.0.0.9", got)
	}
}
