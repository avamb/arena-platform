// Package hiam implements HTTP handlers for the IAM domain:
// organizations, memberships, superadmin console, impersonation,
// admin-org management, admin-membership management, and admin-user creation.
package hiam

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

const pgUniqueViolation = "23505"
const pgForeignKeyViolation = "23503"

// TxStarter is the narrow subset of PoolDB that hiam requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all IAM HTTP handlers.
type Handler struct {
	orgQueries        *gen.Queries
	membershipQueries *gen.Queries
	superadminQueries *gen.Queries
	pool              TxStarter
	audit             audit.Writer
	logger            *slog.Logger
	stub              *auth.StubProvider
}

// New constructs a Handler from the caller's dependencies.
func New(
	orgQ, membershipQ, superadminQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
	stub *auth.StubProvider,
) *Handler {
	return &Handler{
		orgQueries:        orgQ,
		membershipQueries: membershipQ,
		superadminQueries: superadminQ,
		pool:              pool,
		audit:             auditWriter,
		logger:            logger,
		stub:              stub,
	}
}
