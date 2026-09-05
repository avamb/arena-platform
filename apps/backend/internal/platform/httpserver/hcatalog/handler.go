// Package hcatalog implements HTTP handlers for the catalog domain:
// events, venues, ticket tiers, publications, sales channels, and sessions.
package hcatalog

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// SessionCancelledPublisher is the callback the session PATCH handler fires
// when a session transitions into "cancelled" (webhook event catalog, feature
// S-1). The canonical implementation lives in the hscanner sub-package;
// catalog_shims.go injects the *Server forwarder so hcatalog never imports
// hscanner (or the parent httpserver package).
type SessionCancelledPublisher func(ctx context.Context, sessionID, eventID, previousStatus string)

// CatalogEventPublisher is the callback the event/session write handlers fire
// when a catalog change must be mirrored by a subscribed site (W1-B7c, #506).
// eventType is one of hscanner's v1.event.published / v1.event.updated /
// v1.session.updated; sessionIDs may be empty, which the dispatcher reads as
// "every session of this event".
//
// Like SessionCancelledPublisher it is injected rather than imported, so
// hcatalog stays free of a dependency on hscanner. A nil publisher disables
// the notification, which is what every handler test gets.
type CatalogEventPublisher func(ctx context.Context, eventType, eventID, orgID string, sessionIDs []string)

// The catalog outbox event types, mirrored from hscanner (W1-B7c, #506).
// They are duplicated rather than imported for the same reason the publisher
// is a callback: hcatalog must not depend on hscanner. A guard test in the
// parent httpserver package — which legitimately imports both — asserts the
// two sets stay identical.
const (
	EventPublishedEventType = "v1.event.published"
	EventUpdatedEventType   = "v1.event.updated"
	SessionUpdatedEventType = "v1.session.updated"
)

const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hcatalog requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all catalog HTTP handlers.
// sessionQueries serves the sessions.sql methods and inventoryQueries the
// inventory_ledger.sql capacity-propagation methods (feature #130); both sit
// on the same *gen.Queries type and are wired from the corresponding *Server
// fields by catalog_shims.go.
type Handler struct {
	eventQueries            *gen.Queries
	venueQueries            *gen.Queries
	tierQueries             *gen.Queries
	channelQueries          *gen.Queries
	publicationQueries      *gen.Queries
	sessionQueries          *gen.Queries
	inventoryQueries        *gen.Queries
	membershipQueries       *gen.Queries // used by requireOrgMembership (PR2-01)
	pool                    TxStarter
	audit                   audit.Writer
	logger                  *slog.Logger
	publishSessionCancelled SessionCancelledPublisher
	// publishCatalogEvent notifies mirrors of a catalog change (W1-B7c,
	// #506). Chainable-wired via WithCatalogEventPublisher rather than added
	// to New, because New already has seven query handles and dozens of test
	// call sites that must keep compiling.
	publishCatalogEvent CatalogEventPublisher
	bindSeating         SeatingBinder // seated session create (AB-36); wired via WithSeatingBinder
	// publicBaseURL is APP_PUBLIC_URL (spec §5.4 "PUBLIC_BASE_URL"); empty
	// string when unset. Consumed by the gateway-credential PUT endpoint
	// (feature #473) to emit base_url/image_url so the WordPress plugin
	// operator does not have to type them in by hand. Wired via
	// WithPublicBaseURL from the httpserver shim layer.
	publicBaseURL string
}

// New constructs a Handler from the caller's dependencies.
func New(
	eventQ, venueQ, tierQ, channelQ, publicationQ, sessionQ, inventoryQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
	publishSessionCancelled SessionCancelledPublisher,
) *Handler {
	return &Handler{
		eventQueries:            eventQ,
		venueQueries:            venueQ,
		tierQueries:             tierQ,
		channelQueries:          channelQ,
		publicationQueries:      publicationQ,
		sessionQueries:          sessionQ,
		inventoryQueries:        inventoryQ,
		pool:                    pool,
		audit:                   auditWriter,
		logger:                  logger,
		publishSessionCancelled: publishSessionCancelled,
	}
}

// WithMembershipQueries attaches a separate *gen.Queries handle used for org
// membership checks (PR2-01). Production wiring calls this in the shim layer;
// tests that omit it will have membership checks silently skip.
func (h *Handler) WithMembershipQueries(q *gen.Queries) *Handler {
	h.membershipQueries = q
	return h
}

// WithCatalogEventPublisher wires the outbox notification for catalog changes
// (W1-B7c, #506). Leaving it unset is deliberate and safe: the handlers guard
// on nil, so a Handler built without it simply never notifies.
func (h *Handler) WithCatalogEventPublisher(p CatalogEventPublisher) *Handler {
	h.publishCatalogEvent = p
	return h
}

// notifyCatalogChange is the single nil-guarded fire site every write handler
// funnels through, so no caller has to repeat the guard.
func (h *Handler) notifyCatalogChange(ctx context.Context, eventType, eventID, orgID string, sessionIDs []string) {
	if h.publishCatalogEvent == nil || eventID == "" {
		return
	}
	h.publishCatalogEvent(ctx, eventType, eventID, orgID, sessionIDs)
}

// WithPublicBaseURL wires the platform's canonical public base URL (feature
// #473, spec §5.4). An empty string is a valid input — the gateway-credential
// PUT endpoint will emit empty base_url/image_url in that case, matching the
// spec text "empty string if unset".
func (h *Handler) WithPublicBaseURL(u string) *Handler {
	h.publicBaseURL = u
	return h
}
