// Package hnetworks implements the operator-network domain handlers:
// /v1/operator-networks (CRUD) and /v1/admin/networks (user + org assignment).
package hnetworks

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Canonical assignment-kind values matching the
// network_organizations.assignment_kind CHECK constraint in 0043_operator_networks.sql.
const (
	NetworkAssignmentKindOrganizer = "organizer"
	NetworkAssignmentKindAgent     = "agent"
)

// Handler holds the narrow set of dependencies needed by the network endpoints.
type Handler struct {
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger
}

// New constructs a Handler. queries may be nil; each method guards against it
// and returns 503 so the server starts cleanly without a database connection.
func New(queries *gen.Queries, aud audit.Writer, logger *slog.Logger) *Handler {
	return &Handler{queries: queries, audit: aud, logger: logger}
}
