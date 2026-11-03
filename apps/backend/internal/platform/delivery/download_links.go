// download_links.go — the "Download PDF" link of each ticket in the ticket
// e-mail (spec 51, decision R-13).
//
// The PDF stays attached, but opening an attachment is invisible to arena and
// to Brevo. A link to arena's public PDF route, marked ?src=email, is what
// lets ticket_access_events (migration 0184) record that the buyer fetched the
// ticket from the letter, with the address and the time a chargeback answer
// quotes. One link per ticket, never one for the whole order: the per-ticket
// PDF is stored, an order PDF would have to be rendered on a public route.
package delivery

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// ticketDownloadURL is the public PDF route of one ticket, as followed from
// the ticket e-mail.
func ticketDownloadURL(apiBase, checkoutToken string, ticketID uuid.UUID) string {
	return strings.TrimRight(apiBase, "/") +
		"/v1/public/checkout/" + url.PathEscape(checkoutToken) +
		"/tickets/" + ticketID.String() + "/pdf?src=email"
}

// attachDownloadLinks fills DownloadURL of the ticket e-mail's lines, which
// ticketLines builds in the same order as members. Only the ticket kind gets
// links; without an API address or a ticket store, or when the lookup fails,
// the letter simply goes out without them.
func attachDownloadLinks(ctx context.Context, opts HandlerOptions, logger *slog.Logger, kind string, members []*letterMember, lines []templates.TicketLine) {
	if kind != templates.TemplateKindTicket || opts.APIBaseURL == "" || opts.TicketQueries == nil || len(lines) != len(members) {
		return
	}
	ids := make([]uuid.UUID, len(members))
	for i, m := range members {
		ids[i] = m.ticketID
	}
	tokens, err := opts.TicketQueries.ListCheckoutTokensForTickets(ctx, ids)
	if err != nil {
		logger.Warn("delivery: checkout tokens lookup failed; sending without download links",
			slog.String("ticket_id", ids[0].String()),
			slog.String("error", err.Error()),
		)
		return
	}
	for i, m := range members {
		if token := tokens[m.ticketID]; token != "" {
			lines[i].DownloadURL = ticketDownloadURL(opts.APIBaseURL, token, m.ticketID)
		}
	}
}
