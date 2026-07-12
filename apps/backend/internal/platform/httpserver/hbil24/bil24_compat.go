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
//	RESERVATION      → create a REAL hold (seated: seatList; GA:
//	                   categoryList) — feature #312 Wave SEAT-D1, wired to
//	                   the hcheckout hold API. The seated branch routes
//	                   through the SEAT-C1 concurrency contract
//	                   (deterministic seat_key locking + monotonic
//	                   seat_status_version stamping); the GA branch takes
//	                   per-tier capacity and records reservation_ga_items
//	                   lines. Responds with the real reservationId,
//	                   cartTimeout (seconds until expiry) and the platform-
//	                   computed sum/discount/charge/totalSum financial
//	                   fields (legacy contract §5.1; totalSum = sum -
//	                   discount + charge).
//	UN_RESERVE       → release a hold previously created by RESERVATION
//	                   (legacy contract §5.1 cancel semantics): seats flip
//	                   back to 'available', reserved capacity is returned,
//	                   and the reservation transitions to 'cancelled'.
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
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
//     "categoryPriceId": "<uuid>", "categoryName": "...",
//     "price": <cents>, "currency": "USD",
//     "pricingMode": "fixed"|"free"|"pwyw",
//     "availableCount": <int or null>
//     }
//
//   - assigned_seats / hybrid — one entry per session_seat, per ADR-005
//     the seat identifier is the platform session_seats.id serialised
//     as a plain UUID string:
//
//     {
//     "seatId":          "<uuid>",       // session_seats.id as string
//     "categoryPriceId": "<uuid>",       // tier UUID (nullable)
//     "sector":          "...",
//     "row":             "...",
//     "number":          "...",
//     "price":           <cents>,        // 0 if no tier bound yet
//     "currency":        "USD",
//     "status":          <BSS int>       // 0 blocked, 1 available, 3 held, 4 sold
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
// Once the wire contract passes, both branches create a REAL hold via
// the hcheckout hold API (injected as callbacks by bil24_shims.go — the
// gateway never imports package httpserver). The tenant context is
// resolved from the request itself: the owning organization via the
// session (sessions → events join) and the sales channel via the fid
// credential (fid → sales_channel per the gateway ID mapping; until the
// compatibility_id_map lands, fid must be the platform sales_channel
// UUID). The response carries the real reservationId, cartTimeout
// (whole seconds until the hold expires — legacy contract §5.1) and the
// platform-computed financial fields (sum / discount / charge /
// totalSum; totalSum = sum - discount + charge, guardrail #15).
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
		h.reservationSeated(w, r.Context(), req, sessionID, admissionMode)
		return
	}
	h.reservationGA(w, r.Context(), req, sessionID, admissionMode)
}

// reservationContext resolves the tenant context of a RESERVATION request:
// the owning organization via the session (sessions → events join) and the
// sales channel addressed by the fid credential (fid → sales_channel per
// the gateway ID mapping; until the compatibility_id_map lands, fid must be
// the platform sales_channel UUID). The hold TTL honours the channel's
// reservation_ttl_override and falls back to the platform default.
//
// On failure the Bil24 error envelope has already been written and
// ok=false is returned.
func (h *Handler) reservationContext(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	sessionID uuid.UUID,
) (orgID, channelID uuid.UUID, expiresAt time.Time, ok bool) {
	orgCtx, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "session not found",
			))
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
		h.logger.Error("bil24_compat: RESERVATION: session org lookup failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve session",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}

	if strings.TrimSpace(req.FID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"fid is required for RESERVATION (sales channel credential)",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	chID, err := TranslateLegacyID(req.FID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"fid must be a valid sales channel identifier",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	channel, err := h.resDeps.CtxQ.GetSalesChannelByID(ctx, chID, orgCtx.OrgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"sales channel not found for fid in this session's organization",
			))
			return uuid.Nil, uuid.Nil, time.Time{}, false
		}
		h.logger.Error("bil24_compat: RESERVATION: sales channel lookup failed",
			slog.String("fid", req.FID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve sales channel",
		))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}

	ttl := hcheckout.DefaultReservationTTL
	if channel.ReservationTTLOverride != nil && *channel.ReservationTTLOverride > 0 {
		ttl = time.Duration(*channel.ReservationTTLOverride) * time.Second
	}
	return orgCtx.OrgID, channel.ID, time.Now().UTC().Add(ttl), true
}

// bil24FinancialFields projects a platform pricing breakdown onto the
// legacy Bil24 financial fields (08_architecture/01_api_compatibility_
// gateway_ru.md): sum = subtotal, discount = discount, charge = service
// charge, and the invariant totalSum = sum - discount + charge is
// preserved by deriving charge from the pipeline total.
func bil24FinancialFields(bd hcheckout.PricingBreakdown) map[string]any {
	charge := bd.Total - (bd.Subtotal - bd.Discount)
	fields := map[string]any{
		"sum":      bd.Subtotal,
		"discount": bd.Discount,
		"charge":   charge,
		"totalSum": bd.Total,
	}
	if bd.Currency != "" {
		fields["currency"] = bd.Currency
	}
	return fields
}

// cartTimeoutSeconds converts an absolute hold deadline into the legacy
// cartTimeout wire field (whole seconds remaining, clamped at zero).
func cartTimeoutSeconds(expiresAt time.Time) int64 {
	secs := int64(time.Until(expiresAt).Seconds())
	if secs < 0 {
		secs = 0
	}
	return secs
}

// writeHoldError translates the typed errors of the hcheckout hold API into
// Bil24 envelopes. Seat conflicts and over-capacity carry structured detail
// alongside the description so migrated clients can highlight the exact
// seats / zones.
func (h *Handler) writeHoldError(w http.ResponseWriter, command string, err error) {
	var conflicts *hcheckout.SeatConflictsError
	var capErr *hcheckout.CapacityError
	switch {
	case errors.Is(err, hcheckout.ErrHoldSessionNotFound):
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeNotFound, "session not found"))
	case errors.Is(err, hcheckout.ErrHoldSeatsNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeInvalidRequest,
			"seatList is not supported on general_admission sessions; use categoryList",
		))
	case errors.Is(err, hcheckout.ErrHoldQuantityNotSupported):
		writeBil24JSON(w, http.StatusOK, bil24Error(
			command, ResultCodeInvalidRequest,
			"categoryList is not supported on assigned_seats sessions; use seatList",
		))
	case errors.Is(err, hcheckout.ErrHoldInvalidInput):
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeInvalidRequest, "invalid reservation payload"))
	case errors.As(err, &conflicts):
		resp := bil24Error(command, ResultCodeInvalidRequest, "one or more requested seats are not available")
		resp.Data = map[string]any{"conflicts": conflicts.Conflicts}
		writeBil24JSON(w, http.StatusOK, resp)
	case errors.As(err, &capErr):
		resp := bil24Error(command, ResultCodeInvalidRequest, "insufficient capacity for this reservation")
		detail := map[string]any{"requested": capErr.Requested}
		if capErr.TierID != nil {
			detail["categoryPriceId"] = TranslatePlatformID(*capErr.TierID)
		}
		resp.Data = map[string]any{"capacity": detail}
		writeBil24JSON(w, http.StatusOK, resp)
	default:
		h.logger.Error("bil24_compat: RESERVATION: hold failed",
			slog.String("command", command),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(command, ResultCodeInternalError, "failed to create reservation"))
	}
}

// reservationSeated is the seated branch of the RESERVATION dispatcher —
// feature #312 second half. It translates the ADR-005 seatList entries
// (session_seats.id AS STRING) into canonical seat_keys, creates a REAL
// hold through the injected hcheckout seated-reservation callback (SEAT-C1
// concurrency contract), prices the held seats through the platform
// pipeline, and responds with the legacy contract fields:
//
//	{
//	  "resultCode": 0, "command": "RESERVATION",
//	  "reservationId":  "<uuid>",                     // real id (string)
//	  "sessionId":      "<uuid>",
//	  "seatList":       ["<session_seat.id>", ...],   // held seats
//	  "seatCount":      N,
//	  "admissionMode":  "assigned_seats" | "hybrid",
//	  "cartTimeout":    <seconds until expiry>,
//	  "sum": <subtotal>, "discount": 0, "charge": <fees>,
//	  "totalSum": <sum - discount + charge>, "currency": "..."
//	}
func (h *Handler) reservationSeated(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	// Deduplicate + validate seat identifiers. Per ADR-005 each entry is
	// the platform session_seats.id serialised as a plain UUID string.
	seen := make(map[string]struct{}, len(req.SeatList))
	seatIDs := make([]uuid.UUID, 0, len(req.SeatList))
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
		id, err := uuid.Parse(s)
		if err != nil {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("seatList entry %q is not a valid session_seat identifier (ADR-005)", s),
			))
			return
		}
		seatIDs = append(seatIDs, id)
	}

	// Self-gate: the real hold path needs the reservation wiring plus the
	// seat id → seat_key translation surface.
	if h.resDeps.SeatedReserve == nil || h.resDeps.CtxQ == nil || h.seatQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	orgID, channelID, expiresAt, ok := h.reservationContext(ctx, w, req, sessionID)
	if !ok {
		return
	}

	// Translate seat ids → seat_keys (the SEAT-C1 lock path orders and
	// locks by seat_key).
	seatKeys := make([]string, 0, len(seatIDs))
	for _, id := range seatIDs {
		seat, err := h.seatQ.GetSessionSeatByID(ctx, id, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				resp := bil24Error(req.Command, ResultCodeNotFound, "seat not found in this session")
				resp.Data = map[string]any{"seatId": id.String()}
				writeBil24JSON(w, http.StatusOK, resp)
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: seat lookup failed",
				slog.String("seat_id", id.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to resolve seat",
			))
			return
		}
		seatKeys = append(seatKeys, seat.SeatKey)
	}

	result, err := h.resDeps.SeatedReserve(ctx, hcheckout.SeatedHoldInput{
		OrgID:     orgID,
		ChannelID: channelID,
		SessionID: sessionID,
		SeatKeys:  seatKeys,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		h.writeHoldError(w, req.Command, err)
		return
	}

	// Price the held seats through the platform pipeline (guardrail #15).
	// Tier prices come from the session's tier snapshot; seats without a
	// bound tier price at 0. A missing tier snapshot degrades to zero
	// prices rather than failing the hold (mirrors GET_SEAT_LIST).
	tierPrice := make(map[string]int64)
	currency := ""
	if h.resDeps.TierQ != nil {
		if tiers, terr := h.resDeps.TierQ.ListTicketTiersBySession(ctx, sessionID); terr == nil {
			for _, t := range tiers {
				tierPrice[t.ID.String()] = t.PriceAmount
				if currency == "" {
					currency = t.Currency
				}
			}
		} else {
			h.logger.Warn("bil24_compat: RESERVATION: tier snapshot failed; pricing seats at zero",
				slog.String("session_id", sessionID.String()),
				slog.String("error", terr.Error()),
			)
		}
	}
	bd := hcheckout.ComputePricingLines(
		hcheckout.BuildSeatedPricingLines(result.Seats, tierPrice),
		0, currency, h.resDeps.PricingRules,
	)

	heldSeatIDs := make([]string, 0, len(result.Seats))
	for _, s := range result.Seats {
		heldSeatIDs = append(heldSeatIDs, s.ID.String())
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "assigned_seats"
	}

	h.logger.Info("bil24_compat: RESERVATION: seated hold created",
		slog.String("reservation_id", result.Reservation.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.Int("seat_count", len(result.Seats)),
		slog.Int64("total_sum", bd.Total),
	)

	extra := map[string]any{
		"reservationId": TranslatePlatformID(result.Reservation.ID),
		"sessionId":     TranslatePlatformID(sessionID),
		"seatCount":     len(result.Seats),
		"seatList":      heldSeatIDs,
		"admissionMode": responseAdmission,
		"cartTimeout":   cartTimeoutSeconds(result.Reservation.ExpiresAt),
	}
	for k, v := range bil24FinancialFields(bd) {
		extra[k] = v
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, extra))
}

// reservationGA is the general-admission branch of the RESERVATION
// dispatcher. It validates the categoryList shape, prices every tier
// platform-side (guardrail #15 — pwyw tiers are rejected because the
// legacy wire has no chosen-price field), creates a REAL hold through the
// injected hcheckout GA callback (per-tier capacity + reservation_ga_items
// lines), and responds with the same financial contract as the seated
// branch.
func (h *Handler) reservationGA(w http.ResponseWriter, ctx context.Context, req bil24Request, sessionID uuid.UUID, admissionMode string) {
	// Validate + aggregate the categoryList (duplicate tiers are summed so
	// the per-tier hold lines stay unique).
	type gaLine struct {
		tierID uuid.UUID
		qty    int32
	}
	order := make([]uuid.UUID, 0, len(req.CategoryList))
	byTier := make(map[uuid.UUID]*gaLine, len(req.CategoryList))
	for i, c := range req.CategoryList {
		if strings.TrimSpace(c.CategoryPriceID) == "" {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("categoryList[%d].categoryPriceId is required", i),
			))
			return
		}
		tierID, err := TranslateLegacyID(c.CategoryPriceID)
		if err != nil {
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
		if line, exists := byTier[tierID]; exists {
			line.qty += int32(c.Quantity) //nolint:gosec // validated > 0 above
		} else {
			byTier[tierID] = &gaLine{tierID: tierID, qty: int32(c.Quantity)} //nolint:gosec // validated > 0
			order = append(order, tierID)
		}
	}

	// Self-gate: the real hold path needs the reservation wiring plus the
	// tier pricing surface.
	if h.resDeps.GAReserve == nil || h.resDeps.CtxQ == nil || h.resDeps.TierQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	orgID, channelID, expiresAt, ok := h.reservationContext(ctx, w, req, sessionID)
	if !ok {
		return
	}

	// Price every tier platform-side.
	items := make([]hcheckout.GAHoldItem, 0, len(order))
	lines := make([]hcheckout.PricingLineInput, 0, len(order))
	currency := ""
	for _, tierID := range order {
		line := byTier[tierID]
		tier, err := h.resDeps.TierQ.GetTicketTierByID(ctx, tierID, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				resp := bil24Error(req.Command, ResultCodeNotFound, "categoryPriceId not found in this session")
				resp.Data = map[string]any{"categoryPriceId": TranslatePlatformID(tierID)}
				writeBil24JSON(w, http.StatusOK, resp)
				return
			}
			h.logger.Error("bil24_compat: RESERVATION: tier lookup failed",
				slog.String("tier_id", tierID.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to resolve ticket tier",
			))
			return
		}
		var unitPrice int64
		switch tier.PricingMode {
		case "free":
			unitPrice = 0
		case "fixed":
			unitPrice = tier.PriceAmount
		default:
			// pwyw (no chosen-price field on the legacy wire) and unknown
			// modes cannot be priced by the gateway.
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("tier %s pricing mode %q is not supported via the compatibility gateway", tierID, tier.PricingMode),
			))
			return
		}
		if currency == "" {
			currency = tier.Currency
		}
		items = append(items, hcheckout.GAHoldItem{TierID: tierID, Quantity: line.qty, UnitPrice: unitPrice})
		lines = append(lines, hcheckout.PricingLineInput{TierID: tierID.String(), Quantity: line.qty, UnitPrice: unitPrice})
	}

	res, err := h.resDeps.GAReserve(ctx, hcheckout.GAHoldInput{
		OrgID:     orgID,
		ChannelID: channelID,
		SessionID: sessionID,
		Items:     items,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		h.writeHoldError(w, req.Command, err)
		return
	}

	bd := hcheckout.ComputePricingLines(lines, 0, currency, h.resDeps.PricingRules)

	echoed := make([]map[string]any, 0, len(items))
	var total int32
	for _, it := range items {
		total += it.Quantity
		echoed = append(echoed, map[string]any{
			"categoryPriceId": TranslatePlatformID(it.TierID),
			"quantity":        it.Quantity,
		})
	}

	responseAdmission := admissionMode
	if responseAdmission == "" {
		responseAdmission = "general_admission"
	}

	h.logger.Info("bil24_compat: RESERVATION: GA hold created",
		slog.String("reservation_id", res.ID.String()),
		slog.String("session_id", sessionID.String()),
		slog.Int("total_quantity", int(total)),
		slog.Int64("total_sum", bd.Total),
	)

	extra := map[string]any{
		"reservationId": TranslatePlatformID(res.ID),
		"sessionId":     TranslatePlatformID(sessionID),
		"categoryList":  echoed,
		"totalQuantity": total,
		"admissionMode": responseAdmission,
		"cartTimeout":   cartTimeoutSeconds(res.ExpiresAt),
	}
	for k, v := range bil24FinancialFields(bd) {
		extra[k] = v
	}
	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, extra))
}

// ─────────────────────────────────────────────────────────────────────────────
// UN_RESERVE — release a hold created by RESERVATION
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24UnReserve maps the legacy cancel semantics of the RESERVATION
// flow (§5.1 of the ticket-agent notes) onto the platform hold release:
// held seats flip back to 'available' (with a seat_status_version bump),
// reserved capacity is returned (session-level for seats, per-tier for GA
// lines), and the reservation transitions to 'cancelled'.
//
// Bil24 request fields used:
//   - reservationId: the id returned by a successful RESERVATION
//
// Response:
//
//	{ "resultCode": 0, "command": "UN_RESERVE",
//	  "reservationId": "<uuid>", "status": "cancelled" }
func (h *Handler) handleBil24UnReserve(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if strings.TrimSpace(req.ReservationID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest, "reservationId is required",
		))
		return
	}
	reservationID, err := TranslateLegacyID(req.ReservationID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			"reservationId must be a valid reservation identifier",
		))
		return
	}

	if h.resDeps.Release == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "reservation service unavailable",
		))
		return
	}

	cancelled, err := h.resDeps.Release(r.Context(), reservationID)
	if err != nil {
		var notReleasable *hcheckout.NotReleasableError
		switch {
		case errors.Is(err, hcheckout.ErrHoldNotFound):
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "reservation not found",
			))
		case errors.As(err, &notReleasable):
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest,
				fmt.Sprintf("reservation cannot be released from state %q", notReleasable.State),
			))
		default:
			h.logger.Error("bil24_compat: UN_RESERVE: release failed",
				slog.String("reservation_id", reservationID.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to release reservation",
			))
		}
		return
	}

	h.logger.Info("bil24_compat: UN_RESERVE: hold released",
		slog.String("reservation_id", cancelled.ID.String()),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"reservationId": TranslatePlatformID(cancelled.ID),
		"status":        "cancelled",
	}))
}
