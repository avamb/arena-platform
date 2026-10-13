# Arena event center (web)

The organizers' web event center: the same functions as the Telegram
event-center bot, in a browser. Spec: `08_architecture/37_web_event_center_ru.md`.

This app is for organizers only. The platform superadmin console is
`apps/admin-web`; never import its screens here.

## Run locally

1. Copy `.env.example` to `.env.local` and set `VITE_API_BASE_URL`.
2. Add the dev origin (`http://localhost:5176`) to `CORS_ALLOWED_ORIGINS` of arena-api.
3. Start the dev server:

```bash
npm --prefix apps/event-center run dev
```

## Tests

- `npm --prefix apps/event-center test` runs vitest. It includes
  `src/parity/parity.test.ts`, which checks every web call against
  `apps/backend/openapi/openapi.yaml` and the parity table of the spec.
  After you change `src/parity/matrix.ts`, rewrite the table with
  `UPDATE_PARITY=1 npm --prefix apps/event-center test`.
- `npm --prefix apps/event-center run test:smoke` runs the Playwright smoke
  (375x812) against a mocked API (`tests/smoke/mockApi.ts`).

## Add an API call

1. Add the route to `src/lib/api/routes.ts` (the path template exactly as in openapi.yaml).
2. Add a function to `src/lib/api/endpoints.ts`.
3. Add the route to its function in `src/parity/matrix.ts` and rewrite the spec table.

## Deploy

Dokploy application, like `tickets-page`: Dockerfile `apps/event-center/Dockerfile`,
build context the repository root, build argument `VITE_API_BASE_URL`, port 8080,
health check `GET /health`. Add the app's origin to `CORS_ALLOWED_ORIGINS` of arena-api.

nginx sends a strict Content-Security-Policy and the other security headers from
`security-headers.conf.template`, included in EVERY location (a location with its own
`add_header` drops the server-level ones). `scripts/check-headers.sh` asserts them on a
running container; CI also runs the smoke against the built image, so the CSP is tested.
