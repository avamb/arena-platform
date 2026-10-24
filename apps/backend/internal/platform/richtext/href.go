package richtext

import (
	"net"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// cleanHref returns the canonical form of a link target, or false when the
// link must be unwrapped. The scheme (case-insensitive, surrounding spaces
// trimmed) must be in the profile; an https link needs a host and has no
// user-info, port, IP literal or localhost; a mailto link is just an address,
// its query is dropped; no whitespace, control, bidi or invisible character
// anywhere.
func cleanHref(raw string, p Profile) (string, bool) {
	s := strings.TrimFunc(raw, func(r rune) bool { return isWS(r) || unicode.IsSpace(r) })
	if s == "" || len(s) > MaxLinkLength {
		return "", false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || charPolicy(r) != charPlain ||
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
		// Only the address survives: "?bcc=…&body=…" and the like would let a
		// link write the mail. Whatever follows the address that is not part
		// of the host fails the host check.
		addr := rest
		if q := strings.IndexByte(addr, '?'); q >= 0 {
			addr = addr[:q]
		}
		local, domain, ok := strings.Cut(addr, "@")
		if !ok || !validLocalPart(local) {
			return "", false
		}
		d, ok := cleanHost(domain)
		if !ok {
			return "", false
		}
		return "mailto:" + local + "@" + d, true
	}
	return "", false
}

// validLocalPart is a mailbox name made of RFC 5322 atext characters and
// dots between them. The percent sign is atext too, but in a mailto: URI it
// starts an escape ("%0D%0A"), so it is not accepted.
func validLocalPart(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.':
		case strings.IndexByte("!#$&'*+-/=?^_`{|}~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// cleanHost validates a host name and returns its ASCII form: the way a
// browser reads it (UTS 46: full-width and other look-alike characters are
// mapped, a Unicode name becomes punycode). Every check runs on that ASCII
// form. User-info, a port, an IP literal (v4, v6, or an all-numeric form a
// browser would read as one) and localhost are refused.
func cleanHost(authority string) (string, bool) {
	if authority == "" || len(authority) > MaxLinkLength || strings.ContainsAny(authority, "@:[]%") {
		return "", false
	}
	host, err := idna.Lookup.ToASCII(authority)
	if err != nil {
		return "", false
	}
	host = strings.ToLower(host)
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", false
	}
	host = strings.TrimSuffix(host, ".") // the root label: "example.com." is "example.com"
	if host == "" {
		return "", false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return "", false
		}
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", false
	}
	if net.ParseIP(host) != nil {
		return "", false
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
// whitespace, the no-break spaces and the line and paragraph separators (in
// ART a line break is a <br>, never a character).
func isWS(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', 0x00a0, 0x2007, 0x202f, 0x2028, 0x2029:
		return true
	}
	return false
}

// charAction says what the one character policy does with a character.
type charAction uint8

const (
	// charPlain: an ordinary character (including the tab and line break
	// family), kept.
	charPlain charAction = iota
	// charKeep: a direction mark or joiner that real texts need (Hebrew,
	// Arabic, Persian, emoji sequences, hyphenation), kept.
	charKeep
	// charStrip: an invisible character that only hides things (isolates, BOM,
	// zero-width space, word joiner), removed silently.
	charStrip
	// charRefuse: a character that must never be stored (bidi overrides,
	// control characters, tag characters, invisible operators), the text is
	// refused when it is written and the character is dropped when it is read.
	charRefuse
	// charLineSep and charParaSep: U+2028 and U+2029, a line break and a
	// paragraph break in plain text, a space in ART.
	charLineSep
	charParaSep
)

// charPolicy is the one character policy of Sanitize, FromLegacy and Render.
// In a link target every character but charPlain is refused.
func charPolicy(r rune) charAction {
	switch r {
	case '\t', '\n', '\r', '\f':
		return charPlain
	case 0x200e, 0x200f, 0x061c, 0x200c, 0x200d, 0x00ad:
		return charKeep
	case 0x2066, 0x2067, 0x2068, 0x2069, 0xfeff, 0x200b, 0x2060:
		return charStrip
	case 0x2028:
		return charLineSep
	case 0x2029:
		return charParaSep
	case 0x180e:
		return charRefuse
	}
	switch {
	case unicode.IsControl(r): // C0, DEL and C1
		return charRefuse
	case r >= 0x202a && r <= 0x202e: // embeddings and overrides: Trojan Source
		return charRefuse
	case r >= 0x2061 && r <= 0x2064: // invisible operators
		return charRefuse
	case r >= 0xfff9 && r <= 0xfffb: // interlinear annotation
		return charRefuse
	case r >= 0xe0000 && r <= 0xe007f: // tag characters: text hidden from readers
		return charRefuse
	}
	return charPlain
}
