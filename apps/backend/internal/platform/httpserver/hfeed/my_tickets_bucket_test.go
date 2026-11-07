package hfeed

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The exchange is limited per client address; without an address each token
// gets its own bucket, never one key shared by every buyer.
func TestMyTicketsExchangeBucket(t *testing.T) {
	h := &Handler{myTicketsSalt: "salt"}
	withIP := httptest.NewRequest("POST", "/x", nil)
	withIP.RemoteAddr = "203.0.113.7:4000"
	if a, b := h.myTicketsExchangeBucket(withIP, "tok-a"), h.myTicketsExchangeBucket(withIP, "tok-b"); a != b || !strings.HasPrefix(a, "mt-exchange|") {
		t.Fatalf("with an address the bucket is per address: %q %q", a, b)
	}
	noIP := httptest.NewRequest("POST", "/x", nil)
	noIP.RemoteAddr = ""
	a, b := h.myTicketsExchangeBucket(noIP, "tok-a"), h.myTicketsExchangeBucket(noIP, "tok-b")
	if a == b || a == "mt-exchange|" || strings.Contains(a, "tok-a") {
		t.Fatalf("without an address: %q %q, want distinct per-token buckets that do not carry the token", a, b)
	}
	if a != h.myTicketsExchangeBucket(noIP, "tok-a") {
		t.Fatal("the per-token bucket is not stable")
	}
}
