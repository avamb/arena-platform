# Arena Sold Out — hosted sales page

A tiny static Vite + vanilla TypeScript app. It resolves
`/{org_slug}/{event_slug}` against `GET /v1/public/pages/{org_slug}/{event_slug}`
and mounts the `<arena-tickets>` widget, self-hosted from the same nginx
container at `/widget/v1/arena-tickets.js` (never jsDelivr or another
third-party CDN — see `Dockerfile`).

## Local development

```
npm install
npm run dev
```

The dev server does not serve `/widget/v1/arena-tickets.js` (that file only
exists after the Docker widget-build stage runs). To see the full page with
a live widget locally, build the widget once and copy its output into
`public/widget/v1/`, or use the Docker image (below).

## Scripts

- `npm run dev` — Vite dev server.
- `npm run build` — type-check (`tsc --noEmit`) then production build.
- `npm test` — Vitest (jsdom) unit tests: path parsing, locale resolution,
  and the not-found / loading / error / event render states.
- `npm run type-check` — `tsc --noEmit --pretty`.

## Environment

- `VITE_API_BASE_URL` — backend API base URL, embedded into the JS bundle
  at build time (e.g. `https://api.arenasoldout.com`). Empty means
  same-origin relative requests.

## Docker image

Build context is the **repo root** (the image also compiles
`apps/widget`):

```
docker build -f apps/tickets-page/Dockerfile \
  --build-arg VITE_API_BASE_URL=https://api.arenasoldout.com \
  -t arena-tickets-page:dev .
```

See `docs/ops/tickets_page_runbook_ru.md` for the full deployment runbook
(how an event becomes visible, Dokploy notes, CORS).

## Known gaps / future waves

- **No server-side rendering.** The SPA fallback (`try_files $uri
  /index.html`) always answers `HTTP 200`, including for an unknown
  `/{org_slug}/{event_slug}` — the branded "not found" state is rendered
  client-side after the resolve call fails, not as a true HTTP 404. Search
  engines and link-preview crawlers (Slack, Telegram, WhatsApp, Twitter/X)
  execute no JavaScript for Open Graph / Twitter Card tags, so **link
  previews for a shared event URL do not work today** — `<meta
  property="og:*">` tags would need to be injected server-side (or via a
  prerender/edge-function layer) before this page can produce a rich
  preview. Tracked as a later wave; do not attempt to fake it with a static
  meta tag in `index.html`, since it cannot vary per event.
- **Slugs only, no Host-based routing.** This app resolves purely from the
  path (`{org_slug}/{event_slug}`) so the same build can later be pointed
  at a client's own domain (a future wave) without touching this resolution
  logic — see the plan note in the feature ticket. Do not add
  subdomain/Host-header branching here.
