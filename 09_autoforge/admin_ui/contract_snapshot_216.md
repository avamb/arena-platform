# SuperAdmin UI — Contract Snapshot (Feature #216)

Date: 2026-06-27
Author: SAUI-00 (planning artifact only — no UI/code changes)
Status: AUTHORITATIVE for downstream SAUI-* tasks until superseded.

This snapshot pins the **current** backend contract that the SuperAdmin
UI tasks will be built against. It supersedes any conflicting statements
in `08_architecture/14_current_implementation_overview_ru.md` and
`09_autoforge/admin_ui/operator_network_design_note.md` for purposes
of the SuperAdmin UI workstream.

Verified by direct inspection of:

- `apps/backend/openapi/openapi.yaml` (HEAD of branch on 2026-06-27)
- `apps/backend/internal/migrations/sql/0042_network_operator_role.sql`
- `apps/backend/internal/migrations/sql/0043_operator_networks.sql`
- `apps/backend/internal/migrations/sql/0044_network_permissions.sql`
- `apps/backend/internal/platform/httpserver/mount_v1.go`
- `apps/backend/internal/platform/httpserver/mount_iam.go`
- `apps/backend/internal/platform/httpserver/mount_auth.go`
- `apps/backend/internal/platform/httpserver/mount_admin.go`
- `apps/backend/internal/platform/httpserver/mount_networks.go`
- `apps/backend/internal/platform/httpserver/me.go`

Verification commands (re-runnable from repo root):

```
rg -n "operator_network|network_operator" \
   apps/backend 09_autoforge 08_architecture
rg -n "/v1/me|/v1/operator-networks|/v1/admin/networks" \
   apps/backend/openapi/openapi.yaml
```

---

## 1. Authentication & current-user (verified contract)

Mounted in `mount_auth.go` and `mount_iam.go` from `mount_v1.go`.

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/v1/auth/register` | public | sqlc-backed |
| GET  | `/v1/auth/verify` | public | email verify token |
| POST | `/v1/auth/login` | public | JWT pair |
| POST | `/v1/auth/refresh` | public | refresh-token rotation |
| POST | `/v1/auth/password-reset/request` | public | logs link in dev |
| POST | `/v1/auth/password-reset/confirm` | public | |
| POST | `/v1/auth/logout` | JWT | stub-provider gated |
| GET  | `/v1/me` | JWT (no permission) | feature #211; handler enforces user-scoping by actor.ID |

`GET /v1/me` response (verified in `openapi.yaml` block at L1975 and
`me.go`):

- `user`: `{id, email, display_name, status, created_at}`
- `roles[]`: global roles attached to the user
- `memberships[]`: `{org_id, role, status, joined_at}` rows
- `assigned_networks[]`: `{network_id, slug, name, status, role, assignment_status}`
  — populated from `network_users` joined to `operator_networks`
  (see #211 / #214). Empty array when caller has no assignments.
- `available_scopes[]`: lexicographically ordered. Values currently
  produced by the handler:
  - `global` — for `admin` / `platform_superadmin` only
  - `platform` — for `platform_operator`
  - `organization:<uuid>` — one per active membership
  - `network:<uuid>` — one per active `network_users` row

## 2. SuperAdmin / cross-tenant endpoints (verified contract)

Mounted in `mount_admin.go`. All gated by `superadmin.read`
(migration 0034).

| Method | Path | Permission |
|---|---|---|
| GET  | `/v1/admin/organizations` | superadmin.read |
| GET  | `/v1/admin/orders` | superadmin.read |
| GET  | `/v1/admin/tickets` | superadmin.read |
| GET  | `/v1/admin/refunds` | superadmin.read |
| POST | `/v1/admin/impersonate` | superadmin.read |

`/v1/admin/geo/countries` and `/v1/admin/geo/cities` (POST/PATCH) are
also mounted, gated by `geo.admin`, via `mount_iam.go`.

## 3. Operator Network endpoints (verified contract)

Mounted in `mount_networks.go`. Two URL families are intentionally used:

- `/v1/operator-networks/*` — network entity CRUD
- `/v1/admin/networks/{id}/*` — roster + attachment joins

This split is what is actually wired today. The original design note
proposed `/v1/admin/networks` for entity CRUD and `/v1/networks/{id}/*`
for network-scoped data; **neither matches the current code**. See §6
for the stale-claim ledger.

### 3.1 Entity CRUD (network.read / .create / .update / .archive)

| Method | Path | Permission | Bound roles (per migration 0044) |
|---|---|---|---|
| GET    | `/v1/operator-networks` | network.read | platform_superadmin, network_operator, admin |
| GET    | `/v1/operator-networks/{id}` | network.read | same |
| POST   | `/v1/operator-networks` | network.create | platform_superadmin, admin |
| PATCH  | `/v1/operator-networks/{id}` | network.update | platform_superadmin, network_operator, admin |
| POST   | `/v1/operator-networks/{id}/archive` | network.archive | platform_superadmin, admin |

### 3.2 Roster — network_users (network.manage_users)

| Method | Path | Permission |
|---|---|---|
| GET    | `/v1/admin/networks/{id}/users` | network.manage_users |
| POST   | `/v1/admin/networks/{id}/users` | network.manage_users |
| DELETE | `/v1/admin/networks/{id}/users/{userId}` | network.manage_users |

`network.manage_users` is bound ONLY to `platform_superadmin` and
legacy `admin` (not `network_operator`). Day-to-day operators cannot
edit the roster.

### 3.3 Organizer attachments (network.manage_organizers)

| Method | Path | Permission |
|---|---|---|
| GET    | `/v1/admin/networks/{id}/organizers` | network.manage_organizers |
| POST   | `/v1/admin/networks/{id}/organizers` | network.manage_organizers |
| DELETE | `/v1/admin/networks/{id}/organizers/{orgId}` | network.manage_organizers |

### 3.4 Agent attachments (network.manage_agents)

| Method | Path | Permission |
|---|---|---|
| GET    | `/v1/admin/networks/{id}/agents` | network.manage_agents |
| POST   | `/v1/admin/networks/{id}/agents` | network.manage_agents |
| DELETE | `/v1/admin/networks/{id}/agents/{orgId}` | network.manage_agents |

### 3.5 Component schemas present in openapi.yaml (verified)

`OperatorNetwork`, `OperatorNetworkList`, `OperatorNetworkEnvelope`,
`OperatorNetworkListResponse`, `OperatorNetworkCreateRequest`,
`OperatorNetworkUpdateRequest`, `NetworkUser`, `NetworkUserList`,
`NetworkUserEnvelope`, `NetworkUserListResponse`,
`NetworkUserAssignRequest`, `NetworkOrganization`,
`NetworkOrganizationList`, `NetworkOrganizationEnvelope`,
`NetworkOrganizationListResponse`, `NetworkOrganizationAttachRequest`,
`NetworkAgents*`, `NetworkOrganizers*`, `MeResponse` (extended with
`assigned_networks` + `available_scopes`).

## 4. Migrations actually deployed

`apps/backend/internal/migrations/sql/` runs **0001 .. 0044** today
(not 0001..0041 as `14_current_implementation_overview_ru.md` claims).

Relevant new files:

- `0042_network_operator_role.sql` (#203) — adds `network_operator` to
  the `memberships.role` CHECK constraint and seeds it as a **global**
  role in `roles` (org_id NULL). Does NOT alter
  `platform_superadmin` / `platform_operator` semantics.
- `0043_operator_networks.sql` (#204) — creates three tables:
  - `operator_networks(id, name, slug, status, archived_at, ...)` —
    `slug` unique among non-archived rows; status in
    `(active, suspended, archived)`.
  - `network_users(id, network_id, user_id, role, status, ...)` — direct
    user↔network roster. `role` constrained to `network_operator` only.
  - `network_organizations(id, network_id, organization_id, assignment_kind, status, ...)`
    — `assignment_kind` constrained to `(organizer, agent)`. **Note: no
    `operator` kind**, unlike the design note's proposal.
- `0044_network_permissions.sql` (#206) — seeds **14** `network.*`
  permissions:
  `read, create, update, archive, manage_users, manage_organizers,
   manage_agents, manage_channels, view_sales, support_orders,
   support_tickets, support_refunds, view_reports, view_audit`.
  Bindings:
  - `platform_superadmin`: all 14
  - `network_operator`: 11 (excludes `create`, `archive`, `manage_users`)
  - `admin`: all 14 (broad-grant pattern from 0008)
  - `platform_operator`: none

## 5. Backend gaps (relative to the design note and to a usable SuperAdmin UI)

Each row = one downstream SAUI-* candidate.

| # | Gap | Endpoint / surface | Permission | Audit | OpenAPI impact |
|---|---|---|---|---|---|
| G1 | No endpoints exist for `network.manage_channels` | TBD `/v1/admin/networks/{id}/channels` | network.manage_channels | needs `auditscope` entry | new paths + schemas |
| G2 | No endpoints for `network.view_sales` | TBD `/v1/admin/networks/{id}/sales` | network.view_sales | read-only, audit on access | new paths + schemas |
| G3 | No endpoints for `network.support_orders` / `.support_tickets` / `.support_refunds` | TBD `/v1/admin/networks/{id}/{orders,tickets,refunds}` | respective permissions | audit on read | new paths + schemas |
| G4 | No endpoints for `network.view_reports` / `network.view_audit` | TBD `/v1/admin/networks/{id}/{reports,audit}` | view_reports / view_audit | audit on read of audit (meta) | new paths + schemas |
| G5 | No per-network scope enforcement middleware. Today a `network_operator` can pass the `network.read` middleware on **any** network because permission resolution is global, not network-scoped. Cross-network leakage is currently prevented only by the SQL filters in the list-by-user handler in `/v1/me`; the per-network entity handlers (`GET /v1/operator-networks/{id}`, etc.) do NOT join through `network_users` to filter by caller. | `requireNetworkScope` middleware on the `/v1/operator-networks/{id}*` and `/v1/admin/networks/{id}/*` subtrees | composite (role + assignment) | every denial -> `network.scope_denied` audit row | response-level 403 path; no schema change |
| G6 | No `platform.superadmin.manage_networks` / `platform.superadmin.manage_roles` permissions exist. The design note specified them; migration 0044 omitted them. Today `network.create` / `.archive` / `.manage_users` are the only platform-side gates. | n/a | n/a | n/a | none unless a follow-up migration adds them |
| G7 | `network_organizations.assignment_kind` does NOT include `operator` (only `organizer` / `agent`). The "carrier org" concept from the design note is therefore not modelled in SQL — `network_users` carries the user↔network link directly. The UI must NOT show an "operator" assignment kind. | n/a | n/a | n/a | none |
| G8 | OpenAPI 3.1 / `nullable: true`: `archived_at` is documented as `type: string, format: date-time` with a prose note. UI clients consuming the generated TS should treat the field as optional even though the schema is not strictly nullable. See `apps/backend/openapi/openapi.yaml` L≈2780 area. | n/a | n/a | n/a | doc-only |

## 6. Stale-claim ledger

### 6.1 `08_architecture/14_current_implementation_overview_ru.md`

| Line | Claim | Status |
|---|---|---|
| 5 | "AutoForge Wave 20, 171/171 passing features, feature backlog 188 total" | STALE. Backlog has grown past 188 (this feature is #216). Re-run `feature_get_stats` for current totals. |
| 18 | "embedded goose migrations 0001..0041" | STALE. Migrations 0042–0044 (#203, #204, #206) are merged. |
| 23–46 | Bounded-context inventory table | STALE. Missing a row for the **Operator Network** context: migrations 0042–0044, query file `network_users.sql` / `network_organizations.sql` / `operator_networks.sql` under `apps/backend/internal/adapters/postgres/queries/`, handlers in `httpserver/networks.go` + `network_users.go` + `network_orgs.go`. |
| 47 | "Полный набор migrations: 0001..0041" | STALE — same as L18. |
| 57 | "go test ./... ... зелёные (на 2026-06-24)" | Still substantively true on 2026-06-27 EXCEPT for the two pre-existing failures documented in `claude-progress.txt` (`TestHttpserverFileSize175`, `TestNoUnaudittedPanic`). |
| 59 | "golangci-lint ... 563 issues" | Believed still accurate; not re-verified in this snapshot. |

### 6.2 `09_autoforge/admin_ui/operator_network_design_note.md`

The note remains useful as design rationale but several specific
proposals were NOT followed by the implementation. Treat the note as
**advisory historical context**, not as a contract.

| Note §  | Proposal | Actual implementation | Verdict |
|---|---|---|---|
| §2.2.2 | `operator_networks(id, name, status, created_by_user_id, ...)` (no slug). | Migration 0043 added `slug` (unique among non-archived) and an `archived_at` soft-delete column; created_by_user_id was NOT added. | STALE — note out of date; current shape is documented in §4 above. |
| §2.2.3 | Single join `operator_network_organizations(network_id, organization_id, relationship_type)` with `relationship_type IN ('organizer','agent','operator')`. | Migration 0043 created `network_organizations(... assignment_kind ...)` — different column name; `operator` kind not included. | STALE. |
| §2.2.4 | Network operators bound via existing `memberships` table on a "carrier organization" tagged `relationship_type='operator'`. | Migration 0043 created a dedicated `network_users` table. The `memberships.role` CHECK still accepts `network_operator` (0042) but the canonical user↔network link lives in `network_users`. | STALE. |
| §2.3 | Permissions listed: `network.read, .create, .update, .archive, .manage_users, .manage_organizers, .manage_agents, .manage_channels, .view_sales, .support_orders, .support_tickets, .support_refunds, .view_reports, .view_audit` PLUS `platform.superadmin.manage_networks`, `platform.superadmin.manage_roles`. | Migration 0044 seeded exactly the 14 `network.*`. The two `platform.superadmin.*` were OMITTED. | PARTIAL — `network.*` set matches; platform.* additions not implemented (see G6). |
| §2.4 | `requireNetworkScope` middleware and `permissions.CheckInNetwork` helper. | Neither exists in `apps/backend/internal/platform/permissions/` nor in `httpserver/`. Gating is by global permission only. | STALE / not implemented (see G5). |
| §2.4 mount sketch | Routes mounted at `/v1/admin/networks` (entity) + `/v1/networks/{network_id}/*` (scoped). | Routes are at `/v1/operator-networks/*` (entity) + `/v1/admin/networks/{id}/{users,organizers,agents}` (joins). | STALE — re-mounting to match the note would be a breaking client change; preserve current paths. |
| §2.5 OpenAPI | "Add a new `networks` tag block". | Done: `- name: networks` is registered in the top-level `tags:` and every operator-network operation carries `tags: [v1, networks]`. | ACCURATE. |
| §4 summary row "Single `operator_network_organizations` join with `relationship_type`" | See §2.2.3 above. | STALE — actual table is `network_organizations` with `assignment_kind`. | STALE. |

### 6.3 Recommended remediation (not done in this feature)

A follow-up doc PR should:

1. Append a "Reconciled 2026-06-27" section to
   `operator_network_design_note.md` linking to this snapshot and
   pointing each stale §-row at the actual migration.
2. Update `14_current_implementation_overview_ru.md` §2 to add the
   Operator Network bounded-context row and bump the migrations range
   to `0001..0044`.

Both edits are pure doc edits and were INTENTIONALLY deferred — this
SAUI-00 task is planning-only.

## 7. Contract anchors the SuperAdmin UI tasks must rely on

When downstream SAUI-* tasks reference "the backend contract", they
mean the following five canonical anchors (all verified above):

- A1: `GET /v1/me` returns `assigned_networks[]` + `available_scopes[]`.
- A2: Network entity CRUD lives under `/v1/operator-networks/*`.
- A3: Roster + attachment endpoints live under
  `/v1/admin/networks/{id}/{users,organizers,agents}`.
- A4: Role/permission matrix per §3 above; `network_operator` cannot
  create/archive networks or edit the roster.
- A5: Per-network scope is currently NOT enforced by middleware (G5);
  the UI must not assume backend filtering on `/v1/operator-networks/{id}`
  and should either (a) defer that flow until G5 is closed, or (b)
  filter client-side from `/v1/me.assigned_networks`.

End of snapshot.
