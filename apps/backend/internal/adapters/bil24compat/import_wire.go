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
type ImportSessionActionEvent struct {
	ActionEventID   int64   `json:"actionEventId"`
	Day             string  `json:"day"`
	Time            string  `json:"time"`
	Currency        string  `json:"currency"`
	SellEndTime     string  `json:"sellEndTime"`
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

// ImportSessionRequest is the full §13.2 request body.
type ImportSessionRequest struct {
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
