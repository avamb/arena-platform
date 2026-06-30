// catalog_shims.go bridges the *Server god-object to the hcatalog sub-package.
// All handler and validation logic lives in hcatalog/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount files compile unchanged.
package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// catalogHandler constructs a hcatalog.Handler from the server's dependencies.
func (s *Server) catalogHandler() *hcatalog.Handler {
	return hcatalog.New(
		s.eventQueries,
		s.venueQueries,
		s.tierQueries,
		s.channelQueries,
		s.publicationQueries,
		s.pool,
		s.audit,
		s.logger,
	)
}

// ──── type aliases ───────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hcatalog without importing that package directly.

type channelResponse = hcatalog.ChannelResponse
type eventResponse = hcatalog.EventResponse
type publicationResponse = hcatalog.PublicationResponse
type tierResponse = hcatalog.TierResponse
type venueResponse = hcatalog.VenueResponse

// ──── package-level forwarders (test files call these as bare functions) ─────

func isValidEventTransition(from, to string) bool {
	return hcatalog.IsValidEventTransition(from, to)
}

func validatePricingMode(mode string, priceAmount int64, pwywMin, pwywMax *int64) (string, string) {
	return hcatalog.ValidatePricingMode(mode, priceAmount, pwywMin, pwywMax)
}

func validateChannelConfig(paymentMode, provider, providerAccountID string) string {
	return hcatalog.ValidateChannelConfig(paymentMode, provider, providerAccountID)
}

func maskProviderAccountID(in *string) *string {
	return hcatalog.MaskProviderAccountID(in)
}

func normalizeChannelSettings(raw json.RawMessage) (json.RawMessage, string) {
	return hcatalog.NormalizeChannelSettings(raw)
}

func channelFromRow(ch gen.SalesChannelRow) channelResponse {
	return hcatalog.ChannelFromRow(ch)
}

func channelFromRowMasked(ch gen.SalesChannelRow) channelResponse {
	return hcatalog.ChannelFromRowMasked(ch)
}

func settingsForResponse(raw json.RawMessage) json.RawMessage {
	return hcatalog.SettingsForResponse(raw)
}

func eventFromRow(e gen.EventRow) eventResponse {
	return hcatalog.EventFromRow(e)
}

func publicationFromRow(ep gen.EventPublicationRow) publicationResponse {
	return hcatalog.PublicationFromRow(ep)
}

func tierFromRow(t gen.TicketTierRow) tierResponse {
	return hcatalog.TierFromRow(t)
}

func venueFromRow(v gen.VenueRow) venueResponse {
	return hcatalog.VenueFromRow(v)
}

// ──── event handler shims ─────────────────────────────────────────────────────

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateEvent(w, r)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListEvents(w, r)
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetEvent(w, r)
}

func (s *Server) handleListEventsByOrg(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListEventsByOrg(w, r)
}

func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateEvent(w, r)
}

func (s *Server) handleUpdateEventStatus(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateEventStatus(w, r)
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteEvent(w, r)
}

// ──── venue handler shims ─────────────────────────────────────────────────────

func (s *Server) handleCreateVenue(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateVenue(w, r)
}

func (s *Server) handleListVenues(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListVenues(w, r)
}

func (s *Server) handleGetVenue(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetVenue(w, r)
}

func (s *Server) handleListVenuesByOrg(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListVenuesByOrg(w, r)
}

func (s *Server) handleUpdateVenue(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateVenue(w, r)
}

func (s *Server) handleDeleteVenue(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteVenue(w, r)
}

// ──── ticket tier handler shims ───────────────────────────────────────────────

func (s *Server) handleCreateTier(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateTier(w, r)
}

func (s *Server) handleListTiers(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListTiers(w, r)
}

func (s *Server) handleGetTier(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetTier(w, r)
}

func (s *Server) handleUpdateTier(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateTier(w, r)
}

func (s *Server) handleDeleteTier(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteTier(w, r)
}

// ──── publication handler shims ───────────────────────────────────────────────

func (s *Server) handlePublishEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandlePublishEvent(w, r)
}

func (s *Server) handleUnpublishEvent(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUnpublishEvent(w, r)
}

func (s *Server) handleListPublications(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListPublications(w, r)
}

// ──── channel handler shims ───────────────────────────────────────────────────

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateChannel(w, r)
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListChannels(w, r)
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetChannel(w, r)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateChannel(w, r)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteChannel(w, r)
}
