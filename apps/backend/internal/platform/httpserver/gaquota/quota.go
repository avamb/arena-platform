// Package gaquota is the single place in arena where a General Admission
// category's quota is created, resized, opened, closed and removed.
//
// Model (plan 08_architecture/23, migration 0101). A GA category OWNS its
// places:
//
//   - ticket_tiers.capacity is the quantity;
//   - the places are the session_seats rows of kind='ga_unit' carrying the
//     category's tier_id, keyed 'ga|t<unit_seq>|<n>';
//   - sessions.capacity_total is ALWAYS the number of places of the session
//     (plan seats plus GA places) and is never edited on its own;
//   - the session-level inventory_ledger row (tier_id IS NULL) mirrors it.
//     Per-category ledger rows do not exist in this model.
//
// The pre-0101 shape — one fungible 'ga|pool|<n>' batch with tier_id NULL,
// stamped on hold and reset on release — is gone: it kept the truth about
// quantity in two places that nothing held equal.
//
// # Transactions and lock order
//
// Every exported mutation takes a *gen.Queries ALREADY BOUND to an open
// transaction owned by the caller (txq). The caller's transaction MUST
// follow the platform-wide hold-mutation lock order, and these functions
// take their locks in exactly that order:
//
//	sessions row (IncrementSessionSeatStatusVersion) FIRST
//	  → inventory_ledger row
//	    → GA place rows (session_seats, kind='ga_unit')
//
// and it must NEVER touch a kind='seat' row after the ledger: the seated
// hold path locks sessions → kind='seat' rows FOR UPDATE → inventory_ledger,
// so doing it the other way round on a hybrid session deadlocks against a
// concurrent seated hold. Nothing here ever locks a seat row; the seated
// counters it reads are plain unlocked counts.
//
// Callers without a transaction of their own use InTx, which opens one and
// retries the whole body on a 40P01 / 40001 race.
package gaquota

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Admission modes touched by the quota mechanism.
const (
	admissionAssignedSeats = "assigned_seats"
	admissionHybrid        = "hybrid"
)

// unitKeyPrefix builds a category's own GA place key prefix from its stable
// per-session number: 'ga|t3' → places 'ga|t3|000001', 'ga|t3|000002', …
//
// The prefix is deliberately NOT 'ga|c': that belongs to the seating-plan
// geometry category index ('ga|c<index>|<n>'), and on a session bound to a
// plan a hand-added category's number would collide with a geometry index
// under UNIQUE (session_id, seat_key).
func unitKeyPrefix(unitSeq int32) string {
	return fmt.Sprintf("ga|t%d", unitSeq)
}

// toInt32 narrows a COUNT(*) bigint to the int32 width the capacity columns
// use. Every count here is bounded by the places of a single session, which
// sessions.capacity_total (an integer column) already caps, so the clamp is
// a guard rather than a reachable path.
func toInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrInvalidQuantity is returned when a quantity is not positive.
	// A GA category with a quantity of 0 is illegal (decision 3, and the
	// ticket_tiers_capacity_positive CHECK).
	ErrInvalidQuantity = errors.New("gaquota: quantity must be positive")

	// ErrSessionNotFound is returned when the session does not exist or has
	// been soft-deleted.
	ErrSessionNotFound = errors.New("gaquota: session not found")

	// ErrCategoryNotFound is returned when the category does not exist,
	// belongs to another session, or has been soft-deleted.
	ErrCategoryNotFound = errors.New("gaquota: category not found")

	// ErrSeatedCategory is returned for a category whose places come from
	// the plan geometry (it owns kind='seat' rows). Its quantity is
	// read-only and it cannot be deleted through the quota mechanism;
	// price and the open/closed flag still work. Handlers map this to
	// 409 tier.seated_category.
	ErrSeatedCategory = errors.New("gaquota: seated category quantity is read-only")

	// ErrCategoryInUse is returned when a category still holds or has sold
	// places, or has ever issued an active ticket, and may therefore only
	// be closed, never removed (decision 2).
	ErrCategoryInUse = errors.New("gaquota: category has held or sold places")
)

// BelowUsedError reports a quantity change refused because it would drop a
// category below the places it cannot give up.
//
// Used is what the category currently holds and has sold. Deletable is how
// many of its available places could actually be removed — smaller than
// "available" when some are still referenced by a cascade-less
// reservation_seats / order_items row and therefore undeletable; in that
// case Floor (Total − Deletable) is the real lower bound.
type BelowUsedError struct {
	TierID    uuid.UUID
	Requested int32
	Used      int32
	Total     int32
	Deletable int32
}

// Floor is the lowest quantity this category can currently be set to.
func (e *BelowUsedError) Floor() int32 { return e.Total - e.Deletable }

func (e *BelowUsedError) Error() string {
	return fmt.Sprintf(
		"gaquota: quantity %d is below the %d places category %s cannot give up (used %d, deletable %d of %d)",
		e.Requested, e.Floor(), e.TierID, e.Used, e.Deletable, e.Total)
}

// ─────────────────────────────────────────────────────────────────────────────
// Kinds and stats
// ─────────────────────────────────────────────────────────────────────────────

// Kind classifies a category. It is derived, never stored: a category is
// seated exactly when it owns kind='seat' rows.
type Kind string

const (
	// KindSeated — the category's places come from the plan geometry. Its
	// quantity is read-only; placement is true on the wire.
	KindSeated Kind = "seated"
	// KindGA — the category owns GA places. Its quantity is editable;
	// placement is false on the wire.
	KindGA Kind = "ga"
)

// Stats is one category's place counters.
type Stats struct {
	TierID uuid.UUID
	// Quantity is the number of places the category owns — held, sold and
	// available together. The quota mechanism keeps ticket_tiers.capacity
	// equal to it.
	Quantity int32
	Held     int32
	Sold     int32
	// Available is what a buyer may still take (before the open/closed
	// flag and the sale window, which the sales paths apply).
	Available int32
}

// Result is what a quantity change did.
type Result struct {
	TierID uuid.UUID
	// Before / After are the number of places the category owned.
	Before int32
	After  int32
	// Added / Removed are the places minted and removed by this call.
	Added   int32
	Removed int32
	// SessionCapacity is the session's recomputed capacity_total.
	SessionCapacity int32
}

// ─────────────────────────────────────────────────────────────────────────────
// Reads
// ─────────────────────────────────────────────────────────────────────────────

// CategoryKind reports whether a category is seated (its places come from
// the plan geometry) or GA. Returns ErrCategoryNotFound when the category
// is not an active category of this session.
func CategoryKind(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID) (Kind, error) {
	if txq == nil {
		return "", errors.New("gaquota: nil queries")
	}
	if _, err := txq.GetTicketTierByID(ctx, tierID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrCategoryNotFound
		}
		return "", fmt.Errorf("gaquota: load category: %w", err)
	}
	seats, err := txq.CountSeatRowsForTier(ctx, sessionID, tierID)
	if err != nil {
		return "", fmt.Errorf("gaquota: count seats of category: %w", err)
	}
	if seats > 0 {
		return KindSeated, nil
	}
	return KindGA, nil
}

// SessionStats returns the place counters of every category of a session
// that owns at least one GA place, keyed by category id. A category with no
// GA place at all (a purely seated one, or one whose places were never
// materialized) is absent from the map; callers treat that as a zero Stats.
func SessionStats(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) (map[uuid.UUID]Stats, error) {
	if txq == nil {
		return nil, errors.New("gaquota: nil queries")
	}
	rows, err := txq.ListGAUnitStatsBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("gaquota: read category place counters: %w", err)
	}
	out := make(map[uuid.UUID]Stats, len(rows))
	for _, r := range rows {
		out[r.TierID] = Stats{
			TierID:    r.TierID,
			Quantity:  toInt32(r.Total),
			Held:      toInt32(r.Held),
			Sold:      toInt32(r.Sold),
			Available: toInt32(r.Available),
		}
	}
	return out, nil
}

// CategoryStats returns one category's place counters. A category with no
// GA place yields a zero-valued Stats, not an error.
func CategoryStats(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID) (Stats, error) {
	all, err := SessionStats(ctx, txq, sessionID)
	if err != nil {
		return Stats{}, err
	}
	if s, ok := all[tierID]; ok {
		return s, nil
	}
	return Stats{TierID: tierID}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Mutations
// ─────────────────────────────────────────────────────────────────────────────

// CreateCategory materializes the places of a category that the caller has
// ALREADY inserted into ticket_tiers, and brings the session capacity and
// ledger in line.
//
// It assigns the category's stable per-session number (unit_seq) when it has
// none — max over ALL categories of the session, soft-deleted included, plus
// one, so a number is never reused — mints `quantity` places under
// 'ga|t<unit_seq>|<n>', writes the quantity back to ticket_tiers.capacity
// and recomputes capacity_total plus the session-level ledger row.
//
// A category may be added at any time, including on a session that is
// already selling (decision 9): existing places of other categories are not
// touched and the session capacity simply grows by the new quantity.
//
// On an assigned_seats session the new category is, by definition, a GA one
// (tickets without a seat), so the session becomes hybrid in the SAME
// transaction (decision 10). The reverse transition is never performed.
func CreateCategory(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID, quantity int32) error {
	if quantity <= 0 {
		return ErrInvalidQuantity
	}
	if txq == nil {
		return errors.New("gaquota: nil queries")
	}

	// Lock order step 1 — the sessions row, before anything else. It is
	// also the serialization point: every read below happens UNDER this
	// row lock, so two concurrent quota changes on one session cannot
	// decide from the same stale place counts.
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}

	sess, err := txq.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("gaquota: load session: %w", err)
	}
	tier, err := txq.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("gaquota: load category: %w", err)
	}

	// Decision 10: the first GA category turns a seated session hybrid.
	// Done under the sessions row lock just taken.
	if sess.AdmissionMode == admissionAssignedSeats {
		if err := txq.SetSessionAdmissionMode(ctx, sessionID, admissionHybrid); err != nil {
			return fmt.Errorf("gaquota: switch session to hybrid: %w", err)
		}
	}

	seq, err := ensureUnitSeq(ctx, txq, sessionID, tier)
	if err != nil {
		return err
	}
	if err := mintPlaces(ctx, txq, sessionID, tierID, seq, quantity, version); err != nil {
		return err
	}
	if _, err := txq.SetTicketTierCapacity(ctx, tierID, sessionID, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("gaquota: write category quantity: %w", err)
	}
	_, err = recompute(ctx, txq, sessionID)
	return err
}

// SetQuantity changes how many places a GA category owns.
//
// Growing mints the difference under the category's own key prefix.
// Shrinking removes its highest-keyed AVAILABLE places, skipping any still
// referenced by a cascade-less reservation_seats / order_items row (deleting
// one of those fails with 23503). A request below what the category cannot
// give up is refused with *BelowUsedError carrying both the used count and
// the real floor. A seated category is refused with ErrSeatedCategory.
func SetQuantity(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID, quantity int32) (Result, error) {
	if quantity <= 0 {
		return Result{}, ErrInvalidQuantity
	}
	if txq == nil {
		return Result{}, errors.New("gaquota: nil queries")
	}

	// Lock order step 1 — the sessions row, before anything else. It is
	// also the serialization point: every read below happens UNDER this
	// row lock, so a concurrent quota change (or a hold, which takes the
	// same lock first) can never make this one decide from stale place
	// counts and then write a quantity the places no longer match.
	version, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrSessionNotFound
		}
		return Result{}, fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}

	tier, err := txq.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrCategoryNotFound
		}
		return Result{}, fmt.Errorf("gaquota: load category: %w", err)
	}
	seats, err := txq.CountSeatRowsForTier(ctx, sessionID, tierID)
	if err != nil {
		return Result{}, fmt.Errorf("gaquota: count seats of category: %w", err)
	}
	if seats > 0 {
		return Result{}, ErrSeatedCategory
	}

	stats, err := CategoryStats(ctx, txq, sessionID, tierID)
	if err != nil {
		return Result{}, err
	}
	used := stats.Held + stats.Sold
	if quantity < used {
		deletable, derr := txq.CountDeletableGAUnitsForTier(ctx, sessionID, tierID)
		if derr != nil {
			return Result{}, fmt.Errorf("gaquota: count deletable places: %w", derr)
		}
		return Result{}, &BelowUsedError{
			TierID: tierID, Requested: quantity, Used: used,
			Total: stats.Quantity, Deletable: toInt32(deletable),
		}
	}

	res := Result{TierID: tierID, Before: stats.Quantity, After: quantity}

	switch {
	case quantity > stats.Quantity:
		add := quantity - stats.Quantity
		seq, serr := ensureUnitSeq(ctx, txq, sessionID, tier)
		if serr != nil {
			return Result{}, serr
		}
		if err := mintPlaces(ctx, txq, sessionID, tierID, seq, add, version); err != nil {
			return Result{}, err
		}
		res.Added = add

	case quantity < stats.Quantity:
		drop := stats.Quantity - quantity
		removed, derr := txq.DeleteAvailableGAUnitsForTier(ctx, sessionID, tierID, drop)
		if derr != nil {
			return Result{}, fmt.Errorf("gaquota: remove places: %w", derr)
		}
		if toInt32(removed) != drop {
			// Not enough deletable places: the rest are referenced by a
			// cascade-less join row and can never be removed. Refuse the
			// whole change; the caller's transaction rolls back.
			deletable, cerr := txq.CountDeletableGAUnitsForTier(ctx, sessionID, tierID)
			if cerr != nil {
				return Result{}, fmt.Errorf("gaquota: count deletable places: %w", cerr)
			}
			return Result{}, &BelowUsedError{
				TierID: tierID, Requested: quantity, Used: used,
				Total: stats.Quantity, Deletable: toInt32(removed) + toInt32(deletable),
			}
		}
		res.Removed = drop
	}

	if _, err := txq.SetTicketTierCapacity(ctx, tierID, sessionID, quantity); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrCategoryNotFound
		}
		return Result{}, fmt.Errorf("gaquota: write category quantity: %w", err)
	}

	capTotal, err := recompute(ctx, txq, sessionID)
	if err != nil {
		return Result{}, err
	}
	res.SessionCapacity = capTotal
	return res, nil
}

// SetOpen opens or closes a category. A closed category accepts no NEW
// holds (the gates themselves live on the sales paths); places already held
// or sold are untouched and an order already placed can still be paid
// (decision 1). Works for seated categories too (decision 10).
//
// The flag changes no place and no capacity, but it does change what a seat
// map shows, so the session's seat_status_version is bumped as well — the
// same first-statement lock the other mutations take.
func SetOpen(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID, open bool) error {
	if txq == nil {
		return errors.New("gaquota: nil queries")
	}
	if _, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID); err != nil {
		return fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}
	if _, err := txq.SetTicketTierOpen(ctx, tierID, sessionID, open); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("gaquota: write category open flag: %w", err)
	}
	return nil
}

// DeleteCategory removes a GA category together with its places and
// soft-deletes the ticket_tiers row.
//
// Refused with ErrSeatedCategory for a seated category, and with
// ErrCategoryInUse when the category still holds or has sold a place or has
// an active ticket — such a category may only be closed (decision 2). Its
// number (unit_seq) stays retired so its old place keys can never be minted
// again.
func DeleteCategory(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID) error {
	if txq == nil {
		return errors.New("gaquota: nil queries")
	}
	// Lock order step 1 — the sessions row, before anything else, so the
	// "nothing held or sold" decision below cannot race a hold.
	if _, err := txq.IncrementSessionSeatStatusVersion(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionNotFound
		}
		return fmt.Errorf("gaquota: bump seat_status_version: %w", err)
	}

	if _, err := txq.GetTicketTierByID(ctx, tierID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("gaquota: load category: %w", err)
	}
	seats, err := txq.CountSeatRowsForTier(ctx, sessionID, tierID)
	if err != nil {
		return fmt.Errorf("gaquota: count seats of category: %w", err)
	}
	if seats > 0 {
		return ErrSeatedCategory
	}

	stats, err := CategoryStats(ctx, txq, sessionID, tierID)
	if err != nil {
		return err
	}
	if stats.Held > 0 || stats.Sold > 0 {
		return ErrCategoryInUse
	}
	tickets, err := txq.CountActiveTicketsForTier(ctx, sessionID, tierID)
	if err != nil {
		return fmt.Errorf("gaquota: count tickets of category: %w", err)
	}
	if tickets > 0 {
		return ErrCategoryInUse
	}

	if stats.Quantity > 0 {
		removed, derr := txq.DeleteAvailableGAUnitsForTier(ctx, sessionID, tierID, stats.Quantity)
		if derr != nil {
			return fmt.Errorf("gaquota: remove places: %w", derr)
		}
		if toInt32(removed) != stats.Quantity {
			// Some places are still referenced by a cascade-less join row.
			// They would be orphaned by the soft delete, so refuse.
			return ErrCategoryInUse
		}
	}

	if _, err := txq.SoftDeleteTicketTier(ctx, tierID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return fmt.Errorf("gaquota: soft-delete category: %w", err)
	}
	_, err = recompute(ctx, txq, sessionID)
	return err
}

// Recompute brings a session's derived capacity back in line with its
// places: sessions.capacity_total = plan seats + GA places, and the
// session-level inventory_ledger row (tier_id IS NULL) to the same number,
// never below capacity_held + capacity_sold (the inventory_ledger_invariant
// CHECK). The ledger row is created when the session has none.
//
// Every mutation in this package ends with it; it is exported so a caller
// that fixes places by hand can restore the invariant the same way.
func Recompute(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) error {
	if txq == nil {
		return errors.New("gaquota: nil queries")
	}
	_, err := recompute(ctx, txq, sessionID)
	return err
}

func recompute(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID) (int32, error) {
	places, err := txq.CountSessionPlaces(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("gaquota: count session places: %w", err)
	}
	total := toInt32(places)
	if total > 0 {
		// sessions_capacity_total_check refuses 0; a session whose last
		// place just went keeps its previous capacity until a category is
		// added back.
		if err := txq.SetSessionCapacityTotal(ctx, sessionID, total); err != nil {
			return 0, fmt.Errorf("gaquota: write session capacity: %w", err)
		}
	}

	ledger, err := txq.GetInventoryLedger(ctx, sessionID, nil)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if total <= 0 {
			return total, nil
		}
		if _, err := txq.InsertInventoryLedger(ctx, sessionID, nil, &total); err != nil {
			return 0, fmt.Errorf("gaquota: create session ledger row: %w", err)
		}
		return total, nil
	case err != nil:
		return 0, fmt.Errorf("gaquota: load session ledger row: %w", err)
	}

	newTotal := total
	if floor := ledger.CapacityHeld + ledger.CapacitySold; newTotal < floor {
		newTotal = floor
	}
	if newTotal <= 0 {
		// inventory_ledger_total_positive refuses 0; leave the row alone.
		return total, nil
	}
	if _, err := txq.UpdateCapacityTotal(ctx, sessionID, nil, &newTotal); err != nil {
		return 0, fmt.Errorf("gaquota: write session ledger capacity: %w", err)
	}
	return total, nil
}

// ensureUnitSeq returns the category's stable per-session number, assigning
// one when the row has none: max over ALL categories of the session,
// soft-deleted included, plus one.
func ensureUnitSeq(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID, tier gen.TicketTierRow) (int32, error) {
	if tier.UnitSeq != nil && *tier.UnitSeq > 0 {
		return *tier.UnitSeq, nil
	}
	maxSeq, err := txq.MaxTicketTierUnitSeq(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("gaquota: read highest category number: %w", err)
	}
	seq, err := txq.AssignTicketTierUnitSeq(ctx, tier.ID, sessionID, maxSeq+1)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Someone numbered it between the read and the write; re-read.
			fresh, gerr := txq.GetTicketTierByID(ctx, tier.ID, sessionID)
			if gerr != nil {
				return 0, fmt.Errorf("gaquota: re-read category number: %w", gerr)
			}
			if fresh.UnitSeq == nil {
				return 0, errors.New("gaquota: category has no number after assignment")
			}
			return *fresh.UnitSeq, nil
		}
		return 0, fmt.Errorf("gaquota: assign category number: %w", err)
	}
	return seq, nil
}

// mintPlaces inserts quantity new available places for a category,
// continuing the index sequence already used under its key prefix.
func mintPlaces(
	ctx context.Context,
	txq *gen.Queries,
	sessionID, tierID uuid.UUID,
	unitSeq, quantity int32,
	statusVersion int64,
) error {
	if quantity <= 0 {
		return nil
	}
	prefix := unitKeyPrefix(unitSeq)
	start, err := txq.MaxGAUnitIndexForTierPrefix(ctx, sessionID, prefix)
	if err != nil {
		return fmt.Errorf("gaquota: read highest place index: %w", err)
	}
	inserted, err := txq.InsertGAUnitsForTier(ctx, sessionID, prefix, start, tierID, quantity, statusVersion)
	if err != nil {
		return fmt.Errorf("gaquota: mint places: %w", err)
	}
	if toInt32(inserted) != quantity {
		return fmt.Errorf("gaquota: minted %d places, wanted %d", inserted, quantity)
	}
	return nil
}
