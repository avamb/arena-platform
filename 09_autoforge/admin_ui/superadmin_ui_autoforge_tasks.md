# SuperAdmin UI AutoForge Tasks

Updated: 2026-06-27

Status: planning artifact for AutoForge. This file is not an implementation.

## Goal

Build the first production-grade slice of the unified role-aware admin web app for the `platform_superadmin` workflow.

This is not a separate SuperAdmin product. It is the first preset inside one future admin console that can later serve platform operators, network operators, organizers, agents, external ticketing operators, and POS users through permissions and scopes.

## Non-negotiable rules

- Do not recreate the four legacy Java applications (`TixManager`, `TixEditor`, `TixReporter`, `TixCassa`).
- Do not copy Swing/Desktop visual layout from screenshots. Screenshots are functional evidence only.
- Use one web admin shell with permission-driven routing, scope selector, and role-specific navigation presets.
- Backend authorization is source of truth. UI hiding is only usability.
- Use `GET /v1/me` for user, roles, permissions, memberships, assigned networks, and available scopes.
- Use backend OpenAPI and generated TypeScript types. Do not invent frontend-only API shapes.
- Do not use mock, dummy, sample, or in-memory production data.
- Every cross-tenant SuperAdmin read/action must expose an audit reason flow where the backend supports it, and must surface missing backend support as a gap instead of silently bypassing it.
- Sensitive data, logs, payment data, credentials, raw barcodes, and personal data are masked by default.

## Required source inputs

AutoForge must read these before starting any task in this backlog:

- `09_autoforge/00_AGENT_GUARDRAILS.md`
- `08_architecture/08_platform_superadmin_observability_ru.md`
- `08_architecture/03_platform_management_api_and_permissions_ru.md`
- `08_architecture/10_compliance_security_privacy_ru.md`
- `08_architecture/14_current_implementation_overview_ru.md`
- `09_autoforge/admin_ui/README.md`
- `09_autoforge/admin_ui/autoforge_admin_task_statement.md`
- `09_autoforge/admin_ui/legacy_admin_reference_map.yaml`
- `09_autoforge/admin_ui/role_network_model.md`
- `09_autoforge/admin_ui/operator_network_design_note.md`
- `apps/backend/openapi/openapi.yaml`

Legacy screenshot references:

- `04_legacy_screenshots/tix_manager/2026-06-11_manager_audit`
- `04_legacy_screenshots/tix_editor/2026-06-12_editor_audit`
- `04_legacy_screenshots/tix_cassa/2026-06-21_cassa_audit`
- `04_legacy_screenshots/raw_misc`

Use screenshots to understand workflows:

- TixManager: networks/frontends/operators/organizers/agents/acquiring/settings.
- TixReporter: dense tables, filters, support lookup, reports.
- TixEditor: event/session/venue/seating workflows, mostly deferred from first SuperAdmin slice except navigation shells.
- TixCassa: POS/shift/purchase/return workflows, deferred from first SuperAdmin slice except route placeholders.

## Current backend facts to respect

The repository currently contains more than the stale `14_current_implementation_overview_ru.md` mentions. Verify again before coding, but as of 2026-06-27 these contracts exist:

- `POST /v1/auth/login`, `POST /v1/auth/refresh`, `POST /v1/auth/logout`.
- `POST /v1/dev/auth/token` and `POST /v1/dev/token` for dev-only JWT minting when enabled.
- `GET /v1/me` returns user, roles, permissions, organization memberships, assigned networks, and available scopes.
- `GET /v1/admin/organizations`, `GET /v1/admin/orders`, `GET /v1/admin/tickets`, `GET /v1/admin/refunds` exist as read-only cross-tenant SuperAdmin endpoints and require `X-Admin-Reason`.
- `POST /v1/operator-networks`, `GET /v1/operator-networks`, `GET /v1/operator-networks/{id}`, `PATCH /v1/operator-networks/{id}`, `POST /v1/operator-networks/{id}/archive` exist.
- `GET/POST/DELETE /v1/admin/networks/{id}/users`, `/organizers`, `/agents` exist for roster and assignment management.
- Migrations `0042_network_operator_role.sql`, `0043_operator_networks.sql`, and `0044_network_permissions.sql` exist.
- `platform/networkscope` exists and enforces network scoping helpers, but each route still needs explicit integration where resource scope matters.
- `apps/admin-web` does not exist yet.

If these facts change, update this file or the task-local contract snapshot before implementing UI.

## Out of first SuperAdmin UI slice

- Full POS mode.
- Full visual seating editor.
- Full BI/report builder.
- Break-glass write flows.
- Sensitive raw log viewer.
- Payment provider credential editing.
- Scanner authority redesign.
- External allocation financial reconciliation editing.
- Replacing public checkout or WordPress plugin UI.

Shell routes may exist for these areas, but they must show honest "not wired yet" states without fake data.

## Model routing

- Use `opus` for tasks that change backend permissions, API contracts, audit semantics, auth/session handling, or cross-scope security.
- Use `sonnet` for bounded UI-only implementation tasks once backend contracts are stable.
- If AutoForge uses different model names, map `opus` to the top-tier reasoning/coding model and `sonnet` to the mid-tier coding model.

## Task List

### SAUI-00 - Reconcile admin UI contracts and stale docs

Category: Architecture / Contract

Model: opus

Depends on: none

Objective:

Create a current contract snapshot for the SuperAdmin UI so later tasks do not work from stale architecture notes.

Implementation notes:

- Compare `08_architecture/14_current_implementation_overview_ru.md` with the current code and migrations.
- Confirm the current OpenAPI paths for auth, `/v1/me`, SuperAdmin, operator networks, network users, organizers, and agents.
- Check whether `09_autoforge/admin_ui/operator_network_design_note.md` is stale relative to migrations `0042` through `0044` and handlers in `apps/backend/internal/platform/httpserver`.
- Update planning docs only if needed. Do not implement UI in this task.

Acceptance criteria:

- A contract snapshot exists in `09_autoforge/admin_ui/` or this file is updated with verified current backend facts.
- Stale claims are explicitly marked as stale instead of silently reused.
- Backend gaps needed by the UI are listed with endpoint, permission, audit, and OpenAPI impact.
- No source code or frontend app is created in this task.

Verification:

- `rg -n "operator_network|network_operator|/v1/me|/v1/operator-networks|/v1/admin/networks" apps/backend 09_autoforge 08_architecture`
- Inspect `apps/backend/openapi/openapi.yaml` for matching routes.

### SAUI-01 - Create admin web app scaffold

Category: Frontend Foundation

Model: sonnet

Depends on: SAUI-00

Objective:

Create `apps/admin-web` as the first web admin application shell.

Implementation notes:

- Use React, TypeScript, Vite, TanStack Router, TanStack Query, TanStack Table, React Hook Form, and Zod.
- Keep the app inside the repository's current Node setup unless a workspace decision is required.
- Add root scripts for admin web development/build/test without breaking existing `gen-ts-client` and `check-ts`.
- The first screen after login is the admin workspace, not a marketing page.
- Use a deliberate admin visual language: dense, operational, readable tables, strong hierarchy, accessible contrast. Do not mimic legacy Swing UI.

Acceptance criteria:

- `apps/admin-web` builds with TypeScript.
- Root scripts document how to run and build the admin web app.
- The scaffold has router, query client, error boundary, loading state, and base layout.
- No production route contains mock business data.
- The app can be launched locally against the backend base URL from env config.

Verification:

- `npm run admin:build`
- `npm run check-ts`
- If Playwright is introduced here, one smoke test opens the empty authenticated shell or login screen.

### SAUI-02 - Implement auth, API client, and current-user context

Category: Frontend Security / API Integration

Model: opus

Depends on: SAUI-01

Objective:

Wire real authentication and current-user context into the admin app.

Implementation notes:

- Implement typed API access using generated OpenAPI TypeScript definitions from `apps/backend/openapi/clients/ts/index.d.ts`.
- Use `POST /v1/auth/login`, `POST /v1/auth/refresh`, and `POST /v1/auth/logout` for normal auth.
- Support dev-only token minting only behind an explicit development UI/environment guard.
- Load `GET /v1/me` immediately after login and use its `permissions` and `available_scopes` as the UI source of truth.
- Redirect unauthenticated users to login on 401.
- Show a permission/scope diagnostic panel in development, but hide it in production builds unless explicitly enabled.

Acceptance criteria:

- Login, refresh, logout, and `/v1/me` are wired to real backend endpoints.
- Tokens are not logged to console and are not stored in places accessible to unrelated scripts beyond the chosen frontend storage policy.
- `/v1/me` failure blocks admin rendering and shows a clear recovery path.
- No hardcoded SuperAdmin user, role list, or permission list is used as production data.

Verification:

- Browser smoke: login, reload page, `/v1/me` is called, logout clears session.
- Unit tests cover 401 handling, refresh failure, and missing permission states.

### SAUI-03 - Permission-driven admin shell and scope selector

Category: Frontend RBAC / Navigation

Model: opus

Depends on: SAUI-02

Objective:

Implement the unified admin shell with navigation filtered by effective permissions and active scope.

Implementation notes:

- Build navigation from permission families, not hardcoded role labels.
- Support scope types from `/v1/me.available_scopes`: `global`, `platform`, `network:<uuid>`, `organization:<uuid>`.
- For `platform_superadmin`, default to `global` or `platform` scope and allow switching into networks/organizations when available.
- For future `network_operator`, start in assigned network scope and never show unrelated platform-wide data.
- Disabled actions must explain missing permission or missing scope.
- Keep role presets as display/navigation defaults only; backend permissions remain authoritative.

Acceptance criteria:

- Navigation changes when `/v1/me.permissions` changes.
- Scope selector displays global/platform/network/organization scopes from backend data.
- Routes require permission checks before rendering.
- Missing permission state is explicit and actionable.
- Direct URL navigation to a hidden route shows a 403-style UI, not a broken page.

Verification:

- Component tests with `/v1/me` fixtures for `platform_superadmin`, `platform_operator`, `network_operator`, and no-permission user.
- Browser smoke confirms direct route access is blocked in UI when permission is absent.

### SAUI-04 - SuperAdmin reason context for cross-tenant reads

Category: Frontend Audit UX

Model: opus

Depends on: SAUI-03

Objective:

Implement the audit-reason UX required by read-only SuperAdmin endpoints.

Implementation notes:

- `GET /v1/admin/organizations`, `/orders`, `/tickets`, `/refunds` require `X-Admin-Reason`.
- Add a SuperAdmin reason prompt before first cross-tenant read.
- Persist the reason only for the current browser session or current SuperAdmin workspace context.
- Show the active reason in the shell and allow changing it.
- Never silently send a generic reason like "admin view".
- If the backend returns `superadmin.missing_reason`, recover by prompting for reason and retrying once.

Acceptance criteria:

- Cross-tenant API calls include `X-Admin-Reason`.
- Empty reason cannot submit.
- Active reason is visible to the user before sensitive data is loaded.
- Retry behavior for `superadmin.missing_reason` is tested.

Verification:

- API client tests assert the header is sent.
- Browser smoke opens SuperAdmin organizations and confirms the reason prompt appears before data load.

### SAUI-05 - SuperAdmin global dashboard

Category: Frontend SuperAdmin

Model: sonnet

Depends on: SAUI-04

Objective:

Create the first SuperAdmin dashboard using existing backend data without inventing aggregate endpoints.

Implementation notes:

- Use existing read-only endpoints for organizations, orders, tickets, and refunds.
- Show operational cards based on loaded data and clearly label whether counts are page-local or backend-total.
- Provide shortcuts to Organizations, Operator Networks, Orders, Tickets, Refunds, Audit/Observability.
- Use TixManager and TixReporter screenshots only for information-density and workflow cues.
- Do not add fake metrics, fake revenue, fake health, or fake logs.

Acceptance criteria:

- Dashboard renders real data or honest empty states.
- Loading, empty, error, and missing-permission states exist.
- Dashboard is usable on desktop and tablet widths.
- There are no console errors on load.

Verification:

- Browser smoke with real backend or seeded test data.
- Accessibility smoke: keyboard reaches all dashboard cards and shortcuts.

### SAUI-06 - Organizations cross-tenant explorer

Category: Frontend SuperAdmin

Model: sonnet

Depends on: SAUI-04

Objective:

Build the SuperAdmin Organizations module for cross-tenant inspection.

Implementation notes:

- Use `GET /v1/admin/organizations` for the cross-tenant list.
- Provide searchable/filterable table UI where supported by local data; do not imply server-side search unless backend supports it.
- Add a detail drawer with organization metadata and links to related networks, orders, tickets, refunds, events, and users where data is available.
- Show "backend gap" empty states for related data not exposed by current API.
- Use legacy Manager "event organizers" screens as workflow evidence only.

Acceptance criteria:

- Organizations list loads with `X-Admin-Reason`.
- Detail drawer opens from a row and does not navigate away unless intentionally requested.
- Related-data sections are real or explicitly marked as not yet wired.
- Missing permission and missing reason states are handled.

Verification:

- Browser smoke opens organizations, filters locally, opens detail drawer, closes drawer.

### SAUI-07 - Operator Networks CRUD UI

Category: Frontend Network Management

Model: sonnet

Depends on: SAUI-03

Objective:

Build the Operator Networks module around the existing network CRUD API.

Implementation notes:

- Use `GET/POST/PATCH /v1/operator-networks` and `POST /v1/operator-networks/{id}/archive`.
- Create network form fields: `name`, `slug`.
- Match slug validation to backend constraint.
- Show create/archive only when permissions include `network.create` / `network.archive`.
- Show update only when permission includes `network.update`.
- Use TixManager Frontends -> Operators as functional reference, not visual reference.

Acceptance criteria:

- List, create, edit, and archive network flows are implemented against real backend.
- Validation errors from backend are displayed next to fields where possible.
- Archive is confirmed and clearly describes consequences.
- Network CRUD never appears for a user lacking required permission.

Verification:

- Browser CRUD smoke with unique network slug.
- Server restart persistence check for created network.
- Cleanup created test network by archive, not destructive deletion.

### SAUI-08 - Network roster and organization/agent assignments

Category: Frontend Network Management

Model: sonnet

Depends on: SAUI-07

Objective:

Build SuperAdmin UI for assigning network operators, organizers, and agents to an operator network.

Implementation notes:

- Use:
  - `GET/POST/DELETE /v1/admin/networks/{id}/users`
  - `GET/POST/DELETE /v1/admin/networks/{id}/organizers`
  - `GET/POST/DELETE /v1/admin/networks/{id}/agents`
- Use existing organization/user lookup endpoints if available; if lookup is insufficient, create a backend-gap note instead of hardcoding IDs.
- Distinguish platform operator, network operator, organizer, and agent in UI copy.
- Do not confuse `platform_operator` with `network_operator`.
- Mutations must show audit consequences. If backend lacks `X-Admin-Reason` support for these mutations, record it as an API gap for SAUI-09.

Acceptance criteria:

- Network detail has tabs: Overview, Users, Organizers, Agents, Audit.
- Assign/detach flows use real IDs and real API responses.
- The UI explains what each assignment kind means.
- Archived networks are read-only for roster changes.
- Permission-gated buttons are hidden or disabled with explanation.

Verification:

- Browser smoke assigns and detaches a test organizer/agent/user where fixture data exists.
- If fixture data does not exist, task must create documented test setup or a backend-gap follow-up, not fake rows.

### SAUI-09 - Close audit-reason gap for network mutations

Category: Backend API / Audit

Model: opus

Depends on: SAUI-08

Objective:

Decide and implement consistent audit-reason handling for operator network mutations if current backend contracts do not support it.

Implementation notes:

- Inspect network mutation handlers:
  - network create/update/archive
  - network user assign/remove
  - organizer attach/detach
  - agent attach/detach
- Compare with architecture rule: cross-tenant and support actions should require reason/context and immutable audit.
- If owner decision is needed, stop and ask. Proposed default: require `X-Admin-Reason` on SuperAdmin-triggered network mutations and include it in audit metadata.
- Update handlers, OpenAPI, generated TS client, tests, and UI client only after the decision is accepted.

Acceptance criteria:

- Either a documented owner decision says current audit metadata is enough, or backend requires/accepts reason consistently.
- OpenAPI documents the chosen behavior.
- Tests cover missing reason, present reason, and audit metadata.
- UI sends the reason only according to documented contract.

Verification:

- `go test ./apps/backend/internal/platform/httpserver/...`
- `npm run gen-ts-client`
- `npm run check-ts`

### SAUI-10 - Orders, Tickets, Refunds SuperAdmin support console

Category: Frontend SuperAdmin Support

Model: sonnet

Depends on: SAUI-04

Objective:

Build read-only SuperAdmin support screens for orders, tickets, and refunds.

Implementation notes:

- Use:
  - `GET /v1/admin/orders`
  - `GET /v1/admin/tickets`
  - `GET /v1/admin/refunds`
- Support filters currently available in backend: `org_id`, `limit`, `offset`, plus state/status filters.
- Use table/list + detail drawer pattern from the legacy Reporter workflow.
- Do not implement refund approval, cancellation, ticket reissue, or payment actions unless separate backend contracts already exist and permissions are confirmed.
- Detail drawer can initially show row-level fields and links to related IDs; if richer detail endpoints are missing, document the gap.

Acceptance criteria:

- Orders, tickets, and refunds routes render real backend data with SuperAdmin reason.
- Filters map exactly to backend query parameters.
- Pagination respects backend `limit` and `offset`.
- Empty states and error states are clear.
- No destructive/support write actions are present.

Verification:

- Browser smoke opens each module, changes filters, uses pagination, opens detail drawer.

### SAUI-11 - Audit and observability shell

Category: Frontend Operations

Model: sonnet

Depends on: SAUI-03

Objective:

Create honest SuperAdmin navigation and shell pages for audit and observability without fake operational data.

Implementation notes:

- Source requirements from `08_architecture/08_platform_superadmin_observability_ru.md`.
- If product-level audit/observability APIs are missing, show "not wired yet" with the exact missing endpoint family.
- Existing top-level `/healthz`, `/readyz`, and `/metrics` may be linked or lightly surfaced if CORS and deployment allow it, but do not parse Prometheus text into fake dashboards unless explicitly implemented.
- Sensitive log access must remain masked by default and permission-gated.

Acceptance criteria:

- Audit/Observability appears for users with relevant permissions only.
- Page documents current API gaps in UI and developer docs.
- No fake logs, fake traces, fake metrics, fake alerts, or fake queue data appear.
- The shell can later accept real dashboards without route redesign.

Verification:

- Browser smoke verifies route visibility by permission.
- Source grep confirms no hardcoded fake observability rows in production code.

### SAUI-12 - Legacy-derived module route placeholders

Category: Frontend Information Architecture

Model: sonnet

Depends on: SAUI-03

Objective:

Add route placeholders for the future unified admin modules so the SuperAdmin UI does not become a dead-end-only app.

Implementation notes:

- Add placeholder routes for:
  - Events and Sessions
  - Frontends and Channels
  - Payments and Fiscal
  - Venues and Seating
  - Reports
  - Notifications and Content
  - POS Mode
- Each placeholder must state source reference, expected future scope, and why it is deferred.
- Use `legacy_admin_reference_map.yaml` for names, priority, roles, and workflow shape.
- Do not implement full POS, full seating editor, or full reports in this task.

Acceptance criteria:

- Placeholder routes are permission-filtered.
- Each placeholder names the backend/API gap or future task, not generic lorem ipsum.
- SuperAdmin navigation feels complete without pretending deferred modules are implemented.

Verification:

- Browser smoke navigates to every visible placeholder route.
- No placeholder uses fake business tables.

### SAUI-13 - Admin UI accessibility and keyboard baseline

Category: Frontend Quality

Model: sonnet

Depends on: SAUI-05, SAUI-06, SAUI-07, SAUI-10

Objective:

Establish baseline accessibility and keyboard usability for dense admin workflows.

Implementation notes:

- Target WCAG 2.2 AA where practical for the admin shell.
- Tables must have accessible names, sortable/filterable controls with labels, and keyboard-reachable row actions.
- Drawers/dialogs must trap focus, restore focus on close, and close via Escape.
- Disabled actions must still expose explanation text.
- Use readable typography and high-contrast operational color tokens.

Acceptance criteria:

- Keyboard-only user can login, choose scope, set admin reason, open organizations, networks, and support tables.
- Dialogs and drawers pass basic focus behavior.
- Critical color states are not color-only.
- Automated accessibility smoke is added where project tooling allows it.

Verification:

- Playwright keyboard smoke.
- Accessibility checks using the selected tooling.

### SAUI-14 - End-to-end smoke tests and real-data guardrails

Category: Testing

Model: opus

Depends on: SAUI-02, SAUI-07, SAUI-10

Objective:

Add browser-level verification that catches fake data, broken auth, permission leaks, and persistence problems.

Implementation notes:

- Use Playwright or the repo-standard browser test runner.
- Tests must create unique test data for network CRUD when possible.
- For data persistence features, verify data survives backend restart if the local test harness supports it.
- Add greps/checks for forbidden production mock patterns: `mockData`, `fakeData`, `sampleData`, `dummyData`, `globalThis`, `devStore`, `TODO.*real`, `STUB`, `MOCK`.
- Test direct URL access to permission-gated routes.

Acceptance criteria:

- Smoke suite covers login, `/v1/me`, SuperAdmin reason prompt, dashboard, organizations, networks CRUD, and support lists.
- Tests use real backend or explicit test fixtures, not production mock data.
- Unauthorized/missing-permission access is covered.
- Console errors fail the smoke run.

Verification:

- `npm run admin:test`
- `npm run admin:build`
- `npm run check-ts`

### SAUI-15 - Admin web documentation and runbook

Category: Documentation

Model: sonnet

Depends on: SAUI-01, SAUI-02, SAUI-14

Objective:

Document how to run, test, and operate the SuperAdmin UI locally.

Implementation notes:

- Add admin web README with:
  - environment variables
  - backend base URL
  - login/dev token flow
  - required permissions for SuperAdmin routes
  - SuperAdmin reason behavior
  - known backend gaps
  - test commands
- Update root README only if it currently claims no admin UI exists.
- Link to `legacy_admin_reference_map.yaml` as reference context, not as UI design source.

Acceptance criteria:

- A new developer can run the admin UI locally from documented commands.
- Documentation distinguishes implemented routes from placeholders.
- Known gaps are concrete endpoint/permission gaps, not vague TODOs.

Verification:

- Follow the documented commands from a clean shell where feasible.

## Dependency graph

```text
SAUI-00
  -> SAUI-01
      -> SAUI-02
          -> SAUI-03
              -> SAUI-04
                  -> SAUI-05
                  -> SAUI-06
                  -> SAUI-10
              -> SAUI-07
                  -> SAUI-08
                      -> SAUI-09
              -> SAUI-11
              -> SAUI-12
          -> SAUI-14
  -> SAUI-15

SAUI-13 depends on SAUI-05, SAUI-06, SAUI-07, SAUI-10.
SAUI-14 depends on SAUI-02, SAUI-07, SAUI-10.
SAUI-15 depends on SAUI-01, SAUI-02, SAUI-14.
```

## First AutoForge execution recommendation

Start with SAUI-00 only. It is small but important because the prepared `operator_network_design_note.md` and current implementation have diverged. After SAUI-00, run SAUI-01 through SAUI-04 as the admin shell foundation before building individual screens.

