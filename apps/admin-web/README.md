# Arena Admin Web

Operational admin shell for the Arena ticketing platform. React + TypeScript
+ Vite + TanStack (Router, Query, Table) + React Hook Form + Zod.

## Status

Scaffold only (SAUI-01). The first authenticated screen is the admin
workspace, not a marketing page. Authentication wiring (SAUI-02) and
backend-driven content land in follow-up tasks.

## Local development

```bash
# 1. Copy env template
cp apps/admin-web/.env.example apps/admin-web/.env.local
# 2. Edit .env.local to point VITE_API_BASE_URL at your local backend
# 3. From repo root:
npm install
npm run admin:dev   # http://localhost:5174
```

## Root scripts (added to /package.json)

| Script              | Action                                            |
| ------------------- | ------------------------------------------------- |
| `npm run admin:dev` | Start Vite dev server with HMR                    |
| `npm run admin:build` | Type-check (tsc -b) and produce production bundle |
| `npm run admin:test`  | Run Vitest                                        |

These scripts run inside `apps/admin-web` and do not interfere with the
existing root `gen-ts-client` and `check-ts` scripts, which continue to
operate against `apps/backend/openapi/clients/ts/index.d.ts`.

## Architecture

```
src/
  main.tsx              StrictMode + ErrorBoundary + QueryClient + Router
  router.tsx            createRouter wired to routeTree
  routeTree.ts          Manually-assembled route tree (no codegen step)
  routes/
    __root.tsx          Root route, AppLayout, devtools
    index.tsx           Authenticated workspace landing
    login.tsx           RHF + Zod login scaffold (not yet wired)
  components/
    AppLayout.tsx       Sidebar + main content shell
    ErrorBoundary.tsx   Root-level render error catcher
    LoadingScreen.tsx   Indeterminate loading indicator
  lib/
    config.ts           VITE_* env var ingestion
    queryClient.ts      TanStack Query defaults
```

No production route ships mock business data. Empty states are explicit.
