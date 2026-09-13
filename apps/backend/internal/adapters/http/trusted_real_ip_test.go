package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTrustedRealIP_RewritesRemoteAddrFromProxyAppendedEntry pins the
// replacement for chi's RealIP: behind one trusted proxy the proxy-appended
// (rightmost) X-Forwarded-For entry becomes RemoteAddr and a client-supplied
// prefix is ignored.
func TestTrustedRealIP_RewritesRemoteAddrFromProxyAppendedEntry(t *testing.T) {
	var seen string
	h := trustedRealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.2:51000"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.5")
	req.Header.Set("X-Real-IP", "7.7.7.7")
	req.Header.Set("True-Client-IP", "8.8.8.8")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "203.0.113.5" {
		t.Fatalf("RemoteAddr = %q, want 203.0.113.5 (proxy-appended client, spoofed headers ignored)", seen)
	}
}

// TestTrustedRealIP_NoHeaderKeepsPeer: without X-Forwarded-For the TCP peer
// address is used, host part only.
func TestTrustedRealIP_NoHeaderKeepsPeer(t *testing.T) {
	var seen string
	h := trustedRealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.2:51000"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "172.18.0.2" {
		t.Fatalf("RemoteAddr = %q, want 172.18.0.2", seen)
	}
}
