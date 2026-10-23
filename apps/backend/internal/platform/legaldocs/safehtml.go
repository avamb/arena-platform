package legaldocs

import (
	"regexp"
	"strings"
)

// articleOpen is the only opening tag with attributes Render writes.
var articleOpen = regexp.MustCompile(`^<article class="legal-document" lang="[a-z]{2}" dir="(?:ltr|rtl)">`)

// plainTags are the attribute-less tags Render writes.
var plainTags = []string{"</article>", "<h2>", "</h2>", "<h3>", "</h3>", "<p>", "</p>", "<ul>", "</ul>", "<li>", "</li>"}

// SafeRenderedHTML is the second line of defence before a stored rendering
// is served as HTML: it accepts exactly the markup Render produces (one
// <article> with lang and dir, h2, h3, p, ul, li) and text in which every
// "<" and ">" is escaped. Anything else - a script, an attribute, a comment,
// a row changed in the database by hand - is refused, so the terms page
// never sends markup Render could not have written. Render's own escaping
// is the first line; this check does not trust it.
func SafeRenderedHTML(s string) bool {
	if !articleOpen.MatchString(s) {
		return false
	}
	for i := 0; i < len(s); {
		switch s[i] {
		case '>':
			return false // a ">" outside a known tag
		case '<':
			rest := s[i:]
			if i == 0 {
				i += len(articleOpen.FindString(rest))
				continue
			}
			matched := false
			for _, t := range plainTags {
				if strings.HasPrefix(rest, t) {
					i += len(t)
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		default:
			i++
		}
	}
	return strings.HasSuffix(strings.TrimRight(s, "\n"), "</article>")
}
