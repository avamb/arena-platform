// Package hgetstarted serves GET /v1/organizations/{org_id}/getting-started:
// which of the four "first steps" of an organization are done.
//
// A step is listed only when the caller may do it IN this organization, by
// the same per-organization checker the route gates use
// (permissions.OrgEffectiveResolver, as GET /v1/organizations/{org_id}/me):
//
//	org_data   -> org.update            (seller data complete)
//	payments   -> payment_config.read   (an accepted active payment config)
//	event      -> import.bil24_session  (any event, a draft counts)
//	sales_link -> sales_link.manage     (any sales link)
//
// Every flag is computed on the server from existing rows. Read-only: no
// audit row, no write.
package hgetstarted

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// Step keys, in the order the answer lists them.
const (
	StepOrgData   = "org_data"
	StepPayments  = "payments"
	StepEvent     = "event"
	StepSalesLink = "sales_link"
)

// Permissions that list each step. PermEvent is the permission the event
// wizard's event-bundle import demands (imports_shims.go).
const (
	PermOrgData   = "org.update"
	PermPayments  = "payment_config.read"
	PermEvent     = "import.bil24_session"
	PermSalesLink = "sales_link.manage"
)

// stepOrder is the fixed order of the steps and the permission of each.
var stepOrder = []struct{ key, perm string }{
	{StepOrgData, PermOrgData},
	{StepPayments, PermPayments},
	{StepEvent, PermEvent},
	{StepSalesLink, PermSalesLink},
}

// Step is one first step of the answer.
type Step struct {
	Key  string `json:"key"`
	Done bool   `json:"done"`
}

// GettingStarted is the body inside "getting_started".
type GettingStarted struct {
	Complete bool   `json:"complete"`
	Steps    []Step `json:"steps"`
}

// Facts is what the organization's data says about each step.
type Facts struct {
	OrgData   bool
	Payments  bool
	Event     bool
	SalesLink bool
}

func (f Facts) done(key string) bool {
	switch key {
	case StepOrgData:
		return f.OrgData
	case StepPayments:
		return f.Payments
	case StepEvent:
		return f.Event
	case StepSalesLink:
		return f.SalesLink
	}
	return false
}

// Decide lists the steps the caller may do (can reports a permission held in
// the organization), in the fixed order, and marks the answer complete when
// every listed step is done. An empty list is complete. Steps is never nil.
func Decide(can func(perm string) bool, f Facts) GettingStarted {
	out := GettingStarted{Complete: true, Steps: []Step{}}
	for _, s := range stepOrder {
		if !can(s.perm) {
			continue
		}
		d := f.done(s.key)
		out.Steps = append(out.Steps, Step{Key: s.key, Done: d})
		out.Complete = out.Complete && d
	}
	return out
}

// MemberCheck decides whether the caller is an active member of orgID.
type MemberCheck func(ctx context.Context, orgID uuid.UUID) (bool, error)

// FactsReader reads the row facts; *gen.Queries satisfies it.
type FactsReader interface {
	GetGettingStartedFacts(ctx context.Context, orgID uuid.UUID) (gen.GettingStartedFacts, error)
}

// SellerCheck reports whether the seller data of the organization is complete
// (hseller.Handler.SellerDataComplete).
type SellerCheck func(ctx context.Context, orgID uuid.UUID) (bool, error)

// Handle serves the route. Who may ask is the rule of GET .../me: an active
// member, an organization API key of that organization, a platform
// superadmin; anyone else is 403 org.access_denied.
func Handle(isMember MemberCheck, resolver permissions.OrgEffectiveResolver, facts FactsReader, seller SellerCheck, logger *slog.Logger) http.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
		if !ok {
			return
		}
		ctx := r.Context()
		actor, ok := auth.ActorFromContext(ctx)
		if !ok {
			httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
				"auth.unauthorized", "Sign in to see the first steps of this organization.", r,
			))
			return
		}
		if isMember == nil || resolver == nil || facts == nil || seller == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.org_access_unavailable", "The server cannot check access right now. Try again in a minute.", r,
			))
			return
		}
		if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
			if !allowed {
				return
			}
		} else {
			if _, err := uuid.Parse(actor.ID); err != nil {
				deny(w, r)
				return
			}
			member, err := isMember(ctx, orgID)
			if err != nil {
				logger.ErrorContext(ctx, "getting started: membership check failed", "error", err.Error())
				fail(w, r)
				return
			}
			if !member && !auth.HasSuperadminOrgAccess(ctx) {
				deny(w, r)
				return
			}
		}

		eff, err := resolver.EffectiveInOrg(ctx, actor, orgID)
		if err != nil {
			logger.ErrorContext(ctx, "getting started: permission lookup failed", "error", err.Error())
			fail(w, r)
			return
		}
		held := make(map[string]struct{}, len(eff.Permissions))
		for _, p := range eff.Permissions {
			held[p] = struct{}{}
		}
		can := func(p string) bool { _, ok := held[p]; return ok }

		var f Facts
		row, err := facts.GetGettingStartedFacts(ctx, orgID)
		if err != nil {
			logger.ErrorContext(ctx, "getting started: facts lookup failed", "error", err.Error())
			failRead(w, r)
			return
		}
		f.Payments, f.Event, f.SalesLink = row.PaymentsConnected, row.HasEvent, row.HasSalesLink
		if can(PermOrgData) {
			done, err := seller(ctx, orgID)
			if errors.Is(err, pgx.ErrNoRows) {
				httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
					"org.not_found", "This organization does not exist or was deleted.", r,
				))
				return
			}
			if err != nil {
				logger.ErrorContext(ctx, "getting started: seller profile lookup failed", "error", err.Error())
				failRead(w, r)
				return
			}
			f.OrgData = done
		}

		httputil.WriteJSON(w, http.StatusOK, map[string]GettingStarted{"getting_started": Decide(can, f)})
	}
}

func deny(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
		"org.access_denied", "You are not a member of this organization. Ask its owner to invite you.", r,
	))
}

func fail(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"org.membership_check_failed", "The server could not check your access. Try again in a minute.", r,
	))
}

func failRead(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"getting_started.lookup_failed", "The server could not read the first steps. Try again in a minute.", r,
	))
}
