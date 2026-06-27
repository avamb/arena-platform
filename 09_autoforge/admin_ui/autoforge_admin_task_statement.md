# AutoForge task statement: unified role-aware admin console

## Objective

Build a modern unified admin console for Bil24/Arena that replaces the legacy four-application model with one permission-filtered web backoffice.

The first architectural foundation must support:

- platform-wide SuperAdmin;
- internal platform operators;
- business operator networks;
- network operators who manage assigned organizers and agents;
- organizers;
- agents;
- external ticketing operators.

## Critical architecture correction

Add a new business layer:

`platform_superadmin -> operator_network -> network_operator -> assigned organizers / assigned agents`

`network_operator` is not the same as `platform_operator`.

The `platform_superadmin` can create an `operator_network`, assign one or more network operator users to it, and bind many organizers and agents to that network.

The `network_operator` sees and manages only data inside assigned networks. This includes scoped organizers, agents, events, channels, orders, tickets, refunds, and reports where permissions allow it.

## Product direction

The legacy Bil24 system had separate applications:

- TixManager;
- TixEditor;
- TixReporter;
- TixCassa.

The new product must not recreate this split. Build one admin web application with:

- one login;
- one shell;
- one navigation model;
- route guards;
- scope selector;
- permission-filtered actions;
- role-specific navigation presets;
- modern contextual hints;
- audit-first support actions.

## Required preparatory task

Before implementing UI screens, complete a dedicated reference-analysis task.

Input:

- `01_official_bil24_docs/api/bil24_app_api_2023.docx`
- `01_official_bil24_docs/api/bil24_ticket_agent_api_notes_ru.md`
- `04_legacy_screenshots/tix_manager/2026-06-11_manager_audit`
- `04_legacy_screenshots/tix_editor/2026-06-12_editor_audit`
- `04_legacy_screenshots/tix_cassa/2026-06-21_cassa_audit`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `08_architecture/08_platform_superadmin_observability_ru.md`
- `09_autoforge/00_AGENT_GUARDRAILS.md`
- `09_autoforge/admin_ui/legacy_admin_reference_map.yaml`
- `09_autoforge/admin_ui/role_network_model.md`

Output:

- refined screen/module inventory grounded in screenshots from TixManager, TixEditor, TixReporter, and TixCassa;
- `legacy_admin_reference_map.yaml` expanded with module priority, role/scope assignment, workflow-to-UI mapping, and uncertainty notes;
- role-to-screen matrix;
- role-to-permission matrix;
- backend API gap list;
- frontend route map;
- MVP vs later scope split.

Reference file to update:

- `09_autoforge/admin_ui/legacy_admin_reference_map.yaml`

The mapping must preserve the rule that there is one modern admin app, not four separate apps.

## Backend contract scope

Implement or verify:

- current user endpoint returning user, memberships, roles, permissions, available scopes;
- `operator_network` data model and CRUD;
- network-to-organizer assignment;
- network-to-agent assignment;
- network-scoped authorization checks;
- OpenAPI definitions for all admin endpoints;
- generated TypeScript client;
- audit reason support for cross-scope actions.

The backend must enforce permissions. Frontend hiding is only a usability layer.

## Frontend scope

Create a dedicated admin web app, likely `apps/admin-web`, unless the repository already has a better established frontend app location.

Expected stack:

- React;
- TypeScript;
- Vite;
- TanStack Router;
- TanStack Query;
- TanStack Table;
- React Hook Form plus Zod;
- generated API client from OpenAPI;
- lightweight component system consistent with the repo.

Do not create a marketing page. The first screen after login is the actual admin workspace.

## MVP modules

P0:

- Dashboard;
- Operator Networks;
- Organizations;
- Users and Access;
- Events and Sessions;
- Orders, Tickets, Refunds;
- Audit and Observability.

P1:

- Frontends and Channels;
- Payments and Fiscal;
- Venues and Seating shell;
- Reports.

P2:

- Notifications and Content;
- POS Mode;
- full visual seating editor;
- advanced BI.

## Role presets

### SuperAdmin

Sees platform scope and all network scopes. Can manage networks, roles, credentials, users, organizations, agents, integrations, observability, audit, and support actions according to permissions.

### Platform Operator

Internal support/moderation role. Can work across permitted platform areas but cannot automatically manage high-risk credentials, roles, break-glass, or sensitive logs.

### Network Operator

Starts in assigned network scope. Sees assigned organizers and agents only. Can manage network users, organizers, agents, events, channels, sales, support, and reports according to network permissions.

### Organizer

Sees own organization scope: events, sessions, venues, pricing, quotas, media, orders, tickets, reports, complimentary tickets, and allowed channels.

### Agent

Sees allowed catalog, reservations, orders, payments, refunds, and reports.

### External Ticketing Operator

Sees external allocations, barcode imports, synchronization, reconciliation, and integration health for assigned scope.

## Acceptance criteria

- One admin app, not four separate apps.
- `network_operator` layer is implemented or explicitly stubbed behind backend contracts.
- SuperAdmin can create a network and attach organizers/agents to it.
- Network operator cannot see unrelated organizers/agents.
- Navigation is permission-driven.
- Disabled actions explain missing permission or missing scope.
- OpenAPI and generated TypeScript client are updated.
- Localhost launch instructions are documented.
- Frontend build passes.
- Backend tests for network scope authorization pass.
- At least basic Playwright/screenshot smoke checks exist for the admin shell.

## Suggested AutoForge ticket split

1. `admin-reference-analysis`
   - Model: gpt-5.4-mini for reference analysis; gpt-5.4 if schema implications are included.
   - Produce refined structured maps from screenshots and docs.

2. `operator-network-schema-rbac`
   - Model: gpt-5.4 or gpt-5.5.
   - Add network data model, permissions, authorization checks, tests.

3. `admin-backend-contract`
   - Model: gpt-5.4 or gpt-5.5.
   - Add `/me` or equivalent, admin OpenAPI gaps, TS client generation.

4. `admin-web-scaffold`
   - Model: gpt-5.4 or gpt-5.5.
   - Create app shell, auth, routing, scope selector, permission-aware nav.

5. `superadmin-network-management`
   - Model: gpt-5.4 or gpt-5.5.
   - SuperAdmin network CRUD and organizer/agent assignment UI.

6. `network-operator-console`
   - Model: gpt-5.4 or gpt-5.5.
   - Network dashboard, scoped organizations/agents, scoped events/orders.

7. `unified-support-screens`
   - Model: gpt-5.4-mini if UI-only; gpt-5.4 or gpt-5.5 if backend gaps remain.
   - Orders, tickets, refunds, audit detail views.

8. `admin-observability-shell`
   - Model: gpt-5.4-mini for UI-only shell; gpt-5.4 or gpt-5.5 if new backend contracts are needed.
