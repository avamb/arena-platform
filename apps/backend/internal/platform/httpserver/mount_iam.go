package httpserver

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// mountMeRoutes mounts the current-user context endpoint introduced in
// feature #211: GET /v1/me. The route requires authentication (via the stub
// JWT provider in dev / a real Provider in prod) but no specific permission —
// every authenticated caller is allowed to read their own context. The handler
// itself enforces user-scoping by keying every query off actor.ID.
func (s *Server) mountMeRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.meQueries == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
		pr.Get("/me", s.handleMe)
	})
}

// mountGeoRoutes mounts geo reference endpoints (feature #123).
func (s *Server) mountGeoRoutes(r chi.Router) {
	if s.geoQueries != nil {
		r.Get("/geo/countries", s.handleListCountries)
		r.Get("/geo/cities", s.handleListCities)
	}
	if s.stub != nil && s.stub.Enabled() && s.geoQueries != nil && s.pool != nil {
		r.Route("/admin/geo", func(ar chi.Router) {
			ar.Group(func(pr chi.Router) {
				s.applyAuth(pr, "geo.admin", "geo")
				pr.Post("/countries", s.handleCreateCountry)
				pr.Patch("/countries/{iso2}", s.handleUpdateCountry)
				pr.Post("/cities", s.handleCreateCity)
				pr.Patch("/cities/{id}", s.handleUpdateCity)
			})
		})
	}
}

// mountOrgRoutes mounts organization CRUD endpoints (feature #119).
func (s *Server) mountOrgRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.orgQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.read", "organizations")
		pr.Get("/organizations", s.handleListOrgs)
		pr.Get("/organizations/{id}", s.handleGetOrg)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.create", "organizations")
		pr.Post("/organizations", s.handleCreateOrg)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Patch("/organizations/{id}", s.handleUpdateOrg)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.delete", "organizations")
		pr.Delete("/organizations/{id}", s.handleDeleteOrg)
	})
}

// mountChannelRoutes mounts sales channel CRUD endpoints (feature #121).
func (s *Server) mountChannelRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.channelQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "channel.read", "channels")
		pr.Get("/organizations/{org_id}/channels", s.handleListChannels)
		pr.Get("/organizations/{org_id}/channels/{id}", s.handleGetChannel)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "channel.create", "channels")
		pr.Post("/organizations/{org_id}/channels", s.handleCreateChannel)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "channel.update", "channels")
		pr.Patch("/organizations/{org_id}/channels/{id}", s.handleUpdateChannel)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "channel.delete", "channels")
		pr.Delete("/organizations/{org_id}/channels/{id}", s.handleDeleteChannel)
	})
}

// mountPaymentConfigRoutes mounts payment-provider-config CRUD endpoints (feature #237).
//
// Routes are gated on payment_config.read for the GET surface and
// payment_config.write for create / update / delete.
func (s *Server) mountPaymentConfigRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.paymentConfigQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "payment_config.read", "payments")
		pr.Get("/organizations/{org_id}/payment-configs", s.handleListPaymentConfigs)
		pr.Get("/organizations/{org_id}/payment-configs/{id}", s.handleGetPaymentConfig)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "payment_config.write", "payments")
		pr.Post("/organizations/{org_id}/payment-configs", s.handleCreatePaymentConfig)
		pr.Patch("/organizations/{org_id}/payment-configs/{id}", s.handleUpdatePaymentConfig)
		pr.Delete("/organizations/{org_id}/payment-configs/{id}", s.handleDeletePaymentConfig)
	})
}

// mountBankAccountRoutes mounts organization bank-account CRUD endpoints (feature #255).
//
// All four operations are gated on `org.update` — banking coordinates are
// sensitive financial data and are deliberately NOT exposed to actors with
// only `org.read` (see the Wave O section of openapi.yaml).
func (s *Server) mountBankAccountRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.bankAccountQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Get("/organizations/{org_id}/bank-accounts", s.handleListBankAccounts)
		pr.Post("/organizations/{org_id}/bank-accounts", s.handleCreateBankAccount)
		pr.Patch("/organizations/{org_id}/bank-accounts/{id}", s.handleUpdateBankAccount)
		pr.Delete("/organizations/{org_id}/bank-accounts/{id}", s.handleDeleteBankAccount)
	})
}

// mountMembershipRoutes mounts membership grant/revoke/list endpoints (feature #120).
func (s *Server) mountMembershipRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.membershipQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.read", "memberships")
		pr.Get("/organizations/{org_id}/members", s.handleListMembers)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.grant", "memberships")
		pr.Post("/organizations/{org_id}/members", s.handleGrantMembership)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "membership.revoke", "memberships")
		pr.Delete("/organizations/{org_id}/members/{user_id}", s.handleRevokeMembership)
	})
}

// mountVenueRoutes mounts venue CRUD endpoints (feature #124).
func (s *Server) mountVenueRoutes(r chi.Router) {
	if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "venue.read", "venues")
			pr.Get("/venues", s.handleListVenues)
			pr.Get("/venues/{id}", s.handleGetVenue)
			pr.Get("/organizations/{org_id}/venues", s.handleListVenuesByOrg)
		})
	}
	if s.stub != nil && s.stub.Enabled() && s.venueQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "venue.create", "venues")
			pr.Post("/organizations/{org_id}/venues", s.handleCreateVenue)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "venue.update", "venues")
			pr.Patch("/organizations/{org_id}/venues/{id}", s.handleUpdateVenue)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "venue.delete", "venues")
			pr.Delete("/organizations/{org_id}/venues/{id}", s.handleDeleteVenue)
		})
	}
}
