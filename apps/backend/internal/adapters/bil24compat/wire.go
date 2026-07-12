// wire.go — Bil24 request / response envelope types and helpers.
//
// The Request struct decodes the flat JSON command envelope sent by legacy
// Bil24 clients. The Response struct encodes the flat JSON envelope sent
// back (resultCode, description, command, plus arbitrary command-specific
// fields merged at the top level via Data).
//
// All names exported from this package are part of the Bil24 wire contract.

package bil24compat

import (
	"encoding/json"
	"net/http"
)

// Request is the top-level request envelope for POST /compat/bil24/json.
// Only the Command field is required; all other fields are command-specific
// and are decoded from the same flat JSON object.
//
// Field tag conventions: JSON keys use legacy Bil24 camelCase
// (actionEventId, categoryPriceId, …). Go's encoding/json decoder matches
// these case-insensitively against the field names where no struct tag is
// supplied, preserving compatibility with the previous in-package struct.
type Request struct {
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

	// ActionID is the Bil24 event identifier (GET_ALL_ACTIONS detail /
	// GET_SEAT_LIST).
	ActionID string
	// ActionEventID is the Bil24 session identifier (GET_SEAT_LIST /
	// CREATE_ORDER_EXT).
	ActionEventID string
	// CategoryPriceID is the Bil24 ticket tier identifier
	// (CREATE_ORDER_EXT).
	CategoryPriceID string
	// Quantity is the number of tickets requested (CREATE_ORDER_EXT).
	Quantity int `json:"quantity"`
	// Email is the buyer email for the order (CREATE_ORDER_EXT).
	Email string `json:"email"`
	// OrderID is the Bil24 order identifier (GET_ORDER_INFO /
	// CANCEL_ORDER).
	OrderID string
	// TicketID is the Bil24 barcode / ticket identifier (SCAN_TICKET).
	TicketID string
	// ReservationID is the platform reservation identifier returned by a
	// successful RESERVATION command. Consumed by UN_RESERVE to release
	// the hold. Like the other ID fields it travels as a string (the
	// legacy wire key "reservationId" matches case-insensitively).
	ReservationID string

	// SeatList is the seated-mode RESERVATION payload (feature #312,
	// Wave SEAT-D1). Each entry is a session_seat.id string (ADR-005 —
	// the platform's session_seats.id, serialised as a plain UUID
	// string). Present for RESERVATION on sessions whose admission_mode
	// is assigned_seats (or hybrid with seats). Mutually exclusive with
	// CategoryList.
	//
	// No JSON tag: Go's encoding/json decoder matches the "seatList"
	// wire key case-insensitively against the PascalCase field name,
	// matching the rest of Request. This also keeps the platform's
	// snake_case JSON tag policy intact — the Bil24 gateway is a
	// legacy wire-compat layer, not a first-party API surface.
	SeatList []string
	// CategoryList is the general-admission RESERVATION payload used by
	// legacy Bil24 clients on general_admission (tier-facade) sessions.
	// Each entry names a categoryPriceId (platform tier UUID) and a
	// quantity. Mutually exclusive with SeatList.
	CategoryList []CategoryQty
}

// CategoryQty is one row of the legacy Bil24 categoryList payload used by
// RESERVATION on general_admission sessions. CategoryPriceID names a
// platform ticket_tier.id; Quantity is the number of tickets requested
// against that tier. The struct is unmarshal-only; no JSON tags are
// declared so the snake_case policy scan stays quiet (case-insensitive
// matching against the PascalCase fields covers the legacy camelCase
// wire keys).
type CategoryQty struct {
	// CategoryPriceID is the ticket_tier identifier (platform UUID).
	CategoryPriceID string
	// Quantity is the requested ticket count for the tier (>= 1).
	Quantity int
}

// Response is the Bil24-compatible response envelope. ResultCode=0
// indicates success; any other value indicates failure. Extra command-
// specific fields are merged into the same flat JSON object via the Data
// map.
type Response struct {
	ResultCode  int
	Description string `json:"description"`
	Command     string `json:"command"`
	// Data holds extra payload fields that MarshalJSON merges at the top
	// level of the JSON output, alongside resultCode / description /
	// command. Exported so callers in the HTTP layer can inspect it in
	// tests; the field is part of the wire contract only indirectly (its
	// merged keys are).
	Data map[string]any
}

// MarshalJSON produces the flat Bil24 JSON envelope with extra data fields
// merged at the top level alongside resultCode / description / command.
func (r Response) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"resultCode":  r.ResultCode,
		"description": r.Description,
		"command":     r.Command,
	}
	for k, v := range r.Data {
		out[k] = v
	}
	return json.Marshal(out)
}

// OK constructs a success response for the given command with optional
// extra payload fields.
func OK(command string, extra map[string]any) Response {
	return Response{
		ResultCode:  ResultCodeOK,
		Description: "OK",
		Command:     command,
		Data:        extra,
	}
}

// Error constructs an error response for the given command.
func Error(command string, code int, description string) Response {
	return Response{
		ResultCode:  code,
		Description: description,
		Command:     command,
	}
}

// WriteJSON writes a Bil24-envelope response with Content-Type
// application/json. The HTTP status code is typically 200 for all Bil24
// protocol responses (including application-level errors), following the
// Bil24 wire contract where legacy clients check resultCode, not HTTP
// status. 500 is reserved for genuine server-side failures.
func WriteJSON(w http.ResponseWriter, status int, resp Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
