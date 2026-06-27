# Session 2026-06-27 -- Feature #225 SAUI-09 audit-reason gap for network mutations

Closed audit-reason gap on operator-network mutations.

## Backend (apps/backend/internal/platform/httpserver)

- networks.go: requireAdminReason on create/update/archive handlers; reason stamped into audit metadata under `reason`.
- network_users.go: requireAdminReason on assign + remove; reason stamped.
- network_orgs.go: requireAdminReason on attach + detach for organizers and agents; reason stamped.

## OpenAPI (apps/backend/openapi/openapi.yaml)

Inlined X-Admin-Reason header parameter on all 9 mutation operations (required: true, minLength: 1). Each 400 response copy lists `missing/empty X-Admin-Reason header (superadmin.missing_reason)`. Inlined rather than shared via $ref because `openapi_docs_test` walks YAML directly without dereferencing $ref.

Generated TS client regenerated via `npm run gen-ts-client`.

## Admin-web (apps/admin-web/src/lib/api)

- reason.ts: `requiresAdminReason` now optionally takes a method. New `REASON_REQUIRED_MUTATION_PREFIXES` (`/v1/operator-networks` and `/v1/admin/networks`) triggers the reason gate only on POST/PATCH/PUT/DELETE so list/detail GETs do not prompt.
- client.ts: threads `opts.method` into `requiresAdminReason` at the initial-attempt and retry call sites.

## Tests

- Backend: new `networks_saui09_test.go` with 14 cases (missing reason on all 9 mutation handlers, blank-reason rejection, present-reason ordering for create + assign, audit metadata reason stamp).
- Pre-existing tests updated to set X-Admin-Reason so body-validation paths stay reachable: `networks_208_test.go`, `network_users_209_test.go`, `network_orgs_210_test.go`, `network_authz_214_test.go`.
- Frontend: `reason.test.ts` gained 16 new `it.each` cases covering the method-aware matrix.

## Verification

- `go test ./apps/backend/internal/platform/httpserver/...` -- ok (docker golang:1.24)
- `npm run gen-ts-client` -- ok
- `npm run check-ts` -- ok
- `npm --prefix apps/admin-web run type-check` -- ok
- `npm --prefix apps/admin-web run test` -- 244 of 244 pass

Marked feature 225 passing.
