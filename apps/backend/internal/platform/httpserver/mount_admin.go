package httpserver

import "github.com/go-chi/chi/v5"

// mountReportRoutes mounts post-event report endpoints (feature #159).
func (s *Server) mountReportRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.reportQueries == nil {
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
	if s.stub == nil || !s.stub.Enabled() || s.billingQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.read", "billing")
		pr.Get("/billing/tariffs/active", s.handleGetActiveTariff)
		pr.Get("/billing/invoices/{id}", s.handleGetInvoice)
		pr.Get("/organizations/{org_id}/billing/usage", s.handleGetUsage)
		pr.Get("/organizations/{org_id}/billing/invoices", s.handleListOrgInvoices)
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
	if s.stub == nil || !s.stub.Enabled() || s.stripeBilling == nil || s.billingQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "billing.admin", "billing")
		pr.Post("/billing/stripe/push-invoice/{id}", s.handlePushInvoiceToStripe)
	})
	// Webhook is public (signature verification inside the handler).
	r.Post("/billing/stripe/webhook", s.handleStripeBillingWebhook)
}

// mountSuperadminRoutes mounts read-only cross-tenant superadmin endpoints
// (feature #166).
func (s *Server) mountSuperadminRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.superadminQueries == nil {
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

// mountAdminOrgRoutes mounts admin-namespace Organizations CRUD endpoints
// (feature #233): POST/PATCH/archive under /v1/admin/organizations. These are
// the admin-console-facing counterparts to /v1/organizations and require
// JWT + RBAC (org.create/org.update/org.delete) + an X-Admin-Reason header.
// The companion GET /v1/admin/organizations list endpoint is mounted by
// mountSuperadminRoutes (cross-tenant superadmin read; feature #166).
func (s *Server) mountAdminOrgRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.orgQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.create", "organizations")
		pr.Post("/admin/organizations", s.handleAdminCreateOrg)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Patch("/admin/organizations/{id}", s.handleAdminUpdateOrg)
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
	if s.stub == nil || !s.stub.Enabled() || s.membershipQueries == nil || s.pool == nil {
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
	if s.stub == nil || !s.stub.Enabled() || s.membershipQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.grant", "users")
		pr.Post("/admin/users", s.handleAdminCreateUser)
	})
}

// mountImpersonationRoutes mounts the scoped impersonation JWT endpoint
// (feature #167).
func (s *Server) mountImpersonationRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "superadmin")
		pr.Post("/admin/impersonate", s.handleImpersonate)
	})
}

// mountWebhookSubscriberRoutes mounts WordPress webhook subscriber registry
// (feature #156).
func (s *Server) mountWebhookSubscriberRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.webhookSubQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "webhook.subscriber.manage", "webhooks")
		pr.Get("/webhooks/subscribers", s.handleListWebhookSubscribers)
		pr.Get("/webhooks/subscribers/{id}", s.handleGetWebhookSubscriber)
	})
	if s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "webhook.subscriber.manage", "webhooks")
			pr.Post("/webhooks/subscribers", s.handleRegisterWebhookSubscriber)
			pr.Delete("/webhooks/subscribers/{id}", s.handleDeactivateWebhookSubscriber)
		})
	}
}
