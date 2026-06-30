// Package hcatalog implements HTTP handlers for the catalog domain:
// events, venues, ticket tiers, publications, and sales channels.
package hcatalog

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

const pgUniqueViolation = "23505"

// TxStarter is the narrow subset of PoolDB that hcatalog requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all catalog HTTP handlers.
type Handler struct {
	eventQueries       *gen.Queries
	venueQueries       *gen.Queries
	tierQueries        *gen.Queries
	channelQueries     *gen.Queries
	publicationQueries *gen.Queries
	pool               TxStarter
	audit              audit.Writer
	logger             *slog.Logger
}

// New constructs a Handler from the caller's dependencies.
func New(
	eventQ, venueQ, tierQ, channelQ, publicationQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		eventQueries:       eventQ,
		venueQueries:       venueQ,
		tierQueries:        tierQ,
		channelQueries:     channelQ,
		publicationQueries: publicationQ,
		pool:               pool,
		audit:              auditWriter,
		logger:             logger,
	}
}
