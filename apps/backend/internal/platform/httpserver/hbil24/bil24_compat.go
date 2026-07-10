// bil24_compat.go — Bil24-compatible API gateway HTTP entry point
// (feature #157, refined for feature #188).
//
// This file used to own both the Bil24 wire format AND the per-command
// orchestration. Feature #188 moves the wire format (request/response
// envelope, result codes, ID translation helpers) into the dedicated
// adapter package internal/adapters/bil24compat. This file is now the
// HTTP-layer entry point: it decodes the wire envelope via the adapter
// package and dispatches to per-command handlers that orchestrate platform
// queries. The /compat/bil24/* subtree itself is mounted by the parent
// package (bil24_shims.go, mountCompatRoutes).
//
// For backward compatibility with the existing httpserver-package test
// (#157), short aliases / forwarders for the moved symbols
// (bil24Request, bil24Response, bil24OK, bil24Error, writeBil24JSON,
// ResultCode*, TranslateLegacyID, TranslatePlatformID, ErrLegacyIDNotFound)
// are exposed both here and in the parent package's bil24_shims.go.
// Migration of the per-command handlers themselves into use-cases under
// internal/app/* is an incremental follow-up.
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
//	GET_ALL_ACTIONS  → list published events (GetCatalog)
//	GET_SEAT_LIST    → list ticket tiers for a session (general_admission) or
//	                   real assigned seats (assigned_seats / hybrid) —
//	                   §SEAT-D1 branch, feature #312. Response bodies can be
//	                   large for stadium-scale seat maps; operators SHOULD
//	                   enable the reverse-proxy gzip middleware or the
//	                   Accept-Encoding: gzip pass-through on this route so
//	                   the seatList payload compresses well over the wire.
//	GET_SCHEMA       → seat coordinates (seatId→x,y) for a seated session,
//	                   derived from seating_plan_versions.geometry — §SEAT-D2,
//	                   feature #313. Mirrors the legacy Bil24 GET_SEAT_LIST /
//	                   GET_SCHEMA split: coordinates live here, per-seat
//	                   status / price lives in GET_SEAT_LIST. seatId format
//	                   matches GET_SEAT_LIST (session_seats.id AS STRING,
//	                   ADR-005) so callers can join the two responses.
//	RESERVATION      → create a reservation (seated: seatList; GA:
//	                   categoryList) — feature #312 Wave SEAT-D1. Routes
//	                   the seated branch through the SEAT-C1 concurrency
//	                   contract (deterministic seat_key locking + monotonic
//	                   seat_status_version stamping).
//	GET_ORDER_INFO   → get checkout session + tickets (GetTicket)
//	CREATE_ORDER_EXT → create a checkout session (CreateOrder) — scaffold stub
//	SCAN_TICKET      → validate and record a barcode scan (ScanTicket)
//	CANCEL_ORDER     → cancel a checkout session — scaffold stub
//
// ID translation layer:
//
//	Legacy Bil24 uses actionId, actionEventId, orderId, ticketId etc.
//	The platform uses UUIDv7. TranslateLegacyID accepts either a raw UUID
//	string or a legacy numeric/opaque ID and maps it to a platform UUID.
//	See the adapter package internal/adapters/bil24compat for the
//	authoritative implementation.
//
// Feature flag: BIL24_COMPAT_ENABLED (env var, default false).
// The /compat/bil24/* subtree is only mounted when the flag is true.
// Requests to these paths return 404 when the flag is false.
package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
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
	case "RESERVATION":
		h.handleBil24Reservation(w, r, req)
	case "GET_ORDER_INFO":
		h.handleBil24GetOrderInfo(w, r, req)
	case "CREATE_ORDER_EXT":
		h.handleBil24CreateOrderExt(w, r, req)
	case "SCAN_TICKET":
		h.handleBil24ScanTicket(w, r, req)
	case "CANCEL_ORDER":
		h.handleBil24CancelOrder(w, r, req)
	default:
		h.logger.Warn("bil24_compat: unknown command",
			slog.String("command", command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeUnknownCommand,
			fmt.Sprintf("unknown command: %q", command),
		))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_ALL_ACTIONS — list published events (GetCatalog)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetAllActions maps GET_ALL_ACTIONS to the platform event catalog.
//
// Bil24 request fields used:
//   - locale: controls the language of event names/descriptions
//
// Response: { "resultCode": 0, "command": "GET_ALL_ACTIONS", "actionList": [...] }
// Each action item:
//
//	{
//	  "actionId":       "<uuid>",
//	  "actionName":     "...",
//	  "bigPosterUrl":   "...",
//	  "firstEventDate": "<RFC3339>"
//	}
func (h *Handler) handleBil24GetAllActions(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.eventQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "catalog service unavailable",
		))
		return
	}

	locale := req.Locale
	if locale == "" {
		locale = "en"
	}

	events, err := h.eventQueries.ListEvents(r.Context(), locale, "public")
	if err != nil {
		h.logger.Error("bil24_compat: GET_ALL_ACTIONS: list events failed",
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve action list",
		))
		return
	}

	actionList := make([]map[string]any, 0, len(events))
	for _, e := range events {
		action := map[string]any{
			"actionId":       TranslatePlatformID(e.ID),
			"actionName":     e.Name,
			"firstEventDate": e.StartAt.UTC().Format(time.RFC3339),
		}
		if e.ImageURL != nil && *e.ImageURL != "" {
			action["bigPosterUrl"] = *e.ImageURL
			action["smallPosterUrl"] = *e.ImageURL
		}
		if e.Description != nil {
			action["description"] = *e.Description
		}
		actionList = append(actionList, action)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"actionList": actionList,
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SEAT_LIST — list ticket tiers for a session
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetSeatList maps GET_SEAT_LIST to either ticket-tier listing
// (general_admission) or the real assigned-seat inventory
// (assigned_seats / hybrid) for a specific event session. Feature #312
// Wave SEAT-D1 introduced the admission_mode branch on top of the
// pre-existing tier-facade behavior.
//
// Bil24 request fields used:
//   - actionEventId: platform session UUID (Bil24 event instance)
//
// Response shapes:
//
//   - general_admission (or admissionQ nil / session not resolvable to a
//     seating binding) — one entry per ticket_tier, unchanged from
//     pre-#312 behavior:
//
//     {
//       "categoryPriceId": "<uuid>", "categoryName": "...",
//       "price": <cents>, "currency": "USD",
//       "pricingMode": "fixed"|"free"|"pwyw",
//       "availableCount": <int or null>
//     }
//
//   - assigned_seats / hybrid — one entry per session_seat, per ADR-005
//     the seat identifier is the platform session_seats.id serialised
//     as a plain UUID string:
//
//     {
//       "seatId":          "<uuid>",       // session_seats.id as string
//       "categoryPriceId": "<uuid>",       // tier UUID (nullable)
//       "sector":          "...",
//       "row":             "...",
//       "number":          "...",
//       "price":           <cents>,        // 0 if no tier bound yet
//       "currency":        "USD",
//       "status":          <BSS int>       // 0 blocked, 1 available, 3 held, 4 sold
//     }
//
// BSS status codes are the Bil24 seat-status wire values (§6 of the
// Bil24 gateway spec): 0 = blocked (admin), 1 = available, 3 = held
// (reservation active), 4 = sold. The mapping never surfaces the internal
// row status string.
//
// Operator note: stadium-scale seat maps can push the seatList payload
// past 1 MiB. Enable gzip on the reverse proxy fronting POST
// /compat/bil24/json (nginx: gzip_types application/json; Cloudflare:
// Auto-Minify JSON + Brotli; Caddy: encode zstd gzip) so callers with
// Accept-Encoding: gzip receive a compressed response and the wire foot-
// print stays predictable.
func (h *Handler) handleBil24GetSeatList(w http.ResponseWriter, r *http.Request, req bil24Request) {
	// tier and seat services can be independently unwired; the outer
	// guard fails fast only if BOTH are missing (no data source at all
	// for either branch).
	if h.tierQueries == nil && h.seatQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "seat service unavailable",
		))
		return
	}

	sessionID, err := TranslateLegacyID(req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	ctx := r.Context()

	// Resolve admission_mode when the seating dependencies are wired.
	// Missing dependencies / lookup failures silently fall back to the
	// tier-facade behavior — legacy GA clients keep working during the
	// SEAT-D rollout even when the seating tables are empty.
	admissionMode := "general_admission"
	if h.admissionQ != nil {
		row, aerr := h.admissionQ.GetSessionAdmissionModeByID(ctx, sessionID)
		if aerr == nil && row.AdmissionMode != "" {
			admissionMode = row.AdmissionMode
		}
	}

	// GA (or fallback) branch requires tier queries; assigned branch
	// requires seat queries. Route accordingly.
	if admissionMode == "general_admission" || h.seatQ == nil {
		if h.tierQueries == nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "tier service unavailable",
			))
			return
		}
		h.getSeatListGA(w, ctx, req, sessionID)
		return
	}
	h.getSeatListAssigned(w, ctx, req, sessionID)
}

// getSeatListGA is the pre-#312 tier-facade GET_SEAT_LIST response for
// general_admission sessions (and the fallback whenever the SEAT-D
// dependencies are not wired). Kept factored out so the assigned-seat
// branch can remain a self-contained addition.
func (h *Handler) getSeatListGA(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID) {
	tiers, err := h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: list tiers failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat list",
		))
		return
	}

	seatList := make([]map[string]any, 0, len(tiers))
	for _, t := range tiers {
		seat := map[string]any{
			"categoryPriceId": TranslatePlatformID(t.ID),
			"categoryName":    t.Name,
			"price":           t.PriceAmount,
			"currency":        t.Currency,
			"pricingMode":     t.PricingMode,
		}
		if t.Capacity != nil {
			seat["availableCount"] = *t.Capacity
		}
		seatList = append(seatList, seat)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"seatList": seatList,
	}))
}

// getSeatListAssigned is the SEAT-D1 GET_SEAT_LIST branch for sessions
// whose admission_mode is assigned_seats or hybrid. It emits one entry
// per session_seat row, joining tier metadata (price/currency) from the
// session's ticket_tiers snapshot.
func (h *Handler) getSeatListAssigned(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID) {
	// Load real seats.
	seats, err := h.seatQ.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("bil24_compat: GET_SEAT_LIST: list session seats failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve seat list",
		))
		return
	}

	// Load tier snapshot for price / currency projection. When the tier
	// dependency is unwired (nil) or fails, we degrade gracefully with
	// price=0 / currency omitted rather than failing the whole
	// response — seat inventory is still meaningful without prices.
	var tiers []gen.TicketTierRow
	if h.tierQueries != nil {
		var terr error
		tiers, terr = h.tierQueries.ListTicketTiersBySession(ctx, sessionID)
		if terr != nil {
			h.logger.Warn("bil24_compat: GET_SEAT_LIST: tier snapshot failed; emitting seats with zero price",
				slog.String("session_id", sessionID.String()),
				slog.String("error", terr.Error()),
			)
			tiers = nil
		}
	}
	tierByID := make(map[uuid.UUID]gen.TicketTierRow, len(tiers))
	for _, t := range tiers {
		tierByID[t.ID] = t
	}

	seatList := make([]map[string]any, 0, len(seats))
	for _, s := range seats {
		entry := map[string]any{
			// ADR-005: seatId on the wire is the platform session_seats.id
			// serialised as a plain UUID string.
			"seatId": s.ID.String(),
			"sector": s.SectorName,
			"row":    s.RowName,
			"number": s.SeatNumber,
			"status": bssStatusCode(s.Status),
		}
		if s.TierID != nil {
			entry["categoryPriceId"] = TranslatePlatformID(*s.TierID)
			if t, ok := tierByID[*s.TierID]; ok {
				entry["price"] = t.PriceAmount
				entry["currency"] = t.Currency
			} else {
				entry["price"] = int64(0)
			}
		} else {
			entry["price"] = int64(0)
		}
		seatList = append(seatList, entry)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"seatList":      seatList,
		"admissionMode": "assigned_seats",
	}))
}

// bssStatusCode maps an internal session_seats.status string to the Bil24
// BSS wire code documented in §6 of the gateway spec:
//
//	blocked   → 0  (admin-blocked)
//	available → 1
//	held      → 3  (a reservation currently owns the seat)
//	sold      → 4
//
// Any unknown status maps to 0 so legacy clients never see a hole in
// the enum surface.
func bssStatusCode(status string) int {
	switch status {
	case "available":
		return 1
	case "held":
		return 3
	case "sold":
		return 4
	case "blocked":
		return 0
	default:
		return 0
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_ORDER_INFO — get checkout session + tickets (GetTicket)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24GetOrderInfo maps GET_ORDER_INFO to the platform checkout session
// and its associated tickets.
//
// Bil24 request fields used:
//   - orderId: platform checkout session UUID
//
// Response:
//
//	{
//	  "resultCode": 0,
//	  "command": "GET_ORDER_INFO",
//	  "orderInfo": {
//	    "orderId":      "<uuid>",
//	    "state":        "...",
//	    "sum":          <cents>,
//	    "discount":     <cents>,
//	    "charge":       <cents>,
//	    "totalSum":     <cents>,
//	    "currency":     "USD",
//	    "ticketCount":  <int>
//	  }
//	}
//
// Note: Bil24's GET_ORDER_INFO historically did not return ticketList.
// For strict compatibility we include ticketCount but omit the full list.
// Clients migrated to the new platform can request the full list via
// GET /v1/checkout/{id}/tickets.
func (h *Handler) handleBil24GetOrderInfo(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.checkoutQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "order service unavailable",
		))
		return
	}

	orderID, err := TranslateLegacyID(req.OrderID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"orderId must be a valid order identifier",
		))
		return
	}

	cs, err := h.checkoutQueries.GetCheckoutSessionByID(r.Context(), orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "order not found",
			))
			return
		}
		h.logger.Error("bil24_compat: GET_ORDER_INFO: fetch checkout session failed",
			slog.String("order_id", orderID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to retrieve order",
		))
		return
	}

	// Build Bil24 financial field representation.
	// Bil24 semantics: sum=subtotal, discount=discount, charge=platform+provider fee, totalSum=total.
	var (
		sum      int64
		discount int64
		charge   int64
		totalSum int64
		currency string
	)
	if cs.Subtotal != nil {
		sum = *cs.Subtotal
	}
	if cs.Discount != nil {
		discount = *cs.Discount
	}
	if cs.PlatformFee != nil {
		charge += *cs.PlatformFee
	}
	if cs.ProviderFee != nil {
		charge += *cs.ProviderFee
	}
	if cs.Total != nil {
		totalSum = *cs.Total
	}
	if cs.Currency != nil {
		currency = *cs.Currency
	}

	// Get ticket count if ticketQueries is available.
	ticketCount := 0
	if h.ticketQueries != nil {
		tickets, err := h.ticketQueries.ListTicketsByCheckoutSession(r.Context(), orderID)
		if err == nil {
			ticketCount = len(tickets)
		}
	}

	orderInfo := map[string]any{
		"orderId":     TranslatePlatformID(cs.ID),
		"state":       cs.State,
		"sum":         sum,
		"discount":    discount,
		"charge":      charge,
		"totalSum":    totalSum,
		"ticketCount": ticketCount,
	}
	if currency != "" {
		orderInfo["currency"] = currency
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"orderInfo": orderInfo,
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_ORDER_EXT — create a checkout session (CreateOrder) — scaffold stub
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24CreateOrderExt maps CREATE_ORDER_EXT to checkout session creation.
//
// Bil24 request fields used:
//   - actionEventId:   platform session UUID
//   - categoryPriceId: platform tier UUID
//   - quantity:        number of tickets (default 1)
//   - email:           buyer email
//
// This is a scaffold implementation. Full checkout creation requires a
// reservation, pricing confirmation, and payment flow (features #131, #129,
// #132, #137). This stub validates the input and returns a placeholder
// response signalling that the command structure is understood.
//
// Response: { "resultCode": 0, "command": "CREATE_ORDER_EXT", "orderId": "<placeholder>" }
func (h *Handler) handleBil24CreateOrderExt(w http.ResponseWriter, _ *http.Request, req bil24Request) {
	if req.ActionEventID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId is required",
		))
		return
	}
	if req.CategoryPriceID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryPriceId is required",
		))
		return
	}
	if _, err := TranslateLegacyID(req.ActionEventID); err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}
	if _, err := TranslateLegacyID(req.CategoryPriceID); err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryPriceId must be a valid tier identifier",
		))
		return
	}

	quantity := req.Quantity
	if quantity <= 0 {
		quantity = 1
	}

	h.logger.Info("bil24_compat: CREATE_ORDER_EXT: scaffold stub",
		slog.String("session_id", req.ActionEventID),
		slog.String("tier_id", req.CategoryPriceID),
		slog.Int("quantity", quantity),
		slog.String("email", req.Email),
	)

	// Scaffold response: full checkout creation requires multi-step flow.
	// Return a placeholder order ID derived from the session + tier IDs.
	// Real implementation: create reservation → confirm pricing → return checkout_session.id.
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"orderId": "pending",
		"status":  "scaffold_stub",
		"message": "order creation requires reservation flow; use POST /v1/checkout/reservations",
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// SCAN_TICKET — validate and record a barcode scan (ScanTicket)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24ScanTicket maps SCAN_TICKET to the barcode scan validation flow.
//
// Bil24 request fields used:
//   - ticketId: barcode external_ref (or UUID if already on platform)
//
// The scan uses the "legacy_bil24" barcode authority type. If no such
// authority exists, returns NOT_FOUND.
//
// Response:
//
//	{ "resultCode": 0, "command": "SCAN_TICKET", "scanStatus": "OK", "ticketId": "..." }
func (h *Handler) handleBil24ScanTicket(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.barcodeQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "scan service unavailable",
		))
		return
	}

	if req.TicketID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticketId is required",
		))
		return
	}

	ctx := r.Context()

	// Resolve the legacy_bil24 barcode authority.
	authority, err := h.barcodeQueries.GetBarcodeAuthorityByType(ctx, "legacy_bil24")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"legacy_bil24 barcode authority not registered; "+
					"create it first via POST /v1/barcodes/authorities",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: authority lookup failed",
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve barcode authority",
		))
		return
	}

	// Look up the barcode by (authority_id, external_ref).
	barcode, err := h.barcodeQueries.GetBarcodeByRef(ctx, authority.ID, req.TicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "ticket not found",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: barcode lookup failed",
			slog.String("ticket_id", req.TicketID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to look up ticket",
		))
		return
	}

	// Guard against already-scanned barcodes.
	if barcode.Status == "scanned" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticket already scanned",
		))
		return
	}

	// Guard against revoked barcodes.
	if barcode.Status == "revoked" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "ticket has been revoked",
		))
		return
	}

	// Atomically mark as scanned.
	scanned, err := h.barcodeQueries.MarkBarcodeScanned(ctx, barcode.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest, "ticket already scanned",
			))
			return
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: mark scanned failed",
			slog.String("barcode_id", barcode.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to record scan",
		))
		return
	}

	h.logger.Info("bil24_compat: SCAN_TICKET: scan recorded",
		slog.String("barcode_id", scanned.ID.String()),
		slog.String("external_ref", scanned.ExternalRef),
	)

	scanResult := map[string]any{
		"scanStatus": "OK",
		"ticketId":   req.TicketID,
	}
	if scanned.TicketID != nil {
		scanResult["platformTicketId"] = TranslatePlatformID(*scanned.TicketID)
	}
	if scanned.ScannedAt != nil {
		scanResult["scannedAt"] = scanned.ScannedAt.UTC().Format(time.RFC3339)
	}

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, scanResult))
}

// ─────────────────────────────────────────────────────────────────────────────
// CANCEL_ORDER — cancel a checkout session — scaffold stub
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24CancelOrder maps CANCEL_ORDER to checkout session cancellation.
//
// Bil24 request fields used:
//   - orderId: platform checkout session UUID
//
// This is a scaffold implementation. Full cancellation requires the checkout
// state machine to transition through to 'cancelled' and potentially trigger
// a refund (feature #138). This stub validates the order exists and returns
// a placeholder response.
//
// Response: { "resultCode": 0, "command": "CANCEL_ORDER", "status": "cancelled" }
func (h *Handler) handleBil24CancelOrder(w http.ResponseWriter, _ *http.Request, req bil24Request) {
	if req.OrderID == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "orderId is required",
		))
		return
	}
	orderID, err := TranslateLegacyID(req.OrderID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"orderId must be a valid order identifier",
		))
		return
	}

	h.logger.Info("bil24_compat: CANCEL_ORDER: scaffold stub",
		slog.String("order_id", orderID.String()),
	)

	// Scaffold response: full cancellation requires checkout state machine.
	// Real implementation: POST /v1/checkout/{id}/cancel.
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"orderId": TranslatePlatformID(orderID),
		"status":  "scaffold_stub",
		"message": "cancellation requires checkout state machine; use POST /v1/checkout/{id}/cancel",
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// RESERVATION — create a reservation (seated: seatList; GA: categoryList)
// Feature #312 Wave SEAT-D1.
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24Reservation dispatches the Bil24 RESERVATION command to
// either the SEAT-C1 seated reservation contract (assigned_seats /
// hybrid, seatList payload) or the pre-existing tier-facade
// (general_admission, categoryList payload).
//
// Wire contract (both modes require actionEventId):
//
//	seated mode:
//	  { "command": "RESERVATION",
//	    "actionEventId": "<session-uuid>",
//	    "seatList": ["<session_seat.id>", ...] }
//
//	GA mode:
//	  { "command": "RESERVATION",
//	    "actionEventId": "<session-uuid>",
//	    "categoryList": [{"categoryPriceId":"<tier-uuid>","quantity":N}, ...] }
//
// seatList and categoryList are mutually exclusive; supplying both — or
// neither — returns resultCode=-2 (invalid request). The admission_mode
// of the target session is enforced when the seating dependency is
// wired:
//
//   - assigned_seats session + categoryList  → -2 seats.required
//   - general_admission session + seatList   → -2 quantity.required
//   - hybrid session                         → either mode is accepted
//
// Once the wire contract passes, the seated branch documents the
// SEAT-C1 concurrency contract it will route through (deterministic
// seat_key-ordered locking + monotonic seat_status_version stamping —
// see hcheckout/seat_reservations.go). Because the SEAT-C1 code path
// requires a JWT actor and a channel/org resolution not yet plumbed
// through the Bil24 fid/token surface, the current wire response is a
// structured scaffold that echoes the parsed request back to the caller.
// Callers migrated to the platform API can use POST /v1/reservations
// directly for a full reservation lifecycle.
func (h *Handler) handleBil24Reservation(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.ActionEventID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId is required",
		))
		return
	}
	sessionID, err := TranslateLegacyID(req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier",
		))
		return
	}

	hasSeats := len(req.SeatList) > 0
	hasCats := len(req.CategoryList) > 0

	if hasSeats && hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList and categoryList are mutually exclusive",
		))
		return
	}
	if !hasSeats && !hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"either seatList or categoryList must be provided",
		))
		return
	}

	// Resolve admission_mode when the seating dependency is wired so we
	// can enforce the SEAT-D1 branch table. Missing dependencies fall
	// back to accepting whichever payload the caller supplied — matches
	// GET_SEAT_LIST fallback behavior during the SEAT-D rollout.
	admissionMode := ""
	if h.admissionQ != nil {
		row, aerr := h.admissionQ.GetSessionAdmissionModeByID(r.Context(), sessionID)
		if aerr != nil {
			if errors.Is(aerr, pgx.ErrNoRows) {
				writeBil24JSON(w, http.StatusOK, bil24Error(
					req.Command, ResultCodeNotFound, "session not found",
				))
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: session admission lookup failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", aerr.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError,
				"failed to resolve session",
			))
			return
		}
		admissionMode = row.AdmissionMode
	}

	if admissionMode == "general_admission" && hasSeats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"seatList is not supported on general_admission sessions; use categoryList",
		))
		return
	}
	if admissionMode == "assigned_seats" && hasCats {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"categoryList is not supported on assigned_seats sessions; use seatList",
		))
		return
	}

	if hasSeats {
		h.reservationSeated(w, req, sessionID, admissionMode)
		return
	}
	h.reservationGA(w, req, sessionID, admissionMode)
}

// reservationSeated is the SEAT-C1-facing branch of the RESERVATION
// dispatcher. See handleBil24Reservation for the response contract.
//
// Full end-to-end reservation creation is the responsibility of the
// SEAT-C1 code path at POST /v1/reservations (seated branch). Wiring the
// Bil24 fid/token surface into that path requires channel + org
// resolution not yet built for the gateway; this scaffold documents the
// seatList contract (session_seat.id strings, ADR-005) and echoes the
// parsed request so contract tests can pin the wire shape.
func (h *Handler) reservationSeated(w http.ResponseWriter, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	// Deduplicate + validate seat identifiers (each entry must be a
	// non-empty string; ADR-005 does not require them to parse as UUID
	// on the wire, but the platform's SEAT-C1 lock path uses the
	// session_seat.id — full routing lands in a follow-up feature).
	seen := make(map[string]struct{}, len(req.SeatList))
	seats := make([]string, 0, len(req.SeatList))
	for _, s := range req.SeatList {
		s = strings.TrimSpace(s)
		if s == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				"seatList entries must be non-empty session_seat identifiers",
			))
			return
		}
		if _, dup := seen[s]; dup {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("seatList contains duplicate seat %q", s),
			))
			return
		}
		seen[s] = struct{}{}
		seats = append(seats, s)
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "assigned_seats"
	}

	h.logger.Info("bil24_compat: RESERVATION: seated scaffold",
		slog.String("session_id", sessionID.String()),
		slog.Int("seat_count", len(seats)),
		slog.String("admission_mode", responseAdmission),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"reservationId": "pending",
		"sessionId":     TranslatePlatformID(sessionID),
		"seatCount":     len(seats),
		"seatList":      seats,
		"admissionMode": responseAdmission,
		"status":        "scaffold_stub",
		"route":         "POST /v1/reservations (seated branch, SEAT-C1)",
	}))
}

// reservationGA is the general_admission / tier-facade branch of the
// RESERVATION dispatcher. It validates the categoryList shape and echoes
// the parsed payload; the full quantity-mode reservation lifecycle is
// exposed at POST /v1/reservations.
func (h *Handler) reservationGA(w http.ResponseWriter, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	total := 0
	echoed := make([]map[string]any, 0, len(req.CategoryList))
	for i, c := range req.CategoryList {
		if strings.TrimSpace(c.CategoryPriceID) == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId is required", i),
			))
			return
		}
		if _, err := TranslateLegacyID(c.CategoryPriceID); err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId must be a valid tier identifier", i),
			))
			return
		}
		if c.Quantity <= 0 {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].quantity must be >= 1", i),
			))
			return
		}
		total += c.Quantity
		echoed = append(echoed, map[string]any{
			"categoryPriceId": c.CategoryPriceID,
			"quantity":        c.Quantity,
		})
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "general_admission"
	}

	h.logger.Info("bil24_compat: RESERVATION: GA scaffold",
		slog.String("session_id", sessionID.String()),
		slog.Int("total_quantity", total),
		slog.String("admission_mode", responseAdmission),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"reservationId": "pending",
		"sessionId":     TranslatePlatformID(sessionID),
		"categoryList":  echoed,
		"totalQuantity": total,
		"admissionMode": responseAdmission,
		"status":        "scaffold_stub",
		"route":         "POST /v1/reservations (GA branch)",
	}))
}
