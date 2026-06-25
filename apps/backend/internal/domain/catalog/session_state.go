// session_state.go contains the pure state-machine primitives for the
// Session aggregate (feature #183).
//
// These declarations have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for the Session status enum, its transition guard, and the
// Session-overlap predicate, callable from both the HTTP layer and the
// application orchestrators.
package catalog

import "time"

// SessionStatus enumerates the supported Session lifecycle states. The named
// type makes "wrong status at the call site" a compile-time concern at every
// domain boundary while remaining wire-format compatible with the existing
// JSON payloads ("draft" / "scheduled" / "cancelled" / "completed").
type SessionStatus string

const (
	// SessionStatusDraft is the initial state; the session is not yet bookable.
	SessionStatusDraft SessionStatus = "draft"
	// SessionStatusScheduled marks the session as live and bookable.
	SessionStatusScheduled SessionStatus = "scheduled"
	// SessionStatusCancelled marks the session as cancelled (refund/audit flows
	// still apply).
	SessionStatusCancelled SessionStatus = "cancelled"
	// SessionStatusCompleted is the terminal "happened" state.
	SessionStatusCompleted SessionStatus = "completed"
)

// ValidSessionStatuses lists the allowed Session status values. Exposed as a
// map so callers can use the idiomatic ok-pattern check
// `ValidSessionStatuses[SessionStatus(input)]`.
var ValidSessionStatuses = map[SessionStatus]bool{
	SessionStatusDraft:     true,
	SessionStatusScheduled: true,
	SessionStatusCancelled: true,
	SessionStatusCompleted: true,
}

// ValidSessionTransitions defines the allowed Session status transitions.
// Only the entries listed here are permitted; all others must be rejected
// with a 422 by the HTTP layer.
//
// Transitions:
//
//	draft     → scheduled | cancelled
//	scheduled → cancelled | completed
//	cancelled → (terminal)
//	completed → (terminal)
//
// The map is exported for read-only inspection but MUST NOT be mutated at
// runtime.
var ValidSessionTransitions = map[SessionStatus]map[SessionStatus]bool{
	SessionStatusDraft: {
		SessionStatusScheduled: true,
		SessionStatusCancelled: true,
	},
	SessionStatusScheduled: {
		SessionStatusCancelled: true,
		SessionStatusCompleted: true,
	},
	SessionStatusCancelled: {},
	SessionStatusCompleted: {},
}

// IsValidSessionStatus reports whether the given string is one of the
// recognised Session status values.
func IsValidSessionStatus(status string) bool {
	return ValidSessionStatuses[SessionStatus(status)]
}

// IsValidSessionTransition returns true when the transition from → to is
// allowed by ValidSessionTransitions. Unknown "from" values, unlisted "to"
// values, and identity transitions all return false.
//
// The function accepts plain strings (rather than SessionStatus) so the HTTP
// layer can call it directly with values parsed from JSON or the database
// without an extra conversion at every call site.
func IsValidSessionTransition(from, to string) bool {
	allowed, ok := ValidSessionTransitions[SessionStatus(from)]
	if !ok {
		return false
	}
	return allowed[SessionStatus(to)]
}

// SessionInterval is the pure-domain projection of a session needed to
// reason about temporal overlaps. The HTTP layer passes adapter row types
// (e.g. gen.SessionRow); the application/domain layer should call
// DetectOverlaps with a slice of SessionInterval to keep the domain free of
// adapter imports.
type SessionInterval struct {
	StartAt time.Time
	EndAt   time.Time
}

// DetectOverlaps returns true when any two intervals in the list overlap.
// Two intervals overlap when a.StartAt < b.EndAt AND a.EndAt > b.StartAt.
// This is an O(n²) check applied at the application layer per the feature
// spec; sessions of a single Event are expected to be few enough that the
// quadratic cost is negligible.
//
// The function is pure: it reads no clock, mutates no state, and returns the
// same answer for the same input every time.
func DetectOverlaps(intervals []SessionInterval) bool {
	for i := 0; i < len(intervals); i++ {
		for j := i + 1; j < len(intervals); j++ {
			a, b := intervals[i], intervals[j] //nolint:gosec // indices bounded by loop conditions above
			if a.StartAt.Before(b.EndAt) && a.EndAt.After(b.StartAt) {
				return true
			}
		}
	}
	return false
}
