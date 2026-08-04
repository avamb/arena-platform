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
	bindSeating             SeatingBinder // seated session create (AB-36); wired via WithSeatingBinder
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
