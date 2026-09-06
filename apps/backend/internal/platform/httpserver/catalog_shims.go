// catalog_shims.go bridges the *Server god-object to the hcatalog sub-package.
// All handler and validation logic lives in hcatalog/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount files compile unchanged.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcatalog"
)

// catalogHandler constructs a hcatalog.Handler from the server's dependencies.
// The session-cancelled webhook publisher is injected as a callback because
// its implementation lives in the hscanner sub-package (see scanner_shims.go);
// the seating binder callback (seated session create, AB-36) is injected the
// same way because its implementation lives in hseating.
func (s *Server) catalogHandler() *hcatalog.Handler {
	return hcatalog.New(
		s.eventQueries,
		s.venueQueries,
		s.tierQueries,
		s.channelQueries,
		s.publicationQueries,
		s.sessionQueries,
		s.inventoryQueries,
		s.pool,
		s.audit,
		s.logger,
		s.publishSessionCancelledEvent,
	).WithMembershipQueries(s.membershipQueries).
		WithSeatingBinder(s.bindSeatingForSessionCreate).
		WithCatalogEventPublisher(s.publishCatalogEvent).
		WithPublicBaseURL(s.appPublicURL())
}

// appPublicURL returns cfg.AppPublicURL when present, otherwise the empty
// string. Callers (currently the gateway-credential PUT endpoint, feature
// #473 spec §5.4) treat "" as "operator has not configured a public URL yet",
// which is a supported development-mode behaviour.
func (s *Server) appPublicURL() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.AppPublicURL
}

// bindSeatingForSessionCreate adapts hseating's inline bind (AB-36 step 3)
// to the hcatalog.SeatingBinder callback shape.
func (s *Server) bindSeatingForSessionCreate(
	ctx context.Context,
	r *http.Request,
	eventID, sessionID, planVersionID uuid.UUID,
	admissionMode string,
) *hcatalog.SeatingBindError {
	bErr := s.seatingHandler().BindSeatingForSessionCreate(ctx, r, eventID, sessionID, planVersionID, admissionMode)
	if bErr == nil {
		return nil
	}
	return &hcatalog.SeatingBindError{
		Status:  bErr.Status,
		Code:    bErr.Code,
		Message: bErr.Message,
		Details: bErr.Details,
	}
}

// ──── type aliases ───────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hcatalog without importing that package directly.

type channelResponse = hcatalog.ChannelResponse
type eventResponse = hcatalog.EventResponse
type publicationResponse = hcatalog.PublicationResponse
type sessionResponse = hcatalog.SessionResponse
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

func isValidSessionTransition(from, to string) bool {
	return hcatalog.IsValidSessionTransition(from, to)
}

func sessionFromRow(sess gen.SessionRow, hasOverlap bool) sessionResponse {
	return hcatalog.SessionFromRow(sess, hasOverlap)
}

func detectSessionOverlaps(sessions []gen.SessionRow) bool {
	return hcatalog.DetectSessionOverlaps(sessions)
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

func (s *Server) handleListEventArtists(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListEventArtists(w, r)
}

func (s *Server) handleCreateEventArtist(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateEventArtist(w, r)
}

func (s *Server) handleUpdateEventArtist(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateEventArtist(w, r)
}

func (s *Server) handleDeleteEventArtist(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteEventArtist(w, r)
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

// AB-48 scheduled pricing + bulk grid shims.
func (s *Server) handleGetTierPriceSchedule(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetTierPriceSchedule(w, r)
}

func (s *Server) handlePutTierPriceSchedule(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandlePutTierPriceSchedule(w, r)
}

func (s *Server) handleBulkSessionPricing(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleBulkSessionPricing(w, r)
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

// ──── channel gateway-credential shims (feature #473, W1-A1d) ────────────────

func (s *Server) handleGetChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetChannelGatewayCredential(w, r)
}

func (s *Server) handlePutChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandlePutChannelGatewayCredential(w, r)
}

func (s *Server) handleDeleteChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteChannelGatewayCredential(w, r)
}

// ──── channel wp-webhook shims (feature #507, W1-B7d) ────────────────────────

func (s *Server) handleGetChannelWPWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetChannelWPWebhook(s.pgxPool, w, r)
}

func (s *Server) handlePutChannelWPWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandlePutChannelWPWebhook(s.pgxPool, w, r)
}

func (s *Server) handleDeleteChannelWPWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteChannelWPWebhook(s.pgxPool, w, r)
}

// ──── session handler shims ───────────────────────────────────────────────────

// onCapacityChange forwards to hcatalog.OnCapacityChange. Kept as a *Server
// method because sessions_test.go invokes the capacity propagation hook on
// *Server directly.
func (s *Server) onCapacityChange(ctx context.Context, sessionID uuid.UUID, oldCapacity, newCapacity int32) {
	s.catalogHandler().OnCapacityChange(ctx, sessionID, oldCapacity, newCapacity)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleCreateSession(w, r)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleListSessions(w, r)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetSession(w, r)
}

func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpdateSession(w, r)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteSession(w, r)
}

// ──── session media gallery shims (AB-47b, feature #435) ─────────────────────

func (s *Server) handleGetSessionMedia(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetSessionMedia(w, r)
}

func (s *Server) handleReplaceSessionMedia(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleReplaceSessionMedia(w, r)
}

// ──── MACS export shim (AB-50b, feature #438) ─────────────────────────────────

func (s *Server) handleMACSExport(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleMACSExport(s.pgxPool, w, r)
}

// ──── MACS webhook shims (AB-50c, feature #439) ───────────────────────────────

func (s *Server) handleGetMACSWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleGetMACSWebhook(s.pgxPool, w, r)
}

func (s *Server) handleUpsertMACSWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleUpsertMACSWebhook(s.pgxPool, w, r)
}

func (s *Server) handleDeleteMACSWebhook(w http.ResponseWriter, r *http.Request) {
	s.catalogHandler().HandleDeleteMACSWebhook(s.pgxPool, w, r)
}
