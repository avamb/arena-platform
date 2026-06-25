package httpserver

import "github.com/go-chi/chi/v5"

// mountPromoRoutes mounts promo-code CRUD + validation (feature #128).
func (s *Server) mountPromoRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.promoQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "promo.read", "promo-codes")
			pr.Get("/organizations/{org_id}/promo-codes", s.handleListPromoCodes)
			pr.Get("/organizations/{org_id}/promo-codes/{id}", s.handleGetPromoCode)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "promo.validate", "promo-codes")
			pr.Post("/checkout/promo-validate", s.handleValidatePromoCode)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.promoQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "promo.create", "promo-codes")
			pr.Post("/organizations/{org_id}/promo-codes", s.handleCreatePromoCode)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "promo.update", "promo-codes")
			pr.Patch("/organizations/{org_id}/promo-codes/{id}", s.handleUpdatePromoCode)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "promo.delete", "promo-codes")
			pr.Delete("/organizations/{org_id}/promo-codes/{id}", s.handleDeletePromoCode)
		})
	}
}

// mountPricingRoutes mounts GET /v1/checkout/quote (feature #129).
func (s *Server) mountPricingRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.tierQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "pricing.quote", "checkout")
		pr.Get("/checkout/quote", s.handleQuote)
	})
}

// mountCheckoutRoutes mounts checkout session state machine + price
// breakdown (features #132, #163).
func (s *Server) mountCheckoutRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.checkoutQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "checkout.read", "checkout")
			pr.Get("/checkout/{id}", s.handleGetCheckoutSession)
			pr.Get("/checkout/{id}/price-breakdown", s.handlePriceBreakdown)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.checkoutQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "checkout.start", "checkout")
			pr.Post("/checkout/start", s.handleStartCheckout)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "checkout.confirm", "checkout")
			pr.Post("/checkout/{id}/confirm", s.handleConfirmCheckout)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "checkout.complete", "checkout")
			pr.Post("/checkout/{id}/complete", s.handleCompleteCheckout)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "checkout.abandon", "checkout")
			pr.Post("/checkout/{id}/abandon", s.handleAbandonCheckout)
		})
	}
}

// mountPaymentIntentRoutes mounts payment intent state machine + webhook
// (feature #137).
func (s *Server) mountPaymentIntentRoutes(r chi.Router) {
	if s.paymentIntentQueries != nil {
		// Webhook is intentionally unauthenticated; idempotency handled inside.
		r.Post("/payment-intents/webhook", s.handlePaymentIntentWebhook)
	}
	if s.stub != nil && s.stub.Enabled() && s.paymentIntentQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "payment_intent.read", "payment_intents")
			pr.Get("/payment-intents/{id}", s.handleGetPaymentIntent)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.paymentIntentQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "payment_intent.create", "payment_intents")
			pr.Post("/payment-intents", s.handleCreatePaymentIntent)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "payment_intent.update", "payment_intents")
			pr.Post("/payment-intents/{id}/transition", s.handleTransitionPaymentIntent)
		})
	}
}

// mountStripeConnectRoutes mounts the Stripe Connect OAuth onboarding
// endpoints (feature #135). Mounted only when StripeConnect is wired.
func (s *Server) mountStripeConnectRoutes(r chi.Router) {
	if s.stripeConnect == nil || s.stub == nil || !s.stub.Enabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "payment_intent.create", "stripe_connect")
		pr.Get("/stripe/connect/authorize", s.handleStripeConnectAuthorize)
		pr.Get("/stripe/connect/callback", s.handleStripeConnectCallback)
	})
}

// mountTicketRoutes mounts the GET /v1/checkout/{id}/tickets read endpoint
// (feature #139). Issuance is internal.
func (s *Server) mountTicketRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.ticketQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "ticket.read", "tickets")
		pr.Get("/checkout/{id}/tickets", s.handleListTickets)
	})
}

// mountCredentialRoutes mounts the lazy ticket credential endpoint
// (feature #140).
func (s *Server) mountCredentialRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.credentialQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "credential.read", "credentials")
		pr.Get("/tickets/{id}/credential", s.handleGetCredential)
	})
}

// mountRefundRoutes mounts the refund state machine + webhook (feature #138).
func (s *Server) mountRefundRoutes(r chi.Router) {
	if s.refundQueries != nil {
		// Webhook — intentionally unauthenticated.
		r.Post("/refunds/webhook", s.handleRefundWebhook)
	}
	if s.stub != nil && s.stub.Enabled() && s.refundQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "refund.read", "refunds")
			pr.Get("/refunds/{id}", s.handleGetRefund)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.refundQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "refund.create", "refunds")
			pr.Post("/refunds", s.handleCreateRefund)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "refund.approve", "refunds")
			pr.Post("/refunds/{id}/approve", s.handleApproveRefund)
			pr.Post("/refunds/{id}/reject", s.handleRejectRefund)
		})
	}
}

// mountGDPRRoutes mounts self-service GDPR data subject endpoints (feature #164).
func (s *Server) mountGDPRRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.gdprQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "gdpr.request", "gdpr")
			pr.Get("/me/data-requests", s.handleListDataRequests)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.gdprQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "gdpr.request", "gdpr")
			pr.Post("/me/data-export", s.handleDataExportRequest)
			pr.Post("/me/data-delete", s.handleDataDeleteRequest)
			pr.Post("/me/consent", s.handleRecordConsent)
		})
	}
}
