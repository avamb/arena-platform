package salelinks

import (
	"context"
	"errors"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/publicurl"
)

// Target kinds of a sales link (the target_kind column of sales_links).
const (
	KindOrgPage      = "org_page"
	KindPromoterPage = "promoter_page"
	KindEvent        = "event"
	KindSession      = "session"
	KindURL          = "url"
)

// ErrOrgNotFound is returned by Resolve when the organization of the link no
// longer exists, so no page can be named at all.
var ErrOrgNotFound = errors.New("salelinks: organization not found")

// Target is what a link points at.
type Target struct {
	Kind       string
	EventID    *uuid.UUID
	SessionID  *uuid.UUID
	PromoterID *uuid.UUID
	URL        string // only for KindURL
}

// Link is the part of a sales_links row the resolution needs.
type Link struct {
	OrgID    uuid.UUID
	Code     string
	Target   Target
	Archived bool
	UTM      map[string]string
}

// Facts are the rows the chain of spec 43 section 4.3 looks at. They are read
// with the conditions of the hosted page itself, so a destination the chain
// accepts is a page that exists.
type Facts struct {
	OrgSlug               string
	EventSlug             string
	EventPublished        bool   // event published and not deleted
	EventPromoterSlug     string // promoter of the EVENT, empty when the organization itself promotes it
	EventPromoterArchived bool
	SessionLive           bool   // session not deleted and not cancelled
	PromoterSlug          string // promoter of the LINK (Target.PromoterID)
	PromoterArchived      bool
}

// ResolveWithFacts walks the fall chain of spec 43 section 4.3 over facts that
// are already loaded. It is pure. degraded is true when the destination is not
// the one the link names: the chain fell to a higher page (or the link is
// archived). An empty destination means no page address can be built at all
// (no base address or no slug).
//
//	session       live session of a published event ? event page + ?session=<id> : event
//	event         published event with a slug ? its page (promoter of the event) : promoter_page
//	promoter_page promoter not archived, with a slug ? its page : org_page
//	org_page      the page of the organization
//	url           as it is, no availability check
//	archived      the page of the organization at once
//
// The promoter in an event address is the promoter of the EVENT; for a
// promoter_page link it is the promoter of the link, and when the chain fell
// from an event link that has no promoter of its own, the promoter of the event.
func ResolveWithFacts(base string, link Link, f Facts) (destination string, degraded bool) {
	orgPage := func() (string, bool) { return publicurl.Page(base, f.OrgSlug, "", ""), true }
	if link.Archived {
		return orgPage()
	}
	kind := link.Target.Kind
	if kind == KindURL {
		return link.Target.URL, false
	}

	if kind == KindSession {
		if f.SessionLive && link.Target.SessionID != nil && eventPageReady(f) {
			// No page address (no base or no slug) means no destination: a bare
			// "?session=..." must never be returned.
			page := eventPage(base, f)
			if page == "" {
				return "", false
			}
			return page + "?session=" + url.QueryEscape(link.Target.SessionID.String()), false
		}
		kind = KindEvent
		degraded = true
	}
	if kind == KindEvent {
		if eventPageReady(f) {
			return eventPage(base, f), degraded
		}
		kind = KindPromoterPage
		degraded = true
	}
	if kind == KindPromoterPage {
		slug, archived := f.PromoterSlug, f.PromoterArchived
		if link.Target.PromoterID == nil {
			slug, archived = f.EventPromoterSlug, f.EventPromoterArchived
		}
		if slug != "" && !archived {
			return publicurl.Page(base, f.OrgSlug, slug, ""), degraded
		}
		return orgPage()
	}
	if kind == KindOrgPage {
		return publicurl.Page(base, f.OrgSlug, "", ""), degraded
	}
	// An unknown kind never happens (CHECK constraint); the safe page is the organization.
	return orgPage()
}

func eventPageReady(f Facts) bool { return f.EventPublished && f.EventSlug != "" }

func eventPage(base string, f Facts) string {
	promoter := ""
	if !f.EventPromoterArchived {
		promoter = f.EventPromoterSlug
	}
	return publicurl.Page(base, f.OrgSlug, promoter, f.EventSlug)
}

// FactsQuerier loads the facts of a link target. *gen.Queries implements it.
type FactsQuerier interface {
	GetSalesLinkTargetFacts(ctx context.Context, orgID uuid.UUID, eventID, sessionID, promoterID *uuid.UUID) (gen.SalesLinkTargetFactsRow, error)
}

// Resolve loads the facts with one query and runs ResolveWithFacts. A plain
// url link needs no facts and no query. When the organization is gone it
// returns ErrOrgNotFound (and degraded=true): there is no page to fall to.
func Resolve(ctx context.Context, q FactsQuerier, base string, link Link) (destination string, degraded bool, err error) {
	if link.Target.Kind == KindURL && !link.Archived {
		return link.Target.URL, false, nil
	}
	row, err := q.GetSalesLinkTargetFacts(ctx, link.OrgID, link.Target.EventID, link.Target.SessionID, link.Target.PromoterID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", true, ErrOrgNotFound
		}
		return "", true, err
	}
	dest, deg := ResolveWithFacts(base, link, Facts{
		OrgSlug:               row.OrgSlug,
		EventSlug:             row.EventSlug,
		EventPublished:        row.EventPublished,
		EventPromoterSlug:     row.EventPromoterSlug,
		EventPromoterArchived: row.EventPromoterArchived,
		SessionLive:           row.SessionLive,
		PromoterSlug:          row.PromoterSlug,
		PromoterArchived:      row.PromoterArchived,
	})
	return dest, deg, nil
}
