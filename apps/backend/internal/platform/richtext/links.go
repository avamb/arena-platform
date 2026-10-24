package richtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// LinkCandidate is an address written in a plain text that may be a link:
// Text is the address as written, Href its canonical link target.
type LinkCandidate struct {
	Text, Href string
}

// LinkCount is the number of links (a elements) in an ART.
func LinkCount(art string) int {
	_, n := parseART(art).html(writeOpts{})
	return n
}

// LinkCandidates lists the addresses of a plain text (the old notation) that
// may be links under profile p: the https:// addresses the very scanner of
// FromLegacy finds (whether or not p auto-links them, and with the same
// reading of the **bold** and _italic_ markers), and e-mail addresses that
// stand on their own, as a mailbox with its whole local part and whole domain.
// An address the profile does not allow as a link (a scheme outside the
// profile, a host that is not a public name) is not listed. The list is not
// capped by the profile's link limit; Sanitize caps the links it keeps.
func LinkCandidates(text string, p Profile) []LinkCandidate {
	if !p.has("a") {
		return nil
	}
	scan := p
	scan.AutoLink = true
	scan.MaxLinks = 1 << 20
	var out []LinkCandidate
	legacyDoc(text, scan).units(func(chunks []chunk) {
		var sb strings.Builder
		var inLink []bool // per byte of the unit's text: part of an https link
		type span struct {
			text, href string
		}
		links := map[int]*span{}
		var order []int
		for _, c := range chunks {
			if c.br {
				sb.WriteByte('\n')
				inLink = append(inLink, false)
				continue
			}
			sb.WriteString(c.text)
			for range len(c.text) {
				inLink = append(inLink, c.href != "")
			}
			if c.href == "" {
				continue
			}
			l := links[c.linkID]
			if l == nil {
				l = &span{href: c.href}
				links[c.linkID] = l
				order = append(order, c.linkID)
			}
			l.text += c.text
		}
		for _, id := range order {
			out = append(out, LinkCandidate{Text: links[id].text, Href: links[id].href})
		}
		out = append(out, emailCandidates(sb.String(), inLink, p)...)
	})
	return out
}

// emailCandidates finds the e-mail addresses of s outside the https links
// (inLink marks their bytes). The local part is the whole run of mailbox
// characters before the "@", the domain the whole run of host characters after
// it, and the characters on both sides must let the address stand alone
// (AddressBoundary), so "sales-info@x.com" is one address and "info@x.com"
// inside it is none.
func emailCandidates(s string, inLink []bool, p Profile) []LinkCandidate {
	var out []LinkCandidate
	for at := 0; at < len(s); at++ {
		if s[at] != '@' || inLink[at] {
			continue
		}
		start := at
		for start > 0 && isMailboxByte(s[start-1]) && !inLink[start-1] {
			start--
		}
		end := at + 1
		for end < len(s) && !inLink[end] {
			r, size := utf8.DecodeRuneInString(s[end:])
			if r != '.' && r != '-' && !isWordRune(r) {
				break
			}
			end += size
		}
		for end > at+1 && (s[end-1] == '.' || s[end-1] == '-') {
			end--
		}
		if start == at || end == at+1 || !AddressBoundary(s[:start], s[end:]) {
			continue
		}
		text := s[start:end]
		href, ok := cleanHref("mailto:"+text, p)
		if !ok {
			continue
		}
		out = append(out, LinkCandidate{Text: text, Href: href})
	}
	return out
}

func isMailboxByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.':
		return true
	}
	return strings.IndexByte("!#$&'*+-/=?^_`{|}~", c) >= 0
}

// AddressBoundary reports whether an address may stand as a link between
// before and after, the text of the same paragraph on each side of it. It may
// not when the address is only a piece of a longer one: the character before
// it is a letter, a digit, a mark or one of "-._~%+/@#?=&", or the character
// after it is a letter, a digit, a mark or one of "-_~%+/@#=&", or a "." or
// "?" followed by a letter or a digit (a sentence ends with ". " and "?", an
// address goes on with ".com" and "?x=1").
func AddressBoundary(before, after string) bool {
	if r, size := utf8.DecodeLastRuneInString(before); size > 0 {
		if isWordRune(r) || strings.ContainsRune("-._~%+/@#?=&", r) {
			return false
		}
	}
	r, size := utf8.DecodeRuneInString(after)
	if size == 0 {
		return true
	}
	if isWordRune(r) || strings.ContainsRune("-_~%+/@#=&", r) {
		return false
	}
	if r == '.' || r == '?' {
		next, n := utf8.DecodeRuneInString(after[size:])
		if n > 0 && isWordRune(next) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}
