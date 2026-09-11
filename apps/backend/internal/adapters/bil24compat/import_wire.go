// import_wire.go — request payload for the operator-side Bil24 session
// import, POST /v1/organizations/{org_id}/imports/bil24-session (feature
// #517, W1-C3c; spec §13.2).
//
// The payload is assembled by the site-side import module out of raw Bil24
// GET_ALL_ACTIONS / GET_SEAT_LIST / image?type=seatingPlan responses, so it
// keeps the legacy Bil24 camelCase key names verbatim. That is why these
// types live in the wire-adapter package (allowlisted in the httpserver
// snake_case guardrail) and not in the handler package: the snake_case
// response shape is defined in httpserver/himports.
//
// Everything here is decoding + normalisation only — no database or HTTP
// concerns. Validation that needs platform state (org membership, existing
// external-id mappings, sales) lives in the handler.

package bil24compat

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ExternalIDCeiling mirrors compatids' invariant: Bil24-originated
// identifiers are always below 1e9, arena-originated ones are at or above
// it. A payload carrying an id at or above the ceiling is a sign the caller
// echoed an arena system id back at us, which would corrupt the mapping
// table — it is rejected with compat.external_id_out_of_range.
const ExternalIDCeiling int64 = 1_000_000_000

// ErrExternalIDOutOfRange is returned by ImportSessionRequest.Validate when
// any Bil24 identifier in the payload is not a positive value below
// ExternalIDCeiling.
var ErrExternalIDOutOfRange = errors.New("bil24 external id out of range")

// ErrArenaIDOutOfRange is the mirror image of ErrExternalIDOutOfRange for the
// arena-native bundle: identifiers arena minted itself are always at or above
// ExternalIDCeiling, so a smaller value is a Bil24 id sent under
// source=arena by mistake (event-bundle spec §3.1 / §5,
// import.arena_id_out_of_range).
var ErrArenaIDOutOfRange = errors.New("arena compat id out of range")

// Import sources accepted by the event-bundle endpoint (event-bundle spec
// §1 / §3). SourceBil24 is the legacy relay path (ids come from Bil24 and are
// below the ceiling); SourceArena is the site/bot path where arena mints the
// ids itself.
const (
	SourceBil24 = "bil24"
	SourceArena = "arena"
)

// MaxExternalRefLength bounds ImportSessionRequest.ExternalRef, matching the
// CHECK on session_external_refs.external_ref (migration 0099).
const MaxExternalRefLength = 200

// KnownImportSource reports whether s is one of the two accepted source
// values. An empty string is NOT a known source — the caller decides whether
// a missing source defaults to bil24 (legacy route) or is a hard error
// (event-bundle route).
func KnownImportSource(s string) bool {
	return s == SourceBil24 || s == SourceArena
}

// ImportSessionAction is the Bil24 "action" (arena: event) block.
type ImportSessionAction struct {
	ActionID       int64  `json:"actionId"`
	ActionName     string `json:"actionName"`
	FullActionName string `json:"fullActionName"`
	Description    string `json:"description"`
	BigPosterURL   string `json:"bigPosterUrl"`
	Age            string `json:"age"`
	OrganizerName  string `json:"organizerName"`
}

// Name returns the best available display name for the event: the full name
// when present, otherwise the short one.
func (a ImportSessionAction) Name() string {
	if n := strings.TrimSpace(a.FullActionName); n != "" {
		return n
	}
	return strings.TrimSpace(a.ActionName)
}

// ImportSessionActionEvent is the Bil24 "actionEvent" (arena: session) block.
//
// Day and Time are LOCAL wall-clock values in the venue timezone, in the
// legacy Bil24 formats "DD.MM.YYYY" and "HH:MM". SellEndTime, by contrast,
// is a fully-qualified RFC3339 instant.
//
// EndTime and SellStartTime are the event-bundle additions (spec §3): EndTime
// is another local "HH:MM" wall-clock value (a value at or before Time means
// the session ends the NEXT day), SellStartTime another RFC3339 instant. Both
// are optional; without EndTime the import keeps its default session length.
type ImportSessionActionEvent struct {
	ActionEventID   int64   `json:"actionEventId"`
	Day             string  `json:"day"`
	Time            string  `json:"time"`
	EndTime         string  `json:"endTime"`
	Currency        string  `json:"currency"`
	SellEndTime     string  `json:"sellEndTime"`
	SellStartTime   string  `json:"sellStartTime"`
	ChargePercent   float64 `json:"chargePercent"`
	SeatingPlanID   int64   `json:"seatingPlanId"`
	SeatingPlanName string  `json:"seatingPlanName"`
}

// ImportSessionVenue is the Bil24 "venue" block. Timezone is an IANA zone
// name and is mandatory when the venue is not already known to arena.
type ImportSessionVenue struct {
	VenueID     int64    `json:"venueId"`
	VenueName   string   `json:"venueName"`
	Address     string   `json:"address"`
	CityID      int64    `json:"cityId"`
	CityName    string   `json:"cityName"`
	CountryID   int64    `json:"countryId"`
	CountryName string   `json:"countryName"`
	Timezone    string   `json:"timezone"`
	GeoLat      *float64 `json:"geoLat"`
	// GeoLon keeps the Bil24 spelling ("Lon"); arena stores it in
	// venues.geo_lng.
	GeoLon *float64 `json:"geoLon"`
}

// ImportSessionCategory is one entry of the Bil24 "categoryList" — an arena
// ticket tier. Price is in the MAJOR currency unit (Bil24 sends floats on the
// wire); arena stores minor units, see PriceMinorUnits.
type ImportSessionCategory struct {
	CategoryPriceID   int64   `json:"categoryPriceId"`
	CategoryPriceName string  `json:"categoryPriceName"`
	Price             float64 `json:"price"`
	Placement         bool    `json:"placement"`
	Availability      int32   `json:"availability"`
}

// PriceMinorUnits converts the wire float major-unit price into the integer
// minor units arena stores in ticket_tiers.price_amount, rounding half away
// from zero to avoid the 24.999999 → 2499 float artefact.
func (c ImportSessionCategory) PriceMinorUnits() int64 {
	return int64(math.Round(c.Price * 100))
}

// ImportSessionSeatLocation is the sector / row / number triple of a seat.
type ImportSessionSeatLocation struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
	Number string `json:"number"`
}

// ImportSessionSeat is one entry of the Bil24 "seatList". It is decoded but
// not yet consumed by the general-admission import slice (feature #517);
// seat materialisation lands with the seating-plan slice (feature #518,
// spec §13.2 step 6).
type ImportSessionSeat struct {
	SeatID          int64                     `json:"seatId"`
	CategoryPriceID int64                     `json:"categoryPriceId"`
	Location        ImportSessionSeatLocation `json:"location"`
	Available       bool                      `json:"available"`
}

// ImportSessionRequest is the full §13.2 request body, extended by the
// event-bundle spec §3 with the top-level Source and ExternalRef fields.
//
// Source selects the identifier regime (see SourceBil24 / SourceArena) and is
// mandatory on the event-bundle route; the legacy /imports/bil24-session route
// pins it to bil24. ExternalRef is the caller's stable idempotency key for the
// session (mandatory for source=arena, optional for bil24), unique inside the
// organization — see migration 0099's session_external_refs.
type ImportSessionRequest struct {
	Source       string                   `json:"source"`
	ExternalRef  string                   `json:"externalRef"`
	Action       ImportSessionAction      `json:"action"`
	ActionEvent  ImportSessionActionEvent `json:"actionEvent"`
	Venue        ImportSessionVenue       `json:"venue"`
	CategoryList []ImportSessionCategory  `json:"categoryList"`
	SeatList     []ImportSessionSeat      `json:"seatList"`
	SVG          string                   `json:"svg"`
	Publish      bool                     `json:"publish"`
}

// HasPlacement reports whether any category is a seated (placement) one.
// Used to select the session admission mode once seating support lands.
func (r ImportSessionRequest) HasPlacement() bool {
	for _, c := range r.CategoryList {
		if c.Placement {
			return true
		}
	}
	return false
}

// TotalAvailability sums the GA capacities declared across all categories.
func (r ImportSessionRequest) TotalAvailability() int32 {
	var total int32
	for _, c := range r.CategoryList {
		if c.Availability > 0 {
			total += c.Availability
		}
	}
	return total
}

// ValidateExternalIDs enforces spec §13.2 step 1: every Bil24 identifier
// carried by the payload must be a positive value strictly below
// ExternalIDCeiling. Optional ids (cityId, countryId, seatingPlanId) are
// only checked when non-zero. Returns an error wrapping
// ErrExternalIDOutOfRange naming the offending field.
func (r ImportSessionRequest) ValidateExternalIDs() error {
	required := []struct {
		field string
		value int64
	}{
		{"action.actionId", r.Action.ActionID},
		{"actionEvent.actionEventId", r.ActionEvent.ActionEventID},
		{"venue.venueId", r.Venue.VenueID},
	}
	for _, f := range required {
		if f.value <= 0 || f.value >= ExternalIDCeiling {
			return fmt.Errorf("%s=%d: %w", f.field, f.value, ErrExternalIDOutOfRange)
		}
	}

	optional := []struct {
		field string
		value int64
	}{
		{"venue.cityId", r.Venue.CityID},
		{"venue.countryId", r.Venue.CountryID},
		{"actionEvent.seatingPlanId", r.ActionEvent.SeatingPlanID},
	}
	for _, f := range optional {
		if f.value == 0 {
			continue
		}
		if f.value < 0 || f.value >= ExternalIDCeiling {
			return fmt.Errorf("%s=%d: %w", f.field, f.value, ErrExternalIDOutOfRange)
		}
	}

	for i, c := range r.CategoryList {
		if c.CategoryPriceID <= 0 || c.CategoryPriceID >= ExternalIDCeiling {
			return fmt.Errorf("categoryList[%d].categoryPriceId=%d: %w", i, c.CategoryPriceID, ErrExternalIDOutOfRange)
		}
	}
	// seatList seat ids are NOT range-checked here: Bil24 seat ids legitimately
	// exceed 1e9 (the spec example carries 2873098559) and they are stored in
	// session_seats.system_seat_id, not in the compat_ids mapping table.
	return nil
}

// NormalizedExternalRef returns ExternalRef with surrounding whitespace
// removed — the form stored in session_external_refs and compared against.
func (r ImportSessionRequest) NormalizedExternalRef() string {
	return strings.TrimSpace(r.ExternalRef)
}

// ValidateArenaIDs enforces the event-bundle spec §3.1 identifier rules for
// source=arena: every compat id is OPTIONAL (zero means "mint one"), but a
// supplied id must be at or above ExternalIDCeiling, because arena only ever
// mints ids in that range. A smaller value means the caller pasted a Bil24 id
// into an arena bundle and would otherwise silently hijack a foreign mapping.
//
// seatList seat ids are ignored entirely: seating is not supported for
// source=arena in this wave (spec §1), and a stray seatList only earns a
// warning.
func (r ImportSessionRequest) ValidateArenaIDs() error {
	fields := []struct {
		field string
		value int64
	}{
		{"action.actionId", r.Action.ActionID},
		{"actionEvent.actionEventId", r.ActionEvent.ActionEventID},
		{"venue.venueId", r.Venue.VenueID},
	}
	for _, f := range fields {
		if f.value == 0 {
			continue
		}
		if f.value < ExternalIDCeiling {
			return fmt.Errorf("%s=%d: %w", f.field, f.value, ErrArenaIDOutOfRange)
		}
	}
	for i, c := range r.CategoryList {
		if c.CategoryPriceID == 0 {
			continue
		}
		if c.CategoryPriceID < ExternalIDCeiling {
			return fmt.Errorf("categoryList[%d].categoryPriceId=%d: %w", i, c.CategoryPriceID, ErrArenaIDOutOfRange)
		}
	}
	return nil
}

// ParseLocalStart converts the wire "DD.MM.YYYY" day and "HH:MM" time into an
// instant, interpreting them as wall-clock values in loc (the venue
// timezone). An empty time component defaults to midnight.
func (e ImportSessionActionEvent) ParseLocalStart(loc *time.Location) (time.Time, error) {
	day := strings.TrimSpace(e.Day)
	if day == "" {
		return time.Time{}, errors.New("actionEvent.day is required")
	}
	clock := strings.TrimSpace(e.Time)
	if clock == "" {
		clock = "00:00"
	}
	// allow:timeformat: legacy Bil24 wire formats, not RFC3339.
	t, err := time.ParseInLocation("02.01.2006 15:04", day+" "+clock, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("actionEvent day/time %q %q: %w", e.Day, e.Time, err)
	}
	return t, nil
}

// ParseSellEnd converts the RFC3339 sellEndTime into an instant. An empty
// value yields (nil, nil) — the sale window simply stays unbounded.
func (e ImportSessionActionEvent) ParseSellEnd() (*time.Time, error) {
	raw := strings.TrimSpace(e.SellEndTime)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("actionEvent.sellEndTime %q: %w", raw, err)
	}
	utc := t.UTC()
	return &utc, nil
}

// ParseLocalEnd converts the optional "HH:MM" endTime into an instant, using
// the same local day as the start and the same venue timezone (event-bundle
// spec §3). An endTime at or before the start time belongs to the NEXT day —
// a concert that starts at 22:00 and ends at 01:00 is one session, not a
// negative-length one. An empty endTime yields (nil, nil): the caller then
// falls back to the import's default session duration.
//
// The day/time pair itself must already be valid; a parse failure here is
// reported against endTime alone so the caller can answer
// import.end_time_invalid.
func (e ImportSessionActionEvent) ParseLocalEnd(loc *time.Location) (*time.Time, error) {
	raw := strings.TrimSpace(e.EndTime)
	if raw == "" {
		return nil, nil
	}
	start, err := e.ParseLocalStart(loc)
	if err != nil {
		return nil, err
	}
	day := strings.TrimSpace(e.Day)
	// allow:timeformat: legacy Bil24 wire formats, not RFC3339.
	end, err := time.ParseInLocation("02.01.2006 15:04", day+" "+raw, loc)
	if err != nil {
		return nil, fmt.Errorf("actionEvent.endTime %q: %w", raw, err)
	}
	if !end.After(start) {
		// AddDate keeps the wall-clock hour across a DST boundary, which is
		// what "the next calendar day at HH:MM" means locally.
		end = end.AddDate(0, 0, 1)
	}
	utc := end.UTC()
	return &utc, nil
}

// ParseSellStart converts the optional RFC3339 sellStartTime into an instant.
// An empty value yields (nil, nil) — sales open immediately. When sellEndTime
// is also present the start must lie strictly before it, mirroring the
// ticket_tiers CHECK (sale_window_end > sale_window_start) from migration
// 0019: rejecting the payload here keeps the caller from meeting a raw 23514
// at COMMIT time.
func (e ImportSessionActionEvent) ParseSellStart() (*time.Time, error) {
	raw := strings.TrimSpace(e.SellStartTime)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("actionEvent.sellStartTime %q: %w", raw, err)
	}
	end, err := e.ParseSellEnd()
	if err != nil {
		return nil, err
	}
	if end != nil && !t.UTC().Before(*end) {
		return nil, fmt.Errorf("actionEvent.sellStartTime %q must be before sellEndTime %q",
			raw, strings.TrimSpace(e.SellEndTime))
	}
	utc := t.UTC()
	return &utc, nil
}
