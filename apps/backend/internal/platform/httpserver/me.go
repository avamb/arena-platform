// me.go implements the current-user context endpoint (GET /v1/me) introduced
// in feature #211 — "Extend current-user context endpoint for network scopes".
//
// The endpoint sits behind auth.Middleware and returns a single, stable
// snapshot of everything the calling client needs to render UI and gate
// behaviour without making additional round-trips:
//
//   - user                    — id, type, issuer, impersonation hints
//   - roles                   — union of JWT roles and active membership roles
//   - permissions             — the flat list expanded from those roles
//   - organization_memberships — every active membership for the user
//   - assigned_networks       — every operator network the user belongs to
//   - available_scopes        — derived authorization scopes the caller can act
//     under: "global" (bypass roles), "platform"
//     (platform_operator), "network:<uuid>", and
//     "organization:<uuid>"
//
// The payload is composed entirely from existing sqlc queries plus the
// purpose-built ListMembershipsByUser query added in #211 — no new tables and
// no breaking changes to other endpoints. Backwards-compatible additions only.
package httpserver

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// meQuerier — narrow read-only interface the handler depends on
// ─────────────────────────────────────────────────────────────────────────────

// meQuerier is the read-only data surface required by handleMe. The real
// *gen.Queries satisfies this interface in production; tests inject a fake to
// exercise the response-shaping logic without spinning up PostgreSQL.
type meQuerier interface {
	GetActiveRolesForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
	GetPermissionsForRoles(ctx context.Context, roleNames []string) ([]string, error)
	ListMembershipsByUser(ctx context.Context, userID uuid.UUID) ([]gen.MembershipRow, error)
	ListNetworksByUser(ctx context.Context, userID uuid.UUID) ([]gen.OperatorNetworkRow, error)
}

// pickMeQueries returns the explicitly-injected meQuerier, or one built from
// the pgxpool when the inject value is nil and pool is non-nil. Returns nil
// when both are absent — mountMeRoutes guards against that and the route is
// simply not mounted.
func pickMeQueries(inject meQuerier, pool *pgxpool.Pool) meQuerier {
	if inject != nil {
		return inject
	}
	if pool != nil {
		return gen.New(pool)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Response DTOs
// ─────────────────────────────────────────────────────────────────────────────

// meUserDTO is the user block inside the /v1/me response. It is intentionally
// JWT-only — no email or password-hash data — so the endpoint never exposes
// data that would not already be present in the bearer token.
type meUserDTO struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	Issuer              string `json:"issuer,omitempty"`
	IsImpersonated      bool   `json:"is_impersonated"`
	ImpersonatedBy      string `json:"impersonated_by,omitempty"`
	ImpersonationReason string `json:"impersonation_reason,omitempty"`
}

// meMembershipDTO mirrors a single row from organization_memberships.
type meMembershipDTO struct {
	ID       string `json:"id"`
	OrgID    string `json:"org_id"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	JoinedAt string `json:"joined_at"`
}

// meNetworkDTO mirrors a single row from assigned_networks.
type meNetworkDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// meResponse is the JSON envelope returned by GET /v1/me. All slice fields are
// guaranteed to be non-nil (empty when no rows) so clients can iterate without
// nil-checks.
type meResponse struct {
	User                    meUserDTO         `json:"user"`
	Roles                   []string          `json:"roles"`
	Permissions             []string          `json:"permissions"`
	OrganizationMemberships []meMembershipDTO `json:"organization_memberships"`
	AssignedNetworks        []meNetworkDTO    `json:"assigned_networks"`
	AvailableScopes         []string          `json:"available_scopes"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Scope derivation
// ─────────────────────────────────────────────────────────────────────────────

// bypass roles mirror networkscope.BypassRoles. platform_operator is
// intentionally NOT a bypass role (see #207) — it gets the non-bypass
// "platform" scope marker instead.
var meBypassRoles = map[string]bool{
	"platform_superadmin": true,
	"admin":               true,
}

// hasPlatformOperator returns true when one of the supplied roles is the
// internal platform_operator role.
func hasPlatformOperator(roles []string) bool {
	for _, r := range roles {
		if r == "platform_operator" {
			return true
		}
	}
	return false
}

// hasBypassRole returns true when one of the supplied roles is a bypass role
// (platform_superadmin or admin).
func hasBypassRole(roles []string) bool {
	for _, r := range roles {
		if meBypassRoles[r] {
			return true
		}
	}
	return false
}

// computeAvailableScopes returns the deduplicated, deterministically-ordered
// list of authorization scopes the caller can act under.
//
// Ordering: "global" first (when present), then "platform" (when present),
// then sorted "network:<uuid>" entries, then sorted "organization:<uuid>"
// entries. The deterministic order keeps the response stable for clients and
// makes the test assertions trivial.
func computeAvailableScopes(roles []string, memberships []gen.MembershipRow, networks []gen.OperatorNetworkRow) []string {
	scopes := make([]string, 0, 2+len(networks)+len(memberships))
	if hasBypassRole(roles) {
		scopes = append(scopes, "global")
	}
	if hasPlatformOperator(roles) {
		scopes = append(scopes, "platform")
	}

	networkScopes := make([]string, 0, len(networks))
	seenNet := make(map[string]bool, len(networks))
	for _, n := range networks {
		s := "network:" + n.ID.String()
		if !seenNet[s] {
			networkScopes = append(networkScopes, s)
			seenNet[s] = true
		}
	}
	sort.Strings(networkScopes)
	scopes = append(scopes, networkScopes...)

	orgScopes := make([]string, 0, len(memberships))
	seenOrg := make(map[string]bool, len(memberships))
	for _, m := range memberships {
		s := "organization:" + m.OrgID.String()
		if !seenOrg[s] {
			orgScopes = append(orgScopes, s)
			seenOrg[s] = true
		}
	}
	sort.Strings(orgScopes)
	scopes = append(scopes, orgScopes...)

	return scopes
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

// handleMe serves GET /v1/me. The route is protected by auth.Middleware, so
// the actor is always present when the handler fires. The handler then:
//
//  1. Parses the actor ID as a UUID — the membership / network queries are
//     keyed by users.id and reject non-UUID JWT subjects with 400.
//  2. Looks up the user's active membership roles and unions them with the
//     JWT roles (deduplicated, sorted).
//  3. Expands the union into a permissions list via GetPermissionsForRoles.
//  4. Loads active memberships and assigned operator networks.
//  5. Derives available_scopes (see computeAvailableScopes).
//  6. Writes the JSON envelope.
//
// Any database error short-circuits to a 503 envelope — the endpoint is
// strictly read-only so transient failures should not poison the audit log.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		// Should never happen — auth.Middleware enforces this — but guard
		// defensively so a misconfigured mount can never leak data.
		writeJSON(w, http.StatusUnauthorized, errorEnvelope("auth.unauthenticated", "authentication required", r))
		return
	}

	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		// Stub tokens occasionally carry non-UUID subjects (legacy fixtures).
		// We still return the JWT-derived blocks so clients can render at
		// least the user header.
		s.writeMeJWTOnly(w, r, actor)
		return
	}

	if s.meQueries == nil {
		// Route should not be mounted in this case; treat as 503 if it ever
		// fires (e.g. dependency stripped at runtime).
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "user context store unavailable", r))
		return
	}

	membershipRoles, err := s.meQueries.GetActiveRolesForUser(ctx, userID)
	if err != nil {
		logger.Warn("me: GetActiveRolesForUser failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "could not load user roles", r))
		return
	}

	roles := mergeRoles(actor.Roles, membershipRoles)

	var permissions []string
	if len(roles) > 0 {
		permissions, err = s.meQueries.GetPermissionsForRoles(ctx, roles)
		if err != nil {
			logger.Warn("me: GetPermissionsForRoles failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "could not load user permissions", r))
			return
		}
	}
	if permissions == nil {
		permissions = []string{}
	}

	memberships, err := s.meQueries.ListMembershipsByUser(ctx, userID)
	if err != nil {
		logger.Warn("me: ListMembershipsByUser failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "could not load user memberships", r))
		return
	}

	networks, err := s.meQueries.ListNetworksByUser(ctx, userID)
	if err != nil {
		logger.Warn("me: ListNetworksByUser failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope("dependency.database_unavailable", "could not load assigned networks", r))
		return
	}

	resp := meResponse{
		User: meUserDTO{
			ID:                  actor.ID,
			Type:                string(actor.Type),
			Issuer:              actor.Issuer,
			IsImpersonated:      actor.IsImpersonated(),
			ImpersonatedBy:      actor.ImpersonatedBy,
			ImpersonationReason: actor.ImpersonationReason,
		},
		Roles:                   roles,
		Permissions:             permissions,
		OrganizationMemberships: membershipsToDTO(memberships),
		AssignedNetworks:        networksToDTO(networks),
		AvailableScopes:         computeAvailableScopes(roles, memberships, networks),
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeMeJWTOnly is the degraded response used when the actor ID is not a
// valid UUID (legacy stub-token fixtures). The handler still returns 200 with
// the JWT-derived user + roles so the client can render the header — there is
// just no membership / network data to attach.
func (s *Server) writeMeJWTOnly(w http.ResponseWriter, _ *http.Request, actor auth.Actor) {
	roles := mergeRoles(actor.Roles, nil)
	resp := meResponse{
		User: meUserDTO{
			ID:                  actor.ID,
			Type:                string(actor.Type),
			Issuer:              actor.Issuer,
			IsImpersonated:      actor.IsImpersonated(),
			ImpersonatedBy:      actor.ImpersonatedBy,
			ImpersonationReason: actor.ImpersonationReason,
		},
		Roles:                   roles,
		Permissions:             []string{},
		OrganizationMemberships: []meMembershipDTO{},
		AssignedNetworks:        []meNetworkDTO{},
		AvailableScopes:         computeAvailableScopes(roles, nil, nil),
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// mergeRoles returns the deduplicated, alphabetically-sorted union of two role
// slices. Empty strings are dropped. The result is never nil — callers can
// pass it directly to GetPermissionsForRoles.
func mergeRoles(jwtRoles, membershipRoles []string) []string {
	seen := make(map[string]bool, len(jwtRoles)+len(membershipRoles))
	for _, r := range jwtRoles {
		if r != "" {
			seen[r] = true
		}
	}
	for _, r := range membershipRoles {
		if r != "" {
			seen[r] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// membershipsToDTO projects the sqlc rows down to JSON-tagged DTOs.
func membershipsToDTO(rows []gen.MembershipRow) []meMembershipDTO {
	out := make([]meMembershipDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, meMembershipDTO{
			ID:       m.ID.String(),
			OrgID:    m.OrgID.String(),
			Role:     m.Role,
			Status:   m.Status,
			JoinedAt: m.JoinedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

// networksToDTO projects the sqlc rows down to JSON-tagged DTOs. Archived
// networks remain visible in this list because they may still appear in
// existing scopes — clients decide whether to grey them out.
func networksToDTO(rows []gen.OperatorNetworkRow) []meNetworkDTO {
	out := make([]meNetworkDTO, 0, len(rows))
	for _, n := range rows {
		out = append(out, meNetworkDTO{
			ID:     n.ID.String(),
			Name:   n.Name,
			Slug:   n.Slug,
			Status: n.Status,
		})
	}
	return out
}
