# Arena Sold Out — organizer storefront (shop page)

A static Vite + vanilla TypeScript app that reproduces the Shopify storefront of
the first client (Einat Winery, Dawn 11, Russian) **one to one** and renders it
from the platform's public shop API (spec
`08_architecture/47_merch_shop_module_ru.md`, sections 4 and 5).

- Served at `https://shop.arenasoldout.com/{org_slug}/...` (stand:
  `shop-staging.arenasoldout.com`), or at the organizer's own domain
  (`einatwinery.com`) with root paths: the app reads `location.host`, asks
  `GET /v1/public/shop/by-host?host=` and caches the answer for an hour.
- Dawn is MIT: its CSS (`src/theme/`), its scripts (`public/theme/`, unmodified:
  header drawer, search modal, quantity input, slider, gallery, share) and the
  markup structure (`src/views/`) are reused directly. See `LICENSE-Dawn.txt`.
- Russian only in this version.

## Pages

| Path (Shopify shapes kept)         | What                                                                      |
| ---------------------------------- | ------------------------------------------------------------------------- |
| `/`                                | announcement bar, header, image banner, featured collection, footer       |
| `/collections/all`                 | all products (card grid, quick add)                                       |
| `/products/{slug}`                 | gallery, price, variant pills, quantity, add to cart, description, share  |
| `/cart`                            | cart page (plus the "item added" notification, as on the live theme)      |
| `/checkout`                        | our own page: contacts, address, flat delivery, 18+ consent, "Оплатить"   |
| `/checkout/{order_token}`          | status: pending (link back to payment) / paid / expired / cancelled       |
| `/checkout/result`                 | the provider's `return_url`; forwards to `/checkout/{token}`              |
| `/pages/{slug}`, `/policies/{slug}`| static pages and policies from the API                                    |
| `/search?q=`                       | client-side search over the catalog                                       |
| anything else                      | 404 in the theme                                                          |

Handles may be Cyrillic; paths are percent-encoded once (`src/lib/route.ts`).
The 18+ gate (Hebrew text, as on the live site) is remembered in `localStorage`.

## Local development

```
npm install
npm run dev:mock                                   # mock API on http://localhost:8787
VITE_API_BASE_URL=http://localhost:8787 npm run dev   # http://localhost:5173/einatwinery/
```

(PowerShell: `$env:VITE_API_BASE_URL="http://localhost:8787"; npm run dev`.)

The mock (`mock/mock-api.cjs`) serves the 7 live products, the pages and
policies, and a fake hosted payment page (`/mock-pay/{token}` with Pay / Expire /
Cancel links) so the whole flow can be walked without the backend. Fixtures in
`mock/fixtures/` are built from the offline Shopify export:

```
npm run fixtures -- C:/Projects/arena_new/.local/einatwinery_export
```

Against the real API set `VITE_API_BASE_URL` to its origin (for example
`https://api-staging.arenasoldout.com`). The API must answer CORS for the shop
origin and `return_url` of the checkout must be on its allow-list.
Step by step release of a shop on the organizer's domain (DNS, Dokploy, CORS, opening the shop,
payment channel, 301, robots/sitemap): `docs/ops/shop_release_checklist_ru.md` in the repo root.

## Scripts

- `npm run dev` — Vite dev server.
- `npm run dev:mock` — mock shop API.
- `npm run build` — type-check, then production build into `dist/`.
- `npm test` — Vitest (jsdom): money formatting, routing, cart logic, image
  URLs, sanitizer, view rendering.
- `npm run type-check` — `tsc --noEmit`.

## Docker image

Build context is the **repo root**:

```
docker build -f apps/shop-page/Dockerfile \
  --build-arg VITE_API_BASE_URL=https://api.arenasoldout.com \
  -t arena-shop-page:dev .
```

nginx 1.27 on port 8080 (non-root), `/health`, SPA fallback, hashed assets
cached for a year, `/theme/*` (Dawn scripts) for five minutes, CSP built from
`VITE_API_BASE_URL` (`security-headers.conf.template`). The CI job
`shop-page-build` runs type-check, tests, build and a container smoke test.

## How it is put together

- `src/lib/` — pure logic with unit tests: `money.ts` (minor units to
  `250.00 NIS`), `route.ts`, `cart.ts` (basket, totals, checkout form),
  `cdn.ts` (responsive image URLs), `sanitize.ts`, `api.ts` (types and client).
- `src/views/` — functions returning Dawn's markup as strings. `common.ts` holds
  the price block and the product card (Dawn `price` / `card-product` snippets).
- `src/main.ts` — boot, router, delegated event handlers (add to cart, variant
  pills, cart quantity, checkout, age gate, notification). Every navigation
  re-renders the page; Dawn's custom elements upgrade as they appear.
- `src/lib/theme.ts` — what Shopify's theme editor held and the API has no field
  for: logo, banner, footer logo, social links, the age-gate texts.

## API contract notes (the real API, backend lane feat/shop-core)

`src/lib/api.ts` is the only file that knows the wire shapes; it maps them to
the model the views use. Read it with `apps/backend/internal/platform/httpserver/hshop/`.

- Catalog `GET /v1/public/shop/{org}` answers `{org, settings, products, pages}`.
  There is **no menus block**: the header menu is "Магазин" plus every static page,
  the footer menu is the four policy pages (`POLICY_SLUGS`: privacy-policy,
  refund-policy, terms-of-service, contact-information), in API order.
- Pages come from `GET /v1/public/shop/pages/{org}` (all pages with text, one call,
  kept in memory for the tab). Policies are ordinary pages, told apart by slug.
  The backend stores `body_html` as sent (no sanitizer): the storefront's own
  sanitizer (`src/lib/sanitize.ts`) is the only filter, and it keeps `<style>` and `<section>`.
- A product answers `{currency, product}`; a variant has `in_stock` (the storefront's
  `available`) and `available` = units left or null. There is no product id (the slug
  stands in), no option name (variants carry `option1..3`) and no picture size.
- Picture URLs are signed and short-lived: never persisted (the basket stores variant ids
  and quantities only; the host cache stores the org slug only).
- Checkout body is decoded strictly (unknown members are 400): `items`, `buyer`, `ship`,
  `delivery_note`, `locale`, `return_url`, `age_confirmed` (true is required while the shop
  has an age gate). Answer: `order_token, number, status, payment_url, currency, subtotal,
  shipping_fee, total, expires_at`; a free order is already `paid` with an empty
  `payment_url` and goes straight to its status page.
- `return_url` is `<origin><base>/checkout/result`; the provider appends `?checkout_token=`
  and the app forwards to `/checkout/{token}` (a missing token falls back to the one kept
  in localStorage). The origin must be on the backend's return-URL allow-list.
- `409 shop.out_of_stock` carries `error.details.items[{variant_id, requested, available}]`;
  the page refreshes the catalog and asks the buyer to check the cart.
- Product kinds (PR #275 final contract): `kind` is physical, digital or service and
  `option_names` lists up to three option names in the order of the variants'
  `option1..option3`. One option keeps Dawn's single group of pills, several get one
  group per option (an unsold combination falls back to the nearest variant).
- `settings.max_line_qty` caps the quantity inputs, the add-to-cart and the stored basket.
- A cart with no physical line takes no address and pays no shipping: the checkout page
  hides the address block and the request has no `ship`. One physical line brings both back.
- A paid order with digital lines calls `GET /v1/public/shop/checkout/{token}/downloads`
  and lists `downloads[]` as links. The URLs are signed for 15 minutes and never stored,
  "Обновить ссылки" asks again, a 404 means not paid yet and the status poll continues.
- `body_html` of products and pages is sanitized by the backend (no images, tables, inline
  styles or classes) and is rendered as received.
- Optional extensions used when present: `related_slugs`, `images[].width/height`,
  `settings.shop_name` (else `org.name`).
- The mock serves three extra products (digital, service, two options) from
  `mock/fixtures/extras.json`. Start it with `npm run dev:mock:live` (no extras) before
  `scripts/compare-live.mjs`, which compares against the live shop's seven products.

## Before the domain moves

- Logo, banner and footer logo (`src/lib/theme.ts`) are served from this app
  (`public/theme/brand/`), so they survive the DNS switch. The product pictures
  come from the API (arena media). Only the mock fixtures (`mock/fixtures`)
  still carry old-shop CDN links; they are never served in production.
- Checkout captcha: when the public shop answer carries `captcha_site_key`, the
  checkout form renders Cloudflare Turnstile and sends `captcha_token`; the CSP
  (`security-headers.conf.template`) allows `challenges.cloudflare.com` for it.
- The age-gate texts and contact e-mail are static / from settings.
- Old URL shapes and their fate: `docs/redirects_301.md`.

## Visual check against the live shop

`PLAYWRIGHT_CORE=<path to playwright-core> node scripts/compare-live.mjs [page ...]` (set `VP=mobile` or
`VP=desktop` for one viewport) shoots www.einatwinery.com and this app (mock API on 8787, vite on 5180) and
writes `docs/screenshots/<page>-<viewport>-side.png` with the share of differing pixels. Result on
2026-10-28: nearly every page is 0.0 to 0.4 percent (desktop and 390 px phone). Known remaining
differences: the empty cart omits the live "log in" prompt (no customer accounts), related-product images
load lazily so the live shot shows blanks, the live cart notification page keeps a different scroll offset,
and the footer reads "На платформе Arena Sold Out". The 301 table is in `docs/redirects_301.md`.
