// Package salesstate is the one rule that decides the sales state of a
// session for every surface: the event center (web and Telegram bot), the
// public feed the widget and the tickets page read, and the gateway catalog
// the WordPress sites sync (owner decision 2026-10-31: one state per session
// for EVERY sales channel, no per-channel setting).
//
// The organizer may set a session to "coming soon" or "sold out" by hand
// (session_sales_states, migration 0183). Everything else is worked out from
// the session's own facts: its status, its sales end and its places. The
// manual state never rewrites those facts, so lifting it ("open") brings
// back the real places and sale windows exactly as they were.
//
// The hold gate (hcheckout.CheckSessionSalesOpen) enforces the manual state;
// the automatic states need no gate of their own — a session without a free
// place or past its sales end already refuses every hold.
package salesstate

import "time"

// Effective sales states of a session, as the event center and the public
// feed report them.
const (
	Open      = "open"      // a buyer can take a ticket right now
	Soon      = "soon"      // set by hand, or no category is selling yet
	SoldOut   = "sold_out"  // set by hand, or no free place is left
	Ended     = "ended"     // the session's sales end has passed
	Cancelled = "cancelled" // the session itself was cancelled
)

// Sources of a change, recorded in session_sales_state_changes.source: the
// client the person used (audit.ClientChannel* values), "api" for a call
// that named none.
const (
	SourceAPI = "api"
)

// ValidManual reports whether s is a state an organizer may set by hand.
// "open" is not one: it lifts the manual state.
func ValidManual(s string) bool { return s == Soon || s == SoldOut }

// ValidRequested reports whether s may be asked for by a PUT: a manual state
// or "open".
func ValidRequested(s string) bool { return s == Open || ValidManual(s) }

// Facts are what the automatic part of the rule needs to know about one
// session at one instant.
type Facts struct {
	// Status is sessions.status.
	Status string
	// SalesEnd is sessions.sales_end_at; zero means none.
	SalesEnd time.Time
	// Categories counts the session's live categories.
	Categories int
	// TotalPlaces counts every place the session owns in a live category,
	// whatever its status.
	TotalPlaces int
	// FreePlaces counts the places nobody holds or bought yet.
	FreePlaces int
	// Selling is true when a free place sits in a category that sells right
	// now (open and inside its sale window).
	Selling bool
	// OpensLater is true when a free place sits in a category that does not
	// sell yet but may: its sale window has not started, or it is closed and
	// its window has not ended (the organizer may still open it, or it waits
	// its turn in a chain of categories).
	OpensLater bool
}

// Effective decides the state of a session. manual is the organizer's state
// ("soon", "sold_out" or "").
//
// Order: a cancelled session is cancelled whatever else is set; then the
// organizer's state; then a passed sales end; then the places. A session
// without categories yet (a fresh one) is "soon", never "sold out" (owner
// decision 2026-10-31); one whose categories own no place rows at all (a
// ledger-only import that predates migration 0101) is left "open" — its
// categories still decide every hold. No free place left is "sold out";
// a free place that sells now is "open"; a free place that will sell later
// is "soon"; free places nobody can sell any more are "ended".
func Effective(manual string, f Facts, now time.Time) string {
	if f.Status == "cancelled" {
		return Cancelled
	}
	if ValidManual(manual) {
		return manual
	}
	if !f.SalesEnd.IsZero() && now.After(f.SalesEnd) {
		return Ended
	}
	switch {
	case f.TotalPlaces <= 0 && f.Categories <= 0:
		return Soon
	case f.TotalPlaces <= 0:
		return Open
	case f.FreePlaces <= 0:
		return SoldOut
	case f.Selling:
		return Open
	case f.OpensLater:
		return Soon
	default:
		return Ended
	}
}

// Wire is the state in the WordPress sites' own vocabulary (the gateway
// catalog's salesState): open / soon / soldout / ended. A cancelled session
// never reaches the catalog; it reads as ended if it ever does.
func Wire(effective string) string {
	switch effective {
	case Open:
		return "open"
	case Soon:
		return "soon"
	case SoldOut:
		return "soldout"
	default:
		return "ended"
	}
}
