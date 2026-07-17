// Package hbankaccounts implements HTTP handlers for the organization
// bank-accounts domain (feature #255): the CRUD surface an org admin uses to
// manage the banking coordinates shown on payouts, refunds, and tax forms.
//
// The rows are metadata only — Stripe Connect / AllPay payout configuration
// continues to live in payment_provider_configs (see hpayments).
//
// The handlers live behind a small Handler struct so *Server can wire them
// via a thin bank_accounts_shims.go bridge in the parent package, matching
// the pattern established by hpayments / hcatalog / hcheckout / htickets and
// friends.
package hbankaccounts

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// TxStarter is the narrow subset of PoolDB that hbankaccounts requires.
// PoolDB satisfies this by structural typing.
type TxStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Handler holds the shared dependencies for all bank-account HTTP handlers.
type Handler struct {
	queries           *gen.Queries
	membershipQueries *gen.Queries // used by requireOrgMembership (PR2-01)
	pool              TxStarter
	audit             audit.Writer
	logger            *slog.Logger
}

// New constructs a Handler from the caller's dependencies. Nil queries and a
// nil pool are allowed; individual handlers self-gate with a 503
// dependency.database_unavailable envelope, matching the *Server route-mount
// precedent.
func New(
	bankAccountQ *gen.Queries,
	pool TxStarter,
	auditWriter audit.Writer,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		queries: bankAccountQ,
		pool:    pool,
		audit:   auditWriter,
		logger:  logger,
	}
}

// WithMembershipQueries attaches a separate *gen.Queries handle used for org
// membership checks (PR2-01). Production wiring calls this in the shim layer;
// tests that omit it will have membership checks silently skip (queries == nil
// means no membership enforcement, matching pre-PR2-01 behaviour).
func (h *Handler) WithMembershipQueries(q *gen.Queries) *Handler {
	h.membershipQueries = q
	return h
}
