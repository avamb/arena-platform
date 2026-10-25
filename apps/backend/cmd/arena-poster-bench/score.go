package main

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// Truth is what a poster really says, as the operator wrote it in truth.json.
// Any field may be null, which means "unknown": it is not scored. Prices are
// in major units (25 or 34.9); an empty list means the poster prints none.
type Truth struct {
	File   string     `json:"file"`
	Title  *string    `json:"title"`
	Date   *string    `json:"date"`
	Time   *string    `json:"time"`
	Venue  *string    `json:"venue"`
	City   *string    `json:"city"`
	Prices *[]float64 `json:"prices"`
	Age    *string    `json:"age"`
}

// Field names, in the order of the report.
var fieldOrder = []string{"title", "date", "time", "venue", "city", "prices", "age"}

// Verdicts of one field.
const (
	verdictMatch = "match"
	verdictMiss  = "miss"
	verdictSkip  = "skip" // the truth is unknown
)

// Score is the field-by-field verdict of one read against its truth.
type Score struct {
	Fields  map[string]string `json:"fields"`
	Matched int               `json:"matched"`
	Scored  int               `json:"scored"`
}

// normText folds case, diacritics and whitespace so "Café  Rika" equals
// "cafe rika". Punctuation stays: it is part of a title.
func normText(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// normAge turns "16", "16+" and " 16 + " into "16+".
func normAge(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.TrimSuffix(s, "+")
	if s == "" {
		return ""
	}
	return s + "+"
}

// minorSet is the sorted, de-duplicated list of prices in minor units.
func minorSet(v []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, x := range v {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameSet(a, b []int64) bool {
	a, b = minorSet(a), minorSet(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func verdict(ok bool) string {
	if ok {
		return verdictMatch
	}
	return verdictMiss
}

// ScoreFacts compares what a model read with the truth, field by field: text
// fields after normalizing case, diacritics and whitespace, date and time
// exactly, prices as a set of minor-unit amounts, age as one of the ratings.
// A truth field that is nil is skipped and counts for nothing.
func ScoreFacts(f posterread.Facts, t Truth) Score {
	s := Score{Fields: map[string]string{}}
	set := func(name, v string) {
		s.Fields[name] = v
		if v != verdictSkip {
			s.Scored++
		}
		if v == verdictMatch {
			s.Matched++
		}
	}
	text := func(name string, want *string, got string) {
		if want == nil {
			set(name, verdictSkip)
			return
		}
		set(name, verdict(normText(*want) == normText(got)))
	}
	exact := func(name string, want *string, got string) {
		if want == nil {
			set(name, verdictSkip)
			return
		}
		set(name, verdict(strings.TrimSpace(*want) == strings.TrimSpace(got)))
	}
	text("title", t.Title, f.Name)
	exact("date", t.Date, f.Date)
	exact("time", t.Time, f.Time)
	text("venue", t.Venue, f.VenueName)
	text("city", t.City, f.City)
	if t.Prices == nil {
		set("prices", verdictSkip)
	} else {
		var want, got []int64
		for _, p := range *t.Prices {
			want = append(want, int64(math.Round(p*100)))
		}
		for _, c := range f.Categories {
			got = append(got, c.PriceMinor)
		}
		set("prices", verdict(sameSet(want, got)))
	}
	if t.Age == nil {
		set("age", verdictSkip)
	} else {
		set("age", verdict(normAge(*t.Age) == normAge(f.Age)))
	}
	return s
}
