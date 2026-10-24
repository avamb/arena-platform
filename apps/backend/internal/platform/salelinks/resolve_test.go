package salelinks

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const testBase = "https://tickets.example"

var (
	sessID  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	evtID   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	promID  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	okFacts = Facts{
		OrgSlug: "acme", EventSlug: "show", EventPublished: true, SessionLive: true,
		EventPromoterSlug: "stage", PromoterSlug: "club",
	}
)

func linkOf(kind string) Link {
	l := Link{OrgID: uuid.New(), Code: "x7k2qpm", Target: Target{Kind: kind, URL: "https://other.example/p?a=1"}}
	switch kind {
	case KindSession:
		l.Target.SessionID, l.Target.EventID = &sessID, &evtID
	case KindEvent:
		l.Target.EventID = &evtID
	case KindPromoterPage:
		l.Target.PromoterID = &promID
	}
	return l
}

func TestResolveWithFacts_EveryRung(t *testing.T) {
	with := func(f Facts, fn func(*Facts)) Facts { fn(&f); return f }
	sessionURL := testBase + "/stage/show?session=" + sessID.String()
	for _, c := range []struct {
		name     string
		link     Link
		facts    Facts
		want     string
		degraded bool
	}{
		{"session live", linkOf(KindSession), okFacts, sessionURL, false},
		{"session live, event promoter archived", linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.EventPromoterArchived = true }),
			testBase + "/acme/show?session=" + sessID.String(), false},
		{"session cancelled falls to event", linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.SessionLive = false }), testBase + "/stage/show", true},
		{"session of an unpublished event falls to the event promoter page", linkOf(KindSession),
			with(okFacts, func(f *Facts) { f.EventPublished = false }), testBase + "/stage", true},
		{"session without a session id falls to event", func() Link { l := linkOf(KindSession); l.Target.SessionID = nil; return l }(),
			okFacts, testBase + "/stage/show", true},
		{"event published", linkOf(KindEvent), okFacts, testBase + "/stage/show", false},
		{"event without promoter", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPromoterSlug = "" }), testBase + "/acme/show", false},
		{"event promoter archived uses the organization page slug", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPromoterArchived = true }), testBase + "/acme/show", false},
		{"event unpublished, event promoter page is the fallback", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false }), testBase + "/stage", true},
		{"event unpublished, no promoter, organization page", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false; f.EventPromoterSlug = "" }), testBase + "/acme", true},
		{"event without a slug", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventSlug = "" }), testBase + "/stage", true},
		{"event whose promoter is archived, unpublished: organization", linkOf(KindEvent),
			with(okFacts, func(f *Facts) { f.EventPublished = false; f.EventPromoterArchived = true }), testBase + "/acme", true},
		{"promoter page", linkOf(KindPromoterPage), okFacts, testBase + "/club", false},
		{"promoter page, promoter archived", linkOf(KindPromoterPage),
			with(okFacts, func(f *Facts) { f.PromoterArchived = true }), testBase + "/acme", true},
		{"promoter page, promoter without slug", linkOf(KindPromoterPage),
			with(okFacts, func(f *Facts) { f.PromoterSlug = "" }), testBase + "/acme", true},
		{"org page", linkOf(KindOrgPage), okFacts, testBase + "/acme", false},
		{"url as it is", linkOf(KindURL), Facts{}, "https://other.example/p?a=1", false},
		{"url ignores unavailable facts", linkOf(KindURL),
			Facts{OrgSlug: "acme", EventPublished: false}, "https://other.example/p?a=1", false},
		{"archived session link goes to the organization page", func() Link { l := linkOf(KindSession); l.Archived = true; return l }(),
			okFacts, testBase + "/acme", true},
		{"archived url link goes to the organization page", func() Link { l := linkOf(KindURL); l.Archived = true; return l }(),
			okFacts, testBase + "/acme", true},
		{"archived org page link", func() Link { l := linkOf(KindOrgPage); l.Archived = true; return l }(),
			okFacts, testBase + "/acme", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, degraded := ResolveWithFacts(testBase, c.link, c.facts)
			if got != c.want || degraded != c.degraded {
				t.Errorf("got (%q, %v), want (%q, %v)", got, degraded, c.want, c.degraded)
			}
		})
	}
}

func TestResolveWithFacts_EventLinkFallsToTheEventPromoter(t *testing.T) {
	// An event link carries no promoter of its own: after the event is gone the
	// promoter of that event is the next page.
	f := okFacts
	f.EventPublished = false
	f.PromoterSlug = "ignored-link-promoter"
	got, deg := ResolveWithFacts(testBase, linkOf(KindEvent), f)
	if got != testBase+"/stage" || !deg {
		t.Errorf("got (%q, %v)", got, deg)
	}
}

func TestResolveWithFacts_NoBaseOrSlug(t *testing.T) {
	if got, _ := ResolveWithFacts("", linkOf(KindOrgPage), okFacts); got != "" {
		t.Errorf("no base address must give an empty destination, got %q", got)
	}
	if got, deg := ResolveWithFacts(testBase, linkOf(KindOrgPage), Facts{}); got != "" || deg {
		t.Errorf("no slug: (%q, %v)", got, deg)
	}
	if got, _ := ResolveWithFacts("", linkOf(KindURL), Facts{}); got != "https://other.example/p?a=1" {
		t.Errorf("a url link needs no base address, got %q", got)
	}
}

type fakeFacts struct {
	row   gen.SalesLinkTargetFactsRow
	err   error
	calls int
	gotE  *uuid.UUID
	gotS  *uuid.UUID
	gotP  *uuid.UUID
}

func (f *fakeFacts) GetSalesLinkTargetFacts(_ context.Context, _ uuid.UUID, e, s, p *uuid.UUID) (gen.SalesLinkTargetFactsRow, error) {
	f.calls++
	f.gotE, f.gotS, f.gotP = e, s, p
	return f.row, f.err
}

func TestResolve(t *testing.T) {
	q := &fakeFacts{row: gen.SalesLinkTargetFactsRow{
		OrgSlug: "acme", EventSlug: "show", EventPublished: true, SessionLive: true, EventPromoterSlug: "stage",
	}}
	dest, deg, err := Resolve(context.Background(), q, testBase, linkOf(KindSession))
	if err != nil || deg || dest != testBase+"/stage/show?session="+sessID.String() {
		t.Fatalf("got (%q, %v, %v)", dest, deg, err)
	}
	if q.calls != 1 || q.gotE == nil || q.gotS == nil || q.gotP != nil {
		t.Errorf("the targets of the link must reach the query: calls=%d %v %v %v", q.calls, q.gotE, q.gotS, q.gotP)
	}

	t.Run("a url link makes no query", func(t *testing.T) {
		q := &fakeFacts{err: errors.New("must not be called")}
		dest, deg, err := Resolve(context.Background(), q, testBase, linkOf(KindURL))
		if err != nil || deg || dest != "https://other.example/p?a=1" || q.calls != 0 {
			t.Errorf("got (%q, %v, %v) calls=%d", dest, deg, err, q.calls)
		}
	})
	t.Run("an archived url link needs the organization", func(t *testing.T) {
		l := linkOf(KindURL)
		l.Archived = true
		q := &fakeFacts{row: gen.SalesLinkTargetFactsRow{OrgSlug: "acme"}}
		dest, deg, err := Resolve(context.Background(), q, testBase, l)
		if err != nil || !deg || dest != testBase+"/acme" {
			t.Errorf("got (%q, %v, %v)", dest, deg, err)
		}
	})
	t.Run("no organization", func(t *testing.T) {
		q := &fakeFacts{err: pgx.ErrNoRows}
		_, deg, err := Resolve(context.Background(), q, testBase, linkOf(KindOrgPage))
		if !errors.Is(err, ErrOrgNotFound) || !deg {
			t.Errorf("err=%v deg=%v", err, deg)
		}
	})
	t.Run("a database error is passed on", func(t *testing.T) {
		boom := errors.New("boom")
		q := &fakeFacts{err: boom}
		_, _, err := Resolve(context.Background(), q, testBase, linkOf(KindEvent))
		if !errors.Is(err, boom) {
			t.Errorf("err=%v", err)
		}
	})
}
