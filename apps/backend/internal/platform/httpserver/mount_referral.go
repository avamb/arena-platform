package httpserver

// mount_referral.go — "Refer a friend" (spec 52, step RF-3): the organization's
// routes of an event's programme and its rewards. Reads need promo.read.
// Changing a programme is the organizer allowing refunds in their own name, so
// it needs promo.update AND refund.create AND refund.approve (spec 52 section
// 7.1: the owner and the manager hold them, an agent does not). Every route
// also needs membership of {org_id}, and the handlers load the event inside
// that organization, so another organization's event is the route's own 404.
// The same six routes serve the shop's programme under /shop. The buyer's code route is public and lives in mount_catalog.go.

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hreferral"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// alsoRequire runs h only when the caller holds every named permission in the
// path organization, on top of the permission the route's gate asked for. The
// gate has already scoped the request to the path organization, so Check
// answers for that organization alone.
func (s *Server) alsoRequire(h http.HandlerFunc, perms ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, p := range perms {
			if s.perms == nil {
				httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
					"permissions.denied", "You do not have permission to perform this action.", r))
				return
			}
			if err := s.perms.Check(r.Context(), p, "refunds"); err != nil {
				if errors.Is(err, permissions.ErrPermissionDenied) {
					httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
						"permissions.denied", "You do not have permission to perform this action.", r))
					return
				}
				s.logger.Error("server: extra permission check failed", "permission", p, "error", err.Error())
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"permissions.internal_error", "An internal error occurred while evaluating permissions.", r))
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) mountReferralRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then, and the
	// route guards see the routes.
	if !s.authEnabled() {
		return
	}
	h := hreferral.New(s.pgxPool, s.audit, s.logger)
	const ev = "/organizations/{org_id}/events/{event_id}"
	// The organization's shop programme (spec 52 section 3.5): no event, the
	// path organization is the target, with the same rights as an event's.
	const shop = "/organizations/{org_id}/shop"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "promo.read", "promo_codes")
		pr.Get(ev+"/referral-program", s.orgGated(h.HandleGet))
		pr.Get(ev+"/referral-program/summary", s.orgGated(h.HandleSummary))
		pr.Get(ev+"/referral-rewards", s.orgGated(h.HandleListRewards))
		pr.Get(shop+"/referral-program", s.orgGated(s.shopFull(h.HandleShopGet)))
		pr.Get(shop+"/referral-program/summary", s.orgGated(s.shopFull(h.HandleShopSummary)))
		pr.Get(shop+"/referral-rewards", s.orgGated(s.shopFull(h.HandleShopListRewards)))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "promo.update", "promo_codes")
		write := func(f http.HandlerFunc) http.HandlerFunc {
			return s.orgGated(s.alsoRequire(f, "refund.create", "refund.approve"))
		}
		pr.Put(ev+"/referral-program", write(h.HandlePut))
		pr.Post(ev+"/referral-program/pause", write(h.HandlePause))
		pr.Post(ev+"/referral-program/resume", write(h.HandleResume))
		pr.Post(ev+"/referral-program/end", write(h.HandleEnd))
		pr.Put(shop+"/referral-program", write(s.shopFull(h.HandleShopPut)))
		pr.Post(shop+"/referral-program/pause", write(s.shopFull(h.HandleShopPause)))
		pr.Post(shop+"/referral-program/resume", write(s.shopFull(h.HandleShopResume)))
		pr.Post(shop+"/referral-program/end", write(s.shopFull(h.HandleShopEnd)))
	})
}
