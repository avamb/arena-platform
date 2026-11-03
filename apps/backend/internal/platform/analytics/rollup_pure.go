package analytics

// Pure parts of analytics.rollup (spec 50 section 7): session splitting,
// touch classification, the article rule and the activity score. Nothing here
// touches the database, so every rule is unit-tested on its own.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SessionGap is the silence after which the next event starts a new session.
const SessionGap = 30 * time.Minute

// Article rule (spec 50 section 7): a blog post counts as read in a session
// when the visitor was engaged on it for at least 30 seconds AND scrolled to
// 75 percent or further.
const (
	ArticleMinSeconds = 30
	ArticleMinDepth   = 75
	// MaxArticles is how many read articles a profile keeps, newest first.
	MaxArticles = 20
)

// Ev is one raw event as the rollup reads it.
type Ev struct {
	At      time.Time
	Surface string
	Name    string
	Props   map[string]any
}

func (e Ev) str(key string) string {
	s, _ := e.Props[key].(string)
	return s
}

func (e Ev) num(key string) int {
	switch v := e.Props[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Touch is where a visit came from.
type Touch struct {
	Source   string
	Medium   string
	Campaign string
	RefHost  string
	// Kind is email_click, utm, organic or referral.
	Kind string
	At   time.Time
}

// Empty reports whether the touch carries no information.
func (t Touch) Empty() bool { return t.Source == "" && t.Kind == "" }

// ownHosts are our own sites: a visit coming from one of them is a click
// inside our pages, not a touch.
var ownHosts = []string{"arenasoldout.com"}

// searchLabels are the host labels of search engines: google.com,
// www.google.cz, search.yahoo.com, yandex.ru, seznam.cz all match.
var searchLabels = map[string]bool{
	"google": true, "bing": true, "yahoo": true, "duckduckgo": true, "yandex": true,
	"baidu": true, "ecosia": true, "seznam": true, "qwant": true, "startpage": true,
	"naver": true, "sogou": true,
}

func isOwnHost(host string) bool {
	for _, o := range ownHosts {
		if host == o || strings.HasSuffix(host, "."+o) {
			return true
		}
	}
	return false
}

func searchEngine(host string) string {
	for _, label := range strings.Split(host, ".") {
		if searchLabels[label] {
			return label
		}
	}
	return ""
}

// ClassifyTouch decides the touch of a session from its entry page view and
// the email_click of the same visit (either may be nil). ok is false when the
// visit carries no touch at all: an entry from one of our own pages.
//
// An email click beats everything: the visitor was brought by our letter.
// Otherwise a utm_source wins; then a search engine or an empty referrer is
// organic; any other host is a referral.
func ClassifyTouch(pageView, emailClick map[string]any, at time.Time) (Touch, bool) {
	pv := Ev{Props: pageView}
	if emailClick != nil {
		ec := Ev{Props: emailClick}
		campaign := ec.str("utm_campaign")
		if campaign == "" {
			campaign = pv.str("utm_campaign")
		}
		return Touch{Source: "email", Medium: "email", Campaign: campaign,
			RefHost: pv.str("ref_host"), Kind: "email_click", At: at}, true
	}
	if pageView == nil {
		return Touch{}, false
	}
	if src := strings.ToLower(pv.str("utm_source")); src != "" {
		return Touch{Source: src, Medium: strings.ToLower(pv.str("utm_medium")),
			Campaign: pv.str("utm_campaign"), RefHost: pv.str("ref_host"), Kind: "utm", At: at}, true
	}
	host := strings.ToLower(pv.str("ref_host"))
	switch {
	case host == "":
		return Touch{Source: "direct", Medium: "organic", Kind: "organic", At: at}, true
	case isOwnHost(host):
		return Touch{}, false
	}
	if engine := searchEngine(host); engine != "" {
		return Touch{Source: engine, Medium: "organic", RefHost: host, Kind: "organic", At: at}, true
	}
	return Touch{Source: host, Medium: "referral", RefHost: host, Kind: "referral", At: at}, true
}

// Session is one run of events of a person on one surface.
type Session struct {
	Surface         string
	Start, End      time.Time
	Pages           int
	EngagedSeconds  int
	EntryPath       string
	Touch           Touch
	HasTouch        bool
	Events          []Ev
	pricingViewedAt time.Time
}

var langPrefixRe = regexp.MustCompile(`^/[a-z]{2}(?:-[a-z]{2})?(?:/|$)`)

// NormalizePath strips the language prefix (/ru/blog/x becomes /blog/x),
// the trailing slash and the case, so one page is one path in every language.
func NormalizePath(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if loc := langPrefixRe.FindString(p); loc != "" {
		p = "/" + p[len(loc):]
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p
}

// IsArticlePath reports whether a normalized site path is a blog article.
func IsArticlePath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/blog/")
	return ok && rest != "" && !strings.Contains(rest, "/")
}

// IsPricingPath reports whether a normalized site path is the price list or
// the calculator (which lives on the price page).
func IsPricingPath(p string) bool {
	return p == "/pricing" || strings.HasPrefix(p, "/pricing/") || p == "/calculator"
}

// SplitSessions builds the sessions of one person from all their events: the
// site and the event center separately, a new session after more than
// SessionGap of silence. Bot events do not make sessions. The input need not
// be sorted.
func SplitSessions(evs []Ev) []Session {
	sorted := append([]Ev(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var out []Session
	for _, surface := range []string{SurfaceSite, SurfaceEventCenter} {
		var cur *Session
		flush := func() {
			if cur != nil {
				finishSession(cur)
				out = append(out, *cur)
				cur = nil
			}
		}
		for _, e := range sorted {
			if e.Surface != surface {
				continue
			}
			if cur != nil && e.At.Sub(cur.End) > SessionGap {
				flush()
			}
			if cur == nil {
				cur = &Session{Surface: surface, Start: e.At}
			}
			cur.End = e.At
			cur.Events = append(cur.Events, e)
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func finishSession(s *Session) {
	var firstPV *Ev
	var click *Ev
	for i := range s.Events {
		e := &s.Events[i]
		switch e.Name {
		case "page_view":
			s.Pages++
			if firstPV == nil {
				firstPV = e
			}
			if p := NormalizePath(e.str("path")); IsPricingPath(p) && s.pricingViewedAt.IsZero() {
				s.pricingViewedAt = e.At
			}
		case "screen_view":
			s.Pages++
			if s.EntryPath == "" {
				s.EntryPath = e.str("screen")
			}
		case "engaged":
			s.EngagedSeconds += e.num("seconds")
		case "email_click":
			if click == nil {
				click = e
			}
		}
	}
	if s.Surface != SurfaceSite {
		return
	}
	switch {
	case firstPV != nil:
		s.EntryPath = NormalizePath(firstPV.str("path"))
	case click != nil:
		s.EntryPath = NormalizePath(click.str("path"))
	}
	var pvProps, clickProps map[string]any
	at := s.Start
	if firstPV != nil {
		pvProps = firstPV.Props
		at = firstPV.At
		if pvProps == nil {
			pvProps = map[string]any{}
		}
	}
	if click != nil {
		clickProps = click.Props
		if clickProps == nil {
			clickProps = map[string]any{}
		}
		if firstPV == nil {
			at = click.At
		}
	}
	s.Touch, s.HasTouch = ClassifyTouch(pvProps, clickProps, at)
}

// ArticleRead is one blog article a person read.
type ArticleRead struct {
	Path    string
	At      time.Time
	Seconds int
}

// ArticleReads applies the article rule to every site session: per session
// and article path, the engaged seconds are summed and the deepest scroll is
// taken; the article counts when both reach the thresholds.
func ArticleReads(sessions []Session) []ArticleRead {
	var out []ArticleRead
	for _, s := range sessions {
		if s.Surface != SurfaceSite {
			continue
		}
		type acc struct {
			seconds, depth int
			last           time.Time
		}
		by := map[string]*acc{}
		for _, e := range s.Events {
			path := NormalizePath(e.str("path"))
			if !IsArticlePath(path) {
				continue
			}
			a := by[path]
			if a == nil {
				a = &acc{}
				by[path] = a
			}
			switch e.Name {
			case "engaged":
				a.seconds += e.num("seconds")
				a.last = e.At
			case "scroll":
				if d := e.num("depth"); d > a.depth {
					a.depth = d
				}
				if e.At.After(a.last) {
					a.last = e.At
				}
			}
		}
		for path, a := range by {
			if a.seconds >= ArticleMinSeconds && a.depth >= ArticleMinDepth {
				out = append(out, ArticleRead{Path: path, At: a.last, Seconds: a.seconds})
			}
		}
	}
	return LatestArticles(out)
}

// LatestArticles keeps the newest read of each path and returns at most
// MaxArticles of them, newest first (ties broken by path, so the result is
// stable).
func LatestArticles(in []ArticleRead) []ArticleRead {
	best := map[string]ArticleRead{}
	for _, a := range in {
		if cur, ok := best[a.Path]; !ok || a.At.After(cur.At) {
			best[a.Path] = a
		}
	}
	out := make([]ArticleRead, 0, len(best))
	for _, a := range best {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > MaxArticles {
		out = out[:MaxArticles]
	}
	return out
}

// ─── milestones, stage ─────────────────────────────────────────────────────

// Milestone names (spec 50 section 5). gmv_30d is the only one that is not a
// time: it maps a currency to minor units sold in the last 30 days.
const (
	MsApplicationStarted   = "application_started"
	MsApplicationSubmitted = "application_submitted"
	MsApplicationApproved  = "application_approved"
	MsApplicationRejected  = "application_rejected"
	MsOrgCreated           = "org_created"
	MsPaymentConnected     = "payment_connected"
	MsFirstEventCreated    = "first_event_created"
	MsFirstEventPublished  = "first_event_published"
	MsFirstSale            = "first_sale"
	MsSales100             = "sales_100"
	MsMemberInvited        = "member_invited"
	MsGMV30d               = "gmv_30d"
)

// Stages of a profile.
const (
	StageLead      = "lead"
	StageApplicant = "applicant"
	StageOrganizer = "organizer"
	StageSelling   = "selling"
)

// StageFor derives the stage from the milestones that are times.
func StageFor(ms map[string]time.Time) string {
	has := func(k string) bool { t, ok := ms[k]; return ok && !t.IsZero() }
	switch {
	case has(MsFirstSale):
		return StageSelling
	case has(MsApplicationApproved), has(MsOrgCreated):
		return StageOrganizer
	case has(MsApplicationStarted), has(MsApplicationSubmitted), has(MsApplicationRejected):
		return StageApplicant
	}
	return StageLead
}

// ─── activity score ────────────────────────────────────────────────────────

// ScoreWeights is the v1 table of the activity index (spec 50 section 7). The
// weights live here, in Go, so they can be tuned after a month of real
// contacts without a migration.
type ScoreWeights struct {
	Visit                float64
	Article              float64
	Pricing              float64
	EmailClick           float64
	ApplicationStarted   float64
	ApplicationSubmitted float64
	// HalfLifeDays: a contribution loses half its weight every this many days.
	HalfLifeDays float64
}

// DefaultScoreWeights is v1.
var DefaultScoreWeights = ScoreWeights{
	Visit: 1, Article: 3, Pricing: 5, EmailClick: 2,
	ApplicationStarted: 10, ApplicationSubmitted: 20, HalfLifeDays: 14,
}

// Score item kinds.
const (
	ScoreVisit                = "visit"
	ScoreArticle              = "article"
	ScorePricing              = "pricing"
	ScoreEmailClick           = "email_click"
	ScoreApplicationStarted   = "application_started"
	ScoreApplicationSubmitted = "application_submitted"
)

// ScoreItem is one thing that happened and counts toward the index.
type ScoreItem struct {
	Kind string
	At   time.Time
}

func (w ScoreWeights) weight(kind string) float64 {
	switch kind {
	case ScoreVisit:
		return w.Visit
	case ScoreArticle:
		return w.Article
	case ScorePricing:
		return w.Pricing
	case ScoreEmailClick:
		return w.EmailClick
	case ScoreApplicationStarted:
		return w.ApplicationStarted
	case ScoreApplicationSubmitted:
		return w.ApplicationSubmitted
	}
	return 0
}

// TruncateDay returns the UTC midnight of t's day.
func TruncateDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// decay is the share of an item's weight left. Age is counted in whole UTC
// days between the calendar dates, so the index changes once a day instead
// of every hour (profiles.changed_at must move only when something changed).
func (w ScoreWeights) decay(at, now time.Time) float64 {
	days := TruncateDay(now).Sub(TruncateDay(at)).Hours() / 24
	if days < 0 {
		days = 0
	}
	if w.HalfLifeDays <= 0 {
		return 1
	}
	return math.Pow(0.5, days/w.HalfLifeDays)
}

// Score is the rounded sum of the decayed weights.
func Score(items []ScoreItem, now time.Time, w ScoreWeights) int {
	var sum float64
	for _, it := range items {
		sum += w.weight(it.Kind) * w.decay(it.At, now)
	}
	return int(math.Round(sum))
}

// ExplainScore is the one line that answers "why this number": the count of
// each kind with its decayed contribution.
func ExplainScore(items []ScoreItem, now time.Time, w ScoreWeights) string {
	counts := map[string]int{}
	parts := map[string]float64{}
	for _, it := range items {
		counts[it.Kind]++
		parts[it.Kind] += w.weight(it.Kind) * w.decay(it.At, now)
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var b strings.Builder
	for i, k := range kinds {
		if i > 0 {
			b.WriteString(" + ")
		}
		fmt.Fprintf(&b, "%s x%d = %.1f", k, counts[k], parts[k])
	}
	if b.Len() == 0 {
		return "no activity"
	}
	fmt.Fprintf(&b, " (half-life %.0f days)", w.HalfLifeDays)
	return b.String()
}

// SessionScoreItems turns sessions into score items: one visit per session,
// one article per read, one pricing item per session that opened the price
// page and one item per email click.
func SessionScoreItems(sessions []Session, reads []ArticleRead) []ScoreItem {
	var items []ScoreItem
	for _, s := range sessions {
		items = append(items, ScoreItem{Kind: ScoreVisit, At: s.Start})
		if !s.pricingViewedAt.IsZero() {
			items = append(items, ScoreItem{Kind: ScorePricing, At: s.pricingViewedAt})
		}
		for _, e := range s.Events {
			if e.Name == "email_click" {
				items = append(items, ScoreItem{Kind: ScoreEmailClick, At: e.At})
			}
		}
	}
	for _, a := range reads {
		items = append(items, ScoreItem{Kind: ScoreArticle, At: a.At})
	}
	return items
}

// ─── touches over time ─────────────────────────────────────────────────────

// SessionTouches returns the touch of the person's first and of their latest
// session that has one.
func SessionTouches(sessions []Session) (first, last Touch) {
	for _, s := range sessions {
		if !s.HasTouch {
			continue
		}
		if first.Empty() {
			first = s.Touch
		}
		last = s.Touch
	}
	return first, last
}

// EarlierTouch picks the earlier of two touches, ignoring an empty one. Used
// to keep a first touch that has outlived the raw events (13 months).
func EarlierTouch(a, b Touch) Touch {
	switch {
	case a.Empty():
		return b
	case b.Empty():
		return a
	case b.At.Before(a.At):
		return b
	}
	return a
}

// LaterTouch picks the later of two touches, ignoring an empty one.
func LaterTouch(a, b Touch) Touch {
	switch {
	case a.Empty():
		return b
	case b.Empty():
		return a
	case b.At.After(a.At):
		return b
	}
	return a
}
