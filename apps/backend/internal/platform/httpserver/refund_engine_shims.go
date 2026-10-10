// refund_engine_shims.go wires the refund engine (internal/platform/refunds,
// PAY-03) for arena-api: the organization's payment module comes from the
// registry and its own payment config, tickets are cancelled through the
// same AB-49 transaction an operator runs, and v1.ticket.refunded goes out
// through the scanner events publisher. arena-worker builds the same engine
// for refund.sweep (cmd/arena-worker/refund_sweep.go).
package httpserver

import (
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
)

// refundEngine builds the engine, or nil when the server has no raw pool
// (unit-test servers) — the approve route then refuses a refundable payment
// with 503 and writes nothing.
func (s *Server) refundEngine() *refunds.Engine {
	if s.pgxPool == nil {
		return nil
	}
	return refunds.New(refunds.Options{
		DB: s.pgxPool,
		Modules: hcheckout.NewRefundModuleSource(gen.New(s.pgxPool), payments.Options{
			StripeAPIBaseURL: s.stripeBaseURL(),
			FlittAPIBaseURL:  s.flittBaseURL(),
		}),
		CancelTicket:    s.ticketsHandler().RefundCanceller(),
		PublishRefunded: s.publishTicketRefundedV1Events,
		Audit:           s.audit,
		Metrics:         s.typedMetrics,
		Logger:          s.logger,
	})
}
