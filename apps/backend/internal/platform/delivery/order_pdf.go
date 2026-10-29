// order_pdf.go — one PDF holding every ticket of an order, rendered on demand
// for the organizer's "download" (spec 46, section 3.4). The pages are the
// buyer's own e-ticket layout, one ticket per page in ticket-number order.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/pdf"
)

// OrderPDFOptions are the dependencies of RenderOrderPDF. They are a subset of
// HandlerOptions, so the API process (which never builds the ticket.deliver
// handler) can supply them.
type OrderPDFOptions struct {
	HandlerOptions
}

// ErrNoTickets is returned by RenderOrderPDF when it is given nothing to draw.
var ErrNoTickets = errors.New("delivery: no tickets to render")

// RenderOrderPDF renders the given tickets as the pages of one PDF. locale is
// the language of the printed labels ("" falls back to English); invitation
// marks the pages as complimentary invitations. Every ticket is resolved from
// its own rows exactly as the e-mail's attachment is (seat, EAN-13, event,
// holder, branding, poster), so the file matches what the guest receives.
func RenderOrderPDF(ctx context.Context, opts OrderPDFOptions, ticketIDs []uuid.UUID, locale string, invitation bool) ([]byte, error) {
	if len(ticketIDs) == 0 {
		return nil, ErrNoTickets
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	resolver := &changeEmail{opts: ChangeEmailOptions{HandlerOptions: opts.HandlerOptions}, logger: logger}

	type page struct {
		payload Payload
		ticket  pdf.Ticket
	}
	pages := make([]page, 0, len(ticketIDs))
	for _, id := range ticketIDs {
		p := resolver.ticketPayload(ctx, id, locale)
		if invitation {
			p.Template = TemplateInvitation
		}
		brand := resolveBranding(ctx, opts.Media, p, logger)
		poster := resolvePoster(ctx, opts.Media, p.PosterMediaID, logger)
		pages = append(pages, page{payload: p, ticket: buildPDFTicket(id, p, brand.Branding, brand.LogoBytes, poster)})
	}
	// Ticket-number order, like the e-mail's attachments.
	sort.SliceStable(pages, func(i, j int) bool {
		a, aErr := strconv.ParseInt(pages[i].payload.TicketNumber, 10, 64)
		b, bErr := strconv.ParseInt(pages[j].payload.TicketNumber, 10, 64)
		return aErr == nil && bErr == nil && a < b
	})

	tickets := make([]pdf.Ticket, len(pages))
	for i, pg := range pages {
		tickets[i] = pg.ticket
	}
	out, err := pdf.RenderMany(ctx, tickets)
	if err != nil {
		return nil, fmt.Errorf("delivery: render order pdf: %w", err)
	}
	return out, nil
}
