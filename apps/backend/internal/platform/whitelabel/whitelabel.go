// Package whitelabel answers one question for every surface a buyer sees:
// does this brand sell without naming Arena Sold Out today? (spec
// 08_architecture/52, owner decisions 2026-11-02.)
//
// A brand is the promoter of an event (event_promoters), or the organization
// itself for an event without a promoter. A brand has white label on a day
// when one of its white_label_periods (migration 0186) covers that UTC
// calendar day. Only a platform superadmin writes periods
// (/v1/admin/billing/white-label); the job billing.white_label charges them.
//
// What white label hides: the platform badge of the widget, the "Arena
// Platform" line and sender name of the buyer letters, the PDF producer, the
// shop footer, and it tells the WordPress sites through GET_ALL_ACTIONS. What
// it never hides: the hosted page tickets.arenasoldout.com (the page is the
// platform's), the links to the platform's terms and privacy policy in the
// checkout consent, and the contact form's data notice (both required by law).
//
// Every lookup is best effort for its caller: an error means "no white label"
// (the platform is named), never a failed purchase or letter.
package whitelabel

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Queries is the query set a Resolver reads. *gen.Queries satisfies it.
type Queries interface {
	ListWhiteLabelEvents(ctx context.Context, eventIDs []uuid.UUID, day time.Time) ([]uuid.UUID, error)
	EventIDForTicket(ctx context.Context, ticketID uuid.UUID) (uuid.UUID, error)
	WhiteLabelActiveForOrg(ctx context.Context, orgID uuid.UUID, day time.Time) (bool, error)
}

// Resolver answers white label lookups. A nil *Resolver answers false.
type Resolver struct {
	q   Queries
	now func() time.Time
}

// New returns a Resolver over q; a nil q gives a nil Resolver.
func New(q Queries) *Resolver {
	if q == nil {
		return nil
	}
	return &Resolver{q: q, now: time.Now}
}

// WithClock replaces the clock (tests).
func (r *Resolver) WithClock(now func() time.Time) *Resolver {
	if r != nil {
		r.now = now
	}
	return r
}

// Today is the UTC calendar day of t, at midnight: the day white label
// periods are compared with.
func Today(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Events returns the events among ids whose brand has white label today.
func (r *Resolver) Events(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if r == nil || len(ids) == 0 {
		return out, nil
	}
	found, err := r.q.ListWhiteLabelEvents(ctx, ids, Today(r.now()))
	if err != nil {
		return out, err
	}
	for _, id := range found {
		out[id] = true
	}
	return out, nil
}

// Event reports whether the event's brand has white label today.
func (r *Resolver) Event(ctx context.Context, eventID uuid.UUID) (bool, error) {
	if r == nil || eventID == uuid.Nil {
		return false, nil
	}
	m, err := r.Events(ctx, []uuid.UUID{eventID})
	return m[eventID], err
}

// Ticket reports whether the brand of the ticket's event has white label
// today.
func (r *Resolver) Ticket(ctx context.Context, ticketID uuid.UUID) (bool, error) {
	if r == nil || ticketID == uuid.Nil {
		return false, nil
	}
	eventID, err := r.q.EventIDForTicket(ctx, ticketID)
	if err != nil {
		return false, err
	}
	return r.Event(ctx, eventID)
}

// Org reports whether the organization itself (its events without a
// promoter, and its shop) has white label today.
func (r *Resolver) Org(ctx context.Context, orgID uuid.UUID) (bool, error) {
	if r == nil || orgID == uuid.Nil {
		return false, nil
	}
	return r.q.WhiteLabelActiveForOrg(ctx, orgID, Today(r.now()))
}
