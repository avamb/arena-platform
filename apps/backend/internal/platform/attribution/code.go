package attribution

import "strings"

const (
	// CodeAlphabet is the alphabet of a sales-link code (spec 43 section 2,
	// decision 8): lower-case letters and digits without 0 o 1 l i, so a code
	// read off a poster cannot be mistaken.
	CodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	// CodeLen is the length of a sales-link code.
	CodeLen = 7
)

// IsCode reports whether s has exactly the shape of a sales-link code:
// CodeLen characters, every one of them in CodeAlphabet. It is case-sensitive
// (codes are stored lower-case); use NormalizeRef to fold case first.
func IsCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(CodeAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// NormalizeRef folds a client-supplied sales-link code to its stored form
// (trimmed, lower-case) and reports whether it has the shape of a code. A
// value that is not a code is not repaired.
func NormalizeRef(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !IsCode(s) {
		return "", false
	}
	return s, true
}
