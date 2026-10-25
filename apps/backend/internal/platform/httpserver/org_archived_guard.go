package httpserver

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// refuseArchivedOrganization closes an archived organization to its members
// and its own API keys. A platform superadmin archives an organization with
// POST /v1/admin/organizations/{org_id}/archive (organizations.deleted_at),
// but the membership guards read memberships without looking at the
// organization, so every org-scoped route kept serving it: the owner of an
// archived organization went on working in it through the Telegram bot with
// a token issued before the archive (found on production 2026-10-25).
//
// It runs in applyAuthScoped after the permission gate and the superadmin
// marker, on every route whose path names an organization (pathOrganization),
// and answers what the organization's own routes already answer for a
// deleted one: 404 org.not_found. A platform superadmin passes, so admin-web
// still reads an archived organization; their writes still need
// X-Admin-Reason, which the membership guards demand as before.
//
// One primary-key lookup per request. An organization that does not exist
// passes, so the route keeps its own not-found answer. A failed lookup is a
// 500: the guard fails closed. It reads through the raw pool (s.pgxPool),
// which production always wires; a server built without one (the unit tests,
// whose fake membership queries cannot answer this lookup) has no
// organization rows to look up and the request passes.
func (s *Server) refuseArchivedOrganization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.pgxPool == nil || auth.HasSuperadminOrgAccess(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		orgID, ok := pathOrganization(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		archived, err := gen.New(s.pgxPool).GetOrganizationArchived(r.Context(), orgID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			next.ServeHTTP(w, r)
		case err != nil:
			s.logger.ErrorContext(r.Context(), "server: organization archive check failed", "error", err.Error())
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"org.lookup_failed", "Could not check the organization. Please try again.", r))
		case archived:
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"org.not_found", "This organization does not exist or was deleted.", r))
		default:
			next.ServeHTTP(w, r)
		}
	})
}
