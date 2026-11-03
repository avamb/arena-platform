package eventpage

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Extract reads a fetched page: JSON-LD first, then Open Graph and the meta
// tags for what is still empty (spec 59 §2). It never calls a model; the
// caller asks NeedsModel and merges a model reading with MergeModel.
func Extract(p Page, now time.Time) Result {
	sc := scanHTML(p.HTML)
	events := parseJSONLD(sc.jsonLD)
	r := Result{Sources: map[string]Source{}}

	r.fromEvents(events, now)
	r.fromMeta(sc)
	r.ImageURLs = imageCandidates(events, sc.meta, p.FinalURL)

	lang := ""
	for _, e := range events {
		if lang = languageTag(e.Language); lang != "" {
			break
		}
	}
	if lang == "" {
		lang = languageTag(sc.lang)
	}
	if lang == "" {
		lang = languageTag(sc.meta["og:locale"])
	}
	r.Language = lang

	text := sc.visibleText
	if sc.title != "" {
		text = oneLine(sc.title, 300) + "\n" + text
	}
	r.Text = clipRunes(text, maxVisibleRunes)
	if len(events) == 0 && len([]rune(sc.visibleText)) < 200 {
		r.warn(Warning{Code: WarnJavaScriptPage})
	}
	r.finish()
	return r
}

// fromEvents fills the facts from the JSON-LD events.
func (r *Result) fromEvents(events []ldEvent, now time.Time) {
	if len(events) == 0 {
		return
	}
	// The name the events share most (a series repeats it on every date).
	counts := map[string]int{}
	best := ""
	for _, e := range events {
		n := oneLine(e.Name, MaxNameRunes)
		if n == "" {
			continue
		}
		counts[n]++
		if counts[n] > counts[best] {
			best = n
		}
	}
	if best != "" {
		r.Facts.Name = best
		r.setSource("name", SourceJSONLD)
	}

	for _, e := range events {
		if e.Online && r.Facts.VenueName == "" && r.Facts.City == "" {
			r.warn(Warning{Code: WarnOnline})
		}
		if e.VenueName == "" && e.City == "" {
			continue
		}
		r.Facts.VenueName = oneLine(e.VenueName, 200)
		r.Facts.City = oneLine(e.City, 120)
		r.Facts.Country = countryCode(e.Country)
		r.Facts.Street = oneLine(e.Street, 200)
		r.setSource("venue", SourceJSONLD)
		break
	}
	for _, e := range events {
		if d := plainText(e.Description, MaxDescriptionRunes); d != "" {
			r.Facts.Description = d
			r.setSource("description", SourceJSONLD)
			break
		}
	}
	for _, e := range events {
		if a := ageRating(e.AgeRange); a != "" {
			r.Facts.Age = a
			r.setSource("age", SourceJSONLD)
			break
		}
	}
	r.datesFrom(events, now)
	r.categoriesFrom(events)
}

// datesFrom keeps the future, not cancelled dates, sorted, at most MaxDates.
func (r *Result) datesFrom(events []ldEvent, now time.Time) {
	type dated struct {
		p     parsedStart
		doors string
	}
	seen := map[string]bool{}
	var keep []dated
	past, cancelled := 0, 0
	for _, e := range events {
		p, ok := parseStart(e.StartDate)
		if !ok {
			continue
		}
		key := p.date + " " + p.clock
		if seen[key] {
			continue
		}
		seen[key] = true
		st := strings.ToLower(lastSegment(e.Status))
		if st == "eventcancelled" || st == "eventpostponed" {
			cancelled++
			continue
		}
		if p.past(now) {
			past++
			continue
		}
		keep = append(keep, dated{p: p, doors: doorsClock(e.DoorTime, p.date)})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i].p, keep[j].p
		if a.date != b.date {
			return a.date < b.date
		}
		return a.clock < b.clock
	})
	if len(keep) > MaxDates {
		r.warn(Warning{Code: WarnDatesSkipped, Count: len(keep) - MaxDates})
		keep = keep[:MaxDates]
	}
	if past > 0 {
		r.warn(Warning{Code: WarnPastDatesSkipped, Count: past})
	}
	if cancelled > 0 {
		r.warn(Warning{Code: WarnCancelledSkipped, Count: cancelled})
	}
	for _, d := range keep {
		r.Facts.Dates = append(r.Facts.Dates, DateFact{Date: d.p.date, Time: d.p.clock, Doors: d.doors, UTCOffset: d.p.offset})
	}
	if len(r.Facts.Dates) > 0 {
		r.setSource("dates", SourceJSONLD)
	}
}

// categoriesFrom reads every offer of every event once.
func (r *Result) categoriesFrom(events []ldEvent) {
	seen := map[string]bool{}
	for _, e := range events {
		for _, o := range e.Offers {
			minor, ok := priceToMinor(o.Price)
			if !ok {
				continue
			}
			c := Category{Name: oneLine(o.Name, 100), PriceMinor: minor, Currency: currencyCode(o.Currency)}
			key := strings.ToLower(c.Name) + "|" + strconv.FormatInt(c.PriceMinor, 10) + "|" + c.Currency
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(r.Facts.Categories) < MaxCategories {
				r.Facts.Categories = append(r.Facts.Categories, c)
			}
		}
	}
	if len(r.Facts.Categories) > 0 {
		r.setSource("categories", SourceJSONLD)
		return
	}
	for _, e := range events {
		if e.PriceLow == "" && e.PriceHigh == "" {
			continue
		}
		vals := []string{e.PriceLow, e.PriceHigh, currencyCode(e.PriceCurrency)}
		r.warn(Warning{Code: WarnPriceRangeOnly, Values: vals})
		return
	}
}

// fromMeta fills what JSON-LD left empty from Open Graph and the meta tags.
func (r *Result) fromMeta(sc scanned) {
	if r.Facts.Name == "" {
		name := sc.meta["og:title"]
		if name == "" {
			name = sc.meta["twitter:title"]
		}
		if name == "" {
			name = sc.title
		}
		if n := oneLine(name, MaxNameRunes); n != "" {
			r.Facts.Name = n
			r.setSource("name", SourceOpenGraph)
		}
	}
	if r.Facts.Description == "" {
		d := sc.meta["og:description"]
		if d == "" {
			d = sc.meta["description"]
		}
		if d = plainText(d, MaxDescriptionRunes); d != "" {
			r.Facts.Description = d
			r.setSource("description", SourceOpenGraph)
		}
	}
}

// imageCandidates lists the page's pictures, best first, absolute and
// http(s) only.
func imageCandidates(events []ldEvent, meta map[string]string, base *url.URL) []ImageCandidate {
	var raw []ImageCandidate
	for _, e := range events {
		for _, s := range e.Images {
			raw = append(raw, ImageCandidate{URL: s, Source: SourceJSONLD})
		}
	}
	for _, k := range []string{"og:image:secure_url", "og:image", "og:image:url", "twitter:image"} {
		raw = append(raw, ImageCandidate{URL: meta[k], Source: SourceOpenGraph})
	}
	seen := map[string]bool{}
	var out []ImageCandidate
	for _, c := range raw {
		s := strings.TrimSpace(c.URL)
		if s == "" {
			continue
		}
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		if base != nil {
			u = base.ResolveReference(u)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			continue
		}
		u.Fragment = ""
		abs := u.String()
		if !seen[abs] {
			seen[abs] = true
			out = append(out, ImageCandidate{URL: abs, Source: c.Source})
		}
		if len(out) == maxImageCandidates {
			break
		}
	}
	return out
}

// maxImageCandidates bounds how many pictures a reading may try to download.
const maxImageCandidates = 3

// finish fills the compatibility fields and the "nothing here" warnings.
func (r *Result) finish() {
	r.Facts.Date, r.Facts.Time = "", ""
	if len(r.Facts.Dates) > 0 {
		r.Facts.Date, r.Facts.Time = r.Facts.Dates[0].Date, r.Facts.Dates[0].Time
	}
	if r.Facts.Dates == nil {
		r.Facts.Dates = []DateFact{}
	}
	if r.Facts.Categories == nil {
		r.Facts.Categories = []Category{}
	}
	if r.Warnings == nil {
		r.Warnings = []Warning{}
	}
	r.dropWarning(WarnNoDates)
	r.dropWarning(WarnNoPrices)
	if len(r.Facts.Dates) == 0 {
		r.Warnings = append(r.Warnings, Warning{Code: WarnNoDates})
	}
	if len(r.Facts.Categories) == 0 && !r.hasWarning(WarnPriceRangeOnly) {
		r.Warnings = append(r.Warnings, Warning{Code: WarnNoPrices})
	}
}

func (r *Result) hasWarning(code string) bool {
	for _, w := range r.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func (r *Result) dropWarning(code string) {
	out := r.Warnings[:0]
	for _, w := range r.Warnings {
		if w.Code != code {
			out = append(out, w)
		}
	}
	r.Warnings = out
}

// AddWarning records a warning the caller found (the picture, the model).
func (r *Result) AddWarning(w Warning) { r.warn(w) }

// SetImage records the stored picture.
func (r *Result) SetImage(img Image, s Source) {
	r.Facts.Image = &img
	r.setSource("image", s)
}
