// uuid_path_param.go provides uuidPathParam, a helper that extracts and
// validates a UUID-typed chi URL parameter. When the raw parameter value is
// not a valid UUID the helper writes the standard JSON error envelope with
// code='http.invalid_path_param' and details.param=<name>, then returns
// (uuid.UUID{}, false) so the caller can return immediately without risking
// a crash on an empty/malformed ID.
//
// Usage inside a handler:
//
//	func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
//	    id, ok := uuidPathParam(w, r, "id")
//	    if !ok {
//	        return // 400 already written
//	    }
//	    // use id …
//	}
//
// This centralises UUID path-param validation so that:
//   - The error code and shape are consistent across all resource handlers.
//   - Feature #41 ("Malformed UUID in path returns 400 envelope") is satisfied
//     for every handler that calls uuidPathParam rather than uuid.Parse inline.
package httpserver

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// uuidPathParam delegates to httputil.UUIDPathParam. Kept as an unexported
// alias so existing handler methods on *Server require no import changes.
func uuidPathParam(w http.ResponseWriter, r *http.Request, paramName string) (uuid.UUID, bool) {
	return httputil.UUIDPathParam(w, r, paramName)
}

// errorEnvelopeWithDetails delegates to httputil.ErrorEnvelopeWithDetails.
// Kept as an unexported alias so existing handler methods require no changes.
func errorEnvelopeWithDetails(code, message string, r *http.Request, details map[string]any) map[string]any {
	return httputil.ErrorEnvelopeWithDetails(code, message, r, details)
}
