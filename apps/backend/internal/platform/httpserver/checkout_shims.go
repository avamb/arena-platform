// checkout_shims.go bridges the *Server god-object to the hcheckout sub-package.
// All handler and validation logic lives in hcheckout/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount files compile unchanged.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// checkoutHandler constructs a hcheckout.Handler from the server's dependencies.
func (s *Server) checkoutHandler() *hcheckout.Handler {
	return hcheckout.New(
		s.checkoutQueries,
		s.reservationQueries,
		s.inventoryQueries,
		s.paymentIntentQueries,
		s.refundQueries,
		s.promoQueries,
		s.tierQueries,
		s.ticketQueries,
		s.channelQueries,
		s.orgQueries,
		s.pool,
		s.logger,
		hcheckout.PricingRules(s.pricingRules),
		s.issueTicketsForCheckout,
		s.enqueueDeliveryJobs,
		s.publishTicketRefundedEvents,
		s.publishTicketRefundedV1Events,
	)
}

// NewReservationProcessor forwards to hcheckout.NewReservationProcessor so
// callers in the httpserver package (e.g. worker wiring) can construct the
// processor without importing hcheckout directly.
func NewReservationProcessor(pool *pgxpool.Pool, queries *gen.Queries, logger *slog.Logger) *hcheckout.ReservationProcessor {
	return hcheckout.NewReservationProcessor(pool, queries, logger)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hcheckout without importing that package directly.

type priceBreakdownResponse = hcheckout.PriceBreakdownResponse
type breakdownLineItem = hcheckout.BreakdownLineItem
type checkoutSessionResponse = hcheckout.CheckoutSessionResponse
type channelTTLLookup = hcheckout.ChannelTTLLookup
type orgTTLLookup = hcheckout.OrgTTLLookup

// ─── pure-function forwarders ─────────────────────────────────────────────────

// buildPriceBreakdown forwards to hcheckout so that price_breakdown_163_test.go
// (package httpserver) continues to call the pure helper directly.
func buildPriceBreakdown(ctx context.Context, cs gen.CheckoutSessionRow) (priceBreakdownResponse, bool) {
	return hcheckout.BuildPriceBreakdown(ctx, cs)
}

// ─── const and var forwarders ─────────────────────────────────────────────────

// defaultReservationTTL forwards hcheckout.DefaultReservationTTL so that
// public_feed_checkout.go (package httpserver) continues to compile without
// importing hcheckout directly.
const defaultReservationTTL = hcheckout.DefaultReservationTTL

// validCheckoutTransitions forwards hcheckout.ValidCheckoutTransitions so that
// checkout_132_test.go (package httpserver) can inspect the state-machine table
// without importing hcheckout directly.
var validCheckoutTransitions = hcheckout.ValidCheckoutTransitions

// validPaymentIntentTransitions forwards hcheckout.ValidPaymentIntentTransitions
// so that payment_intents_137_test.go and openapi_payment_intents_271_test.go
// (package httpserver) can inspect the state-machine table without importing
// hcheckout directly.
var validPaymentIntentTransitions = hcheckout.ValidPaymentIntentTransitions

// isTerminalPaymentIntentState forwards to hcheckout so that
// payment_intents_137_test.go (package httpserver) continues to compile and call
// the state-machine logic without importing hcheckout directly.
func isTerminalPaymentIntentState(state string) bool {
	return hcheckout.IsTerminalPaymentIntentState(state)
}

// webhookEventTypeToState forwards hcheckout.WebhookEventTypeToState so that
// payment_intents_137_test.go (package httpserver) can inspect the event-type
// mapping table without importing hcheckout directly.
var webhookEventTypeToState = hcheckout.WebhookEventTypeToState

// validRefundTransitions forwards hcheckout.ValidRefundTransitions so that
// openapi_refunds_274_test.go (package httpserver) can inspect the state-machine
// table without importing hcheckout directly.
var validRefundTransitions = hcheckout.ValidRefundTransitions

// isTerminalRefundState forwards to hcheckout so that refunds_138_test.go
// (package httpserver) continues to compile without importing hcheckout directly.
func isTerminalRefundState(state string) bool {
	return hcheckout.IsTerminalRefundState(state)
}

// refundWebhookEventTypeToState forwards the map so refunds_138_test.go
// (package httpserver) can inspect the event-type mapping table without
// importing hcheckout directly.
var refundWebhookEventTypeToState = hcheckout.RefundWebhookEventTypeToState

// isValidReservationTransition forwards to hcheckout so that
// reservation_131_test.go (package httpserver) continues to compile and call
// the state-machine logic without importing hcheckout directly.
func isValidReservationTransition(from, to string) bool {
	return hcheckout.IsValidReservationTransition(from, to)
}

// validReservationTransitions forwards the map so structural tests can inspect
// the state machine table (e.g. check terminal states have no outgoing edges).
var validReservationTransitions = hcheckout.ValidReservationTransitions

// resolveReservationTTL forwards to hcheckout so that reservation_ttl_177_test.go
// (package httpserver) continues to compile and exercise the TTL resolver with
// in-memory fakes for the channel and organization lookups.
func resolveReservationTTL(
	ctx context.Context,
	channelQ hcheckout.ChannelTTLLookup,
	orgQ hcheckout.OrgTTLLookup,
	channelID, orgID uuid.UUID,
) time.Duration {
	return hcheckout.ResolveReservationTTL(ctx, channelQ, orgQ, channelID, orgID)
}

// checkoutSessionFromRow forwards to hcheckout.CheckoutSessionFromRow so that
// public_feed_checkout.go (package httpserver) and checkout_132_test.go
// continue to compile and access struct fields on the returned value.
func checkoutSessionFromRow(cs gen.CheckoutSessionRow) checkoutSessionResponse {
	return hcheckout.CheckoutSessionFromRow(cs)
}

// ─── checkout session handler shims ──────────────────────────────────────────

func (s *Server) handleStartCheckout(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleStartCheckout(w, r)
}

func (s *Server) handleGetCheckoutSession(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleGetCheckoutSession(w, r)
}

func (s *Server) handleConfirmCheckout(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleConfirmCheckout(w, r)
}

func (s *Server) handleCompleteCheckout(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCompleteCheckout(w, r)
}

func (s *Server) handleAbandonCheckout(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleAbandonCheckout(w, r)
}

// ─── reservation handler shims ────────────────────────────────────────────────

func (s *Server) handleCreateReservation(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCreateReservation(w, r)
}

func (s *Server) handleGetReservation(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleGetReservation(w, r)
}

func (s *Server) handleActivateReservation(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleActivateReservation(w, r)
}

func (s *Server) handleCancelReservation(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCancelReservation(w, r)
}

// ─── payment intent handler shims ────────────────────────────────────────────

func (s *Server) handleCreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCreatePaymentIntent(w, r)
}

func (s *Server) handleGetPaymentIntent(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleGetPaymentIntent(w, r)
}

func (s *Server) handleTransitionPaymentIntent(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleTransitionPaymentIntent(w, r)
}

func (s *Server) handlePaymentIntentWebhook(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandlePaymentIntentWebhook(w, r)
}

// ─── refund handler shims ─────────────────────────────────────────────────────

func (s *Server) handleCreateRefund(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCreateRefund(w, r)
}

func (s *Server) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleGetRefund(w, r)
}

func (s *Server) handleApproveRefund(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleApproveRefund(w, r)
}

func (s *Server) handleRejectRefund(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleRejectRefund(w, r)
}

func (s *Server) handleRefundWebhook(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleRefundWebhook(w, r)
}

// ─── price breakdown handler shim ────────────────────────────────────────────

func (s *Server) handlePriceBreakdown(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandlePriceBreakdown(w, r)
}

// ─── promo code handler shims ─────────────────────────────────────────────────

func (s *Server) handleCreatePromoCode(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleCreatePromoCode(w, r)
}

func (s *Server) handleListPromoCodes(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleListPromoCodes(w, r)
}

func (s *Server) handleGetPromoCode(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleGetPromoCode(w, r)
}

func (s *Server) handleUpdatePromoCode(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleUpdatePromoCode(w, r)
}

func (s *Server) handleDeletePromoCode(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleDeletePromoCode(w, r)
}

func (s *Server) handleValidatePromoCode(w http.ResponseWriter, r *http.Request) {
	s.checkoutHandler().HandleValidatePromoCode(w, r)
}

