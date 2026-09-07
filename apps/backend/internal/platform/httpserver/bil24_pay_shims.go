// bil24_pay_shims.go bridges the *Server god-object to hbil24's PAY_ORDER
// surface (feature #494, W1-B2a, spec §7.9). It lives apart from
// bil24_shims.go purely to keep that file under the repo's 400-line ceiling
// for top-level httpserver files.
//
// Both dependencies are closures rather than interfaces for the usual reason:
// package httpserver imports hbil24, so hbil24 must never import back.
package httpserver

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbil24"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htickets"
)

// bil24PayOrderAction is the audit_events.action for the spec §7.9 step 2
// operator alert. Operators filter on it to find paid orders whose inventory
// could not be restored.
const bil24PayOrderAction = "bil24.pay_order.manual_review"

// bil24PayDeps builds the PAY_ORDER dependency bundle.
//
// IssueTickets is the load-bearing one: spec §7.10 has the WordPress site poll
// GET_TICKETS_BY_ORDER five times with 2/4/8s backoff, and issuing
// synchronously right after the payment commit is what makes the FIRST poll
// succeed. Without ticket queries there is nothing to issue, so the bundle is
// returned unwired and PAY_ORDER self-gates with -5 rather than reporting a
// payment it cannot fulfil.
//
// Alert is optional: hbil24 logs the manual-review incident at error level
// regardless, so a Server without an audit writer degrades the alert to
// log-only instead of dropping it.
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

	if s.audit != nil {
		w := s.audit
		deps.Alert = func(ctx context.Context, orderID uuid.UUID, actor string, metadata map[string]any) {
			md := make(map[string]any, len(metadata)+1)
			for k, v := range metadata {
				md[k] = v
			}
			// audit_events.actor_id is a uuid column cast with
			// NULLIF($3,'')::uuid, so the non-UUID 'gateway:<fid>' label has to
			// travel as metadata — passing it as ActorID aborts the enclosing
			// transaction with SQLSTATE 22P02.
			md["actor"] = actor
			if err := audit.WriteEvent(ctx, w, "gateway", "", bil24PayOrderAction,
				"order", orderID.String(), md); err != nil {
				s.logger.Error("bil24_compat: PAY_ORDER: manual-review audit write failed",
					"order_id", orderID.String(), "error", err.Error())
			}
		}
	}

	return deps
}
