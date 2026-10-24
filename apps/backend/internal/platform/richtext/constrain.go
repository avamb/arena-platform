package richtext

import (
	"sort"
	"strings"
)

// ConstrainOptions say what Constrain takes out of an ART (spec 44, RT-04:
// the code that checks the answer of the formatting model).
type ConstrainOptions struct {
	// MaxMarksPerUnit is the most emphasised fragments (strong, em and u runs
	// together) one paragraph, heading, quote paragraph or list item may keep.
	// The first ones in reading order stay, the rest lose their mark and keep
	// their text. Zero or less means no limit.
	MaxMarksPerUnit int
	// KeepLink decides for every link: text is the whole visible text of the
	// link, href its canonical target ("https://..." or "mailto:..."), and before
	// and after the text of the same unit on each side of it. A
	// link it refuses is unwrapped and its text stays. Nil keeps every link.
	KeepLink func(text, href, before, after string) bool
}

// Constrain returns art with the links KeepLink refuses unwrapped and the
// emphasis above MaxMarksPerUnit removed, serialised again by Sanitize for the
// profile (so it is canonical and under the profile's limits). It never
// changes a character of the visible text.
func Constrain(art string, p Profile, o ConstrainOptions) (string, error) {
	d := parseART(art)
	d.units(func(chunks []chunk) {
		if o.KeepLink != nil {
			constrainLinks(chunks, o.KeepLink)
		}
		if o.MaxMarksPerUnit > 0 {
			constrainMarks(chunks, o.MaxMarksPerUnit)
		}
	})
	out, _ := d.html(writeOpts{})
	return Sanitize(out, p)
}

// constrainLinks unwraps every link of one unit that keep refuses. keep also
// gets the text of the unit before and after the link (a line break counts as
// "\n"), so it can tell an address that stands alone from a piece of a longer
// one.
func constrainLinks(chunks []chunk, keep func(text, href, before, after string) bool) {
	type link struct {
		text        []byte
		href        string
		first, last int // chunk indexes
	}
	var all strings.Builder
	starts := make([]int, len(chunks))
	ends := make([]int, len(chunks))
	for i, c := range chunks {
		starts[i] = all.Len()
		if c.br {
			all.WriteByte('\n')
		} else {
			all.WriteString(c.text)
		}
		ends[i] = all.Len()
	}
	unit := all.String()
	links := map[int]*link{}
	for i, c := range chunks {
		if c.href == "" || c.br {
			continue
		}
		l := links[c.linkID]
		if l == nil {
			l = &link{href: c.href, first: i}
			links[c.linkID] = l
		}
		l.last = i
		l.text = append(l.text, c.text...)
	}
	refused := map[int]bool{}
	for id, l := range links {
		if !keep(string(l.text), l.href, unit[:starts[l.first]], unit[ends[l.last]:]) {
			refused[id] = true
		}
	}
	for i := range chunks {
		if chunks[i].href != "" && refused[chunks[i].linkID] {
			chunks[i].href, chunks[i].linkID = "", 0
		}
	}
}

// constrainMarks clears the strong, em and u runs of one unit beyond the
// first limit. A run is a stretch of text chunks with the mark that no line
// break interrupts: exactly one tag of the serialisation.
func constrainMarks(chunks []chunk, limit int) {
	type run struct{ flag, from, to int } // flag 0 strong, 1 em, 2 u; chunks [from, to)
	var spans []run
	flags := func(c chunk) [3]bool { return [3]bool{c.strong, c.em, c.u} }
	for flag := 0; flag < 3; flag++ {
		start := -1
		for i, c := range chunks {
			on := !c.br && flags(c)[flag]
			if on && start >= 0 && c.linkID != chunks[start].linkID {
				// The link is the outermost tag: a new link opens a new tag.
				spans = append(spans, run{flag, start, i})
				start = -1
			}
			if on && start < 0 {
				start = i
			}
			if !on && start >= 0 {
				spans = append(spans, run{flag, start, i})
				start = -1
			}
		}
		if start >= 0 {
			spans = append(spans, run{flag, start, len(chunks)})
		}
	}
	if len(spans) <= limit {
		return
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	for _, r := range spans[limit:] {
		for i := r.from; i < r.to; i++ {
			switch r.flag {
			case 0:
				chunks[i].strong = false
			case 1:
				chunks[i].em = false
			case 2:
				chunks[i].u = false
			}
		}
	}
}
