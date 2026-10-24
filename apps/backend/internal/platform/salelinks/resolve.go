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

// The reason a code does not lead to the page its target names (the
// destination_reason of the SalesLink answer). Empty means the code leads to
// its target. The chain of ResolveWithFacts may fall more than once; the
// reason kept is the one that decided the FINAL page, so a client can say what
// would bring the code back (publish the event, pick another date, restore the
// promoter) and never advise a retarget that changes nothing.
const (
	// ReasonArchived: the link is in the archive, the code leads to the
	// organization's page whatever the target says.
	ReasonArchived = "archived"
	// ReasonEventNotPublished: the event of the target is not published (a
	// draft, off sale or deleted); publishing it brings the code back.
	ReasonEventNotPublished = "event_not_published"
	// ReasonEventNoSlug: the event is published but has no page address of
	// its own (no slug), so no event page exists to lead to.
	ReasonEventNoSlug = "event_no_slug"
	// ReasonSessionNotLive: the date is cancelled or deleted; the code leads
	// to the event's page. Another date would change the address.
	ReasonSessionNotLive = "session_not_live"
	// ReasonPromoterUnavailable: the promoter the link names is archived or
	// has no page address; the code leads to the organization's page.
	ReasonPromoterUnavailable = "promoter_unavailable"
	// ReasonNoPageAddress: no page address can be built at all (the platform
	// has no PUBLIC_TICKETS_BASE_URL, or the organization has no slug); the
	// redirect sends the buyer to the platform's fallback page.
	ReasonNoPageAddress = "no_page_address"
	// ReasonOrgNotFound: the organization of the link is gone.
	ReasonOrgNotFound = "org_not_found"
)

// Resolution is where a code leads right now and why that is not its target.
type Resolution struct {
	Destination string
	Degraded    bool
	Reason      string
}

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
	r := ResolveDetailed(base, link, f)
	return r.Destination, r.Degraded
}

// ResolveDetailed is ResolveWithFacts with the reason of the fall (see the
// Reason* constants). A destination that is empty for a page target always
// carries ReasonNoPageAddress, whatever moved the chain before: the buyer is
// sent to the platform's fallback page, and that is what the client must say.
func ResolveDetailed(base string, link Link, f Facts) Resolution {
	r := resolveChain(base, link, f)
	if r.Destination == "" && link.Target.Kind != KindURL {
		r.Reason = ReasonNoPageAddress
	}
	return r
}

func resolveChain(base string, link Link, f Facts) Resolution {
	orgPage := func(reason string) Resolution {
		return Resolution{Destination: publicurl.Page(base, f.OrgSlug, "", ""), Degraded: true, Reason: reason}
	}
	if link.Archived {
		return orgPage(ReasonArchived)
	}
	kind := link.Target.Kind
	if kind == KindURL {
		return Resolution{Destination: link.Target.URL}
	}

	var degraded bool
	var reason string
	if kind == KindSession {
		if f.SessionLive && link.Target.SessionID != nil && eventPageReady(f) {
			// No page address (no base or no slug) means no destination: a bare
			// "?session=..." must never be returned.
			page := eventPage(base, f)
			if page == "" {
				return Resolution{}
			}
			return Resolution{Destination: page + "?session=" + url.QueryEscape(link.Target.SessionID.String())}
		}
		kind = KindEvent
		degraded, reason = true, ReasonSessionNotLive
	}
	if kind == KindEvent {
		if eventPageReady(f) {
			return Resolution{Destination: eventPage(base, f), Degraded: degraded, Reason: reason}
		}
		// The event decides the final page: a cancelled date of an unpublished
		// event is explained by the event.
		kind = KindPromoterPage
		degraded, reason = true, ReasonEventNotPublished
		if f.EventPublished {
			reason = ReasonEventNoSlug
		}
	}
	if kind == KindPromoterPage {
		slug, archived := f.PromoterSlug, f.PromoterArchived
		if link.Target.PromoterID == nil {
			slug, archived = f.EventPromoterSlug, f.EventPromoterArchived
		}
		if slug != "" && !archived {
			return Resolution{Destination: publicurl.Page(base, f.OrgSlug, slug, ""), Degraded: degraded, Reason: reason}
		}
		if link.Target.PromoterID != nil {
			// The link's own promoter is gone; an event link that fell this far
			// keeps the event's reason.
			reason = ReasonPromoterUnavailable
		}
		return orgPage(reason)
	}
	if kind == KindOrgPage {
		return Resolution{Destination: publicurl.Page(base, f.OrgSlug, "", ""), Degraded: degraded, Reason: reason}
	}
	// An unknown kind never happens (CHECK constraint); the safe page is the organization.
	return orgPage("")
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
	r, err := ResolveFull(ctx, q, base, link)
	return r.Destination, r.Degraded, err
}

// ResolveFull is Resolve with the reason. When the organization is gone the
// Resolution carries ReasonOrgNotFound next to ErrOrgNotFound; any other
// error leaves the reason empty.
func ResolveFull(ctx context.Context, q FactsQuerier, base string, link Link) (Resolution, error) {
	if link.Target.Kind == KindURL && !link.Archived {
		return Resolution{Destination: link.Target.URL}, nil
	}
	row, err := q.GetSalesLinkTargetFacts(ctx, link.OrgID, link.Target.EventID, link.Target.SessionID, link.Target.PromoterID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resolution{Degraded: true, Reason: ReasonOrgNotFound}, ErrOrgNotFound
		}
		return Resolution{Degraded: true}, err
	}
	return ResolveDetailed(base, link, Facts{
		OrgSlug:               row.OrgSlug,
		EventSlug:             row.EventSlug,
		EventPublished:        row.EventPublished,
		EventPromoterSlug:     row.EventPromoterSlug,
		EventPromoterArchived: row.EventPromoterArchived,
		SessionLive:           row.SessionLive,
		PromoterSlug:          row.PromoterSlug,
		PromoterArchived:      row.PromoterArchived,
	}), nil
}
