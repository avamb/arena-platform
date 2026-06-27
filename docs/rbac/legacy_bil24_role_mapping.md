# Legacy Bil24 → arena_new RBAC Role Mapping

> **Feature #249 — RBAC / Legacy Mapping.**
> Source-of-truth mapping document for Memberships backend (#3 / #120),
> Users tab UI (#10), and seed data (#16). All other RBAC-facing features
> MUST use this file as the canonical legacy role catalogue; do not invent
> roles from backend stubs.

## 1. Scope and method

This document is the result of auditing every role-bearing surface of the
legacy Bil24/TixGear ecosystem that is reachable from the AutoForge
reference material checked into this repo:

| Legacy source | Path in repo |
|---|---|
| TixManager UI screenshots | `04_legacy_screenshots/tix_manager/2026-06-11_manager_audit/` |
| TixEditor UI screenshots  | `04_legacy_screenshots/tix_editor/2026-06-12_editor_audit/` |
| TixCassa UI screenshots   | `04_legacy_screenshots/tix_cassa/2026-06-21_cassa_audit/` |
| Mixed legacy captures     | `04_legacy_screenshots/raw_misc/` |
| Official Bil24 API doc    | `01_official_bil24_docs/api/bil24_app_api_2023.docx` |
| Ticket Agent API notes    | `01_official_bil24_docs/api/bil24_ticket_agent_api_notes_ru.md` |
| AutoForge admin map       | `09_autoforge/admin_ui/legacy_admin_reference_map.yaml` |
| Network/role design note  | `09_autoforge/admin_ui/role_network_model.md` |
| Operator network note     | `09_autoforge/admin_ui/operator_network_design_note.md` |

Method: every legacy screen that exposes a role selector, a login flow,
or a permission-bearing tab was inspected; the visible role labels and
capabilities were extracted; the resulting catalogue was compared
against the current arena_new RBAC seeds in
`apps/backend/internal/migrations/sql/0008_rbac.sql`,
`0011_memberships.sql`, `0034_superadmin.sql`,
`0042_network_operator_role.sql`, and `0044_network_permissions.sql`.

## 2. Extracted legacy roles

The legacy Bil24 suite ships as four separate Swing/desktop applications
(TixManager, TixEditor, TixReporter, TixCassa) plus one external "Ticket
Agent" API surface. Identity is shared across the four apps, but each
app forces the user to pick a role at login (the role dropdown is
visible in `tix_editor/.../17_role_dropdown.png` and
`24_role_dropdown_open.png`, and the per-app login launchers appear in
`tix_manager/.../30_tixreporter_auth.png`,
`31_tixeditor_auth.png`, `32_tixcassa_auth.png`).

The full list of legacy roles, extracted from those screens and the
official API doc (`bil24_app_api_2023.docx` — which enumerates
`OPERATOR`, `AGENT`, `ORGANIZER` as application principals — and the
Ticket Agent notes):

| # | Legacy role label (as seen in UI / docs) | Legacy scope | Where visible | Capabilities visible in UI |
|---|---|---|---|---|
| L1 | `Operator` / `OPERATOR` (legacy network operator, a.k.a. business operator) | Operator network (one operator owns a network of organizers/agents) | TixManager → Frontends → Operators tab (`09_frontends_operators.png`, `00_frontends_operators.png`); role dropdown entry `19_frontends_operator_dropdown_list.png` | Bind organizers, agents, trusted agents, ETS connections, promotions, channels to an operator network; assign acquiring; gate event publication |
| L2 | `Event Organizer` / `ORGANIZER` (a.k.a. "Organizer of events", "Event organizer") | One organizer organization | TixManager → Frontends → Event organizers (`10_frontends_event_organizers.png`); TixEditor login role dropdown (`17_role_dropdown.png`, `28_role_operator_selected.png`, `66_role_typed_event_organizer.png`); raw_misc editor desktop | Create/edit events, sessions, venues, seating, quotas, sale windows, media, prices, sync |
| L3 | `Trusted Agent` (a.k.a. "Trusted ticket agent") | One sales partner inside an operator network | TixManager → Frontends → Trusted agents (`11_frontends_trusted_agents.png`) | Sell tickets on assigned catalog; receive trusted-agent credentials; participate in network sales channels |
| L4 | `Agent` / `AGENT` (a.k.a. "Ticket Agent", "Билетный Агент") | One sales partner, more limited than trusted agent | TixManager → Frontends → Agents (`14_frontends_agents.png`); Ticket Agent API doc | RPC API access (CREATE_USER, RESERVATION, CREATE_ORDER, PAY_ORDER); sells from common ticket pool; cannot create events |
| L5 | `Cassa` / `Кассир` (Cashier) | POS terminal at a venue | TixManager → TixCassa launcher (`32_tixcassa_auth.png`); `manager_audit/cassa_*` and `tix_cassa/...` POS flows | Start/close shift, sell at POS, return tickets, print, choose template; bound to a fiscal device |
| L6 | `Editor` (TixEditor principal) | Per-event editor inside one organizer | TixManager → TixEditor launcher (`31_tixeditor_auth.png`); TixEditor role dropdown (`17_role_dropdown.png`) | Edit a specific event's content: media, sessions, venue assignments, quotas, sale windows |
| L7 | `Reporter` | Per-organizer / per-network reporting | TixManager → TixReporter launcher (`30_tixreporter_auth.png`); `reporter_roles_dropdown.png`, `reporter_role_click.png` | Read sales / orders / tickets / refunds grid with filters; export data; no write |
| L8 | `Manager` (TixManager principal — platform-level back-office staff) | Whole legacy back office | Self: TixManager (`current_state_overview.png`, `before_manager_login.png`) | Configure frontends, acquiring, fiscal data, subscriptions, widget, notifications, news, MFC |
| L9 | `MFC` operator (`Многофункциональный центр` — government desk channel) | One MFC channel | TixManager → MFC tab (`07_mfc.png`) | Issue tickets on behalf of citizens; channel-scoped reporting |
| L10 | `ETS` connector ("Connection to ETS" — external ticketing system) | Integration | TixManager → Connections to ETS (`12_frontends_connections_to_ets.png`) | Push allocation/barcodes to an external system; sync quotas; reconcile |
| L11 | `Acquiring` operator | Payment-provider configuration | TixManager → Acquiring (`01_acquiring.png`, `16_acquiring_agent_dropdown_open.png`) | Configure provider keys, fiscal data, agent commission |
| L12 | Subscriptions / Notifications / News editor | Content surface within TixManager | `03_subscriptions.png`, `05_notifications.png`, `06_news.png`, `04_widget.png` | Manage marketing-style content; not a real RBAC principal — captured here so we don't accidentally model it as one |

Of those twelve labels, **L1–L10 represent real principals** with
permission impact; **L11 and L12 are configuration surfaces** that the
legacy UI exposes under whichever role is logged into TixManager (no
separate sign-in flow). They are kept in the table for traceability but
do **not** map to new RBAC roles — they map to permission grants
(`payments.write`, `notifications.write`, etc.) on the new
`platform_operator` / `network_operator` / `organizer` roles.

## 3. New RBAC roles in arena_new (current state)

Source of truth: the migrations referenced in §1. Two distinct role
families exist in the new model:

### 3.1 Built-in technical roles (from `0008_rbac.sql`)

These are infrastructure roles, not business identities. They MUST NOT
be exposed as choices in the Users tab UI.

| Role | Source | Purpose |
|---|---|---|
| `admin` | `0008` | Bootstrap break-glass role with every permission. Reserved for migrations and tests. |
| `geo_admin` | `0008` | Maintains country/city reference data. |
| `scaffold_user` | `0008` | Historical scaffold-echo test principal. |

### 3.2 Business membership roles (from `0011`, `0034`, `0042`)

These are the values legal in `memberships.role`
(`memberships_role_check`) and are the only roles the Users tab and seed
data should reference.

| Role (new) | Source migration | Scope | Notes |
|---|---|---|---|
| `organizer` | `0011_memberships.sql` | Organization | Replaces legacy `ORGANIZER` / Event organizer / Editor (for content authoring). |
| `agent` | `0011_memberships.sql` | Organization (the agent's own org) | Replaces legacy `AGENT` / Trusted Agent / Cassa / MFC. |
| `platform_operator` | `0011_memberships.sql` | Platform | Internal Arena staff. **Distinct from** the legacy `OPERATOR` role. |
| `external_ticketing_operator` | `0011_memberships.sql` | Org or integration | Replaces legacy ETS connector. |
| `platform_superadmin` | `0034_superadmin.sql` | Platform | Cross-tenant high-trust role. |
| `network_operator` | `0042_network_operator_role.sql` | Operator network | Replaces the legacy *business* `OPERATOR` (the one that owns a network of organizers/agents). |

### 3.3 Network-level permission catalogue (`0044_network_permissions.sql`)

14 codes: `network.read`, `network.create`, `network.update`,
`network.archive`, `network.manage_users`, `network.manage_organizers`,
`network.manage_agents`, `network.manage_channels`, `network.view_sales`,
`network.support_orders`, `network.support_tickets`,
`network.support_refunds`, `network.view_reports`, `network.view_audit`.

Bindings:

* `platform_superadmin` → all 14 (including lifecycle).
* `network_operator` → 11 operational (no `create` / `archive` /
  `manage_users`).
* `platform_operator` → none (preserved unchanged).
* `admin` → all 14 (broad-grant pattern).

## 4. Legacy → new mapping (the source-of-truth table)

`type` column key:

* **1:1** — legacy role maps onto exactly one new role.
* **many:1** — multiple legacy roles collapse onto one new role.
* **1:many** — one legacy role splits into more than one new role.
* **rename** — same concept, new name.
* **gap** — legacy role has no current new-model home (action item).
* **permission-only** — legacy "role" is really a permission grant on
  another role.

| # | Legacy role | New role(s) | Type | Mapping notes |
|---|---|---|---|---|
| L1 | Operator (business operator of a network) | `network_operator` (+ `platform_superadmin` for lifecycle ops) | **rename + 1:many** | The legacy "Operator" tab in TixManager Frontends models the same concept as the new `operator_networks` entity. The user that runs an operator network is the new `network_operator`. Lifecycle of the network itself (`network.create`, `network.archive`, `network.manage_users`) is reserved for `platform_superadmin` — this is **not** a regression, it's an intentional permission split per `0044`. |
| L2 | Event Organizer / `ORGANIZER` | `organizer` | **rename** | One-to-one. Same scope (one organization), same capability set. The TixEditor role-dropdown entry `event_organizer` is the same principal — the new model collapses TixEditor's "Editor" surface into the same `organizer` role plus the `event.write` permission. |
| L3 | Trusted Agent | `agent` (+ `network.manage_agents` granted to the parent `network_operator`) | **many:1** | The legacy distinction between "Trusted agent" and "Agent" is purely operational (trust grade for credential issuance). In the new model both are a single `agent` role; trust is expressed via the parent `operator_network` binding and audit policy, not by a separate RBAC principal. |
| L4 | Agent / `AGENT` / "Билетный Агент" | `agent` | **rename** | The Bil24 RPC API principal (FID 1271 in the ticket-agent notes) is the same business identity as the new `agent` role. RPC-vs-REST is a transport concern, not a permission concern. |
| L5 | Cassa / Cashier | `agent` with `pos.execute` permission (and POS-terminal scope) | **many:1 + permission-only** | The legacy "Cashier" is not a separate identity in the new model — it's an `agent` operating in POS mode. `pos.execute` is the gating permission (see `legacy_admin_reference_map.yaml` → role_to_permission_matrix.agent). POS shift/printer/template are scoped settings on the POS terminal, not a role. |
| L6 | Editor (TixEditor principal) | `organizer` with `event.write` permission | **many:1** | The legacy split between "Organizer" and "Editor" is a UI split, not a permission split. In the new model both reduce to `organizer` + event-write. |
| L7 | Reporter | scoped read permissions (`order.read_scoped`, `report.read_scoped`, `network.view_reports`) on `organizer` / `agent` / `network_operator` | **permission-only** | "Reporter" was never a real identity — it was a read-only login mode against the same user account. The new model expresses it as scoped read permissions. A user gets reporting access by being granted those permissions on whichever business role they already hold. |
| L8 | Manager (TixManager principal — internal back-office staff) | `platform_operator` | **rename** | Internal Arena/Bil24 staff that configures frontends, acquiring, content. Distinct from `network_operator` (which is *business* operator of a customer-facing network). The collision of the word "Operator" between the legacy `OPERATOR` API principal and the modern `platform_operator` is the single most important source of confusion and is called out explicitly in `role_network_model.md`. |
| L9 | MFC operator | `agent` with channel-scoped permissions (`catalog.read`, `reservation.create`, `order.write_scoped`) | **many:1** | MFC is a channel for the same `agent` role, not a new principal. |
| L10 | ETS connector | `external_ticketing_operator` | **rename** | Renamed and scoped to the integration; permission set already enumerated in the admin map (`integration.*`, `barcode.import`, `allocation.*`, `reconciliation.*`). |
| L11 | Acquiring operator | **permission-only** — `payments.write` / `payments.read` on `platform_operator` (or `organizer` for org-scoped acquiring) | **permission-only** | Legacy "Acquiring" tab is a settings surface gated by permission, not a separate role. |
| L12 | Subscriptions / Notifications / News editor | **permission-only** — `notifications.write` / `content.write` on the role that already holds the surface (`platform_operator`, `network_operator`, or `organizer`) | **permission-only** | Same pattern as L11. |

### 4.1 Gap analysis

Identified gaps after the mapping pass:

| Gap ID | Description | Status / next action |
|---|---|---|
| G-NET-1 | Operator-network entity (`operator_networks`) and the binding tables (`operator_network_organizations`, `operator_network_users`) | **Closed** by migration `0043_operator_networks.sql`. |
| G-NET-2 | `network_operator` role | **Closed** by `0042_network_operator_role.sql`. |
| G-NET-3 | `network.*` permission catalogue and `platform_superadmin` / `network_operator` bindings | **Closed** by `0044_network_permissions.sql`. |
| G-POS-1 | `pos.execute` permission (gates POS-mode UI for `agent`) | **Open**. Not yet registered in `permissions`. Track in a follow-up RBAC migration before the POS surface (feature #10/POS) ships. |
| G-CONT-1 | `content.write` / `notifications.write` permissions for the legacy Subscriptions/News/Widget surface | **Open**. Track when the notifications module ships. |
| G-ACQ-1 | `payments.write` / `payments.read` permission grants on `organizer` for org-scoped acquiring | **Open**. Track with payments-fiscal module. |
| G-MFC-1 | Channel-scoped permission for MFC-style desks (`channel.mfc.execute`) | **Open**. Deferred — no current MFC surface in arena_new. |
| G-NAMING-1 | The word "Operator" is reused by three distinct concepts (legacy `OPERATOR`, modern `platform_operator`, modern `network_operator`). | **Mitigated**, not closed. The admin UI must consistently say "Network operator" (never bare "Operator") and "Internal operator" / "Platform operator" for staff. Users tab UI (feature #10) MUST enforce this copy. |

## 5. Final role list for MVP Users tab UI

The Users tab and seed data (#16) MUST present **only** these six
business roles, in this exact order, with this exact copy:

1. **Organizer** — value `organizer`. Scope: organization.
2. **Agent** — value `agent`. Scope: organization (sales surface).
3. **External Ticketing Operator** — value `external_ticketing_operator`. Scope: organization / integration.
4. **Network Operator** — value `network_operator`. Scope: operator network. UI MUST NOT call this "Operator".
5. **Platform Operator** — value `platform_operator`. Scope: platform. UI MUST NOT call this "Operator". Internal Arena staff only.
6. **Platform Superadmin** — value `platform_superadmin`. Scope: platform.

Roles NOT to expose in the Users tab dropdown:

* `admin`, `geo_admin`, `scaffold_user` — infrastructure-only.
* "Trusted Agent", "Cassa/Cashier", "Editor", "Reporter", "Manager",
  "MFC", "Acquiring", "Content editor" — legacy labels. The Users tab
  expresses them as permission grants on the six roles above, never as
  separate roles.

## 6. Seed data implications for feature #16

The seed data feature (#16) MUST:

1. Use **only** the six values listed in §5 when seeding `memberships.role`.
2. NOT re-introduce legacy labels (`OPERATOR`, `Trusted Agent`, `Editor`,
   etc.) as new role rows.
3. Seed one `network_operator` membership and one `operator_network`
   row so the Network Operator surface has a working demo target.
4. NOT seed `admin`, `geo_admin`, or `scaffold_user` memberships — those
   roles are not membership-eligible.

## 7. Memberships backend implications for feature #3 / #120

The Memberships backend MUST:

1. Validate `memberships.role` against the six values in §5 (the
   `memberships_role_check` constraint already enforces this — do not
   widen it without updating this document first).
2. When resolving effective permissions, walk **both** the membership
   role's permissions (`role_permissions` via the global roles seeded in
   `0011`/`0034`/`0042`) **and** any operator-network scope bindings
   created via `0044`.
3. Reject API attempts to grant `admin`, `geo_admin`, or
   `scaffold_user` via the membership API — those are not membership
   roles.
4. Treat the legacy labels in §2 as **input synonyms only** if and when
   an import job from legacy Bil24 is implemented. The import job MUST
   translate using the mapping in §4 before writing to `memberships`.

## 8. Stability and update policy

This document is the source of truth for legacy → new role mapping. Any
RBAC migration, seed, or UI change that introduces or removes a role
MUST update §3 and §4 in the same PR. Reviewers MUST reject PRs that
re-introduce legacy role labels as `memberships.role` values.

Last reconciled against migrations: **0008, 0011, 0034, 0042, 0043,
0044** (as of feature #249).
