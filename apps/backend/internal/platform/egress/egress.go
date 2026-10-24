// Package egress is the one policy for outbound HTTP requests whose
// destination comes from a user: a webhook callback URL an organization
// registers, a poster URL in an event import. Such a request must reach the
// public internet only — never the platform's own containers, the host, a
// private network or the cloud metadata service.
//
// The guard sits on the CONNECTION, not on the text of the URL: the dialer's
// Control hook sees the exact IP the socket is about to connect to and refuses
// a forbidden one. A hostname that resolves to a private address, a DNS answer
// that changes between a check and the dial (rebinding), and a redirect to an
// internal host are therefore all refused by the same single check. CheckURL
// adds an early, friendlier answer at registration time; it is not what keeps
// the request out.
//
// Private destinations — and plain http, which every local receiver speaks —
// are allowed only when SetAllowPrivate(true) was called
// (EGRESS_ALLOW_PRIVATE_TARGETS, refused in production by config.Validate) or
// inside a test binary, where hundreds of tests point these clients at
// httptest servers on http://127.0.0.1. A test that proves the refusal pins
// PrivateDeny on its Policy.
package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Sentinel errors. Their text is deliberately generic: it reaches API
// responses, outbox rows and alert messages, and must never name the address
// that was refused.
var (
	// ErrInvalidURL is a URL that is not absolute, has no host, carries user
	// info or a fragment, or does not parse.
	ErrInvalidURL = errors.New("egress: url is not a valid absolute url")
	// ErrSchemeNotAllowed is a scheme other than the policy accepts.
	ErrSchemeNotAllowed = errors.New("egress: url scheme is not allowed")
	// ErrDestinationNotAllowed is a destination on a loopback, private,
	// link-local, multicast or otherwise non-public address.
	ErrDestinationNotAllowed = errors.New("egress: destination is not allowed")
)

// PrivateMode says whether a Policy may reach non-public addresses.
type PrivateMode int

const (
	// PrivateInherit follows the process setting (SetAllowPrivate, or the
	// test-binary default). It is read at dial time, so a client built before
	// startup configuration ran still obeys it.
	PrivateInherit PrivateMode = iota
	// PrivateDeny always refuses non-public addresses (and plain http when
	// the policy does not allow it).
	PrivateDeny
	// PrivateAllow always permits them, over plain http too.
	PrivateAllow
)

// allowPrivate is the process-wide setting behind PrivateInherit.
var allowPrivate atomic.Bool

func init() { allowPrivate.Store(testing.Testing()) }

// SetAllowPrivate sets the process-wide setting behind PrivateInherit. The API
// and the worker call it once at startup from EGRESS_ALLOW_PRIVATE_TARGETS.
func SetAllowPrivate(v bool) { allowPrivate.Store(v) }

// Policy describes what one kind of outbound request may do.
type Policy struct {
	// AllowHTTP accepts plain http:// URLs as well as https://.
	AllowHTTP bool
	// MaxRedirects is how many redirects the client follows; 0 means none (a
	// 3xx is then returned to the caller as the response). Every redirect
	// target is dialed through the same guard.
	MaxRedirects int
	// Private decides whether non-public addresses may be reached.
	Private PrivateMode
}

// Webhook is the policy for a POST to a callback URL: https only, and a
// redirect is a failed delivery, never a reason to resend the signed body to
// another host.
var Webhook = Policy{}

// Fetch is the policy for downloading a public file (an event poster): http
// or https, a few redirects allowed because image hosts commonly redirect.
var Fetch = Policy{AllowHTTP: true, MaxRedirects: 5}

func (p Policy) privateAllowed() bool {
	switch p.Private {
	case PrivateAllow:
		return true
	case PrivateDeny:
		return false
	default:
		return allowPrivate.Load()
	}
}

// resolver is swapped in tests.
var resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
} = net.DefaultResolver

// lookupTimeout bounds CheckURL's best-effort resolution.
const lookupTimeout = 3 * time.Second

// CheckURL validates a user-supplied URL before it is stored or used. It
// returns one of the sentinel errors above, or nil.
//
// A hostname is resolved best effort: when it resolves to a forbidden address
// the URL is refused now, with a clear answer; when it does not resolve at all
// it is accepted, because a site's DNS may not exist yet when it is registered
// — the dial-time guard refuses it later if it ever points inward.
func (p Policy) CheckURL(ctx context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || u.Opaque != "" {
		return ErrInvalidURL
	}
	if u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return ErrInvalidURL
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !p.AllowHTTP && !p.privateAllowed() {
			return ErrSchemeNotAllowed
		}
	default:
		return ErrSchemeNotAllowed
	}
	host := u.Hostname()
	if host == "" {
		return ErrInvalidURL
	}
	if port := u.Port(); port != "" {
		if n, ok := parsePort(port); !ok || n == 0 {
			return ErrInvalidURL
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return ErrInvalidURL
	}
	if p.privateAllowed() {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !Public(addr) {
			return ErrDestinationNotAllowed
		}
		return nil
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return ErrDestinationNotAllowed
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := resolver.LookupNetIP(lctx, "ip", host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if !Public(a) {
			return ErrDestinationNotAllowed
		}
	}
	return nil
}

func parsePort(s string) (int, bool) {
	if len(s) > 5 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n <= 65535
}

// Client returns an HTTP client whose every connection goes through the
// policy's destination guard. It ignores HTTP_PROXY/HTTPS_PROXY: a proxy would
// make the guard see the proxy's address instead of the real destination.
func (p Policy) Client(timeout time.Duration) *http.Client {
	base, _ := http.DefaultTransport.(*http.Transport)
	var tr *http.Transport
	if base != nil {
		tr = base.Clone()
	} else {
		tr = &http.Transport{}
	}
	tr.Proxy = nil
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   p.control,
	}
	tr.DialContext = dialer.DialContext
	tr.DialTLSContext = nil

	maxRedirects := p.MaxRedirects
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return http.ErrUseLastResponse
			}
			if req.URL.Scheme != "https" &&
				(req.URL.Scheme != "http" || (!p.AllowHTTP && !p.privateAllowed())) {
				return ErrSchemeNotAllowed
			}
			return nil
		},
	}
}

// control is the net.Dialer hook: it runs after name resolution, once per
// address the dialer actually tries, before connect(2).
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	if p.privateAllowed() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrDestinationNotAllowed
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !Public(addr) {
		return ErrDestinationNotAllowed
	}
	return nil
}

// blocked lists the special-purpose ranges the netip predicates do not cover.
var blocked = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, includes broadcast
	"::/96",           // IPv4-compatible (deprecated), includes :: and ::1
	"64:ff9b::/96",    // NAT64, can embed any IPv4
	"64:ff9b:1::/48",  // local-use NAT64
	"100::/64",        // discard
	"2001:db8::/32",   // documentation
	"2002::/16",       // 6to4, can embed any IPv4
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Public reports whether addr is a globally routable unicast address that a
// user-supplied URL may reach.
func Public(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap().WithZone("")
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() ||
		!addr.IsGlobalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// StripURL removes the request URL that net/http puts into a *url.Error
// ("Post \"https://host/path?query\": ..."), keeping the operation and the
// cause, so a callback's path or query never reaches a log, an outbox row or
// an alert. errors.Is still sees the cause (ErrDestinationNotAllowed, a
// timeout). Any other error is returned unchanged.
func StripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &strippedError{op: ue.Op, err: ue.Err}
	}
	return err
}

type strippedError struct {
	op  string
	err error
}

func (e *strippedError) Error() string { return e.op + ": " + e.err.Error() }
func (e *strippedError) Unwrap() error { return e.err }

// Origin returns scheme://host[:port] of a URL with user info, path, query
// and fragment removed, for logs and audit rows. An unparseable URL yields "".
func Origin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host
}
