package salelinks

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The reason names what decided the FINAL page, so a client can say what
// brings the code back and never advises a retarget that changes nothing.
func TestResolveDetailed_ReasonNamesWhatDecidedThePage(t *testing.T) {
	with := func(f Facts, fn func(*Facts)) Facts { fn(&f); return f }
	archived := func(kind string) Link { l := linkOf(kind); l.Archived = true; return l }
	for _, c := range []struct {
		name   string
		base   string
		link   Link
		facts  Facts
		reason string
	}{
		{"session live leads to its target", testBase, linkOf(KindSession), okFacts, ""},
		{"event published leads to its target", testBase, linkOf(KindEvent), okFacts, ""},
		{"promoter page leads to its target", testBase, linkOf(KindPromoterPage), okFacts, ""},
		{"org page leads to its target", testBase, linkOf(KindOrgPage), okFacts, ""},
		{"url leads to its target", "", linkOf(KindURL), Facts{}, ""},
		{"session cancelled: the date", testBase, linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.SessionLive = false }), ReasonSessionNotLive},
		{"session cancelled of an unpublished event: the event decides", testBase, linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.SessionLive = false; f.EventPublished = false }), ReasonEventNotPublished},
		{"session live of an unpublished event: the event", testBase, linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.EventPublished = false }), ReasonEventNotPublished},
		{"event unpublished", testBase, linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false }), ReasonEventNotPublished},
		{"event unpublished, no promoter, organization page: still the event", testBase, linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false; f.EventPromoterSlug = "" }), ReasonEventNotPublished},
		{"event unpublished, event promoter archived: still the event", testBase, linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false; f.EventPromoterArchived = true }), ReasonEventNotPublished},
		{"event published without a slug", testBase, linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventSlug = "" }), ReasonEventNoSlug},
		{"promoter archived", testBase, linkOf(KindPromoterPage),
			with(okFacts, func(f *Facts) { f.PromoterArchived = true }), ReasonPromoterUnavailable},
		{"promoter without a slug", testBase, linkOf(KindPromoterPage),
			with(okFacts, func(f *Facts) { f.PromoterSlug = "" }), ReasonPromoterUnavailable},
		{"archived link", testBase, archived(KindSession), okFacts, ReasonArchived},
		{"archived url link", testBase, archived(KindURL), okFacts, ReasonArchived},
		{"no base address: the fallback page, whatever the target", "", linkOf(KindEvent), okFacts, ReasonNoPageAddress},
		{"no base address for a session", "", linkOf(KindSession), okFacts, ReasonNoPageAddress},
		{"no base address for an archived link", "", archived(KindEvent), okFacts, ReasonNoPageAddress},
		{"no organization slug", testBase, linkOf(KindOrgPage), Facts{}, ReasonNoPageAddress},
		{"unpublished event and no base: the fallback page wins", "", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false; f.EventPromoterSlug = ""; f.OrgSlug = "" }), ReasonNoPageAddress},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := ResolveDetailed(c.base, c.link, c.facts)
			if r.Reason != c.reason {
				t.Errorf("reason = %q, want %q (destination %q, degraded %v)", r.Reason, c.reason, r.Destination, r.Degraded)
			}
			// The two-value form is the same chain.
			dest, deg := ResolveWithFacts(c.base, c.link, c.facts)
			if dest != r.Destination || deg != r.Degraded {
				t.Errorf("ResolveWithFacts (%q, %v) differs from ResolveDetailed (%q, %v)", dest, deg, r.Destination, r.Degraded)
			}
			if r.Reason != "" && r.Reason != ReasonNoPageAddress && !r.Degraded {
				t.Errorf("a reason without degraded: %+v", r)
			}
		})
	}
}

func TestResolveFull_ReasonOnErrors(t *testing.T) {
	q := &fakeFacts{err: pgx.ErrNoRows}
	r, err := ResolveFull(context.Background(), q, testBase, linkOf(KindOrgPage))
	if !errors.Is(err, ErrOrgNotFound) || r.Reason != ReasonOrgNotFound || !r.Degraded || r.Destination != "" {
		t.Errorf("no organization: %+v, %v", r, err)
	}
	boom := errors.New("boom")
	r, err = ResolveFull(context.Background(), &fakeFacts{err: boom}, testBase, linkOf(KindEvent))
	if !errors.Is(err, boom) || r.Reason != "" || !r.Degraded {
		t.Errorf("database error: %+v, %v", r, err)
	}
	r, err = ResolveFull(context.Background(), &fakeFacts{row: gen.SalesLinkTargetFactsRow{OrgSlug: "acme", EventSlug: "show"}}, testBase, linkOf(KindEvent))
	if err != nil || r.Reason != ReasonEventNotPublished || r.Destination != testBase+"/acme" {
		t.Errorf("unpublished event through the query: %+v, %v", r, err)
	}
}
