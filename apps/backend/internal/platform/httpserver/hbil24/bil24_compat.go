// bil24_compat.go — Bil24-compatible API gateway HTTP entry point
// (feature #157, refined for feature #188, split per-command by
// feature #476).
//
// This file used to own both the Bil24 wire format AND every per-command
// orchestration function. The wire format now lives in the dedicated
// adapter package internal/adapters/bil24compat (feature #188); the
// per-command handler bodies live in cmd_catalog.go (GET_ALL_ACTIONS,
// GET_SEAT_LIST, GET_SCHEMA is in schema.go), cmd_order.go
// (GET_ORDER_INFO, CREATE_ORDER_EXT, CANCEL_ORDER), cmd_tickets.go
// (SCAN_TICKET) and cmd_cart.go (RESERVATION, UN_RESERVE + shared
// pricing/error helpers). This file keeps only:
//
//   - the package-level constant re-exports (ResultCode*),
//   - the request/response envelope aliases + tiny forwarders
//     (bil24Request / bil24Response / bil24OK / bil24Error /
//     writeBil24JSON),
//   - the legacy ID-translation forwarders retained for #157/#188
//     sentinels (TranslateLegacyID / TranslatePlatformID +
//     ErrLegacyIDNotFound),
//   - the top-level dispatcher HandleBil24Command.
//
// Everything else moved out; the /compat/bil24/* subtree itself is still
// mounted by the parent package (bil24_shims.go, mountCompatRoutes).
//
// For backward compatibility with the existing httpserver-package test
// (#157), short aliases / forwarders for the moved symbols are exposed
// both here and in the parent package's bil24_shims.go. Migration of the
// per-command handlers themselves into use-cases under internal/app/* is
// an incremental follow-up.
//
// Wire compatibility:
//
//	The old WordPress / widget / partner client can POST the same JSON shape:
//	  { "command": "...", "fid": "...", "token": "...", "locale": "...", ... }
//	and receive Bil24-style responses:
//	  { "resultCode": 0, "description": "OK", "command": "..." }
//
// Supported commands (7 most-used first):
//
//	GET_ALL_ACTIONS  → cmd_catalog.go
//	GET_SEAT_LIST    → cmd_catalog.go (GA + per-unit branches)
//	GET_SCHEMA       → schema.go
//	RESERVATION      → cmd_cart.go   (seated + GA branches)
//	UN_RESERVE       → cmd_cart.go
//	GET_ORDER_INFO   → cmd_order.go
//	CREATE_ORDER_EXT → cmd_order.go  (NOT_IMPLEMENTED scaffold)
//	SCAN_TICKET      → cmd_tickets.go
//	CANCEL_ORDER     → cmd_order.go  (NOT_IMPLEMENTED scaffold)
//	ADD_PROMO_CODES  → dispatched inline below (NOT_IMPLEMENTED scaffold)
//
// ID translation layer:
//
//	Legacy Bil24 uses actionId, actionEventId, orderId, ticketId etc.
//	The platform uses UUIDv7. TranslateLegacyID accepts either a raw UUID
//	string or a legacy numeric/opaque ID and maps it to a platform UUID.
//	See the adapter package internal/adapters/bil24compat for the
//	authoritative implementation. The int64-first wave-1 entry
//	(ResolveLegacyIntID) also lives in the adapter package.
//
// Feature flag: BIL24_COMPAT_ENABLED (env var, default false).
// The /compat/bil24/* subtree is only mounted when the flag is true.
// Requests to these paths return 404 when the flag is false.
package hbil24

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

// ─────────────────────────────────────────────────────────────────────────────
// Result codes (re-exported from the adapter package)
// ─────────────────────────────────────────────────────────────────────────────

// Bil24 wire result codes — re-exported from internal/adapters/bil24compat so
// existing in-package references and the #157 test suite continue to compile
// without churn. The adapter package is the source of truth.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = bil24compat.ResultCodeOK
	// ResultCodeSessionExpired signals expired gateway session (feature #477).
	ResultCodeSessionExpired = bil24compat.ResultCodeSessionExpired
	// ResultCodeUserVisible signals a user-visible business failure whose
	// description is shown to the buyer verbatim (feature #477).
	ResultCodeUserVisible = bil24compat.ResultCodeUserVisible
	// ResultCodeTransient signals a transient/retry-able failure — DB/pool
	// errors, deadlocks, timeouts (Bil24 wire: -1, feature #477).
	ResultCodeTransient = bil24compat.ResultCodeTransient
	// ResultCodeUnknownCommand is a deprecated alias for
	// ResultCodeInvalidRequest; its value moved from -1 to -2 in feature
	// #477 (unknown command names are now -2, per spec section 6).
	//
	// Deprecated: use ResultCodeInvalidRequest.
	ResultCodeUnknownCommand = bil24compat.ResultCodeUnknownCommand //nolint:staticcheck // intentional re-export of deprecated alias
	// ResultCodeInvalidRequest is returned when the request is malformed:
	// missing/malformed field, JSON parse failure, unknown command name
	// (Bil24 wire: -2).
	ResultCodeInvalidRequest = bil24compat.ResultCodeInvalidRequest
	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform (Bil24 wire: -3).
	ResultCodeNotFound = bil24compat.ResultCodeNotFound
	// ResultCodeUnauthorized is returned when the fid/token credential pair
	// is invalid or missing. Platform extension (feature #374).
	ResultCodeUnauthorized = bil24compat.ResultCodeUnauthorized
	// ResultCodeNotImplemented is returned for recognized commands that are
	// not yet wired to platform functionality (feature #374).
	ResultCodeNotImplemented = bil24compat.ResultCodeNotImplemented
	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99). Reserved for panic-recovery.
	ResultCodeInternalError = bil24compat.ResultCodeInternalError
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / response envelope (aliased from the adapter package)
// ─────────────────────────────────────────────────────────────────────────────

// bil24Request is the top-level request envelope for POST /compat/bil24/json.
// Aliased to the adapter package so the wire format has exactly one
// definition.
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

// ─────────────────────────────────────────────────────────────────────────────
// ID translation layer (re-exported from the adapter package)
// ─────────────────────────────────────────────────────────────────────────────

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

// ─────────────────────────────────────────────────────────────────────────────
// Main gateway handler
// ─────────────────────────────────────────────────────────────────────────────

// HandleBil24Command is the single-entry-point for POST /compat/bil24/json.
//
// It parses the command field and dispatches to the appropriate domain
// adapter. All errors are returned in the Bil24 envelope format so that
// legacy clients receive machine-readable error codes without needing to
// understand HTTP status codes beyond 200.
//
// HTTP status is always 200 for protocol errors (unknown command, bad input)
// so that legacy clients that hard-code 200 checks remain compatible.
// 500 is reserved for genuine server-side failures.
func (h *Handler) HandleBil24Command(w http.ResponseWriter, r *http.Request) {
	var req bil24Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			"", ResultCodeInvalidRequest, "request body must be valid JSON",
		))
		return
	}

	command := strings.ToUpper(strings.TrimSpace(req.Command))
	if command == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			"", ResultCodeInvalidRequest, "command field is required",
		))
		return
	}

	// Recover from panics caused by database calls on a nil pool (e.g. in
	// test environments where gen.New(nil) is passed). This ensures legacy
	// Bil24 clients always receive a machine-readable Bil24 envelope error
	// (resultCode=-99) instead of an HTTP 500 from the middleware recoverer.
	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("bil24_compat: recovered panic in command handler",
				slog.String("command", command),
				slog.Any("panic", rec),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				command, ResultCodeInternalError, "service temporarily unavailable",
			))
		}
	}()

	h.logger.Info("bil24_compat: command received",
		slog.String("command", command),
		slog.String("fid", req.FID),
		slog.String("locale", req.Locale),
	)

	switch command {
	case "GET_ALL_ACTIONS":
		h.handleBil24GetAllActions(w, r, req)
	case "GET_SEAT_LIST":
		h.handleBil24GetSeatList(w, r, req)
	case "GET_SCHEMA":
		h.handleBil24GetSchema(w, r, req)
	case "RESERVATION":
		h.handleBil24Reservation(w, r, req)
	case "UN_RESERVE":
		h.handleBil24UnReserve(w, r, req)
	case "GET_ORDER_INFO":
		h.handleBil24GetOrderInfo(w, r, req)
	case "CREATE_ORDER_EXT":
		h.handleBil24CreateOrderExt(w, r, req)
	case "SCAN_TICKET":
		h.handleBil24ScanTicket(w, r, req)
	case "CANCEL_ORDER":
		h.handleBil24CancelOrder(w, r, req)
	case "ADD_PROMO_CODES":
		// ADD_PROMO_CODES is recognized but explicitly not implemented in
		// this gateway version. Returning resultCode=-5 (NOT_IMPLEMENTED)
		// rather than -1 (unknown command) so legacy clients that inspect
		// the description can distinguish "command unknown" from "command
		// exists but not available here" (feature #374).
		h.logger.Warn("bil24_compat: ADD_PROMO_CODES is not implemented in this gateway version",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeNotImplemented,
			"ADD_PROMO_CODES is not implemented; apply promo codes via POST /v1/checkout/{id}/promos",
		))
	default:
		// Feature #477 / spec section 6: unknown command name is a
		// malformed-request condition and maps to ResultCodeInvalidRequest
		// (-2), not the pre-#477 ResultCodeUnknownCommand (which used to
		// occupy -1; that slot is now ResultCodeTransient for DB/pool
		// failures).
		h.logger.Warn("bil24_compat: unknown command",
			slog.String("command", command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeInvalidRequest,
			fmt.Sprintf("unknown command: %q", command),
		))
	}
}
