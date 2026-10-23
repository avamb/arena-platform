// mount_billing_accounts.go mounts the billing-account routes (billing tasks
// B3-3, B3-4, B4): the organizer's balance, packs, invoices and payer, the
// platform Stripe and bank webhooks, and the operator's invoice, payment-fee
// and bank-transaction routes under /v1/admin/billing.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/stripebilling"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
)

// platformStripe is the PLATFORM's own Stripe account (never an
// organizer's), built from BILLING_STRIPE_SECRET_KEY and
// BILLING_STRIPE_WEBHOOK_SECRET; nil when the key is not set, which switches
// card payment of platform invoices off. The endpoint follows the same test
// seam as the hosted checkout (Options.StripeAPIBaseURL, then
// STRIPE_API_BASE_URL, which production refuses). Built per request: tests
// set the config after New, and billing traffic is small.
func (s *Server) platformStripe() hbilling.PlatformStripe {
	if s.cfg == nil || s.cfg.BillingStripeSecretKey == "" {
		return nil
	}
	return stripebilling.New(stripebilling.Config{
		SecretKey:     s.cfg.BillingStripeSecretKey,
		WebhookSecret: s.cfg.BillingStripeWebhookSecret,
		BaseURL:       s.stripeBaseURL(),
	})
}

func (s *Server) eventCenterURL() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.EventCenterPublicURL
}

func (s *Server) accountBillingHandler() *hbilling.AccountBillingHandler {
	bank := ""
	if s.cfg != nil {
		bank = s.cfg.BillingBankWebhookSecret
	}
	return hbilling.NewAccountBilling(s.pgxPool, s.platformStripe(), s.eventCenterURL(), bank, s.logger)
}

// platformStripeWebhookEnabled says whether POST /v1/billing/stripe/webhook is
// served by the billing-account webhook (platform invoices) instead of the
// legacy tariff-invoice receiver.
func (s *Server) platformStripeWebhookEnabled() bool {
	return s.pgxPool != nil && s.cfg != nil && s.cfg.BillingStripeSecretKey != ""
}

// mountAccountBillingRoutes mounts the routes. The organizer routes carry
// {org_id}: membership and the permission are judged in that organization.
func (s *Server) mountAccountBillingRoutes(r chi.Router) {
	if !s.authEnabled() {
		return // a handler without a database answers 503, like the price-list screen
	}
	h := s.accountBillingHandler
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.read", "billing")
		pr.Get("/organizations/{org_id}/billing/balance", func(w http.ResponseWriter, r *http.Request) { h().HandleBalance(w, r) })
		pr.Get("/organizations/{org_id}/billing/packs", func(w http.ResponseWriter, r *http.Request) { h().HandlePacks(w, r) })
		pr.Get("/organizations/{org_id}/billing/ledger", func(w http.ResponseWriter, r *http.Request) { h().HandleLedger(w, r) })
		pr.Get("/organizations/{org_id}/billing/service-fee-quote", func(w http.ResponseWriter, r *http.Request) { h().HandleServiceFeeQuote(w, r) })
		pr.Get("/organizations/{org_id}/billing/account-invoices", func(w http.ResponseWriter, r *http.Request) { h().HandleListInvoices(w, r) })
		pr.Get("/organizations/{org_id}/billing/account-invoices/{invoice_id}", func(w http.ResponseWriter, r *http.Request) { h().HandleGetInvoice(w, r) })
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.purchase", "billing")
		pr.Post("/organizations/{org_id}/billing/packs/{pack_id}/order", func(w http.ResponseWriter, r *http.Request) { h().HandleOrderPack(w, r) })
		pr.Post("/organizations/{org_id}/billing/account-invoices/{invoice_id}/pay-by-card", func(w http.ResponseWriter, r *http.Request) { h().HandlePayByCard(w, r) })
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.payer_manage", "billing")
		pr.Put("/organizations/{org_id}/billing/payer", func(w http.ResponseWriter, r *http.Request) { h().HandlePutPayer(w, r) })
	})
	// Public, guarded by their signatures.
	if s.platformStripeWebhookEnabled() {
		r.Post("/billing/stripe/webhook", func(w http.ResponseWriter, r *http.Request) { h().HandleStripeWebhook(w, r) })
	}
	r.Post("/billing/bank/webhook/{provider}", func(w http.ResponseWriter, r *http.Request) { h().HandleBankWebhook(w, r) })

	a := s.billingAdminHandler
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.admin", "billing")
		pr.Use(s.requireBillingSuperadmin)
		pr.Get("/admin/billing/invoices", func(w http.ResponseWriter, r *http.Request) { a().HandleListBillingInvoices(w, r) })
		pr.Get("/admin/billing/invoices/{invoice_id}", func(w http.ResponseWriter, r *http.Request) { a().HandleGetBillingInvoice(w, r) })
		pr.Post("/admin/billing/accounts/{account_id}/invoices", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateUsageInvoice(w, r) })
		pr.Post("/admin/billing/invoices/{invoice_id}/issue", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateInvoiceIssue(w, r) })
		pr.Post("/admin/billing/invoices/{invoice_id}/mark-paid", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateInvoicePayment(w, r) })
		pr.Post("/admin/billing/invoices/{invoice_id}/void", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateInvoiceVoid(w, r) })
		pr.Get("/admin/billing/payment-fees", func(w http.ResponseWriter, r *http.Request) { a().HandleListPaymentFees(w, r) })
		pr.Post("/admin/billing/payment-fees", func(w http.ResponseWriter, r *http.Request) { a().HandleCreatePaymentFee(w, r) })
		pr.Get("/admin/billing/bank-transactions", func(w http.ResponseWriter, r *http.Request) { a().HandleListBankTransactions(w, r) })
		pr.Post("/admin/billing/bank-transactions", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateBankImport(w, r) })
		pr.Post("/admin/billing/bank-transactions/{transaction_id}/match", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateBankMatch(w, r) })
		pr.Post("/admin/billing/bank-transactions/{transaction_id}/ignore", func(w http.ResponseWriter, r *http.Request) { a().HandleCreateBankIgnore(w, r) })
	})
}
