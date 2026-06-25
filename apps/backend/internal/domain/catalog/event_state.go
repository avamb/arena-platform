// event_state.go contains the pure state-machine primitives for the Event
// aggregate (feature #183).
//
// These declarations have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for the Event status enum and its transition guard, callable from
// both the HTTP layer and the application orchestrators.
package catalog

// EventStatus enumerates the supported Event lifecycle states. Storing the
// strings as a named type makes "wrong status at the call site" a
// compile-time concern at every domain boundary, while remaining wire-format
// compatible with the existing JSON payloads ("draft" / "published" /
// "cancelled" / "archived").
type EventStatus string

const (
	// EventStatusDraft is the initial state; events are not visible publicly.
	EventStatusDraft EventStatus = "draft"
	// EventStatusPublished marks events as visible and bookable.
	EventStatusPublished EventStatus = "published"
	// EventStatusCancelled marks events as cancelled (but still visible for
	// audit + refund flows).
	EventStatusCancelled EventStatus = "cancelled"
	// EventStatusArchived is the terminal state; archived events are hidden
	// from public listings.
	EventStatusArchived EventStatus = "archived"
)

// ValidEventTransitions defines the allowed Event status transitions.
// Only the entries listed here are permitted; all others must be rejected
// with a 422 by the HTTP layer.
//
// Transitions:
//
//	draft     → published | cancelled
//	published → cancelled | archived
//	cancelled → archived
//	archived  → (terminal)
//
// The map is exported for read-only inspection (e.g. by tests) but MUST NOT
// be mutated at runtime; treat it as a compile-time constant.
var ValidEventTransitions = map[EventStatus]map[EventStatus]bool{
	EventStatusDraft: {
		EventStatusPublished: true,
		EventStatusCancelled: true,
	},
	EventStatusPublished: {
		EventStatusCancelled: true,
		EventStatusArchived:  true,
	},
	EventStatusCancelled: {
		EventStatusArchived: true,
	},
	EventStatusArchived: {},
}

// IsValidEventTransition returns true when the transition from → to is
// allowed by ValidEventTransitions. Unknown "from" values and unlisted
// "to" values both return false. Identity transitions (from == to) return
// false as well, since the map intentionally never lists a self-loop.
//
// The function accepts plain strings (rather than EventStatus) so the HTTP
// layer can call it directly with values parsed from JSON or the database
// without an extra conversion at every call site.
func IsValidEventTransition(from, to string) bool {
	allowed, ok := ValidEventTransitions[EventStatus(from)]
	if !ok {
		return false
	}
	return allowed[EventStatus(to)]
}
