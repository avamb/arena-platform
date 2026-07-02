// Package hbilling implements HTTP handlers for the platform billing domain:
// the service billing ledger (feature #161 — tariffs, usage records, invoice
// state machine), the Stripe Billing adapter for SaaS invoices (feature #162),
// and the Stripe Connect Standard OAuth onboarding flow (feature #135).
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin billing_shims.go bridge in the parent package, matching the
// pattern established by hcatalog / hcheckout / htickets / hbarcode /
// hscanner / hreconciliation.
package hbilling

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Handler holds the shared dependencies for all billing-domain HTTP handlers.
// The billingQueries object exposes both the billing_ledger.sql methods
// (tariffs / usage records / invoices / invoice lines) and the
// stripe_billing.sql methods (stripe customer mapping, stripe invoice ID
// persistence), since both query groups sit on the same *gen.Queries type.
type Handler struct {
	billingQueries *gen.Queries
	stripeBilling  StripeBillingHelper
	stripeConnect  StripeConnectHelper
	logger         *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and
// nil adapters are allowed; individual handlers self-gate with a 503
// *.unavailable envelope, matching the *Server route-mount precedent.
func New(
	billingQ *gen.Queries,
	stripeBilling StripeBillingHelper,
	stripeConnect StripeConnectHelper,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		billingQueries: billingQ,
		stripeBilling:  stripeBilling,
		stripeConnect:  stripeConnect,
		logger:         logger,
	}
}
