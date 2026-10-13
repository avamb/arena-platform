package delivery

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ContactFormAnchor is the fragment of a hosted page that opens its "Write
// to the organizer" form (tickets-page lib/contact.ts).
const ContactFormAnchor = "contact"

// ContactRoute is how a letter tells the buyer to reach the organizer, and
// where replies to the letter go. Owner rule (2026-10-13): personal addresses
// are never published.
//
//   - Email set: the sales page's public e-mail, or - for a ticket sold
//     WITHOUT a hosted page - the organization's contact_email, as before.
//     It is printed and is the letter's Reply-To.
//   - Email empty and FormURL set: the ticket was sold through a hosted page
//     that has no public e-mail, and the form's messages can go to the
//     organization's contact_email or its owner's account e-mail (neither is
//     printed). The letter prints the link to the
//     page's contact form, has no Reply-To and goes from the platform address.
//   - Both empty: nothing is printed and there is no Reply-To.
type ContactRoute struct {
	Email   string
	FormURL string
}

// ContactRouteFor applies the rule to one ticket's route row. ticketsBaseURL
// is PUBLIC_TICKETS_BASE_URL; without it, or without a page slug, a hosted
// page gets no link (and still never the organization's address).
func ContactRouteFor(r gen.TicketContactRoute, ticketsBaseURL string) ContactRoute {
	if v := trimPtr(r.PublicEmail); v != "" {
		return ContactRoute{Email: v}
	}
	if !r.HostedPage {
		return ContactRoute{Email: trimPtr(r.OrgEmail)}
	}
	// The page offers its form only when a message has somewhere to go: the
	// public e-mail, else the organization's own address, else its owner's
	// account e-mail (hfeed contactForm). Without any the link would open a
	// page with no form.
	if trimPtr(r.OrgEmail) == "" && !r.OwnerReachable {
		return ContactRoute{}
	}
	base := strings.TrimRight(strings.TrimSpace(ticketsBaseURL), "/")
	page := trimPtr(r.PageSlug)
	if base == "" || page == "" {
		return ContactRoute{}
	}
	link := base + "/" + url.PathEscape(page)
	if ev := trimPtr(r.EventSlug); ev != "" {
		link += "/" + url.PathEscape(ev)
	}
	return ContactRoute{FormURL: link + "#" + ContactFormAnchor}
}

// contactRouteQuerier is the one query the rule needs.
type contactRouteQuerier interface {
	GetTicketContactRoute(ctx context.Context, ticketID uuid.UUID) (gen.TicketContactRoute, error)
}

// applyContactRoute resolves the ticket's route when the letter is rendered
// and writes it onto the payload: OrgContactEmail (printed, Reply-To) and
// ContactFormURL. A job queued before this rule, or one whose enqueuer copied
// an address, is corrected here. When the lookup fails the payload keeps
// what the enqueuer stored - GetOrgBrandingByTicketID applies the same rule.
func applyContactRoute(ctx context.Context, q contactRouteQuerier, ticketsBaseURL string, ticketID uuid.UUID, p *Payload, logger *slog.Logger) {
	if q == nil || ticketID == uuid.Nil {
		return
	}
	row, err := q.GetTicketContactRoute(ctx, ticketID)
	if err != nil {
		if logger != nil {
			logger.Warn("delivery: contact route lookup failed; keeping the enqueued contact",
				slog.String("ticket_id", ticketID.String()), slog.String("error", err.Error()))
		}
		return
	}
	route := ContactRouteFor(row, ticketsBaseURL)
	p.OrgContactEmail = route.Email
	p.ContactFormURL = route.FormURL
}

// queriesOrNil keeps a nil *gen.Queries from turning into a non-nil interface.
func queriesOrNil(q *gen.Queries) contactRouteQuerier {
	if q == nil {
		return nil
	}
	return q
}

func trimPtr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
