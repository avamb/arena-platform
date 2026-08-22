package httpserver

import "github.com/go-chi/chi/v5"

// mountFeedTokenRoutes mounts agent feed token management + the public
// feed read endpoint (feature #122).
func (s *Server) mountFeedTokenRoutes(r chi.Router) {
	if s.feedTokenQueries != nil {
		// Public feed read (no auth — token in path is the credential).
		r.Get("/feeds/{token}", s.handlePublicFeed)
	}
	if s.authEnabled() && s.feedTokenQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "feed_token.read", "feed_tokens")
			pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleListFeedTokens)
			pr.Get("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleGetFeedToken)
		})
	}
	if s.authEnabled() && s.feedTokenQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "feed_token.create", "feed_tokens")
			pr.Post("/organizations/{org_id}/channels/{channel_id}/feed-tokens", s.handleCreateFeedToken)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "feed_token.delete", "feed_tokens")
			pr.Delete("/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", s.handleRevokeFeedToken)
		})
	}
}

// mountEventRoutes mounts event CRUD endpoints (feature #125).
func (s *Server) mountEventRoutes(r chi.Router) {
	if s.authEnabled() && s.eventQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.read", "events")
			pr.Get("/events", s.handleListEvents)
			pr.Get("/events/{id}", s.handleGetEvent)
			pr.Get("/organizations/{org_id}/events", s.handleListEventsByOrg)
		})
	}
	if s.authEnabled() && s.eventQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.create", "events")
			pr.Post("/organizations/{org_id}/events", s.handleCreateEvent)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.update", "events")
			pr.Patch("/organizations/{org_id}/events/{id}", s.handleUpdateEvent)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.publish", "events")
			pr.Post("/organizations/{org_id}/events/{id}/status", s.handleUpdateEventStatus)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.delete", "events")
			pr.Delete("/organizations/{org_id}/events/{id}", s.handleDeleteEvent)
		})
		// AB-45: event artists CRUD
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.read", "events")
			pr.Get("/organizations/{org_id}/events/{id}/artists", s.handleListEventArtists)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.update", "events")
			pr.Post("/organizations/{org_id}/events/{id}/artists", s.handleCreateEventArtist)
			pr.Patch("/organizations/{org_id}/events/{id}/artists/{aid}", s.handleUpdateEventArtist)
			pr.Delete("/organizations/{org_id}/events/{id}/artists/{aid}", s.handleDeleteEventArtist)
		})
	}
}

// mountSessionRoutes mounts session CRUD endpoints (feature #126).
func (s *Server) mountSessionRoutes(r chi.Router) {
	if s.authEnabled() && s.sessionQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.read", "sessions")
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions", s.handleListSessions)
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleGetSession)
		})
	}
	if s.authEnabled() && s.sessionQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.create", "sessions")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions", s.handleCreateSession)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.update", "sessions")
			pr.Patch("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleUpdateSession)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.delete", "sessions")
			pr.Delete("/organizations/{org_id}/events/{event_id}/sessions/{id}", s.handleDeleteSession)
		})
	}
}

// mountSessionMediaRoutes mounts the per-session media gallery endpoints
// (AB-47b, feature #435). GET requires session.read; PUT requires
// session.update. Tenancy is enforced inside the handler via
// GetSessionOrgContext → requireOrgMembership because the routes are
// flat (no {org_id} path segment).
func (s *Server) mountSessionMediaRoutes(r chi.Router) {
	if s.authEnabled() && s.sessionQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.read", "sessions")
			pr.Get("/sessions/{id}/media", s.handleGetSessionMedia)
		})
	}
	if s.authEnabled() && s.sessionQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.update", "sessions")
			pr.Put("/sessions/{id}/media", s.handleReplaceSessionMedia)
		})
	}
}

// mountTierRoutes mounts ticket tier CRUD endpoints (feature #127).
func (s *Server) mountTierRoutes(r chi.Router) {
	if s.authEnabled() && s.tierQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "tier.read", "tiers")
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers", s.handleListTiers)
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleGetTier)
			// AB-48: scheduled price windows (read).
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule", s.handleGetTierPriceSchedule)
		})
	}
	if s.authEnabled() && s.tierQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "tier.create", "tiers")
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers", s.handleCreateTier)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "tier.update", "tiers")
			pr.Patch("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleUpdateTier)
			// AB-48: scheduled price windows (replace-all) + bulk grid.
			pr.Put("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule", s.handlePutTierPriceSchedule)
			pr.Post("/organizations/{org_id}/events/{event_id}/sessions/pricing-bulk", s.handleBulkSessionPricing)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "tier.delete", "tiers")
			pr.Delete("/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}", s.handleDeleteTier)
		})
	}
}

// mountPublicationRoutes mounts event publication endpoints (feature #151).
func (s *Server) mountPublicationRoutes(r chi.Router) {
	if s.authEnabled() && s.publicationQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "publication.read", "publications")
			pr.Get("/events/{event_id}/publications", s.handleListPublications)
		})
	}
	if s.authEnabled() && s.publicationQueries != nil && s.pool != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "publication.create", "publications")
			pr.Post("/events/{event_id}/publications", s.handlePublishEvent)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "publication.delete", "publications")
			pr.Delete("/events/{event_id}/publications/{feed_token_id}", s.handleUnpublishEvent)
		})
	}
}

// mountMACSExportRoutes mounts the MACS JSON export endpoint for a session
// (AB-50b, feature #438). Requires session.read auth + org membership.
// The handler self-gates on a nil pgxPool with a 503 envelope.
func (s *Server) mountMACSExportRoutes(r chi.Router) {
	if s.authEnabled() && s.sessionQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "session.read", "sessions")
			pr.Get("/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export", s.handleMACSExport)
		})
	}
}

// mountMACSWebhookRoutes mounts the MACS webhook subscriber management endpoints
// (AB-50c, feature #439). Requires org membership.
//   - GET    /organizations/{org_id}/macs-webhook  — get active subscriber
//   - PUT    /organizations/{org_id}/macs-webhook  — upsert subscriber
//   - DELETE /organizations/{org_id}/macs-webhook  — deactivate subscriber
func (s *Server) mountMACSWebhookRoutes(r chi.Router) {
	if s.authEnabled() && s.membershipQueries != nil {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.read", "macs_webhook")
			pr.Get("/organizations/{org_id}/macs-webhook", s.handleGetMACSWebhook)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "event.update", "macs_webhook")
			pr.Put("/organizations/{org_id}/macs-webhook", s.handleUpsertMACSWebhook)
			pr.Delete("/organizations/{org_id}/macs-webhook", s.handleDeleteMACSWebhook)
		})
	}
}

// mountPublicFeedRoutes mounts the unauthenticated public feed event +
// checkout endpoints (features #152, #153, #319 WID-0b, #320 WID-0c,
// #322 WID-0e).
func (s *Server) mountPublicFeedRoutes(r chi.Router) {
	if s.publicFeedQueries != nil {
		r.Get("/public/feeds/{feed_token}/events", s.handlePublicFeedEvents)
		r.Get("/public/feeds/{feed_token}/events/{event_id}", s.handlePublicFeedEvent)
	}
	// Funnel telemetry sink — WID-0e (feature #322).
	// Always mounted; handler self-gates with 503 when funnelQueries is nil.
	r.Post("/public/feeds/{feed_token}/events", s.handlePublicFeedFunnelEvents)
	if s.publicFeedQueries != nil && s.checkoutQueries != nil && s.reservationQueries != nil {
		r.Post("/public/feeds/{feed_token}/checkout/start", s.handlePublicFeedCheckoutStart)
	}
	// Anonymous order-status endpoint — WID-0b (feature #319).
	// Gated on checkoutQueries + reservationQueries; ticket/credential queries
	// are optional (handler self-gates for the paid-tickets section).
	if s.checkoutQueries != nil && s.reservationQueries != nil {
		r.Get("/public/checkout/{checkout_token}", s.handlePublicCheckoutStatus)
		// Hold-expiry recovery endpoint — WID-0c (feature #320).
		r.Post("/public/checkout/{checkout_token}/recover", s.handlePublicCheckoutRecover)
	}
	if s.checkoutQueries != nil && s.ticketQueries != nil && s.credentialQueries != nil {
		r.Get("/public/checkout/{checkout_token}/tickets/{ticket_id}/pdf", s.handlePublicTicketPDF)
	}
}
