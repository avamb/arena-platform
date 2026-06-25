// reservations.go contains the pure state-machine primitives for the
// Reservation aggregate (feature #184).
//
// These declarations have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for the Reservation state enum and its transition guard, callable
// from both the HTTP layer (POST/PATCH/DELETE reservation endpoints) and the
// background ReservationProcessor worker.
package inventory

// ReservationState enumerates the supported reservation lifecycle states.
// Storing the strings as a named type makes "wrong state at the call site"
// a compile-time concern at every domain boundary, while remaining
// wire-format compatible with the existing JSON payloads ("draft",
// "active", "converted", "expired", "cancelled").
type ReservationState string

const (
	// ReservationStateDraft is the initial state created by
	// POST /v1/reservations. Inventory is already held at this point.
	ReservationStateDraft ReservationState = "draft"
	// ReservationStateActive is reached via PATCH .../activate; the
	// reservation participates in checkout and is still holding inventory.
	ReservationStateActive ReservationState = "active"
	// ReservationStateConverted is a terminal state reached when checkout
	// captures payment; the inventory is moved from held → sold by the
	// payment-confirmation flow.
	ReservationStateConverted ReservationState = "converted"
	// ReservationStateExpired is a terminal state reached by the background
	// ReservationProcessor when the TTL elapses without conversion. Held
	// inventory is released as part of the expiry transition.
	ReservationStateExpired ReservationState = "expired"
	// ReservationStateCancelled is a terminal state reached via
	// DELETE /v1/reservations/{id}. Held inventory is released.
	ReservationStateCancelled ReservationState = "cancelled"
)

// ValidReservationTransitions defines the allowed Reservation state
// transitions. Only the entries listed here are permitted; all others must
// be rejected with a 422 by the HTTP layer.
//
// Transitions:
//
//	draft     → active | cancelled
//	active    → converted | expired | cancelled
//	converted → (terminal)
//	expired   → (terminal)
//	cancelled → (terminal)
//
// The map is exported for read-only inspection (e.g. by tests) but MUST NOT
// be mutated at runtime; treat it as a compile-time constant.
var ValidReservationTransitions = map[ReservationState]map[ReservationState]bool{
	ReservationStateDraft: {
		ReservationStateActive:    true,
		ReservationStateCancelled: true,
	},
	ReservationStateActive: {
		ReservationStateConverted: true,
		ReservationStateExpired:   true,
		ReservationStateCancelled: true,
	},
	ReservationStateConverted: {},
	ReservationStateExpired:   {},
	ReservationStateCancelled: {},
}

// IsValidReservationTransition returns true when the transition from → to is
// allowed by ValidReservationTransitions. Unknown "from" values and
// unlisted "to" values both return false. Identity transitions (from == to)
// return false as well, since the map intentionally never lists a self-loop.
//
// The function accepts plain strings (rather than ReservationState) so the
// HTTP layer can call it directly with values parsed from JSON or the
// database without an extra conversion at every call site.
func IsValidReservationTransition(from, to string) bool {
	allowed, ok := ValidReservationTransitions[ReservationState(from)]
	if !ok {
		return false
	}
	return allowed[ReservationState(to)]
}

// IsTerminalReservationState returns true when the given state admits no
// further transitions. Equivalent to "state is one of {converted, expired,
// cancelled}". Unknown states return false.
func IsTerminalReservationState(state string) bool {
	allowed, ok := ValidReservationTransitions[ReservationState(state)]
	return ok && len(allowed) == 0
}
