// Package compatids resolves and mints the bigint compatibility ids that the
// Bil24 wire protocol expects for arena catalog entities (spec
// 08_architecture/18_bil24_compat_wave1_specification_ru.md §3.1, §4).
//
// The package is the sole gatekeeper for compatibility_id_map: every read or
// write of the table goes through Ensure, EnsureMany, Resolve or
// RegisterExternal so the numeric-range invariant (arena ids >= 1e9, bil24
// ids < 1e9) and the "one platform_id ↔ one system_id" invariant hold at all
// times.
//
// All public functions accept a gen.DBTX so callers can pass either
// *pgxpool.Pool (outside a transaction) or a pgx.Tx (inside one). Lazy
// minting during a compat read MUST run inside the same transaction as the
// enclosing gateway command so a concurrent duplicate insert cannot separate
// mint from read.
package compatids

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Kind is the enum of catalog entity kinds carried by compatibility_id_map.
// Values match the CHECK constraint in migration 0090.
type Kind string

const (
	KindAction        Kind = "action"
	KindActionEvent   Kind = "action_event"
	KindCategoryPrice Kind = "category_price"
	KindVenue         Kind = "venue"
	KindCity          Kind = "city"
	KindCountry       Kind = "country"
)

// AllKinds enumerates every Kind accepted by the CHECK constraint in
// migration 0090. Kept in sync with the migration via the compatids
// unit tests.
var AllKinds = []Kind{
	KindAction,
	KindActionEvent,
	KindCategoryPrice,
	KindVenue,
	KindCity,
	KindCountry,
}

// externalIDCeiling is the exclusive upper bound for externally-registered
// (bil24-source) ids. compatibility_system_id_seq starts at 1e9 so any id
// at or above the ceiling would eventually collide with a locally-minted
// arena id.
const externalIDCeiling int64 = 1_000_000_000

// Sentinel errors — callers use errors.Is to differentiate cases and map to
// the Bil24 result codes documented in spec §6.
var (
	// ErrExternalIDOutOfRange signals a RegisterExternal attempt with a
	// system_id >= 1e9. Documented as compat.external_id_out_of_range in
	// spec §4.
	ErrExternalIDOutOfRange = errors.New("compat.external_id_out_of_range")

	// ErrNotFound is returned by Resolve when the (kind, system_id) pair is
	// unknown. Callers translate this to gateway result code -3 / 101.
	ErrNotFound = errors.New("compat.not_found")

	// ErrExternalIDCollision signals RegisterExternal was asked to insert a
	// system_id already claimed by a different platform_id (of the same kind).
	ErrExternalIDCollision = errors.New("compat.external_id_collision")

	// ErrExternalIDConflict signals RegisterExternal was asked to insert a
	// (kind, platform_id) already registered with a different system_id.
	ErrExternalIDConflict = errors.New("compat.external_id_conflict")

	// ErrUnknownKind is returned when a caller passes a Kind that is not one
	// of AllKinds.
	ErrUnknownKind = errors.New("compat.unknown_kind")
)

// ValidateKind returns ErrUnknownKind unless k is one of AllKinds.
func ValidateKind(k Kind) error {
	for _, allowed := range AllKinds {
		if k == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownKind, string(k))
}

// Ensure returns the system_id for (kind, platformID), minting a new
// arena-owned row from compatibility_system_id_seq on first sight. The
// returned id is always >= 1e9 for arena-source rows; for platform_ids that
// were previously RegisterExternal'd the returned id is the externally
// registered value (< 1e9).
//
// db must be a transactional handle when the caller cares about
// insert+read atomicity under concurrency (production callers always run
// this inside the request-scoped gateway tx).
func Ensure(ctx context.Context, db gen.DBTX, kind Kind, platformID uuid.UUID) (int64, error) {
	if err := ValidateKind(kind); err != nil {
		return 0, err
	}
	if platformID == uuid.Nil {
		return 0, fmt.Errorf("compatids.Ensure: platformID is uuid.Nil")
	}

	q := gen.New(db)
	inserted, err := q.EnsureCompatibilityID(ctx, string(kind), platformID)
	if err == nil {
		return inserted.SystemID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("compatids.Ensure: insert (%s, %s): %w", kind, platformID, err)
	}
	// Row already existed (ON CONFLICT DO NOTHING swallowed the insert).
	existing, err := q.GetCompatibilityIDByPlatformID(ctx, string(kind), platformID)
	if err != nil {
		return 0, fmt.Errorf("compatids.Ensure: read-after-conflict (%s, %s): %w", kind, platformID, err)
	}
	return existing.SystemID, nil
}

// EnsureMany batches Ensure for a slice of platform ids of the same kind.
// The returned map preserves the input order semantics: caller can index by
// platformID to get the system_id. Duplicate platformIDs in the input are
// deduplicated silently.
//
// The current implementation calls Ensure sequentially because
// compatibility_id_map is a very small hot table and each ON CONFLICT DO
// NOTHING is a single round trip. Batching to a single VALUES () statement
// is a future optimisation guarded by a benchmark, not a correctness need.
func EnsureMany(ctx context.Context, db gen.DBTX, kind Kind, platformIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if err := ValidateKind(kind); err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(platformIDs))
	for _, pid := range platformIDs {
		if _, ok := out[pid]; ok {
			continue
		}
		id, err := Ensure(ctx, db, kind, pid)
		if err != nil {
			return nil, err
		}
		out[pid] = id
	}
	return out, nil
}

// Resolve reverse-maps a (kind, systemID) pair to its platform uuid. Returns
// ErrNotFound when the mapping does not exist.
func Resolve(ctx context.Context, db gen.DBTX, kind Kind, systemID int64) (uuid.UUID, error) {
	if err := ValidateKind(kind); err != nil {
		return uuid.Nil, err
	}
	q := gen.New(db)
	row, err := q.GetCompatibilityIDBySystemID(ctx, string(kind), systemID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("%w: (%s, %d)", ErrNotFound, kind, systemID)
		}
		return uuid.Nil, fmt.Errorf("compatids.Resolve: (%s, %d): %w", kind, systemID, err)
	}
	return row.PlatformID, nil
}

// RegisterExternal records a Bil24-supplied system_id for a platform entity.
// Rejects system_id >= 1e9 with ErrExternalIDOutOfRange because that range
// is reserved for arena-minted ids (compatibility_system_id_seq starts at
// 1e9).
//
// Idempotent: re-registering the same (kind, platformID, systemID) triple
// returns nil. A conflict with a DIFFERENT system_id for the same platformID
// returns ErrExternalIDConflict; a conflict with a DIFFERENT platformID for
// the same systemID returns ErrExternalIDCollision.
func RegisterExternal(ctx context.Context, db gen.DBTX, kind Kind, platformID uuid.UUID, systemID int64) error {
	if err := ValidateKind(kind); err != nil {
		return err
	}
	if platformID == uuid.Nil {
		return fmt.Errorf("compatids.RegisterExternal: platformID is uuid.Nil")
	}
	if systemID <= 0 {
		return fmt.Errorf("compatids.RegisterExternal: systemID must be positive (got %d)", systemID)
	}
	if systemID >= externalIDCeiling {
		return fmt.Errorf("%w: kind=%s system_id=%d ceiling=%d", ErrExternalIDOutOfRange, kind, systemID, externalIDCeiling)
	}

	q := gen.New(db)
	_, err := q.RegisterExternalCompatibilityID(ctx, string(kind), platformID, systemID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("compatids.RegisterExternal: insert (%s, %s, %d): %w", kind, platformID, systemID, err)
	}
	// Conflict: differentiate collision (system_id taken by another
	// platform_id) from re-registration (same platform_id, same system_id) or
	// conflict (same platform_id, different system_id).
	byPID, pidErr := q.GetCompatibilityIDByPlatformID(ctx, string(kind), platformID)
	switch {
	case pidErr == nil && byPID.SystemID == systemID:
		return nil // idempotent
	case pidErr == nil:
		return fmt.Errorf("%w: kind=%s platform_id=%s existing_system_id=%d requested_system_id=%d",
			ErrExternalIDConflict, kind, platformID, byPID.SystemID, systemID)
	case !errors.Is(pidErr, pgx.ErrNoRows):
		return fmt.Errorf("compatids.RegisterExternal: read-after-conflict (%s, %s): %w", kind, platformID, pidErr)
	}
	// Platform_id side is free — must be a system_id collision.
	bySID, sidErr := q.GetCompatibilityIDBySystemID(ctx, string(kind), systemID)
	if sidErr != nil {
		return fmt.Errorf("compatids.RegisterExternal: read-after-conflict (%s, %d): %w", kind, systemID, sidErr)
	}
	return fmt.Errorf("%w: kind=%s system_id=%d claimed_by_platform_id=%s requested_platform_id=%s",
		ErrExternalIDCollision, kind, systemID, bySID.PlatformID, platformID)
}
