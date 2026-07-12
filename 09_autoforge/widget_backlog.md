# AutoForge backlog: ticket-selection widget — Wave WID

Updated: 2026-07-11
Status: planning artifact for AutoForge. This file is not an implementation.
Design authority: `08_architecture/16_ticket_widget_ux_and_technology_ru.md`
(owner-approved UX research, technology decision and principles — READ IT
FULLY FIRST). Owner decisions therein (§6, §7) are final.

## 1. Goal

Ship the platform's own embeddable ticket-purchase widget: a buyer opens
an organizer's page, sees the seat map instantly, taps seats (or GA
zones), pays via Stripe, and receives tickets — **without accounts,
codes, or page reloads, optimized for mobile**. This is the
highest-leverage surface of the whole platform: most tickets are bought
on phones, and the owner has declared buyer UX the critical priority.

The widget replaces the licensed Bil24 fluwid (Flutter Web, ~4 MB)
whose UX flaws are documented in the design note §2. Target: **≤150 KB
gzip total JS to interactive seat map**, first render ≈1 s on 4G.

## 2. Fixed decisions (owner, 2026-07-10)

1. **Stack: TypeScript + Svelte 5, compiled and packaged as a Web
   Component** (`<arena-tickets>`, Shadow DOM). No React/Vue/Flutter.
   New app `apps/widget` (Vite build, single-file IIFE/ES output +
   custom-element define).
2. **Seat map renders as SVG in the DOM** from the canonical geometry
   JSON — every seat is a focusable element with an aria-label. Canvas
   is a later large-venue milestone, NOT this wave.
3. No buyer accounts/cabinet in v1. No email-verification codes ever.
   **E-mail is entered exactly once, at the payment step.**
4. Payment: **redirect to Stripe Checkout** (the platform's existing
   payment-intent flow). Embedded Elements — out of scope.
5. Buyer fields beyond email are **org/channel-toggleable flags**
   (name, phone; default off), designed as a field LIST for future
   custom fields.
6. Hybrid venues: **zero switchers** — GA/standing zones are tappable
   areas on the map itself; geometry-less GA tiers are always-visible
   cards under the map; ONE mixed cart, one hold timer (design note
   §4.11).
7. i18n: en/ru/cs/he with RTL support for he. Theming via CSS custom
   properties on the host element (design tokens; deeper than fluwid's
   palette-only).

## 3. Non-negotiable rules

- Guardrails `09_autoforge/00_AGENT_GUARDRAILS.md` apply (esp. #15:
  totals always come from the platform; the widget NEVER computes
  prices; #16: large-venue path must stay open — status polling uses
  the versioned delta contract).
- WCAG 2.2 AA: seat = button role, aria-label «Sector, row N, seat M,
  price, status», full keyboard operation (arrows navigate within a
  row, tab between rows/zones), visible focus, contrast tokens.
- No mock data anywhere; the demo page runs against the local compose
  backend (seeded by arena-seed) or a fixture-driven Playwright server.
- Bundle-size budget is a CI GATE, not advice: gzip of the shipped
  entry ≤150 KB fails the build (size check script + CI job).
- The widget consumes ONLY the public feed-token surface (below). It
  must never require platform JWTs.

## 4. Backend contract — what exists vs what this wave must add

Existing public (anonymous, feed-token-scoped) surface the widget rides:
- `GET /v1/public/feeds/{feed_token}` + events/sessions payloads —
  sessions carry `schema_url` / `seat_status_url` for seated/hybrid
  sessions (hfeed/public_feed.go:135).
- `GET /v1/event-sessions/{id}/schema` — geometry+categories→tier
  resolution, strong ETag = geometry checksum, immutable cache.
- `GET /v1/event-sessions/{id}/seat-status` (+`?since_version=`) —
  snapshot + delta.
- `POST /v1/public/feeds/{feed_token}/checkout/start` — GA checkout
  initiation (tier+qty) → checkout session + payment redirect URL.
- Stripe payment-intent flow + success/fail redirect (hcheckout).

**Wave WID-0 (backend, Go — do this FIRST):** close the public-surface
gaps. Follow every convention in `09_autoforge/seating_backlog.md` §4
(sub-package pattern, gates, OpenAPI 3.1 + drift, gen/queries style):

- **WID-0a. Seats/mixed carts through the public checkout.** Extend
  `POST /v1/public/feeds/{feed_token}/checkout/start` to accept
  `{"session_id", "seats": ["<seat_key>", …], "ga_items":
  [{"tier_id","quantity"}, …]}` in any combination valid for the
  session's admission_mode (delegating to the hcheckout seated/mixed
  reservation path — SEAT-C1 + the ga_items extension; if ga_items has
  not landed yet, implement it per seating_backlog SEAT-C1 first).
  Response gains the reservation `expires_at` and a
  `checkout_token` (below). Deterministic 409 with conflicting seat
  keys passes through to the widget.
- **WID-0b. Anonymous order-status endpoint.** `GET
  /v1/public/checkout/{checkout_token}` — checkout_token is an opaque
  high-entropy token minted at start (NOT the checkout session UUID),
  returning status (pending/paid/expired/failed), the held seats/zones
  with labels+prices (platform-computed totals), `expires_at`, and —
  once paid — per-ticket `{sector,row,number, human_code}` plus links
  to ticket PDFs. Powers the widget's cart restore, success page and
  the payment-return deep link. Rate-limited like the public feed.
- **WID-0c. Hold-expiry recovery.** `POST
  /v1/public/checkout/{checkout_token}/recover` — one-click re-capture
  of the SAME seats/zones if still available (fresh reservation +
  expires_at; 409 with per-seat availability otherwise). Design note
  §4.4: the fluwid dead-end this kills is documented from a live test.
- **WID-0d. Buyer-field flags in the public payload.** Channel/org
  config exposing `{collect_name: bool, collect_phone: bool}` (field
  LIST shape: `buyer_fields: [{key, required, enabled}]`) in the feed
  session payload; checkout/start accepts `buyer: {email, name?,
  phone?}` and validates per flags. Migration for the flags on
  sales_channels (default off) — organizers toggle per channel.
- **WID-0e. Funnel telemetry sink (minimal).** `POST
  /v1/public/feeds/{feed_token}/events` accepting batched widget
  funnel events (schema_viewed, seat_selected, cart_opened,
  payment_started, recovered) → outbox/audit-grade table for later
  reporting. Fire-and-forget, heavily rate-limited, no PII beyond the
  checkout_token linkage.

## 5. Frontend waves (apps/widget)

**WID-A. Scaffold + toolchain.** Vite + Svelte 5 + TS strict; build
to `dist/arena-tickets.js` (custom element, Shadow DOM); vitest +
Playwright (repo has playwright-cli); size-limit script wired as `npm
run size` failing >150 KB gzip; demo page `apps/widget/demo/index.html`
with attribute matrix (feed-token, session-id, locale, theme vars); CI:
new `widget` job in .github/workflows/ci.yml (install, test, build,
size gate) — additive job, keep ci_workflow_test.go green (update its
expected-jobs list if it pins one).

**WID-B. Event/session view + seat map render.** Fetch feed payload →
session list (date chips like fluwid's, price-category legend); fetch
schema (honor ETag), render SVG: sections/rows/seats + decor backdrop
+ standing_zones as labeled polygons; category colors from geometry;
pan/zoom (pointer events: pinch/drag/wheel; fit + reset buttons);
seat-status snapshot + delta polling (2–5 s, backoff on hidden tab via
Page Visibility); statuses recolor without re-render (keyed updates).
Budgets: 1500-seat map interactive <100 ms after data; no layout
thrash (transform-based zoom).

**WID-C. Selection, hybrid, cart, hold timer.** Tap/keyboard seat
selection with optimistic marking; GA zone tap → quantity stepper in
the same bottom sheet; geometry-less GA tiers as cards under the map;
floating mini-cart (count+total, platform-provided prices only) →
full cart bottom sheet: line items (seat rows + zone rows), visible
countdown from `expires_at`, warning at T-2min, remove lines, single
CTA. Single-seat-gap warning flag (client-side hint only; org policy
enforcement is future backend work). «Best available» button: picks N
adjacent seats in a chosen category from the schema data (client-side
heuristic over row ranks).

**WID-D. Checkout handoff + result + recovery.** Buyer form per
buyer_fields flags: email (single entry; typo suggestions for common
domains — gmail/outlook/yandex/seznam etc., inline confirm), name/
phone when enabled; POST checkout/start (seats+ga_items) →
redirect to payment URL; return deep-link `?checkout_token=…` →
order-status view: paid → success panel with order ref, seat list,
inline QR/human_code per ticket (from WID-0b payload) + «send again»
note; expired → recovery CTA (WID-0c) with the exact re-capture UX
from the design note; failed → retry path. All copy localized.

**WID-E. Theming, i18n, a11y hardening.** CSS custom-property token
set (colors, radius, font-stack) with docs; he RTL layout audit;
axe-core automated pass in Playwright (WCAG 2.2 AA rules) over the
demo page states — gate in CI like ops/accessibility does for the WP
plugin; keyboard-only purchase E2E.

**WID-F. Distribution.** Embed snippet docs (script tag + attributes,
CSP note, iframe fallback page served from the same dist); WordPress:
shortcode/Gutenberg block in apps/wp-plugin rendering the element with
the channel's feed token; versioned CDN layout (dist/vN/); publish
artifact from CI on master (extend the existing build-and-push wave or
attach to GH release — document the chosen path).

## 6. E2E acceptance (Definition of Done for the wave)

Playwright, against compose backend seeded with the Palác Akropolis
plan (260 seats) bound to a hybrid test session (seats + one standing
zone + one geometry-less GA tier):
1. Cold load on emulated Moto G / Fast 3G: interactive map ≤3 s;
   transferred JS ≤150 KB gzip (hard assert).
2. Select 2 adjacent seats + 2 GA units → ONE cart, one timer; totals
   equal platform response verbatim.
3. Concurrent second browser holding one of the seats → 409 path shows
   the conflicting seat highlighted, cart intact.
4. Full purchase through Stripe test mode → success panel shows order
   ref, 4 tickets with sector/row/seat + human codes (once SEAT-C4
   lands; otherwise ticket ids) — no email code anywhere in the flow.
5. Let a hold expire → recovery CTA re-captures the same seats when
   free; shows per-seat availability when not.
6. Keyboard-only + screen-reader (axe clean, focus order sane) for the
   entire flow; he locale renders RTL.
7. Existing backend suites stay green; lint 0; drift both ways; widget
   CI job green.

## 7. Out of scope (this wave)

Canvas/large-venue renderer, waiting room, embedded Stripe Elements,
buyer cabinet, Telegram Mini App variant and messenger delivery
(design note §7.1 — next wave), seat-map visual editor, custom buyer
fields beyond name/phone, offline/PWA.

## 8. Wave WID-R — remediation (added 2026-07-11 after implementation review)

The WID-A..F/E2E delivery (#318-#329) was reviewed. The backend
foundation (WID-0) is solid and stays. The following gaps MUST be
closed before the widget can be called done. Read the review context
in git history (commits 6fe94be..b5a1ecd fixed the immediate backend
and safety defects); this wave is the remaining product work.

**WID-R1. Wire the purchase loop into the running element.** The
WID-C/D modules exist and are unit-tested but are NOT part of
`<arena-tickets>`: ArenaTickets.svelte imports only SessionList +
SeatMapView; BuyerForm.svelte and OrderStatus.svelte are orphaned;
cart.ts/selection.ts/checkout.ts are exported but unconsumed;
SeatMapView has no seat click/keyboard selection handlers; the feed
is not fetched (loadFromFeed is a no-op and admission_mode is
hardcoded). Deliver the assembled flow per the design note §4:
seat tap/keyboard selection → floating mini-cart → cart bottom sheet
with ONE hold timer (mixed seat+GA lines) → buyer form per
buyer_fields flags → checkout/start → redirect → `?checkout_token=`
deep-link restore → order status (paid: order ref, seats, human codes,
PDF links; expired: recovery CTA hitting the recover endpoint; failed:
retry). Persist checkout_token (sessionStorage) for the deep link.
Totals come ONLY from the checkout/start response (the platform now
returns the priced breakdown — guardrail #15; delete cartTotal-style
arithmetic).

**WID-R2. Surface conflict details.** Use the structured error from
api.ts (code + details.conflicts with real backend codes
`reservation.seats_conflict` and per-seat statuses) to highlight
conflicting seats on the map and keep the rest of the cart intact.

**WID-R3. Real acceptance E2E.** Rewrite #329: Playwright drives the
ACTUAL rendered element against the LOCAL COMPOSE BACKEND seeded with
the Palác Akropolis plan (260 seats) on a hybrid session — no
page.route mocks for платформенные endpoints, no fetch()-in-evaluate
tautologies, no test-computed expectations. Cover widget_backlog §6
items 1-7 for real: cold-load budget asserted on gzip transfer, mixed
cart one-timer, concurrent 409 from a second context, full purchase
through the payment flow in test mode, hold-expiry recovery,
keyboard-only purchase, RTL. The static-server mock config may remain
as a separate fast smoke suite, clearly named as such — it is not
acceptance. claude-progress.txt must stop counting mocked flows as
passing acceptance.

**WID-R4. A11y completion.** With the map no longer aria-hidden
(fixed), implement the design-note §3 keyboard model (arrows within a
row, tab between rows/zones) and ensure aria-labels carry price+status
live updates; axe gate must run against a POPULATED map state. When
the roving tabindex lands, update the no-trap E2E in keyboard.e2e.ts
accordingly (it currently tolerates per-seat tabbing by design).

### Prerequisites ALREADY LANDED (state as of 2026-07-12, master 1753e1c) — do not redo

- `POST …/checkout/start` returns the platform pricing breakdown
  (`pricing` with per-tier `lines`) and prices seats correctly;
  documented in openapi (`PublicFeedCheckoutStartResponse.pricing`).
- `api.ts postCheckoutStart` throws typed `ApiError{status, code,
  details}` (409 carries `details.conflicts`).
- `reservation_ga_items` (migration 0063): order-status returns GA
  lines with labels+prices; recovery re-captures seats AND GA.
- decor_svg passes through the strict `svg-sanitize` module; seat map
  is AT-visible (labeled group, focusable seats); accent/error colors
  are WCAG-AA (#4f46e5 / #b91c1c) — do not reintroduce #6366f1/#dc2626.
- The mocked Palac suite is excluded via `testIgnore` in
  playwright.config.ts; the Playwright webServer script is
  `scripts/serve-demo.cjs` (CommonJS — the package is ESM).
- Bil24 gateway RESERVATION is real (hcheckout hold API); human codes
  (SEAT-C4) are live — the paid order-status payload already carries
  them.

### CI note for WID-R3

The Widget CI job must run the real-backend E2E in CI, not only
locally: boot the backend inside the job (docker compose services or
postgres service-container + the Go binaries — your choice), seed the
Akropolis plan + hybrid session via arena-seed or the API, then run
Playwright against it. Keep the job wall-clock reasonable (parallelize
with the existing unit/size steps if needed). The mock smoke suite may
stay as a separate fast step, clearly labeled.

Out of scope for WID-R: everything in §7 above.
