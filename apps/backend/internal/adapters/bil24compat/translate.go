// translate.go — legacy Bil24 ↔ platform UUID translation helpers.
//
// Legacy Bil24 uses opaque/numeric identifiers (actionId, actionEventId,
// orderId, ticketId, …). The platform uses UUIDv7. TranslateLegacyID
// accepts either a raw UUID string or a legacy non-UUID ID and maps it to
// a platform UUID.
//
// For this scaffold stage non-UUID IDs return ErrLegacyIDNotFound because
// the compatibility_id_map table is a follow-up feature. These functions
// are intentionally pure (no I/O) so they can be unit-tested without a
// database, and so the adapter package can be exercised by the
// httpserver-package tests via re-exported forwarders.

package bil24compat

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrLegacyIDNotFound is returned by TranslateLegacyID when the provided
// legacy identifier cannot be resolved to a platform UUID. This happens
// when the ID is non-UUID format and no entry exists in the compatibility
// table.
var ErrLegacyIDNotFound = errors.New("bil24compat: legacy ID not found in translation table")

// TranslateLegacyID converts a legacy Bil24 identifier (actionId,
// actionEventId, orderId, ticketId, …) to the platform's UUIDv7.
//
// Translation strategy:
//  1. If the raw string is already a valid UUID, return it unchanged. This
//     handles clients that have already been migrated to platform IDs.
//  2. Otherwise, attempt a future DB lookup (compatibility_id_map table).
//     For this scaffold, non-UUID IDs return ErrLegacyIDNotFound.
func TranslateLegacyID(raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, fmt.Errorf("bil24compat: empty legacy ID")
	}
	// Attempt direct UUID parse — handles clients already sending UUIDs.
	if id, err := uuid.Parse(raw); err == nil {
		return id, nil
	}
	// Non-UUID format: would require a DB lookup in the
	// compatibility_id_map table (a future feature). Return
	// ErrLegacyIDNotFound for now.
	return uuid.Nil, fmt.Errorf("%w: %q", ErrLegacyIDNotFound, raw)
}

// TranslatePlatformID converts a platform UUID to the Bil24 legacy ID
// format. For this scaffold, the UUID string is returned as-is since the
// platform uses UUID strings as the primary ID format.
func TranslatePlatformID(id uuid.UUID) string {
	return id.String()
}
