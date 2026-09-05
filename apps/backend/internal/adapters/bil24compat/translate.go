// translate.go — legacy Bil24 ↔ platform UUID translation helpers.
//
// Wave-1 (feature #476, spec §4) makes int64 the ONE-AND-ONLY external ID on
// the Bil24 wire: sites and widgets always send numeric IDs (either as JSON
// numbers or as strings containing a number, spec §7 preamble). Handlers
// resolve those numeric IDs to the platform's UUIDv7 via the
// compatibility_id_map table (see internal/platform/compatids).
//
// This file exposes two layers of translation helpers so the migration can be
// done incrementally:
//
//   - The pure `TranslateLegacyID(raw)` and `TranslatePlatformID(id)` helpers
//     from feature #157/#188 stay in place. They only understand UUID
//     strings (no DB lookup) and are used by the legacy tests. Handlers that
//     have not yet migrated to int64 keep calling these.
//   - `ParseLegacyIntID(raw)` and `ResolveLegacyIntID(ctx, db, kind, raw)`
//     are the wave-1 wire-compliant helpers. Together they:
//       1. Refuse UUID input outright (spec §7 preamble: response IDs are
//          numeric only; #476 mandates the same for request IDs).
//       2. Parse the raw string as a positive int64 (accepts a JSON number
//          that Go's decoder handed us as either float64/int64/string).
//       3. Reverse-map (kind, int64) → uuid via compatids.Resolve.
//
// Handlers that emit numeric IDs on the wire pair `ResolveLegacyIntID` for
// the request side with `compatids.Ensure` for the response side (inside the
// same tx per spec §4 lazy-mint rule) so a UUID never appears in either
// direction.

package bil24compat

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/compatids"
)

// ErrLegacyIDNotFound is returned by TranslateLegacyID when the provided
// legacy identifier cannot be resolved to a platform UUID. It is also
// wrapped by ResolveLegacyIntID when compatids returns ErrNotFound so
// callers only need to check for this one sentinel to map to Bil24
// result code -3 / 101 per spec §6.
var ErrLegacyIDNotFound = errors.New("bil24compat: legacy ID not found in translation table")

// ErrLegacyIDUUIDRejected is returned by ParseLegacyIntID (and by
// ResolveLegacyIntID) when a raw request field is a valid UUID string.
// Wave 1 (spec §4, feature #476) forbids UUIDs on the Bil24 wire: clients
// must send the int64 compatibility ids minted by compatibility_id_map.
// Callers map this sentinel to Bil24 result code -2 (invalid request).
var ErrLegacyIDUUIDRejected = errors.New("bil24compat: UUID identifiers are not accepted on the Bil24 wire (send int64)")

// ErrLegacyIDInvalid is returned by ParseLegacyIntID when the raw string is
// not empty, not a UUID, but also not a positive int64 (e.g. contains a
// letter, has a decimal point, is negative, or is zero). Maps to result
// code -2.
var ErrLegacyIDInvalid = errors.New("bil24compat: legacy ID must be a positive int64")

// TranslateLegacyID converts a legacy Bil24 identifier to a platform UUIDv7
// using only the legacy UUID-passthrough contract from feature #157. It
// returns ErrLegacyIDNotFound for non-UUID inputs — the DB lookup is done
// by ResolveLegacyIntID instead.
//
// New handler code SHOULD prefer ResolveLegacyIntID (which is spec-compliant
// per §4) over this function. The signature is preserved for the #157
// forwarders in httpserver and for callsites that have not yet migrated to
// the int64 wire.
func TranslateLegacyID(raw string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.Nil, fmt.Errorf("bil24compat: empty legacy ID")
	}
	// Attempt direct UUID parse — handles legacy clients still on the UUID wire.
	if id, err := uuid.Parse(raw); err == nil {
		return id, nil
	}
	// Non-UUID format: caller must use ResolveLegacyIntID for the DB path.
	return uuid.Nil, fmt.Errorf("%w: %q", ErrLegacyIDNotFound, raw)
}

// TranslatePlatformID converts a platform UUID to its wire form. The pure
// function returns the UUID string (legacy #157 contract). Handlers that
// have migrated to int64 output MUST NOT use this — they call
// compatids.Ensure(ctx, tx, kind, id) and emit the int64 instead.
//
// Kept for the #157/#188 layout sentinels and for the not-yet-migrated
// scan-ticket platformTicketId path.
func TranslatePlatformID(id uuid.UUID) string {
	return id.String()
}

// ParseLegacyIntID parses a Bil24 wire identifier as a positive int64.
//
// Spec §7 preamble: numeric IDs are accepted both as JSON numbers and as
// strings that contain a number ("2593277"). Handlers that receive the
// request via encoding/json see the string form for fields decoded into a
// `string` field on Request; this helper covers that form.
//
// Rules (feature #476 / spec §4):
//   - Empty string → ErrLegacyIDInvalid (caller-level check for missing key).
//   - Valid UUID (any of the four canonical variants) → ErrLegacyIDUUIDRejected.
//     Wave 1 forbids UUIDs on the wire; the site must send int64.
//   - Positive int64 (>= 1) → returned as-is.
//   - Everything else (letters, negative, zero, float, overflow) →
//     ErrLegacyIDInvalid.
func ParseLegacyIntID(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("%w: empty", ErrLegacyIDInvalid)
	}
	if _, err := uuid.Parse(raw); err == nil {
		return 0, fmt.Errorf("%w: %q", ErrLegacyIDUUIDRejected, raw)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrLegacyIDInvalid, raw, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%w: %d must be > 0", ErrLegacyIDInvalid, n)
	}
	return n, nil
}

// ResolveLegacyIntID resolves a Bil24 wire int64 identifier of the given kind
// to its platform UUID by looking it up in compatibility_id_map.
//
// This is the wave-1 spec-compliant translation entry point (spec §4). It
// MUST be called inside the same transaction that carries the enclosing
// gateway command so the read is consistent with any lazy-mint the same
// transaction performs on the response side.
//
// Error mapping (spec §6, feature #476):
//   - Empty / not int64 / UUID / non-positive input → ErrLegacyIDInvalid /
//     ErrLegacyIDUUIDRejected (map to Bil24 result code -2).
//   - Unknown mapping (compatids.ErrNotFound) → wrapped as ErrLegacyIDNotFound
//     (map to Bil24 result code -3 or 101 depending on command).
//   - Any other db failure surfaces the original error for the caller to log
//     and map to -1 / -99.
func ResolveLegacyIntID(ctx context.Context, db gen.DBTX, kind compatids.Kind, raw string) (uuid.UUID, error) {
	n, err := ParseLegacyIntID(raw)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := compatids.Resolve(ctx, db, kind, n)
	if err != nil {
		if errors.Is(err, compatids.ErrNotFound) {
			return uuid.Nil, fmt.Errorf("%w: kind=%s system_id=%d", ErrLegacyIDNotFound, kind, n)
		}
		return uuid.Nil, fmt.Errorf("bil24compat.ResolveLegacyIntID: kind=%s system_id=%d: %w", kind, n, err)
	}
	return id, nil
}
