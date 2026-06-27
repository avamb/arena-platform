# Arena Admin Web

Operational admin shell for the Arena ticketing platform. React + TypeScript
+ Vite + TanStack (Router, Query, Table) + React Hook Form + Zod.

This is the **operator / SuperAdmin workspace**. It is the only admin UI
checked into the repository; the legacy Bil24/TixGear admin is reference
material only (see `09_autoforge/admin_ui/legacy_admin_reference_map.yaml`).

## Contents

1. [Status](#status)
2. [Local development](#local-development)
3. [Environment variables](#environment-variables)
4. [Backend base URL & required services](#backend-base-url--required-services)
5. [Login flow](#login-flow)
6. [Dev token flow](#dev-token-flow)
7. [Required permissions for SuperAdmin routes](#required-permissions-for-superadmin-routes)
8. [SuperAdmin `X-Admin-Reason` behavior](#superadmin-x-admin-reason-behavior)
9. [Known backend gaps (UI side)](#known-backend-gaps-ui-side)
10. [Test, build, and type-check commands](#test-build-and-type-check-commands)
11. [Architecture](#architecture)
12. [Reference material](#reference-material)

## Status

Implemented through SAUI-14 (catalog/checkout legacy module placeholders,
audit reason gate, guarded routes, observability shell, networks CRUD shell,
organizations / orders / tickets / refunds explorers). Several surfaces are
intentionally shell-only because the backend contract has not been exposed
yet — see [Known backend gaps](#known-backend-gaps-ui-side).

No production route renders mock business data. Empty states are explicit
and call out the missing backend endpoint by name.

## Local development

```bash
# 1. From the repo root, install root + admin-web dependencies once:
npm install
npm run admin:install

# 2. Configure the dev environment:
cp apps/admin-web/.env.example apps/admin-web/.env.local
# Edit .env.local to point VITE_API_BASE_URL at your local backend.
# The repo default (docker-compose up) exposes the API on http://localhost:8080.

# 3. Start the dev server:
npm run admin:dev   # http://localhost:5174
```

The Vite dev server runs independently of the backend; it does not proxy
API requests. CORS on the backend (`CORS_ALLOWED_ORIGINS`) must include
the Vite origin (`http://localhost:5174` by default) or every request
will fail at the browser.

## Environment variables

All variables are read at build time by Vite and must be prefixed `VITE_`.
The full list is mirrored in `apps/admin-web/.env.example`.

| Variable                       | Required | Default                  | Purpose |
| ------------------------------ | -------- | ------------------------ | ------- |
| `VITE_API_BASE_URL`            | yes      | `http://localhost:8080`  | Backend HTTP API base. No trailing slash. Used by `src/lib/config.ts`. |
| `VITE_ENABLE_QUERY_DEVTOOLS`   | no       | `true` in dev, `false` in prod | When `true`, mounts the TanStack Query devtools panel. |

`.env.local` is gitignored. `.env.example` is the canonical template; keep
it in sync when adding new variables.

## Backend base URL & required services

The admin web talks to exactly one backend: the `arena-api` HTTP server.
For local development the easiest way to bring up the full dependency
stack (PostgreSQL 17, Redis 7, OTLP collector, Prometheus, Grafana,
arena-api, arena-worker) is:

```bash
# From the repo root:
docker-compose up -d
```

Once `arena-api` reports healthy (`curl http://localhost:8080/healthz`),
point `VITE_API_BASE_URL=http://localhost:8080` and start `npm run admin:dev`.

The admin web depends on:

* `GET  /healthz`, `GET /readyz`, `GET /metrics` — exposed by `arena-api`.
  The Observability shell deep-links these.
* `POST /v1/auth/login`, `POST /v1/auth/refresh`, `POST /v1/auth/logout`,
  `GET /v1/me` — the real auth surface.
* `POST /v1/dev/token` — dev-only JWT mint (only mounted when
  `ENABLE_DEV_AUTH=true`; see [Dev token flow](#dev-token-flow)).
* All SuperAdmin cross-tenant readers under `/v1/admin/*` — see
  [Required permissions](#required-permissions-for-superadmin-routes).

## Login flow

1. The login screen (`/login`) accepts `email` + `password` and POSTs to
   `/v1/auth/login`. Validation is React Hook Form + Zod.
2. On success the backend returns `{ access_token, refresh_token,
   expires_at, user_id }`. The client persists these in the in-memory +
   sessionStorage `tokenStore` (`src/lib/api/tokenStore.ts`). Tokens are
   sent on every authenticated request as `Authorization: Bearer <token>`.
   They are deliberately **never** placed in cookies, so CORS preflight
   is minimal and `credentials: "omit"` is used on every fetch.
3. After login, `AuthProvider` calls `GET /v1/me` and exposes the
   resulting permission set + assigned scopes to the React tree via
   `AuthContext` / `useAuth`.
4. On 401, `authedFetch` (`src/lib/api/client.ts`) silently calls
   `POST /v1/auth/refresh` exactly once. Refresh failure clears the
   session and routes the operator back to `/login`. Auth endpoints
   themselves are never retried (would loop).
5. Logout calls `POST /v1/auth/logout` and clears local state. Server
   failure on logout is non-fatal — the local session is always cleared.

The shell does not store passwords, refresh tokens in localStorage, or
fall back to insecure transports.

## Dev token flow

For automated testing and bootstrap work the backend exposes a
**development-only** JWT mint:

```
POST /v1/dev/token
Content-Type: application/json

{
  "actor_id":   "00000000-0000-0000-0000-000000000001",
  "actor_type": "stub_user",
  "roles":      ["admin"],
  "ttl_seconds": 3600
}
```

Source: `apps/backend/internal/platform/httpserver/devtoken.go`.

This endpoint is **only mounted when `ENABLE_DEV_AUTH=true`** (the config
validator forbids that flag in staging/production), so production never
exposes it. The admin web does not call this endpoint from the UI: it is
intended for `curl` / integration scripts. To use a dev token from a
fresh browser tab:

```bash
# 1. Mint a token:
TOKEN=$(curl -s -X POST http://localhost:8080/v1/dev/token \
  -H "Content-Type: application/json" \
  -d '{"actor_id":"00000000-0000-0000-0000-000000000001","roles":["admin"]}' \
  | jq -r .token)

# 2. Verify it works:
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/v1/me
```

You can then paste the token into the admin web's tokenStore via the
browser devtools console for debugging, but the production-shaped path
is the real `/v1/auth/login` flow above.

## Required permissions for SuperAdmin routes

Navigation visibility is driven entirely by the permission set returned
by `/v1/me` — role names do **not** appear in the UI as guards. The
canonical mapping lives in `src/lib/auth/navConfig.ts`. Direct-URL
navigation to any guarded route additionally passes through
`<RequirePermission />` so a stale nav cache cannot bypass authorization.

| Route             | Permission rule (any of)                                                | Scope filter                                |
| ----------------- | ----------------------------------------------------------------------- | ------------------------------------------- |
| `/`               | always (any authenticated user)                                         | —                                           |
| `/networks`       | `network.read`, `network.create`                                        | global / platform / network                 |
| `/organizations`  | `superadmin.read`                                                       | global / platform                           |
| `/events`         | `superadmin.read`, `event.read`, `org.read`, `network.view_sales`       | global / platform / network / organization  |
| `/venues`         | `superadmin.read`, `org.read`, `venue.read`, `venue.write`              | global / platform / network / organization  |
| `/orders`         | `superadmin.read`                                                       | global / platform                           |
| `/tickets`        | `superadmin.read`                                                       | global / platform                           |
| `/refunds`        | `superadmin.read`                                                       | global / platform                           |
| `/channels`       | `superadmin.read`, `network.manage_channels`, `integration.read`        | global / platform / network / organization  |
| `/payments`       | `superadmin.read`, `payment.read`, `network.view_sales`                 | global / platform / network                 |
| `/reports`        | `superadmin.read`, `network.view_reports`, `report.read`                | global / platform / network / organization  |
| `/content`        | `superadmin.read`                                                       | global / platform / network / organization  |
| `/pos`            | `superadmin.read`, `pos.execute`                                        | global / platform / network                 |
| `/audit`          | `superadmin.read`                                                       | global / platform                           |
| `/observability`  | `superadmin.read`                                                       | global / platform                           |
| `/geo`            | `geo.admin`                                                             | global / platform                           |

A user with **only** `superadmin.read` sees every SuperAdmin surface
above. Operators (`network.read` / `org.read` / etc.) see the subset
that matches their grants. Role presets influence only the default
active scope (`src/lib/auth/scope.ts`); they do not appear in any guard.

## SuperAdmin `X-Admin-Reason` behavior

Every cross-tenant superadmin read **and** every operator-network
mutation must carry a non-empty `X-Admin-Reason` header. Missing or
empty headers come back as
`400 { error: { code: "superadmin.missing_reason" } }`.

The full predicate lives in `src/lib/api/reason.ts`. Summary:

| Predicate                  | Applies to                                                                 |
| -------------------------- | -------------------------------------------------------------------------- |
| Required on every method   | `/v1/admin/organizations`, `/v1/admin/orders`, `/v1/admin/tickets`, `/v1/admin/refunds`, `/v1/admin/impersonate` |
| Required on mutations only | `/v1/operator-networks`, `/v1/admin/networks` (POST / PATCH / PUT / DELETE) |

UI behavior:

1. The first time a request that needs `X-Admin-Reason` is about to fire,
   `ReasonContext` prompts the operator with a modal. The chosen reason
   is persisted in **sessionStorage** under `arena.admin.adminReason`
   so it survives an in-tab reload. It is **never** persisted in
   localStorage — closing the tab MUST drop the reason and re-prompt
   the next session.
2. The active reason is rendered as a badge in the top bar so the
   operator can see (and change) what is being attached to their audit
   trail. There is no silent default ("Operator browsing" et al.) —
   this is called out as a regression vector in the spec.
3. If the backend rejects a request with `superadmin.missing_reason`
   even after a reason was attached (e.g. the server-side rule changed
   mid-session), `authedFetch` clears the cached reason, prompts again,
   and retries exactly once. A second failure of the same kind
   propagates to the caller.
4. If the operator cancels the prompt, the client raises a synthetic
   `ApiError(code='superadmin.reason_required')` so the calling page
   can show a clear error instead of issuing a header-less request that
   the backend would reject anyway.

## Known backend gaps (UI side)

The shell surfaces these gaps honestly rather than hiding the buttons.
Each is reproducible from the source code under `apps/admin-web/src`.

| Surface                                | Symptom in UI                                 | Missing backend contract                                                                                                                          |
| -------------------------------------- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/events` Events & Sessions            | Shell only, "backend gap" callout             | Modern event / session / quota / sale-window / media resource model under `/v1/admin/*` (legacy ADD_ACTION / SAVE_ACTION not replaced).            |
| `/venues` Venues & Seating             | Shell only, visual seating editor disabled    | Country / city / venue / seating-plan / category / quota resources. Visual seating editor explicitly deferred in `00_AGENT_GUARDRAILS.md`.         |
| `/channels` Frontends & Channels       | Shell only                                    | Trusted-agent, widget, external-ticketing connection, promotion resources are not yet exposed under `/v1/admin/*`.                                |
| `/payments` Payments & Fiscal          | Shell only                                    | Acquiring / fiscal-settings / payment-provider status endpoints not yet exposed. POS fiscal dependency listed but unwired.                        |
| `/reports`                             | Shell only                                    | Unified reporting endpoints (platform / network / organizer / agent / event / period) explicitly out of scope this milestone.                     |
| `/content` Notifications & Content     | Shell only                                    | Notifications / news / subscription / widget-content endpoints not yet exposed.                                                                   |
| `/pos` POS Mode                        | Shell only                                    | POS execution (shifts / cart / payment / fiscal / printing / returns) explicitly out of scope this milestone.                                     |
| `/audit` Audit Log                     | Capability tiles rendered as "backend gap"    | Cross-tenant audit reader (`GET /v1/admin/audit` family) not yet exposed.                                                                         |
| `/observability` dashboards            | Deep-links to `/healthz`, `/readyz`, `/metrics` only | Dashboard / SLO / alert read endpoints not yet exposed. Grafana is the operational surface for now.                                          |
| `/organizations` related-resource tiles | Some tiles disabled with "backend gap" badge | Org-scoped readers (memberships, agents, organizer settings) under `/v1/admin/organizations/{id}/*` not yet exposed.                               |
| `/orders`, `/tickets`, `/refunds`      | Related-resource drill-down tiles disabled    | Per-entity expansion endpoints (refund lines, ticket holders, payment journal) not yet exposed; only the cross-tenant list endpoints are wired.   |
| `/networks/<id>` Operator-network detail | "Network / organizer link" tile flagged     | Linking organizers to operator networks via a stable contract is not yet shipped (see `network_orgs.go` note in `routes/networkDetail.tsx`).      |
| Forbidden (`403`)                      | Client-rendered until server tells us otherwise | A programmatic 403 from the server side of guarded routes (the UI today fences on `/v1/me` permissions; the server endpoint may not exist yet). |

Each disabled surface in the UI carries a "backend gap" badge whose
text matches the rows above. Searching the source for `backend gap`
will round-trip to the rendering code.

## Test, build, and type-check commands

All scripts are wired from the repo root so CI can run them from a clean
shell without `cd`-ing into the package:

| Script                       | What it runs                                            | When to use                                  |
| ---------------------------- | ------------------------------------------------------- | -------------------------------------------- |
| `npm run admin:install`      | `npm --prefix apps/admin-web install`                   | First-time setup or after dependency bumps   |
| `npm run admin:dev`          | Vite dev server with HMR on http://localhost:5174       | Day-to-day development                       |
| `npm run admin:build`        | `tsc -b` (type-check) + Vite production bundle          | Pre-commit verification; CI gate             |
| `npm run admin:test`         | Vitest run (unit + library tests)                       | After changes to `src/lib/**` or routes      |
| `npm run admin:type-check`   | `tsc -b --pretty`                                       | Fast TS-only feedback loop                   |
| `npm run admin:smoke-guardrails` | AutoForge guardrail smoke (`scripts/admin-smoke-guardrails.mjs`) | Verifies forbidden patterns absent       |
| `npm run admin:smoke`        | guardrail smoke + test + build + root `check-ts`        | Full pre-PR gate                             |

Validation from a clean shell (the canonical CI ordering):

```bash
npm install
npm run admin:install
npm run admin:type-check
npm run admin:test
npm run admin:build
npm run admin:smoke-guardrails
```

The package-local equivalents (`npm run dev`, `npm run build`,
`npm run test`, `npm run type-check`) work from `apps/admin-web/`
and are wired the same way. The root scripts exist so contributors
do not need to remember the `--prefix` incantation.

## Architecture

```
src/
  main.tsx              StrictMode + ErrorBoundary + QueryClient + Router
  router.tsx            createRouter wired to routeTree
  routeTree.ts          Manually-assembled route tree (no codegen step)
  routes/
    __root.tsx          Root route, AppLayout, devtools
    index.tsx           Authenticated workspace landing
    login.tsx           RHF + Zod login (calls /v1/auth/login)
    guarded.tsx         Legacy-derived module placeholders (events, venues, channels, ...)
    networks.tsx,       Operator-network CRUD shell + detail view
      networkDetail.tsx
    organizations.tsx,  SuperAdmin cross-tenant explorers
      orders.tsx,
      tickets.tsx,
      refunds.tsx
    audit.tsx           Audit log shell (capability matrix + backend gaps)
    observability.tsx   Probes deep-links + missing-dashboard gaps
    legacyPlaceholders.tsx  Stub pages for legacy modules during migration
  components/
    AppLayout.tsx       Sidebar + main content shell
    ErrorBoundary.tsx   Root-level render error catcher
    LoadingScreen.tsx   Indeterminate loading indicator
    Forbidden.tsx       403 surface for permission-gated routes
  lib/
    config.ts           VITE_* env var ingestion
    queryClient.ts      TanStack Query defaults
    a11y/               Accessibility helpers (focus trap, live regions)
    admin/              Legacy module catalog + support-console helpers
    api/
      client.ts         Bearer + ErrorEnvelope + 401 refresh + missing-reason retry
      reason.ts         X-Admin-Reason predicate, resolver, sessionStorage
      tokenStore.ts     Access / refresh token state
      types.ts          Hand-authored types for the admin subset of the API
    auth/
      AuthProvider.tsx, AuthContext.ts, useAuth.ts
      ReasonContext.tsx  Modal-driven reason resolver
      ScopeContext.tsx   Active scope selection
      navConfig.ts       Permission-driven nav definition (source of truth)
      scope.ts           Scope parsing and default selection
```

## Reference material

The legacy Bil24 / TixGear admin reference map lives at
`09_autoforge/admin_ui/legacy_admin_reference_map.yaml`. It is **context
only** — the modern admin web does not depend on it at runtime. Use it
to cross-check which legacy concepts (frontends, agents, fiscal, POS,
seating, promotions, reports) map onto each modern surface, and to
understand why each "shell only" module is annotated the way it is.

Related architecture documents (under `08_architecture/`):

* `00_backend_architecture_brief_ru.md` — modular monolith brief.
* `10_compliance_security_privacy_ru.md` — audit/reason gate rationale.
* `11_architecture_decision_log_ru.md` — RBAC + scope decisions.
* `12_master_platform_specification_ru.md` — full platform contract.
* `13_backend_go_initial_specification_ru.md` — backend-foundation source.

For AutoForge guardrails and the SuperAdmin UI task statement see
`09_autoforge/admin_ui/autoforge_admin_task_statement.md` and
`09_autoforge/00_AGENT_GUARDRAILS.md`.
