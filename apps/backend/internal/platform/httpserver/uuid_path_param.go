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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// uuidPathParam extracts the chi URL parameter named paramName and parses it
// as a UUID. On success it returns (id, true). On failure it writes a 400
// JSON error envelope (code='http.invalid_path_param', details.param=paramName)
// and returns (uuid.UUID{}, false). The caller MUST return immediately when
// ok==false.
func uuidPathParam(w http.ResponseWriter, r *http.Request, paramName string) (uuid.UUID, bool) {
	raw := chi.URLParam(r, paramName)
	id, err := uuid.Parse(raw)
	if err != nil {
		env := errorEnvelopeWithDetails(
			"http.invalid_path_param",
			"path parameter '"+paramName+"' must be a valid UUID, got: '"+raw+"'",
			r,
			map[string]any{"param": paramName},
		)
		writeJSON(w, http.StatusBadRequest, env)
		return uuid.UUID{}, false
	}
	return id, true
}

// errorEnvelopeWithDetails is identical to errorEnvelope but additionally
// sets the optional error.details field to the provided map. Use this when
// the error context (e.g. which path parameter was malformed) must be
// machine-readable by clients.
func errorEnvelopeWithDetails(code, message string, r *http.Request, details map[string]any) map[string]any {
	env := errorEnvelope(code, message, r)
	if details != nil {
		env["error"].(map[string]any)["details"] = details
	}
	return env
}
