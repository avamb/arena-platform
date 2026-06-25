// Package ids provides ID generation utilities for the arena platform.
//
// # ID Strategy: UUIDv7 (sortable, 128-bit)
//
// According to the technology stack decision in CLAUDE.md, UUIDv7 is the
// canonical ID type for all new entities. UUIDv7 encodes a Unix millisecond
// timestamp in the most-significant bits, yielding lexicographically sortable
// identifiers that cluster together in B-tree indexes and avoid index bloat.
//
// # PostgreSQL vs Go generation
//
// In SQL statements, prefer the native PostgreSQL uuidv7() function:
//
//	INSERT INTO events (id, ...) VALUES (uuidv7(), ...)
//	INSERT INTO orders (id, ...) SELECT uuidv7(), ...
//
// The DB-side function is preferred because it keeps ID generation close to
// the INSERT, participates naturally in CTEs, and avoids an extra round-trip.
//
// Use [NewUUIDv7] (this package) only when the ID must be known in Go before
// the INSERT executes — for example:
//   - Constructing idempotency keys derived from the entity ID.
//   - Building domain events that reference a not-yet-persisted aggregate ID.
//   - Unit tests that need deterministic IDs.
package ids

import (
	"fmt"

	"github.com/google/uuid"
)

// NewUUIDv7 generates a new time-ordered UUIDv7 using the Go-side generator.
//
// Sequential calls return monotonically ordered IDs: later calls produce IDs
// that sort after earlier ones, even within the same millisecond (an internal
// counter provides sub-millisecond monotonicity).
//
// The returned UUID has:
//   - Version bits set to 7 (0111).
//   - Variant bits set to 10 (RFC 4122).
//   - Bytes 0–5: 48-bit Unix timestamp in milliseconds (big-endian).
//
// Prefer using the native PostgreSQL uuidv7() function in SQL statements when
// the ID can be assigned by the database. Use this function only when the ID
// must be available in Go before the SQL INSERT executes.
func NewUUIDv7() (uuid.UUID, error) {
	return uuid.NewV7()
}

// MustNewUUIDv7 is like [NewUUIDv7] but panics on error.
//
// Suitable for package-level variable initialisation, test helpers, and any
// call site where an error is genuinely impossible (entropy exhaustion is
// system-fatal in practice).
func MustNewUUIDv7() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// allow:panic: MustNewUUIDv7 is the documented panic-on-error variant
		// of NewUUIDv7, intended for package-level var init and test helpers
		// where entropy exhaustion is treated as system-fatal. Request-path
		// code must use NewUUIDv7 and propagate the error.
		panic(fmt.Sprintf("ids: failed to generate UUIDv7: %v", err))
	}
	return id
}
