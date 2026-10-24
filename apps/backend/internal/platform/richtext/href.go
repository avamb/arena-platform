package richtext

import (
	"net"
	"strings"
	"unicode"
)

// cleanHref returns the canonical form of a link target, or false when the
// link must be unwrapped. The scheme (case-insensitive, surrounding spaces
// trimmed) must be in the profile; an https link needs a host and has no
// user-info, port, IP literal or localhost; no whitespace anywhere.
func cleanHref(raw string, p Profile) (string, bool) {
	s := strings.TrimFunc(raw, func(r rune) bool { return isWS(r) || unicode.IsSpace(r) })
	if s == "" || len(s) > MaxLinkLength {
		return "", false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || isBadRune(r) ||
			r == '\\' || r == '"' || r == '<' || r == '>' {
			return "", false
		}
	}
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return "", false // relative, protocol-relative ("//host") or fragment
	}
	scheme := strings.ToLower(s[:colon])
	if !p.allowsScheme(scheme) {
		return "", false
	}
	rest := s[colon+1:]
	switch scheme {
	case "https":
		if !strings.HasPrefix(rest, "//") {
			return "", false
		}
		rest = rest[2:]
		end := strings.IndexAny(rest, "/?#")
		authority, tail := rest, ""
		if end >= 0 {
			authority, tail = rest[:end], rest[end:]
		}
		host, ok := cleanHost(authority)
		if !ok {
			return "", false
		}
		return "https://" + host + tail, true
	case "mailto":
		addr := rest
		if q := strings.IndexByte(addr, '?'); q >= 0 {
			addr = addr[:q]
		}
		local, domain, ok := strings.Cut(addr, "@")
		if !ok || local == "" || strings.ContainsAny(local, ",;%") {
			return "", false
		}
		d, ok := cleanHost(domain)
		if !ok {
			return "", false
		}
		return "mailto:" + local + "@" + d + rest[len(addr):], true
	}
	return "", false
}

// cleanHost validates and lower-cases a host name. User-info, a port, an IP
// literal (v4, v6, or an all-numeric form a browser would read as one) and
// localhost are refused.
func cleanHost(authority string) (string, bool) {
	if authority == "" || strings.ContainsAny(authority, "@:[]%") {
		return "", false
	}
	host := strings.ToLower(authority)
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return "", false
	}
	for _, r := range host {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '_') {
			return "", false
		}
	}
	last := host[strings.LastIndexByte(host, '.')+1:]
	if isNumericLabel(last) {
		return "", false // 127.1, 2130706433, 0x7f.1: read as an IPv4 address
	}
	return host, true
}

func isNumericLabel(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "0x") {
		s = s[2:]
		if s == "" {
			return true
		}
		for _, r := range s {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isWS is a character the sanitizer treats as a plain space: the HTML
// whitespace and the no-break spaces.
func isWS(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', 0x00a0, 0x2007, 0x202f:
		return true
	}
	return false
}

// isBadRune is a bidi override/isolate/mark or a zero-width character: the
// set of eventnote.go plus the zero-width space and the word joiner. The
// zero-width joiner and non-joiner are allowed: emoji sequences and Persian
// need them.
func isBadRune(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
		r == 0x200e || r == 0x200f || r == 0xfeff || r == 0x061c ||
		r == 0x200b || r == 0x2060 || r == 0x2028 || r == 0x2029
}

// isForbidden is a character no text may contain: a control character other
// than the line break and tab family, or a bad rune.
func isForbidden(r rune) bool {
	switch r {
	case '\n', '\r', '\t', '\f':
		return false
	}
	return unicode.IsControl(r) || isBadRune(r)
}
