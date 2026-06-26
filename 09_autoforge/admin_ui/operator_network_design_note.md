# Operator Network — Design Note (Feature #202)

Short design note that informs the implementation of the `operator_network`
business layer described in `role_network_model.md`. Derived from inspection
of current RBAC, memberships, organizations, superadmin routes, OpenAPI, and
SQL migrations as they exist on the `arena_new` codebase as of 2026-06-26.

## 1. Current state (what already exists)

### RBAC engine (migration `0008_rbac.sql`)

- `roles(id, name, org_id NULL, description)` — supports global (org_id NULL)
  and org-scoped named roles.
- `permissions(id, name UNIQUE, description)` — named capability codes
  (e.g. `geo.admin`, `membership.read`, `superadmin.read`).
- `role_permissions(role_id, permission_id)` — M:N join.
- `user_roles(user_id, role_id, org_id NULL)` — assigns users to roles,
  optionally scoped per org.
- Seeded global roles: `admin`, `geo_admin`, `scaffold_user`.

### Memberships (migration `0011_memberships.sql`)

- `memberships(id, user_id, org_id, role, status, joined_at)` with CHECK on
  `role IN ('organizer','agent','platform_operator',
  'external_ticketing_operator','platform_superadmin')`.
- Seeds those five names into `roles` so the RBAC engine can resolve
  permissions from membership roles.
- Membership permissions: `membership.grant`, `membership.revoke`,
  `membership.read`. Granted to `admin` and `org_admin`.

### Superadmin (migration `0034_superadmin.sql`)

- Adds the global `platform_superadmin` role.
- Adds the `superadmin.read` permission (granted to `admin` and
  `platform_superadmin`).
- Powers `/v1/admin/*` read-only cross-tenant endpoints
  (`mountSuperadminRoutes` in `mount_admin.go`) and the impersonation
  endpoint (`mountImpersonationRoutes`).

### Permission middleware

- `httpserver.Server.applyAuth(router, permission, resource)` — single
  choke point used by every mount file (`mount_iam.go`,
  `mount_admin.go`, etc.). Wraps a chi router group with JWT + permission
  + idempotency + audit middleware.
- Permission resolution is the union of JWT roles and **active membership
  roles** (see `memberships.go` docstring and `DBChecker` +
  `MembershipQuerier`). Grants take effect immediately without
  re-authentication.

### Memberships API

- `POST /v1/organizations/{org_id}/members` — `membership.grant`
- `GET  /v1/organizations/{org_id}/members` — `membership.read`
- `DELETE /v1/organizations/{org_id}/members/{user_id}` — `membership.revoke`

### Gap

There is **no** `operator_network`, `network_operator` role, or
`network.*` permission anywhere in the backend
(`grep operator_network apps/backend` returns no hits). This is the layer
to add.

## 2. Recommended design

### 2.1 Role placement

Add `network_operator` as a **global role** (org_id NULL in `roles`).
Scope is enforced by a new join table (`operator_network_users`) plus
permission middleware, not by `roles.org_id`. The existing org-scoped
`org_id` column in `roles` and `user_roles` only models the
organization-membership scope and would be misused if reinterpreted as
network scope.

The membership.role CHECK constraint should be **extended**, not
replaced, to include `network_operator`. This keeps the membership API
generic: a `network_operator` is bound to a "carrier" organization
(see §2.2) via memberships exactly like an organizer or agent.

`platform_operator` keeps its current meaning (internal staff). The new
role is strictly `network_operator`, distinct from `platform_operator`
per `role_network_model.md`.

### 2.2 Network ↔ organization data model — prefer organization-with-type

The existing domain already uses `organizations` as the universal tenant
container. Recommendation: **do not introduce parallel `agents` /
`network_operators` tables.** Instead:

1. Extend `organizations` with an optional `type` enum (e.g.
   `'organizer' | 'agent' | 'network_operator_carrier'`) — or, if the
   table already carries a flexible flags column, reuse it. The
   implementation agent should look at `organizations` columns before
   choosing the exact mechanism.
2. Add a single `operator_networks` table:
   ```
   operator_networks(
       id              uuid PK DEFAULT uuidv7(),
       name            text NOT NULL,
       status          text NOT NULL DEFAULT 'active'
                       CHECK (status IN ('active','suspended','archived')),
       created_by_user_id uuid NOT NULL REFERENCES users(id),
       created_at      timestamptz NOT NULL DEFAULT now(),
       updated_at      timestamptz NOT NULL DEFAULT now()
   )
   ```
3. Add a single join table `operator_network_organizations` that covers
   organizers, agents, *and* the network operator carrier org:
   ```
   operator_network_organizations(
       network_id        uuid REFERENCES operator_networks(id) ON DELETE CASCADE,
       organization_id   uuid REFERENCES organizations(id)    ON DELETE CASCADE,
       relationship_type text NOT NULL
                         CHECK (relationship_type IN
                                ('organizer','agent','operator')),
       status            text NOT NULL DEFAULT 'active',
       attached_at       timestamptz NOT NULL DEFAULT now(),
       PRIMARY KEY (network_id, organization_id, relationship_type)
   )
   ```
   The composite PK with `relationship_type` allows the data model
   `role_network_model.md` calls out as "future many-to-many" without
   committing to it on day one — a UI policy of one primary network per
   organizer/agent can be enforced at the application layer.
4. Network-operator users are bound to a network via the existing
   `memberships` table by giving them the `network_operator` role in
   the carrier organization that has
   `operator_network_organizations.relationship_type = 'operator'`.

This avoids `operator_network_users` and `operator_network_agents`
entirely, keeping a single source of truth for "user belongs to org with
role" (memberships) and a single source of truth for "org belongs to
network in some capacity" (operator_network_organizations).

### 2.3 Permission seeding

Add a migration (next free number after `0041_reconciliation_reports.sql`)
that seeds:

- Role: `network_operator` (global).
- Permissions: `network.read`, `network.create`, `network.update`,
  `network.archive`, `network.manage_users`, `network.manage_organizers`,
  `network.manage_agents`, `network.manage_channels`, `network.view_sales`,
  `network.support_orders`, `network.support_tickets`,
  `network.support_refunds`, `network.view_reports`, `network.view_audit`.
- Platform-level superadmin additions:
  `platform.superadmin.manage_networks`,
  `platform.superadmin.manage_roles`, etc.
- Grants: all `network.*` to `network_operator`; all
  `platform.superadmin.*` plus all `network.*` to `platform_superadmin`
  and `admin`.

### 2.4 Permission middleware extension points

Two extensions are required to `applyAuth`-style chains:

1. **Network scope binding.** Add a thin middleware `requireNetworkScope`
   that:
   - reads `{network_id}` from the URL,
   - looks up the active membership of the actor in the carrier org of
     that network (via `operator_network_organizations` +
     `memberships`), or — for `admin` / `platform_superadmin` — bypasses
     the check,
   - rejects with 403 `network.scope_denied` otherwise,
   - stores the resolved network on the request context for handlers.

2. **Network-scoped permission resolution.** The existing
   `DBChecker.Check(ctx, action, resource)` already unions JWT roles
   with active membership roles. To enforce that a `network_operator`
   can only act on *organizations attached to their network*, a sibling
   helper `permissions.CheckInNetwork(ctx, action, networkID)` should be
   added and used by network endpoints. The implementation joins
   memberships → operator_network_organizations and only permits the
   action when the user holds an active `network_operator` membership in
   that network (or a global override role).

Concretely, the chi mount file should look like:

```go
func (s *Server) mountNetworkRoutes(r chi.Router) {
    if s.stub == nil || !s.stub.Enabled() || s.networkQueries == nil {
        return
    }
    // Platform-scoped: superadmin creates/lists networks.
    r.Group(func(pr chi.Router) {
        s.applyAuth(pr, "platform.superadmin.manage_networks", "networks")
        pr.Post("/admin/networks",            s.handleCreateNetwork)
        pr.Get ("/admin/networks",            s.handleListNetworks)
        pr.Post("/admin/networks/{id}/organizations",
                                              s.handleAttachOrgToNetwork)
        pr.Delete("/admin/networks/{id}/organizations/{org_id}",
                                              s.handleDetachOrgFromNetwork)
    })
    // Network-scoped: network_operator manages its own network.
    r.Group(func(pr chi.Router) {
        s.applyAuth(pr, "network.read", "networks")
        pr.Use(s.requireNetworkScope) // new middleware
        pr.Get ("/networks/{network_id}",              s.handleGetNetwork)
        pr.Get ("/networks/{network_id}/organizations",
                                                       s.handleListNetworkOrgs)
        pr.Get ("/networks/{network_id}/sales",        s.handleNetworkSales)
        // ... etc, each guarded by the appropriate network.* permission
        //     via a second applyAuth call or a per-route guard.
    })
}
```

This pattern matches the existing convention used in `mount_iam.go`
(`applyAuth` per group of related verbs).

### 2.5 OpenAPI surface

Add a new `networks` tag block with the routes above. The existing
superadmin section under `/v1/admin/*` is the right place for the
platform-scoped create/list/attach endpoints; the network-operator
endpoints belong under `/v1/networks/{network_id}/*`.

## 3. Out of scope for this design note

- Migrating existing `agents` data (none exists yet as a distinct
  domain).
- Front-end navigation tree changes (covered by
  `autoforge_admin_task_statement.md`).
- Reporting/observability scoping — handled when the dashboards are
  wired.

## 4. Summary of decisions

| Question | Decision |
|---|---|
| Add `network_operator` as a new role? | Yes, global role; assigned via existing `memberships` table. |
| Parallel `agents` / `network_users` tables? | No. Reuse `organizations` + memberships. |
| Network ↔ org join shape? | Single `operator_network_organizations` join with `relationship_type` (`organizer` / `agent` / `operator`). |
| Permission scope mechanism? | New `network.*` permissions + `requireNetworkScope` middleware + `permissions.CheckInNetwork` helper. |
| Migration placement? | New migration after `0041_reconciliation_reports.sql`. |
| Breaking changes to memberships? | Only extending the role CHECK constraint to allow `network_operator`. |
