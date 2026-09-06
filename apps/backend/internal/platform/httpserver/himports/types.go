// types.go defines the snake_case response shape of the Bil24 session import
// (spec §13.2 step 9).
package himports

import "github.com/google/uuid"

// Warning codes emitted by the import. Warnings never fail the import — they
// tell the operator which parts of the payload could not be applied verbatim
// so the site side can decide whether to follow up.
const (
	// WarnSeatingNotImported — the payload carried a seatList but no svg, so
	// there is no geometry to hang the seats on and the session stays
	// general admission (spec §13.2 step 6).
	WarnSeatingNotImported = "import.seating_not_imported"
	// WarnSeatsBlocked — seatList entries with available:false were imported
	// as blocked ('unavailable') seats and are not on sale.
	WarnSeatsBlocked = "import.seats_blocked"
	// WarnSeatNotInPlan — a seatList entry referenced a seat id the svg
	// seating plan does not contain; the entry was ignored.
	WarnSeatNotInPlan = "import.seat_not_in_plan"
	// WarnCategoryUnmapped — the seating plan references a category that
	// categoryList does not declare, so those seats carry no ticket tier.
	WarnCategoryUnmapped = "import.category_unmapped"
	// WarnPosterSkipped — action.bigPosterUrl could not be side-loaded.
	WarnPosterSkipped = "import.poster_skipped"
	// WarnCountryUnresolved — venue.countryName did not match any known
	// countries row and cannot be created (arena needs ISO codes + currency,
	// which the Bil24 payload does not carry).
	WarnCountryUnresolved = "import.country_unresolved"
	// WarnCityUnresolved — the city could not be resolved or created,
	// usually because its country is unresolved.
	WarnCityUnresolved = "import.city_unresolved"
	// WarnCurrencyLocked — the payload's currency differs from the stored
	// one but the session already has tickets, so the currency was kept.
	WarnCurrencyLocked = "import.currency_locked"
	// WarnChargePercentMismatch — actionEvent.chargePercent differs from the
	// channel fee_percent. Per spec §13.2 step 7 the channel is NEVER
	// modified by an import; the value is informational only.
	WarnChargePercentMismatch = "import.charge_percent_mismatch"
	// WarnPublishSkipped — publish:true was requested but the standard
	// publish gate rejected the transition.
	WarnPublishSkipped = "import.publish_skipped"
	// WarnVenueTimezoneKept — the payload carried a timezone that differs
	// from the one already stored on the existing venue; the stored value
	// wins because changing it would move every existing session.
	WarnVenueTimezoneKept = "import.venue_timezone_kept"
)

// Warning is one non-fatal note about the import.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ImportSessionResponse is the 200/201 body of the Bil24 session import
// (spec §13.2 step 9).
//
// TierIDs maps the Bil24 categoryPriceId (as a decimal string, because JSON
// object keys are always strings) to the arena ticket-tier UUID.
//
// SeatingPlanVersionID and SeatsMaterialized describe the seated half of the
// import (spec §13.2 step 6): the plan version the session was bound to and
// the number of session_seats rows (assigned seats plus GA units) the session
// carries. Both stay null / 0 for a payload without an svg block, which is
// imported as a pure general-admission session.
type ImportSessionResponse struct {
	EventID              uuid.UUID            `json:"event_id"`
	SessionID            uuid.UUID            `json:"session_id"`
	TierIDs              map[string]uuid.UUID `json:"tier_ids"`
	SeatingPlanVersionID *uuid.UUID           `json:"seating_plan_version_id"`
	SeatsMaterialized    int                  `json:"seats_materialized"`
	Warnings             []Warning            `json:"warnings"`
	Created              bool                 `json:"created"`
}

// warningSink accumulates warnings in emission order while de-duplicating on
// code — a repeated code with a different message would only add noise.
type warningSink struct {
	seen  map[string]struct{}
	items []Warning
}

func newWarningSink() *warningSink {
	return &warningSink{seen: make(map[string]struct{})}
}

func (s *warningSink) add(code, message string) {
	if _, dup := s.seen[code]; dup {
		return
	}
	s.seen[code] = struct{}{}
	s.items = append(s.items, Warning{Code: code, Message: message})
}

// list returns the accumulated warnings, never nil — the response contract
// promises an array.
func (s *warningSink) list() []Warning {
	if s.items == nil {
		return []Warning{}
	}
	return s.items
}
