// bil24_shims.go bridges the *Server god-object to the hbil24 sub-package.
// All per-command handler bodies live in hbil24/; this file keeps three
// kinds of surface in package httpserver:
//
//   - thin delegating methods with the ORIGINAL lowercase names
//     (handleBil24Command) so route mounting stays unchanged;
//   - the route mount (mountCompatRoutes) and the BIL24_COMPAT_ENABLED
//     feature-flag accessor (bil24CompatEnabled), which touch *Server state
//     (s.router / s.bil24Enabled) and therefore cannot move;
//   - the Bil24 wire-format aliases / forwarders (bil24Request, bil24Response,
//     bil24OK, bil24Error, writeBil24JSON, ResultCode*, TranslateLegacyID,
//     TranslatePlatformID, ErrLegacyIDNotFound) that bil24_compat_157_test.go
//     references unqualified in package httpserver. The adapter package
//     internal/adapters/bil24compat remains the source of truth.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbil24"
)

// bil24Handler constructs an hbil24.Handler from the server's dependencies.
// A fresh handler per request keeps the wiring uniform with hwordpress /
// hgeo / hfeed and avoids stale captures when test code mutates *Server
// fields between calls.
func (s *Server) bil24Handler() *hbil24.Handler {
	return hbil24.New(
		s.eventQueries,
		s.tierQueries,
		s.checkoutQueries,
		s.ticketQueries,
		s.barcodeQueries,
		s.logger,
	)
}

// ─── result codes (re-exported from the adapter package) ─────────────────────

// Bil24 wire result codes — re-exported from internal/adapters/bil24compat so
// existing in-package references and the #157 test suite continue to compile
// without churn. The adapter package is the source of truth.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = bil24compat.ResultCodeOK
	// ResultCodeUnknownCommand is returned when the gateway receives a command
	// name it does not recognise (Bil24 wire: -1).
	ResultCodeUnknownCommand = bil24compat.ResultCodeUnknownCommand
	// ResultCodeInvalidRequest is returned when a required request field is
	// missing or malformed (Bil24 wire: -2).
	ResultCodeInvalidRequest = bil24compat.ResultCodeInvalidRequest
	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform (Bil24 wire: -3).
	ResultCodeNotFound = bil24compat.ResultCodeNotFound
	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99).
	ResultCodeInternalError = bil24compat.ResultCodeInternalError
)

// ─── request / response envelope (aliased from the adapter package) ──────────

// bil24Request is the top-level request envelope for POST /compat/bil24/json.
// Aliased to the adapter package so the wire format has exactly one
// definition.
//
//nolint:unused // source-grep witness: alias surface kept for test #157
type bil24Request = bil24compat.Request

// bil24Response is the Bil24-compatible response envelope, aliased to the
// adapter package.
type bil24Response = bil24compat.Response

// bil24OK constructs a success response for the given command with optional
// extra payload fields. Forwarder to bil24compat.OK.
func bil24OK(command string, extra map[string]any) bil24Response {
	return bil24compat.OK(command, extra)
}

// bil24Error constructs an error response for the given command. Forwarder
// to bil24compat.Error.
func bil24Error(command string, code int, description string) bil24Response {
	return bil24compat.Error(command, code, description)
}

// writeBil24JSON writes a Bil24-envelope response with Content-Type
// application/json. Forwarder to bil24compat.WriteJSON.
func writeBil24JSON(w http.ResponseWriter, status int, resp bil24Response) {
	bil24compat.WriteJSON(w, status, resp)
}

// ─── ID translation layer (re-exported from the adapter package) ─────────────

// ErrLegacyIDNotFound is returned by TranslateLegacyID when the provided
// legacy identifier cannot be resolved to a platform UUID. Re-exported from
// the adapter package so existing references resolve to the same sentinel
// value (errors.Is still works because it is the very same variable).
var ErrLegacyIDNotFound = bil24compat.ErrLegacyIDNotFound

// TranslateLegacyID converts a legacy Bil24 identifier (actionId,
// actionEventId, orderId, ticketId, …) to the platform's UUIDv7.
// Forwarder to bil24compat.TranslateLegacyID.
func TranslateLegacyID(raw string) (uuid.UUID, error) {
	return bil24compat.TranslateLegacyID(raw)
}

// TranslatePlatformID converts a platform UUID to the Bil24 legacy ID
// format. Forwarder to bil24compat.TranslatePlatformID.
func TranslatePlatformID(id uuid.UUID) string {
	return bil24compat.TranslatePlatformID(id)
}

// ─── gateway feature-flag guard ───────────────────────────────────────────────

// bil24CompatEnabled returns true when the Bil24 compatibility gateway has
// been enabled at server construction time. When false, the
// /compat/bil24/* subtree is not mounted and requests to those paths get a
// chi 404 via handleNotFound. Individual commands may still return 503 if a
// specific query subset is missing.
//
//nolint:unused // referenced by test #157 as identifier surface check
func (s *Server) bil24CompatEnabled() bool {
	return s.bil24Enabled
}

// ─── command gateway handler shim ─────────────────────────────────────────────

// handleBil24Command delegates to hbil24.(*Handler).HandleBil24Command, the
// single-entry-point dispatcher for POST /compat/bil24/json.
func (s *Server) handleBil24Command(w http.ResponseWriter, r *http.Request) {
	s.bil24Handler().HandleBil24Command(w, r)
}

// ─── router mounting ──────────────────────────────────────────────────────────

// mountCompatRoutes mounts the Bil24-compatible API gateway under /compat/bil24/*.
//
// The subtree is only mounted when bil24Enabled is true (env: BIL24_COMPAT_ENABLED).
// When disabled the paths do not exist in the router; chi returns 404 via handleNotFound.
// Feature #157.
func (s *Server) mountCompatRoutes() {
	if !s.bil24Enabled {
		return
	}
	s.router.Route("/compat/bil24", func(r chi.Router) {
		// POST /compat/bil24/json — Bil24 command gateway.
		// Accepts { "command": "...", "fid": "...", "token": "...", ... }
		// and dispatches to the appropriate domain adapter.
		// No JWT auth — the gateway uses fid/token credentials from the request body.
		r.Post("/json", s.handleBil24Command)
	})
}
