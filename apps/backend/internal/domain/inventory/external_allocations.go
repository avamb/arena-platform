// external_allocations.go contains the pure state-machine primitives for
// the ExternalAllocation aggregate (feature #184).
//
// These declarations have NO dependencies beyond the standard library, NO
// persistence side effects, and NO time-of-day reads. They are the canonical
// home for the allocation status enum, its transition guard, and the
// terminal-state predicate, callable from both the HTTP layer
// (POST/GET/PATCH external-allocations endpoints) and the partner-portal
// reconciliation workflow.
package inventory

// AllocationStatus enumerates the supported ExternalAllocation lifecycle
// statuses. Storing the strings as a named type makes "wrong status at the
// call site" a compile-time concern at every domain boundary, while
// remaining wire-format compatible with the existing JSON payloads
// ("pending", "active", "reconciled", "disputed").
type AllocationStatus string

const (
	// AllocationStatusPending is the initial status of a freshly-created
	// allocation. No platform inventory has been reserved yet.
	AllocationStatusPending AllocationStatus = "pending"
	// AllocationStatusActive marks an allocation as holding platform
	// inventory; the partner organisation may sell tickets against it.
	AllocationStatusActive AllocationStatus = "active"
	// AllocationStatusReconciled is a terminal status: consumed quota has
	// been settled (held → sold) and the remainder released.
	AllocationStatusReconciled AllocationStatus = "reconciled"
	// AllocationStatusDisputed marks an allocation where consumption is
	// contested; inventory remains held until reconciliation.
	AllocationStatusDisputed AllocationStatus = "disputed"
)

// ValidAllocationTransitions defines the allowed ExternalAllocation status
// transitions. Only the entries listed here are permitted; all others must
// be rejected with a 409 by the HTTP layer.
//
// Transitions:
//
//	pending    → active | reconciled        (direct-reconcile is an edge case)
//	active     → reconciled | disputed
//	disputed   → reconciled
//	reconciled → (terminal)
//
// The map is exported for read-only inspection (e.g. by tests) but MUST NOT
// be mutated at runtime; treat it as a compile-time constant.
var ValidAllocationTransitions = map[AllocationStatus]map[AllocationStatus]bool{
	AllocationStatusPending: {
		AllocationStatusActive:     true,
		AllocationStatusReconciled: true,
	},
	AllocationStatusActive: {
		AllocationStatusReconciled: true,
		AllocationStatusDisputed:   true,
	},
	AllocationStatusDisputed: {
		AllocationStatusReconciled: true,
	},
	AllocationStatusReconciled: {},
}

// AllAllocationStatuses is the complete set of valid ExternalAllocation
// statuses in canonical order (matches the wire enum order used by the
// OpenAPI contract). The slice is exported so tests and dispatch tables can
// iterate it without referencing implementation details.
var AllAllocationStatuses = []AllocationStatus{
	AllocationStatusPending,
	AllocationStatusActive,
	AllocationStatusReconciled,
	AllocationStatusDisputed,
}

// IsValidAllocationStatus returns true when the given string is one of the
// values in AllAllocationStatuses.
func IsValidAllocationStatus(status string) bool {
	_, ok := ValidAllocationTransitions[AllocationStatus(status)]
	return ok
}

// IsTerminalAllocationStatus returns true when the given status admits no
// further transitions. Unknown statuses return false (callers should treat
// them as invalid, not terminal).
func IsTerminalAllocationStatus(status string) bool {
	allowed, ok := ValidAllocationTransitions[AllocationStatus(status)]
	return ok && len(allowed) == 0
}

// IsValidAllocationTransition returns true when the transition from → to is
// allowed by ValidAllocationTransitions. Unknown "from" values and unlisted
// "to" values both return false. Identity transitions (from == to) return
// false as well, since the map intentionally never lists a self-loop.
//
// The function accepts plain strings (rather than AllocationStatus) so the
// HTTP layer can call it directly with values parsed from JSON or the
// database without an extra conversion at every call site.
func IsValidAllocationTransition(from, to string) bool {
	allowed, ok := ValidAllocationTransitions[AllocationStatus(from)]
	if !ok {
		return false
	}
	return allowed[AllocationStatus(to)]
}
