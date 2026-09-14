// category_gate.go — the ONE "may this category still be sold?" check,
// shared by every entry point that creates a NEW hold.
//
// Plan 08_architecture/23 step 4 gives a ticket category two ways of being
// withdrawn from sale without touching its places:
//
//   - ticket_tiers.is_open = false — an operator closed it (migration
//     0101). Decision 4: the gateway wire has no "closed" flag, so a
//     closed category reports availability 0 and reads as sold out on the
//     site.
//   - ticket_tiers.sale_window_start / sale_window_end — decision 5 turns
//     the previously-unused columns into an automatic close by date.
//
// Both refuse only a NEW hold. Decision 1: an order already placed can
// still be paid, so ReacquireHoldTx (hold_shrink.go) and the gateway's
// PAY_ORDER deliberately do NOT call this — the places behind that order
// are already the buyer's.
//
// The check runs INSIDE the hold transaction, after the
// sessions.seat_status_version bump, so an operator closing a category
// concurrently either loses the race cleanly or wins it before the places
// are taken. It reads ticket_tiers only and takes no lock of its own, so
// it cannot disturb the platform-wide hold-mutation lock order
// (sessions → seats/ledger).
package hcheckout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Reasons a category may refuse a new hold. They are the errors.Is
// targets of *CategoryNotSellableError, so a caller can either test the
// sentinel or unwrap the struct for the tier id and the window bounds.
var (
	// ErrCategoryClosed — the category's is_open flag is false.
	ErrCategoryClosed = errors.New("hcheckout: ticket category is closed")
	// ErrCategoryNotOnSale — now is outside the category's sale window.
	ErrCategoryNotOnSale = errors.New("hcheckout: ticket category is not on sale")
	// ErrCategoryNotFound — the category does not exist, belongs to
	// another session, or has been soft-deleted.
	ErrCategoryNotFound = errors.New("hcheckout: ticket category not found")
)

// CategoryNotSellableError reports which category refused the hold and
// why. Start / End carry the stored sale window when the reason is
// ErrCategoryNotOnSale (either bound may be nil — only the violated one is
// guaranteed set).
type CategoryNotSellableError struct {
	TierID uuid.UUID
	Reason error
	Start  *time.Time
	End    *time.Time
	Now    time.Time
}

// Error implements the error interface.
func (e *CategoryNotSellableError) Error() string {
	switch {
	case errors.Is(e.Reason, ErrCategoryClosed):
		return fmt.Sprintf("hcheckout: category %s is closed", e.TierID)
	case errors.Is(e.Reason, ErrCategoryNotFound):
		return fmt.Sprintf("hcheckout: category %s not found in this session", e.TierID)
	case e.Start != nil && e.Now.Before(*e.Start):
		return fmt.Sprintf("hcheckout: category %s is not on sale until %s",
			e.TierID, e.Start.UTC().Format(time.RFC3339))
	case e.End != nil:
		return fmt.Sprintf("hcheckout: category %s stopped selling at %s",
			e.TierID, e.End.UTC().Format(time.RFC3339))
	default:
		return fmt.Sprintf("hcheckout: category %s is not on sale", e.TierID)
	}
}

// Is lets callers match the sentinel reasons with errors.Is.
func (e *CategoryNotSellableError) Is(target error) bool { return target == e.Reason }

// Unwrap exposes the reason so errors.Is walks to it as well.
func (e *CategoryNotSellableError) Unwrap() error { return e.Reason }

// CategorySellable reports whether a loaded category row may take a NEW
// hold at `now`. Split out from CheckCategorySellable so callers that
// already hold the row (availability projections) apply exactly the same
// rule as the sales paths.
func CategorySellable(t gen.TicketTierRow, now time.Time) error {
	if !t.IsOpen {
		return &CategoryNotSellableError{TierID: t.ID, Reason: ErrCategoryClosed, Now: now}
	}
	if (t.SaleWindowStart != nil && now.Before(*t.SaleWindowStart)) ||
		(t.SaleWindowEnd != nil && now.After(*t.SaleWindowEnd)) {
		return &CategoryNotSellableError{
			TierID: t.ID, Reason: ErrCategoryNotOnSale,
			Start: t.SaleWindowStart, End: t.SaleWindowEnd, Now: now,
		}
	}
	return nil
}

// CheckCategorySellable loads one category of a session and applies
// CategorySellable. A refusal is always a *CategoryNotSellableError; any
// other error is an infrastructure failure.
func CheckCategorySellable(ctx context.Context, txq *gen.Queries, sessionID, tierID uuid.UUID, now time.Time) error {
	if txq == nil {
		return errors.New("hcheckout: CheckCategorySellable requires queries")
	}
	t, err := txq.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &CategoryNotSellableError{TierID: tierID, Reason: ErrCategoryNotFound, Now: now}
		}
		return fmt.Errorf("hcheckout: load ticket category: %w", err)
	}
	return CategorySellable(t, now)
}

// CheckCategoriesSellable applies CheckCategorySellable to every distinct
// category id, in the order given, and returns the first refusal.
func CheckCategoriesSellable(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID, tierIDs []uuid.UUID, now time.Time) error {
	seen := make(map[uuid.UUID]struct{}, len(tierIDs))
	for _, id := range tierIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if err := CheckCategorySellable(ctx, txq, sessionID, id, now); err != nil {
			return err
		}
	}
	return nil
}

// CheckSeatCategoriesSellable applies the same gate to the categories of a
// set of seat rows (decision 10: a closed SEATED category refuses a new
// hold of its seats too). Seats with no category bound are skipped — there
// is no category to close.
func CheckSeatCategoriesSellable(ctx context.Context, txq *gen.Queries, sessionID uuid.UUID, seats []gen.SessionSeatRow, now time.Time) error {
	ids := make([]uuid.UUID, 0, len(seats))
	for _, s := range seats {
		if s.TierID != nil {
			ids = append(ids, *s.TierID)
		}
	}
	return CheckCategoriesSellable(ctx, txq, sessionID, ids, now)
}

// CategoryGateErrorCode maps a category-gate refusal onto the REST /
// widget error envelope code, or "" when err is not one.
func CategoryGateErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrCategoryClosed):
		return "tier.closed"
	case errors.Is(err, ErrCategoryNotOnSale):
		return "tier.not_on_sale"
	case errors.Is(err, ErrCategoryNotFound):
		return "tier.not_found"
	default:
		return ""
	}
}

// CategoryGateMessage is the human-readable half of CategoryGateErrorCode.
func CategoryGateMessage(err error) string {
	switch {
	case errors.Is(err, ErrCategoryClosed):
		return "ticket category is closed"
	case errors.Is(err, ErrCategoryNotOnSale):
		return "ticket category is not on sale right now"
	case errors.Is(err, ErrCategoryNotFound):
		return "ticket category not found in this session"
	default:
		return ""
	}
}

// CategoryGateTierID returns the category a gate refusal names, or
// uuid.Nil when err is not a gate refusal.
func CategoryGateTierID(err error) uuid.UUID {
	var gate *CategoryNotSellableError
	if errors.As(err, &gate) {
		return gate.TierID
	}
	return uuid.Nil
}

// writeCategoryGateError answers a REST / widget caller that hit the
// category gate: 409 tier.closed / tier.not_on_sale (the request is fine,
// the category simply is not selling) and 404 tier.not_found. Any other
// error is an infrastructure failure and answers 500.
func writeCategoryGateError(w http.ResponseWriter, r *http.Request, err error) {
	code := CategoryGateErrorCode(err)
	details := map[string]any{}
	if tid := CategoryGateTierID(err); tid != uuid.Nil {
		details["tier_id"] = tid.String()
	}
	switch code {
	case "":
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"reservation.tier_gate_failed", "failed to check the ticket category", r,
		))
	case "tier.not_found":
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelopeWithDetails(
			code, CategoryGateMessage(err), r, details,
		))
	default:
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			code, CategoryGateMessage(err), r, details,
		))
	}
}

// WriteCategoryGateError is the exported alias of writeCategoryGateError
// for hfeed, which serves the same envelopes on the widget surface.
func WriteCategoryGateError(w http.ResponseWriter, r *http.Request, err error) {
	writeCategoryGateError(w, r, err)
}
