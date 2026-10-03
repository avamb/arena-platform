// Package sessionchange is the ONE place that decides whether saving a session
// changes something a buyer can see, and that tells the buyers when it does
// (08_architecture/30_session_change_notifications_ru.md).
//
// What counts as a change for a buyer: the DATE or the TIME at which the
// session starts, the VENUE, and the session being CANCELLED. Price, title,
// description, capacity, poster and age never count, and neither does the END
// time on its own — no ticket prints it, so an importer that recomputes a
// default duration must never send a letter.
//
// Every write path that can move or cancel a session (PATCH, the event-bundle
// import, the Bil24-format import, soft delete) calls Apply inside the SAME
// transaction as its UPDATE. Apply journals the change and queues one letter
// job per affected order; if queueing fails the whole move rolls back, so a
// session can never move without its buyers being told. A static guardrail
// (tests/staticanalysis) keeps the write paths from growing a sibling.
package sessionchange

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Change kinds, as stored in session_changes.kinds and sent to clients.
const (
	// KindDate means the session now starts on another calendar day (in the
	// venue's own time zone).
	KindDate = "date"
	// KindTime means the start moved within the same calendar day.
	KindTime = "time"
	// KindVenue means the session moved to another venue.
	KindVenue = "venue"
	// KindCancelled means the session was cancelled or deleted. It is never
	// combined with the other kinds: a cancelled session has nothing to move.
	KindCancelled = "cancelled"
)

// StatusCancelled is the sessions.status value that cancels a session.
const StatusCancelled = "cancelled"

// State is the buyer-visible slice of a session, taken before and after a save.
type State struct {
	StartAt   time.Time `json:"start_at"`
	EndAt     time.Time `json:"end_at"`
	VenueID   uuid.UUID `json:"venue_id"`
	VenueName string    `json:"venue_name"`
	// Timezone is the venue's IANA zone; the day/time split is judged in it.
	Timezone string `json:"timezone"`
	Status   string `json:"status"`
	// Deleted is true when the session row carries deleted_at.
	Deleted bool `json:"deleted"`
}

// Cancelled reports whether the session no longer takes place.
func (s State) Cancelled() bool {
	return s.Deleted || strings.EqualFold(strings.TrimSpace(s.Status), StatusCancelled)
}

func (s State) location() *time.Location {
	if s.Timezone != "" {
		if loc, err := time.LoadLocation(s.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// Kinds returns the buyer-visible changes between two states, in the fixed
// order date, time, venue — or just cancelled. Empty means "nothing a buyer
// needs to be told".
func Kinds(oldState, newState State) []string {
	if newState.Cancelled() {
		if oldState.Cancelled() {
			return nil // already cancelled: nobody is told twice
		}
		return []string{KindCancelled}
	}
	var kinds []string
	if !oldState.StartAt.Equal(newState.StartAt) {
		oy, om, od := oldState.StartAt.In(oldState.location()).Date()
		ny, nm, nd := newState.StartAt.In(newState.location()).Date()
		if oy != ny || om != nm || od != nd {
			kinds = append(kinds, KindDate)
		} else {
			kinds = append(kinds, KindTime)
		}
	}
	if oldState.VenueID != newState.VenueID {
		kinds = append(kinds, KindVenue)
	}
	return kinds
}

// Has reports whether kinds contains kind.
func Has(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// MovesBuyers reports whether the kinds require a new ticket PDF (everything
// but a cancellation: a cancelled ticket has nothing to show).
func MovesBuyers(kinds []string) bool {
	return len(kinds) > 0 && !Has(kinds, KindCancelled)
}
