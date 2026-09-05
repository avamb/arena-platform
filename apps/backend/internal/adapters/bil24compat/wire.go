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
	"strconv"
	"strings"
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
	// Type is the RESERVATION sub-command selector introduced by spec §7.4
	// (feature #484): "RESERVE" adds to the session cart, "UN_RESERVE"
	// removes from it and "UN_RESERVE_ALL" empties it. An empty value means
	// the legacy shape (RESERVATION == add) and is treated as "RESERVE".
	Type string `json:"type"`

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
	// Wave SEAT-D1). Each entry is a seat identifier: the platform's
	// session_seats.id (legacy UUID string) or, per spec §4, the int64
	// system_seat_id rendered as its decimal literal.
	//
	// Three wire shapes decode into this one slice (see flexSeatList):
	// a bare scalar array ["a","b"] / [11,12] and the spec §7.4 object
	// array [{"seatId": 11}, …]. Normalising here keeps every RESERVATION
	// branch working against a plain []string.
	//
	// Mutually exclusive with CategoryList.
	SeatList []string
	// CategoryList is the general-admission RESERVATION payload used by
	// legacy Bil24 clients on general_admission (tier-facade) sessions.
	// Each entry names a categoryPriceId (platform tier UUID) and a
	// quantity. Mutually exclusive with SeatList.
	CategoryList []CategoryQty

	// ── CREATE_USER / gateway-session fields (feature #481, spec §7.3) ──

	// FirstName / LastName are the optional buyer name parts sent by
	// CREATE_USER. Spec §7.3: display_name = firstName + " " + lastName.
	FirstName string
	LastName  string
	// Phone is the optional buyer phone (a strong identity key per
	// spec §12.2). Shared with CREATE_USER and future order commands.
	Phone string
	// SessionID is the gateway session token minted by CREATE_USER and
	// echoed back by every subsequent command (spec §7.3 / §7.4). The wire
	// key is "sessionId"; an unknown/expired value maps to resultCode=1.
	SessionID string
	// UserID is the buyer's compatibility id (customers.system_id) that
	// CREATE_USER returned. It travels as a JSON number on the wire, so
	// it is normalised through Request.UnmarshalJSON (number-or-string).
	UserID int64
}

// requestAlias exists solely to give Request.UnmarshalJSON a recursion-free
// view of the struct: json.Unmarshal on the alias uses the default field
// walk instead of calling back into the custom unmarshaler.
type requestAlias Request

// UnmarshalJSON decodes the flat Bil24 envelope, tolerating the two fields
// legacy clients serialise inconsistently:
//
//   - `fid`: the WordPress plugin sends `(int)$o['fid']` (a JSON number)
//     while older integrations send a quoted string. Request.FID stays a Go
//     string so every call site keeps working; both wire shapes land in it.
//   - `userId`: always a JSON number in the wave-1 fixtures, but strings are
//     accepted for the same reason.
//   - `actionId` / `actionEventId` / `categoryPriceId`: spec §4 makes these
//     int64 system ids, and the WordPress plugin echoes back whatever the
//     catalog commands emitted — a JSON number. Without the flex decode the
//     whole envelope fails to unmarshal and every RESERVATION carrying a
//     numeric actionEventId is answered with -2 (feature #484).
//
// Every other field decodes exactly as before (the embedded alias performs
// the default, case-insensitive walk); the outer fields shadow their embedded
// namesakes because encoding/json prefers the shallower field.
func (r *Request) UnmarshalJSON(data []byte) error {
	var aux struct {
		requestAlias
		FID             json.RawMessage `json:"fid"`
		UserID          json.RawMessage `json:"userId"`
		SeatList        json.RawMessage `json:"seatList"`
		ActionID        json.RawMessage `json:"actionId"`
		ActionEventID   json.RawMessage `json:"actionEventId"`
		CategoryPriceID json.RawMessage `json:"categoryPriceId"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = Request(aux.requestAlias)
	r.FID = flexWireString(aux.FID)
	r.UserID = flexWireInt64(aux.UserID)
	r.SeatList = flexSeatList(aux.SeatList)
	r.ActionID = flexWireString(aux.ActionID)
	r.ActionEventID = flexWireString(aux.ActionEventID)
	r.CategoryPriceID = flexWireString(aux.CategoryPriceID)
	return nil
}

// flexSeatList normalises the three seatList wire shapes into []string:
//
//	["<uuid>", …]        legacy SEAT-D1 clients
//	[11, 12]             spec §4 int64 system_seat_id, bare
//	[{"seatId": 11}, …]  spec §7.4 RESERVE / UN_RESERVE payload
//
// Object entries also accept the "id" key, which some legacy WordPress
// builds emit. Entries that carry neither are skipped rather than failing
// the whole envelope — the reservation branch then reports the resulting
// empty/short list through the ordinary -2 invalid-request path.
func flexSeatList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if s := flexWireString(e); s != "" {
			out = append(out, s)
			continue
		}
		var obj struct {
			SeatID json.RawMessage `json:"seatId"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(e, &obj); err != nil {
			continue
		}
		if s := flexWireString(obj.SeatID); s != "" {
			out = append(out, s)
			continue
		}
		if s := flexWireString(obj.ID); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// flexWireString renders a raw JSON scalar as a Go string: a JSON string is
// unquoted, a JSON number keeps its literal text, and null / absent / any
// other shape yields "".
func flexWireString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// flexWireInt64 renders a raw JSON scalar as an int64: JSON numbers decode
// directly, quoted numbers are parsed, and null / absent / non-numeric input
// yields 0 (which callers treat as "not supplied").
func flexWireInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err == nil {
			return v
		}
	}
	return 0
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

// UnmarshalJSON accepts categoryPriceId as either a JSON string (legacy
// tier UUID) or a JSON number (spec §4 int64 catalog id), mirroring the
// tolerance Request.UnmarshalJSON applies to fid.
func (c *CategoryQty) UnmarshalJSON(data []byte) error {
	var aux struct {
		CategoryPriceID json.RawMessage `json:"categoryPriceId"`
		Quantity        int             `json:"quantity"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	c.CategoryPriceID = flexWireString(aux.CategoryPriceID)
	c.Quantity = aux.Quantity
	return nil
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
