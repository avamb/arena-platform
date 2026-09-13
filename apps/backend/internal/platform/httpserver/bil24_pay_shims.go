// bil24_pay_shims.go bridges the *Server god-object to hbil24's PAY_ORDER
// surface (feature #494, W1-B2a, spec §7.9; payment-window contract, owner
// decision 2026-09-13). It lives apart from bil24_shims.go purely to keep
// that file under the repo's 400-line ceiling for top-level httpserver
// files.
//
// The dependency is a closure rather than an interface for the usual reason:
// package httpserver imports hbil24, so hbil24 must never import back.
package httpserver

import (
	"context"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbil24"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// bil24PayDeps builds the PAY_ORDER dependency bundle.
//
// IssueTickets is the load-bearing one: spec §7.10 has the WordPress site poll
// GET_TICKETS_BY_ORDER five times with 2/4/8s backoff, and issuing
// synchronously right after the payment commit is what makes the FIRST poll
// succeed. Without ticket queries there is nothing to issue, so the bundle is
// returned unwired and PAY_ORDER self-gates with -5 rather than reporting a
// payment it cannot fulfil.
//
// There is no operator-alert dependency any more: by owner decision
// (2026-09-13) PAY_ORDER never parks an order in manual_review — every
// outcome (paid, cancelled, expired) resolves automatically, so there is
// nothing for an operator to be paged about.
func (s *Server) bil24PayDeps() hbil24.PayOrderDeps {
	var deps hbil24.PayOrderDeps

	if s.ticketQueries != nil {
		th := s.ticketsHandler()
		deps.IssueTickets = func(ctx context.Context, cs gen.CheckoutSessionRow, suppressDelivery bool) (int, error) {
			tickets, err := th.IssueTicketsForCheckoutWithOptions(ctx, cs, htickets.IssuanceOptions{
				// Spec §7.9 step 6: the WordPress shop mails its own PDF, so
				// arena's delivery e-mail is suppressed unless the channel sets
				// settings.gateway.platform_email = true.
				SuppressDelivery: suppressDelivery,
			})
			return len(tickets), err
		}
	}

	return deps
}
