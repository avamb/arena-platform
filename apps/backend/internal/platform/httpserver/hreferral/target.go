package hreferral

// target.go — what a programme route acts on: one event of the organization,
// or the organization's shop (spec 52 section 3.5). The handlers are the same
// for both; only the queries that find, lock and start the programme differ.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// target is the thing a programme belongs to.
type target struct {
	orgID uuid.UUID
	// eventID is the event, nil for the organization's shop.
	eventID *uuid.UUID
}

func (t target) isShop() bool { return t.eventID == nil }

// noun is how an answer names the target.
func (t target) noun() string {
	if t.isShop() {
		return "shop"
	}
	return "event"
}

// eventTarget reads {org_id} and {event_id} (see scope).
func (h *Handler) eventTarget(w http.ResponseWriter, r *http.Request) (target, bool) {
	orgID, eventID, ok := h.scope(w, r)
	if !ok {
		return target{}, false
	}
	return target{orgID: orgID, eventID: &eventID}, true
}

// shopTarget reads {org_id}; the route's gate has already proved the caller is
// a member of that organization, and every query is scoped by it.
func (h *Handler) shopTarget(w http.ResponseWriter, r *http.Request) (target, bool) {
	if !h.ready(w, r) {
		return target{}, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return target{}, false
	}
	return target{orgID: orgID}, true
}

// latest returns the target's live programme, else its newest ended one.
func (t target) latest(ctx context.Context, q *gen.Queries) (gen.ReferralProgramRow, error) {
	if t.isShop() {
		return q.GetLatestReferralProgramForShop(ctx, t.orgID)
	}
	return q.GetLatestReferralProgramForEvent(ctx, *t.eventID, t.orgID)
}

// lock serializes the writers of the target's programme.
func (t target) lock(ctx context.Context, q *gen.Queries) error {
	if t.isShop() {
		return q.LockReferralProgramShop(ctx, t.orgID)
	}
	return q.LockReferralProgramEvent(ctx, *t.eventID)
}

// insert starts the target's programme.
func (t target) insert(ctx context.Context, q *gen.Queries, s settings, by, via string) (gen.ReferralProgramRow, error) {
	if t.isShop() {
		return q.InsertReferralShopProgram(ctx, gen.InsertReferralShopProgramParams{
			OrgID: t.orgID, FriendDiscountPct: s.discount, CashbackPct: s.cashback, CashbackCapPct: s.cap,
			MaxFriends: s.maxFriends, EndsAt: s.endsAt, CreatedBy: by, CreatedVia: via,
		})
	}
	return q.InsertReferralProgram(ctx, gen.InsertReferralProgramParams{
		OrgID: t.orgID, EventID: *t.eventID, FriendDiscountPct: s.discount, CashbackPct: s.cashback, CashbackCapPct: s.cap,
		MaxFriends: s.maxFriends, EndsAt: s.endsAt, CreatedBy: by, CreatedVia: via,
	})
}
