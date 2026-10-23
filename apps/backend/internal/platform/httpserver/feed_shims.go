// feed_shims.go bridges the *Server god-object to the hfeed sub-package. All
// handler bodies live in hfeed/; these thin delegating methods preserve the
// unexported *Server method surface so mount_catalog.go and the structural
// test files (feed_tokens_test.go, public_feed_152_test.go,
// public_feed_checkout_153_test.go) compile unchanged.
//
// The public feed rate limiter lives in public_feed_ratelimit.go.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hfeed"
)

// ─── handler construction ─────────────────────────────────────────────────────

// feedHandler constructs an hfeed.Handler from the server's dependencies. A
// fresh handler per request keeps the wiring uniform with hbilling / hgeo /
// hgdpr and avoids stale captures when test code mutates *Server fields
// between calls.
func (s *Server) feedHandler() *hfeed.Handler {
	trustedProxies := 0
	if s.cfg != nil {
		trustedProxies = s.cfg.TrustedProxyCount
	}
	return hfeed.New(
		s.feedTokenQueries,
		s.publicFeedQueries,
		s.sessionQueries,
		s.tierQueries,
		s.checkoutQueries,
		s.reservationQueries,
		s.inventoryQueries,
		s.promoQueries,
		s.ticketQueries,
		s.credentialQueries,
		s.funnelQueries,
		s.pool,
		s.logger,
		s.audit,
		s.publicFeedRL,
		hcheckout.PricingRules(s.pricingRules),
		trustedProxies,
	).WithMediaSigner(s.signedMediaURL).
		WithPayments(
			s.hostedPaymentStarter(),
			s.checkoutReturnURLPolicy(),
			widgetPaymentWindow(s.cfg),
			widgetPaymentGrace(s.cfg),
			s.eventQueries,
		).
		WithContactForm(s.contactFormReady(), s.contactFormSiteKey(), s.contactFormPrivacyURL()).
		WithCheckoutConsent(s.platformTermsURL(), s.contactFormPrivacyURL()).
		WithLegalTerms(s.legalTermsAPIBase()).
		WithMetrics(s.typedMetrics).
		WithPromoAttempts(s.promoAttemptRecorder())
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These keep the original unexported type names live in package httpserver so
// test files (feed_tokens_test.go, public_feed_checkout_153_test.go) compile
// without importing the hfeed sub-package.

type feedTokenResponse = hfeed.FeedTokenResponse
type publicFeedCheckoutStartRequest = hfeed.PublicFeedCheckoutStartRequest
type publicGAItem = hfeed.PublicGAItem

// ─── pure-function forwarders ─────────────────────────────────────────────────
// feed_tokens_test.go calls these unqualified — keep the original lowercase
// names live in package httpserver so callers do not learn about the hfeed
// sub-package.

// feedTokenFromRow forwards to hfeed.FeedTokenFromRow.
func feedTokenFromRow(ft gen.FeedTokenRow) feedTokenResponse {
	return hfeed.FeedTokenFromRow(ft)
}

// generateFeedToken forwards to hfeed.GenerateFeedToken.
func generateFeedToken() (string, error) {
	return hfeed.GenerateFeedToken()
}

// ─── feed token management handler shims ──────────────────────────────────────

func (s *Server) handleCreateFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleCreateFeedToken(w, r)
}

func (s *Server) handleListFeedTokens(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleListFeedTokens(w, r)
}

func (s *Server) handleGetFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleGetFeedToken(w, r)
}

func (s *Server) handleRevokeFeedToken(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.feedHandler().HandleRevokeFeedToken(w, r)
}

// ─── public feed handler shims ────────────────────────────────────────────────

func (s *Server) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeed(w, r)
}

func (s *Server) handlePublicFeedEvents(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedEvents(w, r)
}

func (s *Server) handlePublicFeedEvent(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedEvent(w, r)
}

// ─── hosted sales page resolver shim ──────────────────────────────────────────

func (s *Server) handlePublicPage(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicPage(w, r)
}

func (s *Server) handlePublicPromoterPage(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicPromoterPage(w, r)
}

// handlePublicPageTrack counts one hit of the page statistics beacon (HP-2a).
func (s *Server) handlePublicPageTrack(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicPageTrack(w, r)
}

// ─── public feed checkout handler shim ────────────────────────────────────────

// handlePublicFeedCheckoutQuote prices a public cart (with an optional promo
// code) without holding anything.
func (s *Server) handlePublicFeedCheckoutQuote(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedCheckoutQuote(w, r)
}

func (s *Server) handlePublicFeedCheckoutStart(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicFeedCheckoutStart(w, r)
}

// ─── public checkout status handler shims (feature #319 WID-0b) ──────────────

func (s *Server) handlePublicCheckoutStatus(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleGetPublicCheckoutStatus(w, r)
}

func (s *Server) handlePublicTicketPDF(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandleGetPublicTicketPDF(w, r)
}

func (s *Server) handlePublicPaymentReturn(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePaymentReturn(w, r)
}

// ─── hold-expiry recovery handler shim (feature #320 WID-0c) ─────────────────

func (s *Server) handlePublicCheckoutRecover(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePublicCheckoutRecover(w, r)
}

// ─── widget funnel telemetry shim (feature #322 WID-0e) ──────────────────────

func (s *Server) handlePublicFeedFunnelEvents(w http.ResponseWriter, r *http.Request) {
	s.feedHandler().HandlePostFunnelEvents(w, r)
}
