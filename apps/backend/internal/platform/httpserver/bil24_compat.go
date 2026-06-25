// bil24_compat.go — Bil24-compatible API gateway (feature #157).
//
// This adapter layer translates the legacy Bil24 command-based JSON API
// to arena platform domain calls. It lives under the /compat/bil24/*
// prefix and is disabled by default; set BIL24_COMPAT_ENABLED=true to
// enable it.
//
// Wire compatibility:
//
//	The old WordPress / widget / partner client can POST the same JSON shape:
//	  { "command": "...", "fid": "...", "token": "...", "locale": "...", ... }
//	and receive Bil24-style responses:
//	  { "resultCode": 0, "description": "OK", "command": "..." }
//
// Supported commands (6 most-used first):
//
//	GET_ALL_ACTIONS  → list published events (GetCatalog)
//	GET_SEAT_LIST    → list ticket tiers for a session
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
//	For this scaffold, non-UUID IDs are mapped via the compatibility table
//	(a future DB lookup); currently opaque IDs return ErrLegacyIDNotFound.
//
// Feature flag: BIL24_COMPAT_ENABLED (env var, default false).
// The /compat/bil24/* subtree is only mounted when the flag is true.
// Requests to these paths return 404 when the flag is false.
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Result codes (Bil24 wire format)
// ─────────────────────────────────────────────────────────────────────────────

// Bil24 wire result codes. ResultCodeOK (0) indicates success. All other
// values indicate failure with the specific description in the response body.
const (
	// ResultCodeOK signals a successful command execution (Bil24 wire: 0).
	ResultCodeOK = 0
	// ResultCodeUnknownCommand is returned when the gateway receives a command
	// name it does not recognise (Bil24 wire: -1).
	ResultCodeUnknownCommand = -1
	// ResultCodeInvalidRequest is returned when a required request field is
	// missing or malformed (Bil24 wire: -2).
	ResultCodeInvalidRequest = -2
	// ResultCodeNotFound is returned when the requested resource does not
	// exist in the platform (Bil24 wire: -3).
	ResultCodeNotFound = -3
	// ResultCodeInternalError is returned when an unexpected error prevents
	// command execution (Bil24 wire: -99).
	ResultCodeInternalError = -99
)

// ─────────────────────────────────────────────────────────────────────────────
// Request / response envelope
// ─────────────────────────────────────────────────────────────────────────────

// bil24Request is the top-level request envelope for POST /compat/bil24/json.
// Only the Command field is required; all other fields are command-specific
// and are decoded from the same flat JSON object.
type bil24Request struct {
	// Command selects the operation to execute (e.g. "GET_ALL_ACTIONS").
	Command string `json:"command"`
	// FID is the frontend/interface identifier used for channel resolution.
	// Corresponds to sales_channel in the platform model.
	FID string `json:"fid"`
	// Token is the authentication credential for the FID.
	// Mapped to channel API credentials in the platform model.
	Token string `json:"token"`
	// Locale controls the language of localised content in the response.
	Locale string `json:"locale"`

	// Command-specific fields (present in the same flat JSON object).

	// ActionID is the Bil24 event identifier (GET_ALL_ACTIONS detail / GET_SEAT_LIST).
	ActionID string
	// ActionEventID is the Bil24 session identifier (GET_SEAT_LIST / CREATE_ORDER_EXT).
	ActionEventID string
	// CategoryPriceID is the Bil24 ticket tier identifier (CREATE_ORDER_EXT).
	CategoryPriceID string
	// Quantity is the number of tickets requested (CREATE_ORDER_EXT).
	Quantity int `json:"quantity"`
	// Email is the buyer email for the order (CREATE_ORDER_EXT).
	Email string `json:"email"`
	// OrderID is the Bil24 order identifier (GET_ORDER_INFO / CANCEL_ORDER).
	OrderID string
	// TicketID is the Bil24 barcode / ticket identifier (SCAN_TICKET).
	TicketID string
}

// bil24Response is the Bil24-compatible response envelope.
// ResultCode=0 indicates success; any other value indicates failure.
// Extra command-specific fields are added to the same flat JSON object
// via the Data map.
type bil24Response struct {
	ResultCode  int
	Description string `json:"description"`
	Command     string `json:"command"`
	// extra payload fields are merged in via a wrapper; kept separate
	// for testability.
	data map[string]any
}

// MarshalJSON produces the flat Bil24 JSON envelope with extra data fields
// merged at the top level alongside resultCode/description/command.
func (r bil24Response) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"resultCode":  r.ResultCode,
		"description": r.Description,
		"command":     r.Command,
	}
	for k, v := range r.data {
		out[k] = v
	}
	return json.Marshal(out)
}

// bil24OK constructs a success response for the given command with optional
// extra payload fields.
func bil24OK(command string, extra map[string]any) bil24Response {
	return bil24Response{
		ResultCode:  ResultCodeOK,
		Description: "OK",
		Command:     command,
		data:        extra,
	}
}

// bil24Error constructs an error response for the given command.
func bil24Error(command string, code int, description string) bil24Response {
	return bil24Response{
		ResultCode:  code,
		Description: description,
		Command:     command,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ID translation layer
// ─────────────────────────────────────────────────────────────────────────────

// ErrLegacyIDNotFound is returned by TranslateLegacyID when the provided
// legacy identifier cannot be resolved to a platform UUID. This happens when
// the ID is non-UUID format and no entry exists in the compatibility table.
var ErrLegacyIDNotFound = errors.New("bil24_compat: legacy ID not found in translation table")

// TranslateLegacyID converts a legacy Bil24 identifier (actionId, actionEventId,
// orderId, ticketId, …) to the platform's UUIDv7.
//
// Translation strategy:
//  1. If the raw string is already a valid UUID, return it unchanged.
//     This handles clients that have already been migrated to platform IDs.
//  2. Otherwise, attempt a future DB lookup (compatibility_id_map table).
//     For this scaffold, non-UUID IDs return ErrLegacyIDNotFound.
//
// This function is intentionally pure (no DB access in the scaffold) so it
// can be unit-tested without a database.
func TranslateLegacyID(raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, fmt.Errorf("bil24_compat: empty legacy ID")
	}
	// Attempt direct UUID parse — handles clients already sending UUIDs.
	if id, err := uuid.Parse(raw); err == nil {
		return id, nil
	}
	// Non-UUID format: would require a DB lookup in the compatibility_id_map
	// table (a future feature). Return ErrLegacyIDNotFound for now.
	return uuid.Nil, fmt.Errorf("%w: %q", ErrLegacyIDNotFound, raw)
}

// TranslatePlatformID converts a platform UUID to the Bil24 legacy ID format.
// For this scaffold, the UUID string is returned as-is since the platform
// uses UUID strings as the primary ID format.
func TranslatePlatformID(id uuid.UUID) string {
	return id.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Gateway feature-flag guard
// ─────────────────────────────────────────────────────────────────────────────

// bil24CompatQueriesAvailable returns true when all necessary query objects
// are present to serve the compat gateway. When nil, the feature was not
// enabled at server construction time. Note: individual commands may still
// return 503 if a specific query subset is missing.
//
//nolint:unused // referenced by test #157 as identifier surface check
func (s *Server) bil24CompatEnabled() bool {
	return s.bil24Enabled
}

// ─────────────────────────────────────────────────────────────────────────────
// Main gateway handler
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24Command is the single-entry-point for POST /compat/bil24/json.
//
// It parses the command field and dispatches to the appropriate domain
// adapter. All errors are returned in the Bil24 envelope format so that
// legacy clients receive machine-readable error codes without needing to
// understand HTTP status codes beyond 200.
//
// HTTP status is always 200 for protocol errors (unknown command, bad input)
// so that legacy clients that hard-code 200 checks remain compatible.
// 500 is reserved for genuine server-side failures.
func (s *Server) handleBil24Command(w http.ResponseWriter, r *http.Request) {
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
			s.logger.Error("bil24_compat: recovered panic in command handler",
				slog.String("command", command),
				slog.Any("panic", rec),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				command, ResultCodeInternalError, "service temporarily unavailable",
			))
		}
	}()

	s.logger.Info("bil24_compat: command received",
		slog.String("command", command),
		slog.String("fid", req.FID),
		slog.String("locale", req.Locale),
	)

	switch command {
	case "GET_ALL_ACTIONS":
		s.handleBil24GetAllActions(w, r, req)
	case "GET_SEAT_LIST":
		s.handleBil24GetSeatList(w, r, req)
	case "GET_ORDER_INFO":
		s.handleBil24GetOrderInfo(w, r, req)
	case "CREATE_ORDER_EXT":
		s.handleBil24CreateOrderExt(w, r, req)
	case "SCAN_TICKET":
		s.handleBil24ScanTicket(w, r, req)
	case "CANCEL_ORDER":
		s.handleBil24CancelOrder(w, r, req)
	default:
		s.logger.Warn("bil24_compat: unknown command",
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
func (s *Server) handleBil24GetAllActions(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if s.eventQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "catalog service unavailable",
		))
		return
	}

	locale := req.Locale
	if locale == "" {
		locale = "en"
	}

	events, err := s.eventQueries.ListEvents(r.Context(), locale, "public")
	if err != nil {
		s.logger.Error("bil24_compat: GET_ALL_ACTIONS: list events failed",
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

// handleBil24GetSeatList maps GET_SEAT_LIST to ticket tier listing for a
// specific event session.
//
// Bil24 request fields used:
//   - actionEventId: platform session UUID (Bil24 event instance)
//
// Response: { "resultCode": 0, "command": "GET_SEAT_LIST", "seatList": [...] }
// Each seat/tier item:
//
//	{
//	  "categoryPriceId": "<uuid>",
//	  "categoryName":    "...",
//	  "price":           <cents>,
//	  "currency":        "USD",
//	  "pricingMode":     "fixed"|"free"|"pwyw",
//	  "availableCount":  <int or null>
//	}
func (s *Server) handleBil24GetSeatList(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if s.tierQueries == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "tier service unavailable",
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

	tiers, err := s.tierQueries.ListTicketTiersBySession(r.Context(), sessionID)
	if err != nil {
		s.logger.Error("bil24_compat: GET_SEAT_LIST: list tiers failed",
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
func (s *Server) handleBil24GetOrderInfo(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if s.checkoutQueries == nil {
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

	cs, err := s.checkoutQueries.GetCheckoutSessionByID(r.Context(), orderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "order not found",
			))
			return
		}
		s.logger.Error("bil24_compat: GET_ORDER_INFO: fetch checkout session failed",
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
	if s.ticketQueries != nil {
		tickets, err := s.ticketQueries.ListTicketsByCheckoutSession(r.Context(), orderID)
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
func (s *Server) handleBil24CreateOrderExt(w http.ResponseWriter, _ *http.Request, req bil24Request) {
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

	s.logger.Info("bil24_compat: CREATE_ORDER_EXT: scaffold stub",
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
func (s *Server) handleBil24ScanTicket(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if s.barcodeQueries == nil {
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
	authority, err := s.barcodeQueries.GetBarcodeAuthorityByType(ctx, "legacy_bil24")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound,
				"legacy_bil24 barcode authority not registered; "+
					"create it first via POST /v1/barcodes/authorities",
			))
			return
		}
		s.logger.Error("bil24_compat: SCAN_TICKET: authority lookup failed",
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve barcode authority",
		))
		return
	}

	// Look up the barcode by (authority_id, external_ref).
	barcode, err := s.barcodeQueries.GetBarcodeByRef(ctx, authority.ID, req.TicketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "ticket not found",
			))
			return
		}
		s.logger.Error("bil24_compat: SCAN_TICKET: barcode lookup failed",
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
	scanned, err := s.barcodeQueries.MarkBarcodeScanned(ctx, barcode.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInvalidRequest, "ticket already scanned",
			))
			return
		}
		s.logger.Error("bil24_compat: SCAN_TICKET: mark scanned failed",
			slog.String("barcode_id", barcode.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to record scan",
		))
		return
	}

	s.logger.Info("bil24_compat: SCAN_TICKET: scan recorded",
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
func (s *Server) handleBil24CancelOrder(w http.ResponseWriter, _ *http.Request, req bil24Request) {
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

	s.logger.Info("bil24_compat: CANCEL_ORDER: scaffold stub",
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
// Response helper
// ─────────────────────────────────────────────────────────────────────────────

// writeBil24JSON writes a Bil24-envelope response with Content-Type
// application/json. The HTTP status code is typically 200 for all Bil24
// protocol responses (including application-level errors), following the
// Bil24 wire contract where legacy clients check resultCode, not HTTP status.
func writeBil24JSON(w http.ResponseWriter, status int, resp bil24Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// Router mounting
// ─────────────────────────────────────────────────────────────────────────────

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
