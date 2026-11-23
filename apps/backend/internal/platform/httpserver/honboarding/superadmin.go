package honboarding

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// RequireSuperadmin lets only a platform superadmin through to the operator
// routes under /v1/admin/onboarding, reads included. The onboarding.review
// and onboarding.settings permissions are not enough on their own: the
// marker auth.HasSuperadminOrgAccess is set by the server only after it has
// resolved the platform_superadmin role from the database, and never for an
// API key. The X-Admin-Reason rule of the state-changing routes stays in the
// handlers.
func RequireSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.HasSuperadminOrgAccess(r.Context()) {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"superadmin.required", "this route is reserved for the platform superadmin", r,
			))
			return
		}
		next.ServeHTTP(w, r)
	})
}
