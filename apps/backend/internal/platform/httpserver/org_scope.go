package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// orgScoped puts the route's organization in the request context before the
// permission gate runs (PERM-ORG): a path with `{org_id}` is decided from the
// caller's roles IN that organization, not from the union of all their
// memberships. It also records the permission the gate asks for so a flat
// route that loads a row later can ask the same question inside the row's
// organization (rowOrgAccess). A path without a parseable org id is left
// as it was.
func orgScoped(perm string) func(http.Handler) http.Handler { return orgScopedFor(perm, true) }

// orgScopedFor is orgScoped with the path-organization scoping switchable.
// With scoped=false only the required permission is recorded: the routes that
// act ACROSS two organizations through one path (an organizer managing an
// external allocation through the PARTNER's path, the caller's authority
// being their membership in the session's organization) keep the union of
// the caller's memberships and their handlers do the per-organization check.
func orgScopedFor(perm string, scoped bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := permissions.WithRequiredPermission(r.Context(), perm)
			if raw := chi.URLParam(r, "org_id"); scoped && raw != "" {
				if id, err := uuid.Parse(raw); err == nil {
					ctx = permissions.WithOrgScope(ctx, id)
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
