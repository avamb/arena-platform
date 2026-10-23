package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// mountReportRoutes mounts post-event report endpoints (feature #159).
func (s *Server) mountReportRoutes(r chi.Router) {
	if !s.authEnabled() || s.reportQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "report.read", "reports")
		pr.Get("/events/{event_id}/report", s.handleGetEventReport)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "report.generate", "reports")
		pr.Post("/events/{event_id}/report", s.handleTriggerEventReport)
	})
}

// mountBillingRoutes mounts the platform service billing ledger endpoints
// (feature #161).
func (s *Server) mountBillingRoutes(r chi.Router) {
	if !s.authEnabled() || s.billingQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.read", "billing")
		pr.Get("/billing/tariffs/active", s.handleGetActiveTariff)
		pr.Get("/billing/invoices/{id}", s.handleGetInvoice)
		pr.Get("/organizations/{org_id}/billing/usage", s.handleGetUsage)
		pr.Get("/organizations/{org_id}/billing/invoices", s.handleListOrgInvoices)
		pr.Get("/organizations/{org_id}/billing/charges", s.handleListOrgCharges)
		pr.Get("/organizations/{org_id}/billing/events/{event_id}/summary", s.handleGetEventBillingSummary)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.admin", "billing")
		pr.Post("/billing/tariffs", s.handleCreateTariff)
		pr.Post("/billing/invoices/generate", s.handleGenerateInvoices)
		pr.Post("/billing/invoices/{id}/issue", s.handleIssueInvoice)
		pr.Post("/billing/invoices/{id}/pay", s.handlePayInvoice)
		pr.Post("/billing/invoices/{id}/void", s.handleVoidInvoice)
	})
}

// mountStripeBillingRoutes mounts the Stripe Billing push + webhook
// endpoints (feature #162).
func (s *Server) mountStripeBillingRoutes(r chi.Router) {
	if !s.authEnabled() || s.stripeBilling == nil || s.billingQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.admin", "billing")
		pr.Post("/billing/stripe/push-invoice/{id}", s.handlePushInvoiceToStripe)
	})
	// Webhook is public (signature verification inside the handler). With the
	// platform billing key set, mountAccountBillingRoutes serves it instead.
	if !s.platformStripeWebhookEnabled() {
		r.Post("/billing/stripe/webhook", s.handleStripeBillingWebhook)
	}
}

// billingAdminHandler builds the handler of the superadmin screen "Price
// lists and rates" (task B1-5).
func (s *Server) billingAdminHandler() *hbilling.AdminHandler {
	return hbilling.NewAdmin(s.pgxPool, s.audit, s.logger).WithPlatformStripe(s.platformStripe(), s.eventCenterURL())
}

// requireBillingSuperadmin lets only a platform superadmin through. The
// billing.admin permission is also held by the platform admin role, but price
// lists and individual rates are the owner's decision, so the gate is the
// same marker the webhook registry uses.
func (s *Server) requireBillingSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasSuperadminOrgAccess(r.Context()) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"billing.superadmin_only", "platform superadmin access is required for price lists and rates", r,
			))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountBillingAdminRoutes mounts the routes of the superadmin screen "Price
// lists and rates" under /v1/admin/billing. Every route needs billing.admin
// AND a platform superadmin; writes need X-Admin-Reason and write an audit
// row in their own transaction (hbilling/admin.go).
func (s *Server) mountBillingAdminRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	h := s.billingAdminHandler
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.admin", "billing")
		pr.Use(s.requireBillingSuperadmin)
		pr.Get("/admin/billing/price-lists", func(w http.ResponseWriter, r *http.Request) { h().HandleListPriceLists(w, r) })
		pr.Get("/admin/billing/price-lists/{id}", func(w http.ResponseWriter, r *http.Request) { h().HandleGetPriceList(w, r) })
		pr.Post("/admin/billing/price-lists", func(w http.ResponseWriter, r *http.Request) { h().HandleCreatePriceList(w, r) })
		pr.Post("/admin/billing/price-lists/{id}/revoke", func(w http.ResponseWriter, r *http.Request) { h().HandleRevokePriceList(w, r) })
		pr.Get("/admin/billing/rate-overrides", func(w http.ResponseWriter, r *http.Request) { h().HandleListRateOverrides(w, r) })
		pr.Post("/admin/billing/rate-overrides", func(w http.ResponseWriter, r *http.Request) { h().HandleCreateRateOverride(w, r) })
		pr.Post("/admin/billing/rate-overrides/{id}/close", func(w http.ResponseWriter, r *http.Request) { h().HandleCloseRateOverride(w, r) })
		pr.Delete("/admin/billing/rate-overrides/{id}", func(w http.ResponseWriter, r *http.Request) { h().HandleDeleteRateOverride(w, r) })
		pr.Get("/admin/billing/accounts", func(w http.ResponseWriter, r *http.Request) { h().HandleListAccounts(w, r) })
		pr.Post("/admin/billing/accounts/{account_id}/terms", func(w http.ResponseWriter, r *http.Request) { h().HandleCreateAccountTerms(w, r) })
	})
}

// mountSuperadminRoutes mounts read-only cross-tenant superadmin endpoints
// (feature #166).
func (s *Server) mountSuperadminRoutes(r chi.Router) {
	if !s.authEnabled() || s.superadminQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "superadmin")
		pr.Get("/admin/organizations", s.handleSuperadminListOrganizations)
		pr.Get("/admin/orders", s.handleSuperadminListOrders)
		pr.Get("/admin/tickets", s.handleSuperadminListTickets)
		pr.Get("/admin/refunds", s.handleSuperadminListRefunds)
	})
}

// mountAdminTicketDeliveryRoutes mounts the support-console ticket-delivery
// inspect + resend endpoints (feature #291, T-4) under /v1/admin/tickets/{id}.
// RBAC: ticket.update (or support.act fallback in the future RBAC engine).
// All three writes carry the same X-Admin-Reason gate as the rest of /v1/admin.
func (s *Server) mountAdminTicketDeliveryRoutes(r chi.Router) {
	if !s.authEnabled() || s.deliveryJobQueries == nil || s.ticketQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "ticket.update", "tickets")
		pr.Get("/admin/tickets/{id}/delivery", s.handleAdminGetTicketDelivery)
		// Resend gate also accepted via support.act; future RBAC engine
		// can grant either permission. AllowAll() passes both today.
		//
		// Always mounted (handler self-gates on s.workerPool == nil with
		// a 503 dependency.database_unavailable envelope) so that the
		// route is documented as part of the openapi.yaml contract
		// (feature #276, A-15) and the drift test does not need a real
		// *pgxpool.Pool to assert coverage.
		pr.Post("/admin/tickets/{id}/delivery/resend", s.handleAdminResendTicketDelivery)
	})
	// Read-only Scans panel for the ticket support drawer (feature #295,
	// S-4). RBAC: scan_event.read (seeded for admin/org_admin/support in
	// 0055_scan_events.sql). Gated separately on feedTokenQueries
	// (currently the scan_events read backend) so the openapi-drift
	// coverage check can wire DeliveryJobQueries to assert the delivery
	// routes (#276) without dragging in the scans surface, which is
	// documented under a future A-* wave. This preserves the deliberate
	// /v1/admin scans gap precedent established in #291 / #295 while
	// closing the delivery gap.
	if s.feedTokenQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "scan_event.read", "scan_events")
			pr.Get("/admin/tickets/{id}/scans", s.handleAdminListTicketScanEvents)
		})
	}
}

// mountAdminOrgRoutes mounts admin-namespace Organizations CRUD endpoints
// (feature #233): POST/PATCH/archive under /v1/admin/organizations. These are
// the admin-console-facing counterparts to /v1/organizations and require
// JWT + RBAC (org.create/org.update/org.delete) + an X-Admin-Reason header.
// The companion GET /v1/admin/organizations list endpoint is mounted by
// mountSuperadminRoutes (cross-tenant superadmin read; feature #166).
func (s *Server) mountAdminOrgRoutes(r chi.Router) {
	if !s.authEnabled() || s.orgQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.create", "organizations")
		pr.Post("/admin/organizations", s.handleAdminCreateOrg)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Patch("/admin/organizations/{id}", s.handleAdminUpdateOrg)
		pr.Get("/admin/organizations/{id}/sender-dns", s.handleAdminOrganizationSenderDNS)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.delete", "organizations")
		pr.Post("/admin/organizations/{id}/archive", s.handleAdminArchiveOrg)
	})
}

// mountAdminMembershipRoutes mounts admin-namespace organization-memberships
// endpoints (feature #234) under /v1/admin/organizations/{org_id}/members.
//
// All four routes (list, add, change role, deactivate) carry the standard
// /v1/admin gate (JWT + RBAC + X-Admin-Reason). They re-use the existing
// membership.read / membership.grant / membership.revoke permissions seeded
// in migration 0011_memberships.sql so no schema change is required.
func (s *Server) mountAdminMembershipRoutes(r chi.Router) {
	if !s.authEnabled() || s.membershipQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.read", "memberships")
		pr.Get("/admin/organizations/{org_id}/members", s.handleAdminListMembers)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.grant", "memberships")
		pr.Post("/admin/organizations/{org_id}/members", s.handleAdminAddMember)
		pr.Patch("/admin/organizations/{org_id}/members/{membership_id}", s.handleAdminChangeMemberRole)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.revoke", "memberships")
		pr.Delete("/admin/organizations/{org_id}/members/{membership_id}", s.handleAdminDeactivateMember)
	})
}

// mountAdminUserRoutes mounts the SuperAdmin user provisioning endpoint.
// The route creates a user, issues a password setup token, and assigns either a
// global platform role or an organization-scoped membership role.
func (s *Server) mountAdminUserRoutes(r chi.Router) {
	if !s.authEnabled() || s.membershipQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "users")
		pr.Get("/admin/users", s.handleAdminListUsers)
		pr.Post("/admin/users", s.handleAdminCreateUser)
		pr.Patch("/admin/users/{user_id}", s.handleAdminUpdateUser)
		pr.Post("/admin/users/{user_id}/global-roles", s.handleAdminGrantGlobalRole)
		pr.Delete("/admin/users/{user_id}/global-roles/{role}", s.handleAdminRevokeGlobalRole)
		pr.Post("/admin/users/{user_id}/deactivate", s.handleAdminDeactivateUser)
		pr.Post("/admin/users/{user_id}/reactivate", s.handleAdminReactivateUser)
		pr.Delete("/admin/users/{user_id}", s.handleAdminDeleteUser)
	})
}

// mountImpersonationRoutes mounts the scoped impersonation JWT endpoint
// (feature #167).
func (s *Server) mountImpersonationRoutes(r chi.Router) {
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "superadmin")
		pr.Post("/admin/impersonate", s.handleImpersonate)
	})
}

// requirePlatformSuperadmin refuses every caller that the server did not mark
// as a platform superadmin. The webhook subscriber registry is one table for
// all organizations, so organization owners and API keys never reach it.
func (s *Server) requirePlatformSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasSuperadminOrgAccess(r.Context()) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"webhook.superadmin_only", "platform superadmin access is required", r,
			))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountWebhookSubscriberRoutes mounts WordPress webhook subscriber registry
// (feature #156).
func (s *Server) mountWebhookSubscriberRoutes(r chi.Router) {
	if !s.authEnabled() || s.webhookSubQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "webhook.subscriber.manage", "webhooks")
		pr.Use(s.requirePlatformSuperadmin)
		pr.Get("/webhooks/subscribers", s.handleListWebhookSubscribers)
		pr.Get("/webhooks/subscribers/{id}", s.handleGetWebhookSubscriber)
		// Recent-deliveries listing (Feature #294 — S-3 admin UI). Read-only;
		// safe to mount alongside the other read routes even when the
		// shared pool is nil because the handler short-circuits on
		// s.pool == nil with a 503.
		pr.Get("/webhooks/subscribers/{id}/recent-deliveries", s.handleListRecentWebhookDeliveries)
	})
	if s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "webhook.subscriber.manage", "webhooks")
			pr.Use(s.requirePlatformSuperadmin)
			pr.Post("/webhooks/subscribers", s.handleRegisterWebhookSubscriber)
			pr.Patch("/webhooks/subscribers/{id}", s.handleUpdateWebhookSubscriber)
			pr.Delete("/webhooks/subscribers/{id}", s.handleDeactivateWebhookSubscriber)
		})
	}
}
