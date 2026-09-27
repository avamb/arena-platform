// Package geoslug renders free-form place names (cities, countries) as arena
// geo slugs: lower-case ASCII words joined by single hyphens.
//
// cities.slug and countries.slug are the i18n_text keys of the localized
// names and are globally unique, so every writer of a geo row — the Bil24 and
// event-bundle imports and the organization-side city create
// (POST /v1/organizations/{org_id}/cities) — must derive them the same way.
// Moved here from himports (where it was slugify) so the two cannot drift.
package geoslug

import (
	"strings"
	"unicode"
)

// cyrillicTranslit maps the Cyrillic alphabet onto its conventional Latin
// transliteration so a Russian city or country name still produces a usable
// arena geo slug. Bil24 is a Russian-origin platform; without this table every
// Cyrillic name would slugify to the empty string.
var cyrillicTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// latinFold maps the accented Latin letters common in the markets arena
// serves (Hungarian, German, Czech) onto their ASCII base letter.
var latinFold = map[rune]rune{
	'á': 'a', 'ä': 'a', 'â': 'a', 'à': 'a', 'å': 'a', 'ã': 'a',
	'é': 'e', 'ë': 'e', 'ê': 'e', 'è': 'e', 'ě': 'e',
	'í': 'i', 'ï': 'i', 'î': 'i', 'ì': 'i',
	'ó': 'o', 'ö': 'o', 'ő': 'o', 'ô': 'o', 'ò': 'o', 'õ': 'o', 'ø': 'o',
	'ú': 'u', 'ü': 'u', 'ű': 'u', 'û': 'u', 'ù': 'u',
	'ý': 'y', 'ç': 'c', 'č': 'c', 'ñ': 'n', 'ß': 's', 'š': 's', 'ž': 'z',
	'ř': 'r', 'ł': 'l',
}

// Slugify renders a free-form place name as an arena geo slug:
// lower-case ASCII words joined by single hyphens. Returns "" when nothing
// usable survives, which callers treat as "unresolvable".
func Slugify(raw string) string {
	var b strings.Builder
	prevHyphen := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if folded, ok := latinFold[r]; ok {
			r = folded
		}
		if tr, ok := cyrillicTranslit[r]; ok {
			if tr != "" {
				b.WriteString(tr)
				prevHyphen = false
			}
			continue
		}
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_':
			// Any other letter/digit is not representable in a slug; treat it
			// (and every separator) as a word boundary.
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// NormalizeName folds a place name for a case- and whitespace-insensitive
// comparison: surrounding whitespace dropped, inner runs collapsed to one
// space, lower-cased. It matches the SQL
// lower(regexp_replace(btrim(v), '\s+', ' ', 'g')) used by the city lookup.
func NormalizeName(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}
