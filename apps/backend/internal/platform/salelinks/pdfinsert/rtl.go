package pdfinsert

// rtl.go — the Hebrew visual order of one printed line.
//
// gofpdf lays glyphs out left to right in the order the string gives them;
// its own RTL() merely reverses a whole string. A Hebrew sentence reversed
// whole reads right, but a hall name, a year or a time inside it would come
// out mirrored. visualLine therefore reverses the LINE while keeping every
// run of Latin letters and digits in its own left-to-right order, and mirrors
// paired brackets. That is the same level of support the e-ticket gives
// Hebrew (real glyphs, no contextual shaping, no full bidi algorithm), stated
// honestly in spec 43 section 6.5.

import (
	"strings"
	"unicode"
)

// isRTLLocale reports whether locale is written right to left. Only Hebrew is
// supported by the catalogs today.
func isRTLLocale(locale string) bool {
	return NormalizeLocale(locale) == "he"
}

// isHebrew reports whether r is a Hebrew letter or point.
func isHebrew(r rune) bool { return unicode.Is(unicode.Hebrew, r) }

// isLTRStrong reports whether r keeps its own order inside a Hebrew line: a
// Latin letter or a digit.
func isLTRStrong(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(unicode.IsLetter(r) && !isHebrew(r)) || unicode.IsDigit(r)
}

// ltrInner are the characters that stay inside a Latin/digit run when they
// sit between two strong characters (a time, a date, an address).
const ltrInner = ".,:/-_+'&@"

// mirrored maps a bracket to its mirror image for a reversed line.
var mirrored = map[rune]rune{'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<', '«': '»', '»': '«'}

// visualLine returns line in visual order for a right-to-left reader when it
// holds Hebrew; a line without Hebrew is returned unchanged.
func visualLine(line string) string {
	if !strings.ContainsFunc(line, isHebrew) {
		return line
	}
	runes := []rune(line)
	type seg struct {
		runes []rune
		ltr   bool
	}
	var segs []seg
	for i := 0; i < len(runes); {
		if isLTRStrong(runes[i]) {
			j := i + 1
			for j < len(runes) {
				if isLTRStrong(runes[j]) {
					j++
					continue
				}
				// An inner punctuation mark stays in the run only when a
				// strong character follows it.
				if strings.ContainsRune(ltrInner, runes[j]) && j+1 < len(runes) && isLTRStrong(runes[j+1]) {
					j += 2
					continue
				}
				break
			}
			segs = append(segs, seg{runes: runes[i:j], ltr: true})
			i = j
			continue
		}
		j := i + 1
		for j < len(runes) && !isLTRStrong(runes[j]) {
			j++
		}
		segs = append(segs, seg{runes: runes[i:j]})
		i = j
	}
	out := make([]rune, 0, len(runes))
	for k := len(segs) - 1; k >= 0; k-- {
		s := segs[k]
		if s.ltr {
			out = append(out, s.runes...)
			continue
		}
		for m := len(s.runes) - 1; m >= 0; m-- {
			r := s.runes[m]
			if mr, ok := mirrored[r]; ok {
				r = mr
			}
			out = append(out, r)
		}
	}
	return string(out)
}
