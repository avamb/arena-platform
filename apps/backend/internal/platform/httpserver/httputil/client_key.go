package httputil

import (
	"net/netip"
	"strings"
)

// ClientKey is the key one client address is counted under, by the per-IP
// rate limits and by the short-link scan de-duplication. An IPv4 address is
// its own key. An IPv6 address is keyed by its /64 prefix: one subscriber is
// normally given a whole /64, so counting single addresses would let one client
// step through 2^64 (temporary, rotating) addresses and look like many. An
// address that does not parse is returned unchanged.
func ClientKey(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}
