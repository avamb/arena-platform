// billing_shims.go bridges the *Server god-object to the hbilling sub-package.
// All handler bodies live in hbilling/; these thin delegating methods preserve
// the unexported *Server method surface so mount_admin.go, mount_commerce.go
// and the structural test files (billing_ledger_161_test.go,
// stripe_billing_162_test.go) compile unchanged.
package httpserver

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	billingdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/billing"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
)

// billingHandler constructs an hbilling.Handler from the server's dependencies.
// A fresh handler per request keeps the wiring uniform with hbarcode /
// hscanner / hreconciliation and avoids stale captures when test code mutates
// *Server fields between calls.
func (s *Server) billingHandler() *hbilling.Handler {
	return hbilling.New(
		s.billingQueries,
		s.stripeBilling,
		s.stripeConnect,
		s.logger,
	)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These keep the original unexported interface names live in package httpserver
// so server_struct.go, wire.go and test files (stripe_billing_162_test.go)
// compile without importing the hbilling sub-package.

type stripeBillingHelper = hbilling.StripeBillingHelper
type stripeConnectHelper = hbilling.StripeConnectHelper

// compile-time interface guard, duplicated from hbilling/stripe_billing.go
// under the original lowercase name: stripe_billing_162_test.go greps the
// aggregated source for the literal "var _ stripeBillingHelper".
var _ stripeBillingHelper = (*stripebilling.Adapter)(nil)

// ─── invoice state machine forwarders ─────────────────────────────────────────
//
// billing_ledger_161_test.go references these unexported package-level names
// at compile time. The canonical source of truth is internal/domain/billing
// (feature #187); hbilling/billing_ledger.go carries its own package-private
// copies of the same forwarders for the moved handler bodies.

// validInvoiceTransitions forwards to billingdomain.ValidInvoiceTransitions.
var validInvoiceTransitions = billingdomain.ValidInvoiceTransitions

// allInvoiceStates forwards to billingdomain.AllInvoiceStates.
var allInvoiceStates = billingdomain.AllInvoiceStates

// isTerminalInvoiceState forwards to billingdomain.IsTerminalInvoiceState.
func isTerminalInvoiceState(state string) bool {
	return billingdomain.IsTerminalInvoiceState(state)
}

// billingPeriodForTime forwards to billingdomain.PeriodForTime.
func billingPeriodForTime(t time.Time) string {
	return billingdomain.PeriodForTime(t)
}

// ─── pure-function forwarders ─────────────────────────────────────────────────
//
// billing_ledger_161_test.go calls these unqualified — keep the original
// lowercase names live in package httpserver so callers do not learn about
// the hbilling sub-package.

func tariffToResponse(r gen.TariffRow) map[string]any {
	return hbilling.TariffToResponse(r)
}

func usageToResponse(r gen.UsageRecordRow) map[string]any {
	return hbilling.UsageToResponse(r)
}

func invoiceToResponse(r gen.InvoiceRow) map[string]any {
	return hbilling.InvoiceToResponse(r)
}

func invoiceLineToResponse(r gen.InvoiceLineRow) map[string]any {
	return hbilling.InvoiceLineToResponse(r)
}

// ─── billing ledger handler shims ─────────────────────────────────────────────

func (s *Server) handleCreateTariff(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleCreateTariff(w, r)
}

func (s *Server) handleGetActiveTariff(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleGetActiveTariff(w, r)
}

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleGetUsage(w, r)
}

func (s *Server) handleGenerateInvoices(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleGenerateInvoices(w, r)
}

func (s *Server) handleGetInvoice(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleGetInvoice(w, r)
}

func (s *Server) handleListOrgInvoices(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleListOrgInvoices(w, r)
}

func (s *Server) handleIssueInvoice(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleIssueInvoice(w, r)
}

func (s *Server) handlePayInvoice(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandlePayInvoice(w, r)
}

func (s *Server) handleVoidInvoice(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleVoidInvoice(w, r)
}

// IncrementBillingUsage increments usage counters for the given org and the
// current billing period. Designed to be called from ticket issuance and
// event publication hooks. Safe to call with nil billingQueries (no-op).
// Delegates to hbilling.(*Handler).IncrementBillingUsage.
func (s *Server) IncrementBillingUsage(
	ctx context.Context,
	orgID uuid.UUID,
	deltaTickets int64,
	deltaComplimentary int64,
	deltaEvents int64,
) {
	s.billingHandler().IncrementBillingUsage(ctx, orgID, deltaTickets, deltaComplimentary, deltaEvents)
}

// ─── Stripe Billing handler shims ─────────────────────────────────────────────

func (s *Server) handlePushInvoiceToStripe(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandlePushInvoiceToStripe(w, r)
}

func (s *Server) handleStripeBillingWebhook(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleStripeBillingWebhook(w, r)
}

// ─── Stripe Connect handler shims ─────────────────────────────────────────────

func (s *Server) handleStripeConnectAuthorize(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleStripeConnectAuthorize(w, r)
}

func (s *Server) handleStripeConnectCallback(w http.ResponseWriter, r *http.Request) {
	s.billingHandler().HandleStripeConnectCallback(w, r)
}
