package analytics

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func ev(offset time.Duration, surface, name string, props map[string]any) Ev {
	return Ev{At: t0.Add(offset), Surface: surface, Name: name, Props: props}
}

func TestSplitSessions_GapSplitsAndSurfacesAreSeparate(t *testing.T) {
	evs := []Ev{
		ev(0, SurfaceSite, "page_view", map[string]any{"path": "/", "ref_host": "google.com"}),
		ev(10*time.Minute, SurfaceSite, "page_view", map[string]any{"path": "/pricing"}),
		ev(10*time.Minute, SurfaceSite, "engaged", map[string]any{"seconds": float64(40)}),
		// exactly 30 minutes of silence keeps the session, one second more splits it
		ev(40*time.Minute, SurfaceSite, "page_view", map[string]any{"path": "/blog/a"}),
		ev(40*time.Minute+31*time.Minute, SurfaceSite, "page_view", map[string]any{"path": "/blog/b"}),
		ev(5*time.Minute, SurfaceEventCenter, "screen_view", map[string]any{"screen": "/events"}),
		ev(6*time.Minute, SurfaceBot, "bot_update", nil),
	}
	got := SplitSessions(evs)
	if len(got) != 3 {
		t.Fatalf("sessions = %d, want 3 (site x2, event center x1; bot makes none): %+v", len(got), got)
	}
	site1 := got[0]
	if site1.Surface != SurfaceSite || site1.Pages != 3 || site1.EngagedSeconds != 40 {
		t.Errorf("first site session = %+v", site1)
	}
	if !site1.End.Equal(t0.Add(40 * time.Minute)) {
		t.Errorf("end = %v", site1.End)
	}
	if site1.EntryPath != "/" || !site1.HasTouch || site1.Touch.Source != "google" || site1.Touch.Medium != "organic" {
		t.Errorf("entry/touch = %q %+v", site1.EntryPath, site1.Touch)
	}
	ec := got[1]
	if ec.Surface != SurfaceEventCenter || ec.EntryPath != "/events" || ec.Pages != 1 {
		t.Errorf("event center session = %+v", ec)
	}
	if got[2].EntryPath != "/blog/b" {
		t.Errorf("second site session entry = %q", got[2].EntryPath)
	}
}

func TestSplitSessions_EmailClickOnlyIsAVisit(t *testing.T) {
	got := SplitSessions([]Ev{ev(0, SurfaceSite, "email_click", map[string]any{"path": "/ru/start/", "utm_campaign": "oct"})})
	if len(got) != 1 {
		t.Fatalf("want one session, got %d", len(got))
	}
	s := got[0]
	if s.EntryPath != "/start" || s.Touch.Kind != "email_click" || s.Touch.Campaign != "oct" || s.Pages != 0 {
		t.Errorf("session = %+v", s)
	}
}

func TestClassifyTouch(t *testing.T) {
	at := t0
	cases := []struct {
		name      string
		pv, click map[string]any
		wantOK    bool
		src, med  string
		kind      string
	}{
		{"utm wins over referrer", map[string]any{"utm_source": "Facebook", "utm_medium": "CPC", "ref_host": "l.facebook.com"}, nil, true, "facebook", "cpc", "utm"},
		{"search engine is organic", map[string]any{"ref_host": "www.google.cz"}, nil, true, "google", "organic", "organic"},
		{"seznam is a search engine", map[string]any{"ref_host": "search.seznam.cz"}, nil, true, "seznam", "organic", "organic"},
		{"no referrer is organic direct", map[string]any{"path": "/"}, nil, true, "direct", "organic", "organic"},
		{"other host is referral", map[string]any{"ref_host": "news.example.org"}, nil, true, "news.example.org", "referral", "referral"},
		{"own site is no touch", map[string]any{"ref_host": "www.arenasoldout.com"}, nil, false, "", "", ""},
		{"email click beats utm", map[string]any{"utm_source": "x"}, map[string]any{"utm_campaign": "c1"}, true, "email", "email", "email_click"},
		{"no page view and no click", nil, nil, false, "", "", ""},
	}
	for _, c := range cases {
		got, ok := ClassifyTouch(c.pv, c.click, at)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v", c.name, ok)
			continue
		}
		if ok && (got.Source != c.src || got.Medium != c.med || got.Kind != c.kind) {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestNormalizePathAndKinds(t *testing.T) {
	cases := map[string]string{
		"/ru/blog/Post-One/": "/blog/post-one",
		"/blog/post-one":     "/blog/post-one",
		"/cs":                "/",
		"/pricing/":          "/pricing",
		"/en-gb/pricing":     "/pricing",
		"":                   "",
		"blog/x":             "/blog/x",
	}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsArticlePath("/blog/x") || IsArticlePath("/blog") || IsArticlePath("/blog/x/y") || IsArticlePath("/pricing") {
		t.Error("IsArticlePath wrong")
	}
	if !IsPricingPath("/pricing") || !IsPricingPath("/calculator") || IsPricingPath("/blog/pricing") {
		t.Error("IsPricingPath wrong")
	}
}

func TestArticleReads_Rule(t *testing.T) {
	post := map[string]any{"path": "/ru/blog/a"}
	scroll := func(d int) map[string]any { return map[string]any{"path": "/blog/a", "depth": float64(d)} }
	engaged := func(sec int) map[string]any { return map[string]any{"path": "/blog/a", "seconds": float64(sec)} }

	read := SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", post),
		ev(20*time.Second, SurfaceSite, "scroll", scroll(75)),
		ev(time.Minute, SurfaceSite, "engaged", engaged(20)),
		ev(2*time.Minute, SurfaceSite, "engaged", engaged(10)),
	})
	if got := ArticleReads(read); len(got) != 1 || got[0].Path != "/blog/a" || got[0].Seconds != 30 {
		t.Errorf("30 s and 75 %% must count: %+v", got)
	}
	tooShort := SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", post),
		ev(time.Second, SurfaceSite, "scroll", scroll(100)),
		ev(time.Minute, SurfaceSite, "engaged", engaged(29)),
	})
	if got := ArticleReads(tooShort); len(got) != 0 {
		t.Errorf("29 s must not count: %+v", got)
	}
	shallow := SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", post),
		ev(time.Second, SurfaceSite, "scroll", scroll(50)),
		ev(time.Minute, SurfaceSite, "engaged", engaged(120)),
	})
	if got := ArticleReads(shallow); len(got) != 0 {
		t.Errorf("50 %% scroll must not count: %+v", got)
	}
	notArticle := SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", map[string]any{"path": "/pricing"}),
		ev(time.Second, SurfaceSite, "scroll", map[string]any{"path": "/pricing", "depth": float64(100)}),
		ev(time.Minute, SurfaceSite, "engaged", map[string]any{"path": "/pricing", "seconds": float64(300)}),
	})
	if got := ArticleReads(notArticle); len(got) != 0 {
		t.Errorf("the price page is not an article: %+v", got)
	}
}

func TestLatestArticles_DedupAndCap(t *testing.T) {
	var in []ArticleRead
	for i := 0; i < 25; i++ {
		in = append(in, ArticleRead{Path: "/blog/p" + string(rune('a'+i)), At: t0.Add(time.Duration(i) * time.Hour)})
	}
	in = append(in, ArticleRead{Path: "/blog/pa", At: t0.Add(100 * time.Hour)}) // newer read of the oldest
	got := LatestArticles(in)
	if len(got) != MaxArticles {
		t.Fatalf("len = %d, want %d", len(got), MaxArticles)
	}
	if got[0].Path != "/blog/pa" {
		t.Errorf("newest first: %+v", got[0])
	}
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a.Path] {
			t.Errorf("duplicate %s", a.Path)
		}
		seen[a.Path] = true
	}
}

func TestScore_WeightsAndDecay(t *testing.T) {
	now := t0
	w := DefaultScoreWeights
	if got := Score([]ScoreItem{{ScoreApplicationSubmitted, now}}, now, w); got != 20 {
		t.Errorf("fresh submitted application = %d, want 20", got)
	}
	if got := Score([]ScoreItem{{ScoreApplicationSubmitted, now.AddDate(0, 0, -14)}}, now, w); got != 10 {
		t.Errorf("after one half-life = %d, want 10", got)
	}
	if got := Score([]ScoreItem{{ScoreApplicationSubmitted, now.AddDate(0, 0, -28)}}, now, w); got != 5 {
		t.Errorf("after two half-lives = %d, want 5", got)
	}
	all := []ScoreItem{
		{ScoreVisit, now}, {ScoreArticle, now}, {ScorePricing, now}, {ScoreEmailClick, now},
		{ScoreApplicationStarted, now}, {ScoreApplicationSubmitted, now},
	}
	if got := Score(all, now, w); got != 1+3+5+2+10+20 {
		t.Errorf("v1 weights sum = %d, want 41", got)
	}
	// Age is counted in calendar days: 23 hours apart across midnight is one day.
	late := time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC)
	early := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	if a, b := Score([]ScoreItem{{ScoreApplicationSubmitted, late}}, late, w), Score([]ScoreItem{{ScoreApplicationSubmitted, late}}, early, w); a != 20 || b >= a {
		t.Errorf("day boundary: same-day %d, next-day %d", a, b)
	}
	// Within one UTC day the number does not move.
	h1 := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	h2 := time.Date(2026, 11, 3, 22, 0, 0, 0, time.UTC)
	it := []ScoreItem{{ScoreApplicationSubmitted, t0}, {ScoreVisit, t0}}
	if Score(it, h1, w) != Score(it, h2, w) {
		t.Error("score must not change within a UTC day")
	}
	if !strings.Contains(ExplainScore(all, now, w), "article x1 = 3.0") {
		t.Errorf("explain = %q", ExplainScore(all, now, w))
	}
	if ExplainScore(nil, now, w) != "no activity" {
		t.Error("empty explain")
	}
}

func TestSessionScoreItems(t *testing.T) {
	sessions := SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", map[string]any{"path": "/pricing"}),
		ev(time.Minute, SurfaceSite, "page_view", map[string]any{"path": "/calculator"}),
		ev(2*time.Minute, SurfaceSite, "email_click", map[string]any{"path": "/"}),
	})
	items := SessionScoreItems(sessions, []ArticleRead{{Path: "/blog/a", At: t0}})
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Kind]++
	}
	if counts[ScoreVisit] != 1 || counts[ScorePricing] != 1 || counts[ScoreEmailClick] != 1 || counts[ScoreArticle] != 1 {
		t.Errorf("counts = %v (pricing counts once per session)", counts)
	}
}

func TestStageFor(t *testing.T) {
	tm := map[string]time.Time{}
	if StageFor(tm) != StageLead {
		t.Error("empty = lead")
	}
	tm[MsApplicationStarted] = t0
	if StageFor(tm) != StageApplicant {
		t.Error("started = applicant")
	}
	tm[MsApplicationApproved] = t0
	if StageFor(tm) != StageOrganizer {
		t.Error("approved = organizer")
	}
	tm[MsFirstSale] = t0
	if StageFor(tm) != StageSelling {
		t.Error("first sale = selling")
	}
}

func TestTouchBounds(t *testing.T) {
	a := Touch{Source: "a", Kind: "utm", At: t0}
	b := Touch{Source: "b", Kind: "utm", At: t0.Add(time.Hour)}
	if EarlierTouch(a, b).Source != "a" || EarlierTouch(b, a).Source != "a" || EarlierTouch(Touch{}, b).Source != "b" {
		t.Error("EarlierTouch")
	}
	if LaterTouch(a, b).Source != "b" || LaterTouch(b, a).Source != "b" || LaterTouch(a, Touch{}).Source != "a" {
		t.Error("LaterTouch")
	}
	f, l := SessionTouches(SplitSessions([]Ev{
		ev(0, SurfaceSite, "page_view", map[string]any{"utm_source": "x"}),
		ev(time.Hour, SurfaceSite, "page_view", map[string]any{"utm_source": "y"}),
	}))
	if f.Source != "x" || l.Source != "y" {
		t.Errorf("first %q last %q", f.Source, l.Source)
	}
}

func TestComputeFlowDaily_EventCenter(t *testing.T) {
	step := func(off time.Duration, who, flow, st string) FlowEv {
		return FlowEv{At: t0.Add(off), Surface: SurfaceEventCenter, Name: "flow_step", Who: who,
			Props: map[string]any{"flow": flow, "step": st}}
	}
	evs := []FlowEv{
		step(0, "user:1", "event_create", "title"),
		step(30*time.Second, "user:1", "event_create", "venue"),
		{At: t0.Add(40 * time.Second), Surface: SurfaceEventCenter, Name: "ui_error", Who: "user:1",
			Props: map[string]any{"screen": "/events/new", "code": "venue.invalid"}},
		step(90*time.Second, "user:1", "event_create", "tickets"),
		// second user quits at venue
		step(0, "user:2", "event_create", "title"),
		step(20*time.Second, "user:2", "event_create", "venue"),
		// error with no flow around it
		{At: t0.Add(5 * time.Hour), Surface: SurfaceEventCenter, Name: "ui_error", Who: "user:3",
			Props: map[string]any{"screen": "/refunds", "code": "x"}},
	}
	rows := ComputeFlowDaily(evs)
	get := func(flow, step string) FlowRow {
		for _, r := range rows {
			if r.Flow == flow && r.Step == step {
				return r
			}
		}
		t.Fatalf("no row %s/%s in %+v", flow, step, rows)
		return FlowRow{}
	}
	title := get("event_create", "title")
	if title.Entered != 2 || title.LeftHere != 0 || title.MedianSeconds == nil || *title.MedianSeconds != 25 {
		t.Errorf("title = %+v (median of 30 and 20 is 25)", title)
	}
	venue := get("event_create", "venue")
	if venue.Entered != 2 || venue.LeftHere != 1 || venue.Errors != 1 {
		t.Errorf("venue = %+v", venue)
	}
	if venue.MedianSeconds == nil || *venue.MedianSeconds != 60 {
		t.Errorf("venue median = %v, want 60 (only user 1 moved on)", venue.MedianSeconds)
	}
	tickets := get("event_create", "tickets")
	if tickets.Entered != 1 || tickets.LeftHere != 1 {
		t.Errorf("tickets = %+v", tickets)
	}
	if r := get(FlowScreens, "/refunds"); r.Errors != 1 || r.Entered != 0 {
		t.Errorf("screen error = %+v", r)
	}
}

func TestComputeFlowDaily_RunsSplitOnGapAndBot(t *testing.T) {
	evs := []FlowEv{
		{At: t0, Surface: SurfaceEventCenter, Name: "flow_step", Who: "user:1", Props: map[string]any{"flow": "refund", "step": "a"}},
		{At: t0.Add(2 * time.Hour), Surface: SurfaceEventCenter, Name: "flow_step", Who: "user:1", Props: map[string]any{"flow": "refund", "step": "a"}},
		{At: t0, Surface: SurfaceBot, Name: "wizard_step", Who: "user:9", Props: map[string]any{"from": "", "to": "name", "mode": "create"}},
		{At: t0.Add(15 * time.Second), Surface: SurfaceBot, Name: "wizard_step", Who: "user:9", Props: map[string]any{"from": "name", "to": "date", "seconds": float64(15)}},
		{At: t0.Add(20 * time.Second), Surface: SurfaceBot, Name: "wizard_input_error", Who: "user:9", Props: map[string]any{"step": "date", "reason": "date_unparsed"}},
		{At: t0.Add(30 * time.Second), Surface: SurfaceBot, Name: "wizard_exit", Who: "user:9", Props: map[string]any{"step": "date", "how": "cancel"}},
		{At: t0.Add(40 * time.Second), Surface: SurfaceBot, Name: "wizard_exit", Who: "user:8", Props: map[string]any{"step": "done", "how": "done"}},
	}
	rows := ComputeFlowDaily(evs)
	var refund, name, date *FlowRow
	for i := range rows {
		r := &rows[i]
		switch {
		case r.Flow == "refund":
			refund = r
		case r.Flow == FlowWizard && r.Step == "name":
			name = r
		case r.Flow == FlowWizard && r.Step == "date":
			date = r
		}
		if r.Step == "done" {
			t.Errorf("a completed wizard is not a drop-off: %+v", r)
		}
	}
	if refund == nil || refund.Entered != 2 || refund.LeftHere != 2 {
		t.Errorf("refund = %+v (two runs, both ended at a)", refund)
	}
	if name == nil || name.Entered != 1 || name.MedianSeconds == nil || *name.MedianSeconds != 15 {
		t.Errorf("name = %+v", name)
	}
	if date == nil || date.Entered != 1 || date.Errors != 1 || date.LeftHere != 1 {
		t.Errorf("date = %+v", date)
	}
}

func TestEmailSHA256_NormalizesCaseAndSpace(t *testing.T) {
	if EmailSHA256(" Anna@Example.COM ") != EmailSHA256("anna@example.com") {
		t.Error("case/space must not matter")
	}
	if len(EmailSHA256("a@b.c")) != 64 {
		t.Error("hex sha-256")
	}
}

func TestCleanAID(t *testing.T) {
	for in, want := range map[string]string{
		"a_7Hk2-abcdEF": "a_7Hk2-abcdEF", " a_7Hk2-abcdEF ": "a_7Hk2-abcdEF",
		"short": "", "has space in it": "", "ünïcode-aid-123": "", "": "",
		"x12345678901234567890123456789012345678901": "",
	} {
		if got := CleanAID(in); got != want {
			t.Errorf("CleanAID(%q) = %q, want %q", in, got, want)
		}
	}
}
