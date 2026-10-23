package seatlayout

import (
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Outcome of one seat inside a batch.
const (
	OutcomeApplied = "applied"
	OutcomeNoop    = "noop"
	OutcomeSkipped = "skipped"
)

// Reasons a batch leaves a seat alone. A skipped seat never fails the
// batch (unless the caller asked for Strict).
const (
	// ReasonSold — the seat is sold (in arena, or upstream before import).
	// Its price is the price it was sold at; nothing may change it.
	ReasonSold = "sold"
	// ReasonHeld — the seat sits in a buyer's hold.
	ReasonHeld = "held"
	// ReasonNotFound — no session_seats row has this key.
	ReasonNotFound = "seat_not_found"
	// ReasonUnknownStatus — the row is in a status the batch does not know.
	ReasonUnknownStatus = "unknown_status"
	// ReasonGAUnit — a General Admission place cannot take another
	// category through a batch (its category owns it, plan 23); opening
	// and closing it is allowed, as the block/unblock route always did.
	ReasonGAUnit = "ga_unit"
	// ReasonChangedSince — a revert finds the seat no longer in the state
	// the reverted batch left it in (a later batch moved it).
	ReasonChangedSince = "changed_since"
	// ReasonTierUnavailable — a revert would put the seat back into a
	// category that has since been deleted.
	ReasonTierUnavailable = "tier_unavailable"
)

// SeatState is a seat's category and status, before or after a batch.
type SeatState struct {
	TierID *uuid.UUID `json:"tier_id"`
	Status string     `json:"status"`
}

// Equal reports whether two states name the same category and status.
func (s SeatState) Equal(o SeatState) bool {
	if s.Status != o.Status {
		return false
	}
	if s.TierID == nil || o.TierID == nil {
		return s.TierID == nil && o.TierID == nil
	}
	return *s.TierID == *o.TierID
}

// Target is what one seat should become. SetTier distinguishes "set the
// category to TierID (which may be nil)" from "keep the category"; Status
// nil keeps the status. Expect, when set, is the state the seat must still
// be in for the target to apply (a revert).
type Target struct {
	SeatKey string
	SetTier bool
	TierID  *uuid.UUID
	Status  *string
	Expect  *SeatState
}

// Result is the decision for one seat.
type Result struct {
	SeatKey string
	Outcome string
	Reason  string
	From    SeatState
	To      SeatState
}

func stateOf(row *gen.SessionSeatRow) SeatState {
	return SeatState{TierID: row.TierID, Status: row.Status}
}

// Decide is the batch rule for one seat, a pure function of the seat's row
// as read under the batch's lock (nil when the key resolved to no row) and
// the target. tierOK reports whether a category may be assigned; ApplyBatch
// refuses an unknown category before deciding, so it only says no during a
// revert.
//
// Order of the checks: a missing row, then a frozen seat (sold, held, an
// unknown status), then a revert's precondition, then the GA rule and the
// category, then noop versus applied.
func Decide(row *gen.SessionSeatRow, t Target, tierOK func(uuid.UUID) bool) Result {
	res := Result{SeatKey: t.SeatKey}
	if row == nil {
		res.Outcome, res.Reason = OutcomeSkipped, ReasonNotFound
		return res
	}
	cur := stateOf(row)
	res.From, res.To = cur, cur
	switch row.Status {
	case StatusAvailable, StatusUnavailable:
	case "sold":
		res.Outcome, res.Reason = OutcomeSkipped, ReasonSold
		return res
	case "held":
		res.Outcome, res.Reason = OutcomeSkipped, ReasonHeld
		return res
	default:
		res.Outcome, res.Reason = OutcomeSkipped, ReasonUnknownStatus
		return res
	}
	if t.Expect != nil && !cur.Equal(*t.Expect) {
		res.Outcome, res.Reason = OutcomeSkipped, ReasonChangedSince
		return res
	}
	want := cur
	if t.SetTier {
		want.TierID = t.TierID
	}
	if t.Status != nil {
		want.Status = *t.Status
	}
	tierMoves := !SeatState{TierID: want.TierID, Status: cur.Status}.Equal(SeatState{TierID: cur.TierID, Status: cur.Status})
	if tierMoves && row.Kind == "ga_unit" {
		res.Outcome, res.Reason = OutcomeSkipped, ReasonGAUnit
		return res
	}
	if tierMoves && want.TierID != nil && tierOK != nil && !tierOK(*want.TierID) {
		res.Outcome, res.Reason = OutcomeSkipped, ReasonTierUnavailable
		return res
	}
	res.To = want
	if want.Equal(cur) {
		res.Outcome = OutcomeNoop
		return res
	}
	res.Outcome = OutcomeApplied
	return res
}
