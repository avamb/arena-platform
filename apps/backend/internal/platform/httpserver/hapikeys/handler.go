// Package hapikeys implements HTTP handlers for the organization API-keys
// management surface (feature #514, W1-C1c; spec §13.1): the CRUD surface an
// org admin uses to issue and revoke service credentials for server-to-server
// callers (e.g. the WordPress "lampyris-ops" plugin) that cannot hold a user
// session.
//
// Issuing (apikeys.Issue) and authenticating (apikeys.Authenticate) the keys
// themselves live in internal/platform/apikeys — this package is HTTP-only
// plumbing on top of that: request decoding, org-membership + X-Admin-Reason
// gating, response shaping (never exposing key_hash), and audit-event writes.
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin api_keys_shims.go bridge in the parent package, matching the
// pattern established by hbankaccounts / hcatalog and friends.
package hapikeys

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// TxStarter is the narrow subset of PoolDB that hapikeys requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all api-key HTTP handlers.
type Handler struct {
	queries           *gen.Queries
	membershipQueries *gen.Queries // used by requireOrgMembership
	pool              TxStarter
	audit             audit.Writer
	logger            *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	apiKeyQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		queries: apiKeyQ,
		pool:    pool,
		audit:   auditWriter,
		logger:  logger,
	}
}

// WithMembershipQueries attaches a separate *gen.Queries handle used for org
// membership checks. Production wiring calls this in the shim layer; tests
// that omit it will have membership checks silently skip (queries == nil
// means no membership enforcement, matching the hbankaccounts precedent).
func (h *Handler) WithMembershipQueries(q *gen.Queries) *Handler {
	h.membershipQueries = q
	return h
}
