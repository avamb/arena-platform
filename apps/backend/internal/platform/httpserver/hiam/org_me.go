// org_me.go implements GET /v1/organizations/{org_id}/me (web event center,
// task A1 of 08_architecture/37_web_event_center_ru.md).
//
// GET /v1/me answers the union of a caller's roles across EVERY organization.
// Since PERM-ORG the routes decide per organization, so a client that hides a
// button by /v1/me shows buttons the route then refuses. This endpoint
// answers the caller's role and effective permissions INSIDE one
// organization, computed by the same per-organization checker the route gates
// use (permissions.OrgEffectiveResolver), so a client's "allowed" is the
// route's "allowed".
package hiam

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// OrgMeQuerier is the read surface of HandleOrgMe. *gen.Queries satisfies it.
type OrgMeQuerier interface {
	ListMembershipsByUser(ctx context.Context, userID uuid.UUID) ([]gen.MembershipRow, error)
	GetOrganizationByID(ctx context.Context, id uuid.UUID) (gen.OrganizationRow, error)
}

// orgMeResponse is the body of GET /v1/organizations/{org_id}/me.
type orgMeResponse struct {
	OrgID              string   `json:"org_id"`
	OrgName            string   `json:"org_name"`
	OrgDisplayNumber   int64    `json:"org_display_number"`
	CallerType         string   `json:"caller_type"`
	Role               string   `json:"role"`
	MembershipRoles    []string `json:"membership_roles"`
	Roles              []string `json:"roles"`
	Permissions        []string `json:"permissions"`
	PlatformSuperadmin bool     `json:"platform_superadmin"`
}

// Caller types of orgMeResponse.CallerType.
const (
	OrgMeCallerUser   = "user"
	OrgMeCallerAPIKey = "api_key"
)

// mainRoleOrder ranks the owner-managed membership roles: the role a client
// shows is the strongest one the caller holds in the organization.
var mainRoleOrder = []string{"org_admin", "organizer", "agent"}

// MainMembershipRole picks the role a client shows for a caller who may hold
// several membership rows in one organization: org_admin, then organizer,
// then agent, then the first other role in alphabetical order. "" when the
// caller holds none.
func MainMembershipRole(roles []string) string {
	for _, want := range mainRoleOrder {
		for _, r := range roles {
			if r == want {
				return want
			}
		}
	}
	if len(roles) == 0 {
		return ""
	}
	sorted := append([]string(nil), roles...)
	sort.Strings(sorted)
	return sorted[0]
}

// HandleOrgMe answers the caller's role and effective permissions inside the
// path organization.
//
// Who may ask: a user with an active membership in the organization, an
// organization API key of that organization (its permissions are its
// scopes), and a platform superadmin (the superadmin marker). Anyone else is
// 403 org.access_denied, the answer every membership guard gives. A
// superadmin needs no X-Admin-Reason here: the call reads the caller's own
// access and no tenant data beyond the organization's name.
func HandleOrgMe(q OrgMeQuerier, resolver permissions.OrgEffectiveResolver, logger *slog.Logger) http.HandlerFunc {
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
				"auth.unauthorized", "Sign in to see your access to this organization.", r,
			))
			return
		}
		if q == nil || resolver == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.org_access_unavailable", "The server cannot check access right now. Try again in a minute.", r,
			))
			return
		}

		resp := orgMeResponse{
			OrgID:           orgID.String(),
			CallerType:      OrgMeCallerUser,
			MembershipRoles: []string{},
		}
		if isService, allowed := httputil.ServiceActorDecision(w, r, orgID); isService {
			if !allowed {
				return
			}
			resp.CallerType = OrgMeCallerAPIKey
		} else {
			userID, err := uuid.Parse(actor.ID)
			if err != nil {
				denyOrgMe(w, r)
				return
			}
			memberships, err := q.ListMembershipsByUser(ctx, userID)
			if err != nil {
				logger.ErrorContext(ctx, "org me: membership lookup failed", "error", err.Error())
				writeOrgMeFailure(w, r)
				return
			}
			for _, m := range memberships {
				if m.OrgID == orgID && m.Status == "active" {
					resp.MembershipRoles = append(resp.MembershipRoles, m.Role)
				}
			}
			resp.PlatformSuperadmin = auth.HasSuperadminOrgAccess(ctx)
			if len(resp.MembershipRoles) == 0 && !resp.PlatformSuperadmin {
				denyOrgMe(w, r)
				return
			}
			sort.Strings(resp.MembershipRoles)
			resp.Role = MainMembershipRole(resp.MembershipRoles)
		}

		org, err := q.GetOrganizationByID(ctx, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"org.not_found", "This organization does not exist or was deleted.", r,
			))
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "org me: organization lookup failed", "error", err.Error())
			writeOrgMeFailure(w, r)
			return
		}
		resp.OrgName = org.Name
		resp.OrgDisplayNumber = org.DisplayNumber

		eff, err := resolver.EffectiveInOrg(ctx, actor, orgID)
		if err != nil {
			logger.ErrorContext(ctx, "org me: permission lookup failed", "error", err.Error())
			writeOrgMeFailure(w, r)
			return
		}
		resp.Roles = eff.Roles
		resp.Permissions = eff.Permissions
		if resp.Roles == nil {
			resp.Roles = []string{}
		}
		if resp.Permissions == nil {
			resp.Permissions = []string{}
		}
		httputil.WriteJSON(w, http.StatusOK, resp)
	}
}

func denyOrgMe(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
		"org.access_denied", "You are not a member of this organization. Ask its owner to invite you.", r,
	))
}

func writeOrgMeFailure(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"org.membership_check_failed", "The server could not check your access. Try again in a minute.", r,
	))
}
