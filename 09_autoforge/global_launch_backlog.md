# AutoForge backlog: global (EU + worldwide) MVP for ticketing sales

Updated: 2026-06-29
Status: planning artifact for AutoForge. This file is not an implementation.

## Goal

Close the smallest set of gaps required to take an organizer through the
end-to-end flow "create org → create venue → create event/session → list
ticket tiers → take payment → deliver e-ticket → notify external scanner".

The platform is positioned for the EU and global markets. **Russia is out
of scope**: no 54-FZ fiscal receipts, no OFD integrations, no Sber /
Tinkoff acquirers, no INN/KPP/OGRN organization fields. The acquiring
stack is **Stripe (worldwide) + AllPay (Israel)** as already implemented
in `apps/backend/internal/adapters/`.

The visual seating-plan editor and per-seat sale flow are **deferred to a
later milestone**. The MVP sells general-admission tier inventory only
(uses existing `ticket_tiers` + `inventory_ledger` machinery).

The scanner is a separate external system. Integration is
**webhook-only**: the scanner subscribes to ticket lifecycle events via
`webhook_subscribers`, the same mechanism the legacy Bil24 notification
feed uses. No native scanner UI in this scope.

## Non-negotiable rules

- Source of truth is OpenAPI + backend code. Frontend does not invent shapes.
- Authorization is backend-enforced; UI hiding is usability only.
- Do not introduce mock or sample data anywhere in production paths.
- All new media (event posters, organization logos) must use the storage
  adapter defined in Wave G; no raw URL fields without a managed lifecycle.
- All cross-tenant SuperAdmin reads/writes continue to require
  `X-Admin-Reason` as already enforced by feature #246.

## Device targets per role preset

There is still **one** admin web app (`apps/admin-web`); device support is
a property of each role preset inside it, not of separate apps.

- **`platform_superadmin` preset** — desktop-only. Minimum supported
  viewport is 1280×800. Tables, dense filters, audit dialogs do not need
  to collapse to mobile. A "this view is not optimized for mobile"
  notice is acceptable below 1024px.
- **`org_admin` / organizer / `network_operator` / agent presets** —
  must work on mobile web (responsive, not a native app). Minimum
  supported viewport is 360×640. Primary flows (login, scope switch,
  list of orders/tickets/refunds, search, ticket detail with resend,
  event/session create-edit, status transitions) must be fully usable
  with touch.

Responsiveness is enforced by Wave M; every UI feature in waves O, V,
E, G, T, S that lives on an organizer/agent preset must satisfy the
acceptance criteria defined there.

## Required source inputs

AutoForge must read before starting any task in this backlog:

- `09_autoforge/00_AGENT_GUARDRAILS.md`
- `09_autoforge/admin_ui/autoforge_admin_task_statement.md`
- `09_autoforge/admin_ui/superadmin_ui_autoforge_tasks.md`
- `08_architecture/12_master_platform_specification_ru.md`
- `08_architecture/13_backend_go_initial_specification_ru.md`
- `08_architecture/03_platform_management_api_and_permissions_ru.md`
- `apps/backend/openapi/openapi.yaml`
- `apps/backend/internal/migrations/sql/` (current schema)

## Current contracts to respect (verify before coding)

- Org CRUD: `POST/GET/PATCH /v1/organizations` (#233, #238, #239) —
  currently exposes only `name`, `slug`, `country`, `default_locale`,
  `reservation_ttl_seconds`.
- Venue CRUD: `POST/GET/PATCH/DELETE /v1/organizations/{org_id}/venues`
  (#242) — currently `name`, `city_id`, `address` (free string),
  `capacity_default`.
- Event / session / ticket_tier handlers exist in
  `apps/backend/internal/platform/httpserver/{events,sessions,ticket_tiers}.go`
  but **are not documented in `openapi.yaml`** and there is no admin UI
  for them (`/events` route is a `LegacyModulePlaceholderRoute`).
- Outbox + webhook subscribers exist (`0002_outbox.sql`,
  `0040_webhook_subscribers.sql`). Event types in tests include
  `v1.order.placed`, `v1.ticket.created`. `v1.ticket.refunded` and
  `v1.ticket.scanned` are NOT yet emitted — see Wave S.
- Sales channels, channel settings, payment provider configs (Stripe,
  AllPay, CloudPayments, YooKassa) already have admin UI (#243, #244).
- GDPR primitives exist (`0018_gdpr.sql`); no new privacy work required
  for MVP beyond surfacing existing flows.

## Out of scope for this backlog

- Visual seating-plan editor, seat maps, per-seat inventory.
- POS / cashier (`TixCassa`) UI.
- 54-FZ fiscal receipts, OFD integration, Russian-specific tax IDs.
- Russian acquirers (Sber, Tinkoff).
- BI / report builder beyond the existing read-only `/v1/admin/*` endpoints.
- Migration of legacy Bil24 production data.

---

## Wave O — Organization legal & billing fields (EU/global)

Goal: an organization carries enough legal and payout data to be the
counterparty on a Stripe payout and to appear on a compliant EU invoice
or receipt.

### O-1. DB migration: organizations legal & contact fields

- New migration `0048_organizations_legal_fields.sql`.
- Add columns to `organizations`:
  - `legal_name TEXT NULL` (juridical name, distinct from display `name`).
  - `tax_id TEXT NULL` (generic VAT/EIN; format validated app-side, not DB-side).
  - `tax_id_scheme TEXT NULL` (enum-by-CHECK: `eu_vat`, `gb_vat`, `il_vat`,
    `us_ein`, `other`).
  - `registration_number TEXT NULL` (company registry, e.g. HRB, OGRN-equivalent
    for the home country).
  - `legal_address_line1`, `legal_address_line2`, `legal_address_postal_code`,
    `legal_address_city`, `legal_address_country` (`country` ISO-3166-1 alpha-2).
  - `contact_email TEXT NULL`, `contact_phone TEXT NULL`,
    `website_url TEXT NULL`.
  - `logo_media_id UUID NULL` (FK to media table in Wave G; nullable until G ships).
  - `kyb_status TEXT NOT NULL DEFAULT 'unverified'`
    (CHECK in `unverified, pending, verified, rejected`).
  - `kyb_verified_at TIMESTAMPTZ NULL`.
- No new RBAC permissions; existing `org.update` covers it.
- Down migration drops the columns.

### O-2. Bank-account child table

- New table `organization_bank_accounts` in the same migration.
- Columns: `id uuidv7 PK`, `org_id FK NOT NULL`, `label TEXT`,
  `holder_name TEXT NOT NULL`, `iban TEXT NULL`, `bic_swift TEXT NULL`,
  `account_number TEXT NULL`, `routing_number TEXT NULL`,
  `currency CHAR(3) NOT NULL`, `is_default BOOLEAN NOT NULL DEFAULT FALSE`,
  `created_at`, `updated_at`, `deleted_at`.
- Partial unique index on `(org_id) WHERE is_default AND deleted_at IS NULL`
  so each org has at most one default account.
- No FX or settlement logic in this wave — table is metadata only;
  Stripe Connect / AllPay payout config still lives in
  `payment_provider_configs`.

### O-3. OpenAPI: extend Organization schemas

- Extend `CreateOrganizationRequest`, `UpdateOrganizationRequest`,
  `OrganizationItem`, `OrganizationDetail` in `apps/backend/openapi/openapi.yaml`
  with all O-1 fields. All new fields optional on create; `legal_name`
  required on `PATCH` once `kyb_status = pending|verified`.
- Add nested `BankAccount*` schemas and a new path group
  `/v1/organizations/{org_id}/bank-accounts` (list/create/patch/delete).
  RBAC: `org.update`.

### O-4. Admin UI: Organization "Legal & billing" tab

- In `apps/admin-web/src/routes/organizations.tsx` add a new drawer tab
  "Legal & billing" between "Overview" and "Users".
- Two sections:
  - "Legal entity" form: legal_name, tax_id + tax_id_scheme, registration_number,
    full legal address, contact email/phone, website, logo upload (delegates
    to Wave G upload component when shipped; placeholder until then).
  - "Bank accounts" CRUD: list, add, edit, delete; designate default.
- Use server-side validation for `tax_id` format per `tax_id_scheme`.
- All copy goes through the existing i18n scaffolding (#251).

---

## Wave V — Venue address & operational metadata

Goal: a venue carries a structured address and the operational data
needed to schedule events correctly and to surface on a customer-facing
page later.

### V-1. DB migration: structured address & timezone

- New migration `0049_venues_extended.sql`.
- Add columns to `venues`:
  - `address_line1 TEXT NULL`, `address_line2 TEXT NULL`,
    `postal_code TEXT NULL`, `country CHAR(2) NULL` (ISO-3166-1 alpha-2;
    must equal `country` of the owning organization at insert time unless
    explicitly overridden).
  - `geo_lat NUMERIC(9,6) NULL`, `geo_lng NUMERIC(9,6) NULL`.
  - `timezone TEXT NULL` (IANA name; validated app-side against tzdata).
  - `contact_phone TEXT NULL`, `contact_email TEXT NULL`,
    `website_url TEXT NULL`.
  - `status TEXT NOT NULL DEFAULT 'active'`
    (CHECK in `active, draft, archived`).
- Keep the existing free-form `address` column for backward compatibility
  (admin UI hides it once structured fields are populated; left for
  read-only legacy data).
- Down migration drops the new columns.

### V-2. OpenAPI: extend Venue schemas

- Extend `VenueItem`, `CreateVenueRequest`, `UpdateVenueRequest` with V-1
  fields.
- Document `timezone` as IANA and add a `422 invalid_timezone` error response.

### V-3. Admin UI: structured address & venue meta

- In `apps/admin-web/src/routes/venues.tsx` replace the single "address"
  field with a structured form: country (preselected from org), city
  (existing `city_id` lookup), postal code, address line 1, address line 2,
  optional lat/lng (no map widget — plain numeric inputs in MVP), timezone
  (autocomplete from IANA list), capacity_default, contacts, website,
  status.
- Reuse the existing CRUD table with new columns: City, Country, Capacity,
  Status, Updated.

---

## Wave E — Event & session admin module

Goal: an organizer can create an event with a poster, define sessions,
attach ticket tiers, and publish. This is the largest gap by
implementation surface.

### E-1. DB migration: event metadata

- New migration `0050_event_metadata.sql`. Add columns to `events`:
  - `slug TEXT NULL` (unique per `org_id` where `deleted_at IS NULL`).
  - `short_description TEXT NULL` (≤ 280 chars; CHECK).
  - `genre TEXT NULL` (free-form for MVP; reference table later).
  - `age_rating TEXT NULL` (CHECK in `0+, 6+, 12+, 16+, 18+, NR`).
  - `duration_minutes INTEGER NULL` (CHECK > 0).
  - `poster_media_id UUID NULL` (FK to media table in Wave G).
  - `teaser_url TEXT NULL`, `trailer_url TEXT NULL`.
  - `meta_description TEXT NULL`, `meta_keywords TEXT NULL`.
- Add child table `event_artists`:
  - `id uuidv7 PK`, `event_id FK NOT NULL`, `name TEXT NOT NULL`,
    `role TEXT NULL`, `bio TEXT NULL`, `photo_media_id UUID NULL`,
    `sort_order INTEGER NOT NULL DEFAULT 0`, timestamps + soft-delete.
- Keep existing `image_url` column read-only for backfill; new code writes
  to `poster_media_id` once Wave G ships.

### E-2. OpenAPI: document events / sessions / tiers / publications

- Add to `apps/backend/openapi/openapi.yaml` the request/response schemas
  and path entries already implemented in
  `internal/platform/httpserver/{events,sessions,ticket_tiers,event_publications}.go`.
- Each endpoint specifies `permissions` and the standard error envelope.
- Include `translations` map for localizable fields (already handled in
  handler; was missing from spec).
- Regenerate Go server types and TypeScript client via
  `generate-clients.sh`. CI lint on the spec must stay green.

### E-3. Admin UI: events list & detail

- New route `apps/admin-web/src/routes/events.tsx` replacing the
  placeholder in `legacyPlaceholders.tsx`.
- List view: filters (org, status, visibility, date-range), columns
  (poster thumb, name, venue, next session, status, channels), pagination.
- Detail drawer with tabs: Overview / Sessions / Ticket tiers /
  Publications / Activity.
- Status transitions (`draft → published`, `published → cancelled`,
  `published → archived`) wired to existing endpoint.

### E-4. Admin UI: session sub-table

- Inside the event drawer, "Sessions" tab is a CRUD over
  `/v1/organizations/{org_id}/events/{event_id}/sessions`.
- Form fields: start_at, end_at, capacity_total, status.
- Enforce client-side `end_at > start_at` and warn on overlap with sibling
  sessions (server already has overlap detection).

### E-5. Admin UI: ticket-tier sub-table

- Inside the session row, "Tiers" expandable section is a CRUD over
  `/v1/.../sessions/{session_id}/tiers`.
- Form fields: name, pricing_mode (`fixed | free | pwyw`), price_amount +
  currency, pwyw_min/max (visible when `pwyw`), capacity, sale_window_start,
  sale_window_end, sort_order.
- Currency selector limited to the organization's supported currencies
  (Stripe & AllPay capabilities).

### E-6. Admin UI: publications

- "Publications" tab manages `event_publications` (publish/unpublish to
  channels). Existing endpoints; no new backend work.
- Optional `city_id` geo-filter exposed as a dropdown.

---

## Wave G — Media storage adapter (posters, logos, artist photos)

Goal: images stop being free-form URLs and get a managed lifecycle so
they survive moves between environments and can be replaced atomically.

### G-1. DB migration & adapter selection

- New migration `0051_media_objects.sql`:
  - `media_objects` table: `id uuidv7 PK`, `org_id FK NULL`
    (NULL = platform-owned), `owner_type TEXT` (`org_logo | event_poster | artist_photo`),
    `owner_id UUID NULL`, `storage_backend TEXT NOT NULL`
    (`s3 | local`), `storage_key TEXT NOT NULL`, `content_type TEXT NOT NULL`,
    `byte_size BIGINT NOT NULL`, `checksum_sha256 TEXT NOT NULL`,
    `width INT NULL`, `height INT NULL`, `created_at`, `deleted_at`.
- Choose between two adapters in `apps/backend/internal/adapters/storage/`:
  - `s3.go` (any S3-compatible: AWS S3, Cloudflare R2, MinIO) — default.
  - `local.go` (filesystem under `MEDIA_LOCAL_ROOT`) — for dev and tests.
- Config via env vars; adapter selected via `MEDIA_BACKEND=s3|local`.

### G-2. Upload endpoint

- New endpoints:
  - `POST /v1/media` — multipart upload, returns `media_object`.
    RBAC: `media.write` (new permission, seeded to `admin`, `org_admin`).
  - `GET /v1/media/{id}` — returns metadata + signed URL (S3 presign;
    local backend serves with short-lived HMAC token).
  - `DELETE /v1/media/{id}` — soft-delete; storage GC runs in worker.
- Worker job `media-gc` removes objects whose `deleted_at` is older than
  7 days and have no live references.
- Backfill not required for MVP; legacy `events.image_url` continues to
  resolve until owner edits.

### G-3. Admin UI: reusable `ImageUpload`

- New component `apps/admin-web/src/components/ImageUpload.tsx`. Used by:
  - Organization logo (Wave O-4).
  - Event poster (Wave E-3).
  - Artist photo (Wave E, future).
- Constraints: jpg/png/webp, ≤ 5 MB, min dimension 600×400 for posters.
- Shows preview, replace, remove. Uses `POST /v1/media` then PATCHes the
  owning entity with the returned `media_id`.

---

## Wave T — E-ticket delivery (email + PDF)

Goal: when a ticket is created, the buyer gets a styled email with a PDF
attachment containing the ticket details, QR code, and basic branding.

### T-1. PDF renderer

- New package `apps/backend/internal/platform/delivery/pdf/`.
- Library choice (single dependency, no headless browser): `github.com/jung-kurt/gofpdf`
  or `github.com/signintech/gopdf`. AutoForge picks one and pins it in
  `go.mod`. Justify the choice in the commit.
- API: `Render(ctx, ticket) ([]byte, error)`. Pure function, no IO.
- Includes: event name, session date in venue TZ, venue name + city,
  tier name, holder name, organization logo (resolved via media adapter),
  QR code encoding `ticket_credentials.code`, ticket id printable, fine
  print (no fiscal receipt block).

### T-2. Email templating

- New directory `apps/backend/internal/platform/delivery/templates/`.
- One Go-html-template per locale (`en`, `de`, `es`, `he` for AllPay
  markets; copy through existing i18n flow).
- One plain-text fallback per locale.
- Worker job `ticket-deliver` (already wired to `delivery_jobs` table)
  loads the template, renders, attaches PDF from T-1, sends via the
  existing SMTP adapter (or adds one if absent — verify
  `apps/backend/internal/adapters/email/`).

### T-3. Branding hooks

- Email header pulls organization `logo_media_id`, `name`, `website_url`.
- Footer pulls organization `legal_name`, `legal_address_*`,
  `contact_email`. Required for EU "commercial communications" minimum
  identification.
- If `logo_media_id` is null, falls back to platform logo.

### T-4. Admin UI: delivery panel on ticket

- In the existing ticket detail (read-only support console), add a
  "Delivery" section: last delivery attempt, status, "Resend" button
  (creates a new `delivery_jobs` row). RBAC: `ticket.update` or
  `support.act`.

---

## Wave S — Scanner integration via webhooks

Goal: the external scanner system receives ticket lifecycle events the
same way the legacy Bil24 notification feed worked, and posts scan
results back over a single HTTP endpoint.

### S-1. Emit scanner-relevant outbox events

- Audit existing outbox emitters. Confirmed in tests:
  - `v1.order.placed`
  - `v1.ticket.created`
- Add new emitters where missing:
  - `v1.ticket.refunded` — on refund finalization.
  - `v1.ticket.revoked` — on complimentary revocation (already migrated
    `0038_complimentary_revocation.sql`).
  - `v1.session.cancelled` — on session status transition to `cancelled`.
- Payload schema per event in `08_architecture/` (new doc
  `15_webhook_event_catalog.md` — short reference table only).

### S-2. Scanner-callback endpoint

- New endpoint `POST /v1/scanner/scan-events`. Authenticated via
  `agent_feed_tokens` (existing `0013_agent_feed_tokens.sql`). Request:
  ```
  { "scans": [
      { "credential_code": "...", "scanned_at": "...", "gate": "...",
        "device_id": "...", "result": "admitted | denied" }
  ] }
  ```
- Idempotent by `(credential_code, scanned_at)`.
- Writes to a new table `scan_events` (migration `0052_scan_events.sql`):
  `id`, `org_id`, `event_id`, `session_id`, `ticket_id`, `credential_code`,
  `scanned_at`, `gate`, `device_id`, `result`, `received_at`.
- Updates `tickets.used_at` on first `admitted` scan (idempotent).
- Emits `v1.ticket.scanned` outbox event so other subscribers (analytics,
  reporting) can fan out.

### S-3. Admin UI: webhook subscribers panel

- New route or tab under SuperAdmin: `apps/admin-web/src/routes/webhooks.tsx`.
- CRUD over `webhook_subscribers` (table already exists; only handlers /
  OpenAPI need verification — if missing, add as part of this task).
- Form: callback_url, signing_secret (write-only after create), event_types
  multi-select, active toggle.
- Show recent delivery attempts (uses existing outbox dispatcher logs).
- RBAC: existing `webhook.subscriber.manage`.

### S-4. Admin UI: scan-events read view

- In the ticket detail drawer, add a "Scans" panel listing rows from
  `scan_events` for that ticket. Read-only.

---

## Wave M — Mobile-responsive organizer & agent UI

Goal: every screen reachable by the `org_admin`, organizer,
`network_operator`, and agent presets is usable on a 360×640 touch
device. SuperAdmin-only screens stay desktop-first.

### M-1. Breakpoints, layout primitives, audit baseline

- Document Tailwind/CSS breakpoints used across `apps/admin-web`:
  `sm 640 / md 768 / lg 1024 / xl 1280`. `md` is the desktop/mobile cut.
- Add a layout primitive in `apps/admin-web/src/components/layout/`:
  - `<ResponsiveTable>` that renders a real `<table>` ≥ `md` and a
    stacked card list < `md`. Same data, same row component contract.
  - `<ResponsiveDrawer>` that opens as a right-side drawer ≥ `md` and as
    a full-screen sheet with a back button < `md`.
- Add an audit doc `09_autoforge/admin_ui/mobile_audit.md`: matrix of
  every existing route × role preset × minimum viewport currently
  supported. Output is the worklist for M-2..M-N.

### M-2. Shell: navigation, scope selector, header

- Update `AppLayout.tsx`:
  - ≥ `md`: existing left sidebar + persistent header.
  - < `md`: hamburger drawer for nav, sticky header with scope selector
    collapsed into a single chip that opens a full-screen scope picker.
  - Bottom safe-area padding for iOS browser chrome.
- Locale switcher (`LocaleSwitcher.tsx`) folds into the nav drawer on mobile.
- `ScopeSelector.tsx` becomes a full-screen sheet < `md` with search.
- SuperAdmin preset opts out: keeps the desktop layout regardless of viewport.

### M-3. Auth & onboarding on mobile

- `login.tsx`, password reset, accept-invite flows must satisfy:
  - inputs ≥ 44×44 CSS px touch targets,
  - native virtual-keyboard hints (`inputMode`, `autoComplete`),
  - no horizontal scroll at 360px width,
  - error toasts not hidden by the on-screen keyboard.

### M-4. Lists used by organizer / agent

Convert these routes to `<ResponsiveTable>` and verify on mobile:

- `organizations.tsx` (only for org_admin scope; superadmin list stays
  desktop),
- `venues.tsx`,
- new `events.tsx` (Wave E-3) — includes the sessions and tiers
  sub-tables: on mobile they become expandable accordions, not nested
  tables,
- `orders.tsx`, `tickets.tsx`, `refunds.tsx` — already exist as
  read-only support consoles; restructure to card lists < `md` and keep
  filters in a `<ResponsiveDrawer>` opened by a filter button.
- `channels.tsx`, `payments.tsx` — same treatment.

### M-5. Forms used by organizer / agent

- Wave O-4 (Legal & billing tab), Wave V-3 (venue address), Wave E-3..E-5
  (event/session/tier forms) must be authored against `<ResponsiveDrawer>`.
- Form acceptance criteria:
  - single-column layout < `md`,
  - sticky bottom action bar (Save / Cancel) ≥ 56 px tall, respecting
    `env(safe-area-inset-bottom)`,
  - validation errors inline under each field (no toast-only errors),
  - destructive actions in an overflow menu, not as adjacent buttons.

### M-6. Image upload on mobile

- `ImageUpload.tsx` (Wave G-3) must:
  - accept `<input type="file" accept="image/*" capture="environment">`
    so mobile browsers can offer the camera,
  - show progress and allow cancel on slow uploads,
  - reject > 5 MB client-side before POST.

### M-7. Webhook + scanner panels on mobile

- The webhook subscribers list (Wave S-3) and scan-events read view
  (Wave S-4) follow the same `<ResponsiveTable>` rules. The signing
  secret field uses a `type=password` reveal toggle and a "copy" button
  with a tap-friendly 44 px target.

### M-8. Accessibility & touch quality gate

- Add a Playwright config in `apps/admin-web/tests/mobile/` that runs
  the smoke set at 360×640 and 768×1024 viewports. CI fails if any
  organizer/agent route renders horizontal scroll at 360px or has a
  tap target below 44×44 CSS px on the primary action.
- Lighthouse accessibility score ≥ 90 on the three highest-traffic
  routes (orders, tickets, events).
- No new native-app work; all of this is responsive web only.

### Acceptance per touched feature

For every UI feature in waves O, V, E, G, T, S that lives on an
organizer or agent preset, the implementing AutoForge feature must
include a screenshot at 360×640 and 1280×800 in the PR description and
must pass the Wave M-8 Playwright gate before being marked done.

## Wave A — OpenAPI sync (cross-cutting)

Goal: every implemented endpoint is documented in `openapi.yaml`. Without
this, frontend cannot use generated types and integrations cannot use
the spec.

### A-1. Endpoint audit

- Walk `apps/backend/internal/platform/httpserver/*.go` and emit a CSV
  `09_autoforge/openapi_endpoint_audit.csv` with columns:
  `method, path, handler_file, in_openapi (yes/no), notes`.
- Land the CSV as the first step; it becomes the worklist for A-2..A-N.

### A-2..A-N. Document each missing group

- Group by file (events, sessions, ticket_tiers, inventory, reservations,
  promo_codes, pricing, checkout_sessions, payment_intents, tickets,
  ticket_credentials, refunds, barcode_authorities, delivery_jobs,
  webhook_subscribers, scan_events).
- One feature per group. Each feature: add schemas + paths, regenerate
  Go + TS, fix breakage, add minimal contract test that validates the
  example payloads against the schema.

---

## Suggested wave ordering for AutoForge

1. **Wave O** (org legal fields) — small, unblocks invoicing copy on
   tickets emitted later.
2. **Wave V** (venue address + timezone) — required for correct session
   times on PDF.
3. **Wave A-1** (endpoint audit CSV) — produces the worklist for spec
   sync; can be done in parallel with O & V.
4. **Wave M-1 + M-2** (responsive layout primitives + shell) — ship
   before E/G UI so every new screen is authored mobile-first.
5. **Wave A-2..N** for events/sessions/tiers/checkout/payment — required
   before the matching UI work.
6. **Wave E** (events admin UI) — depends on A having documented those
   endpoints and on M-1/M-2 having the responsive primitives.
7. **Wave G** (media storage) — needed by O-4 (logo) and E-3 (poster);
   may ship before E if scheduling allows.
8. **Wave T** (e-ticket PDF + email) — depends on O (org legal data on
   footer) and V (venue TZ).
9. **Wave S** (scanner webhooks) — depends on T only insofar as the
   ticket lifecycle is then real; the webhook bits themselves are
   independent and could start earlier.
10. **Wave M-3..M-8** — finishing pass: convert remaining lists, add
    Playwright mobile gate, retrofit the organizer/agent screens that
    already shipped (orders, tickets, refunds, channels, payments).

## Definition of done for the MVP slice

An organizer can, using only the admin web app:

- create an organization with legal name, tax id, legal address, default
  bank account, and logo;
- create a venue with structured address, country, timezone, and capacity;
- create an event with name, description, poster, genre, age rating,
  duration, and at least one artist;
- create sessions with start/end in venue TZ and a capacity_total;
- create at least one ticket tier with a fixed price in EUR or USD;
- publish the event to a sales channel;
- take a card payment via Stripe (worldwide) or AllPay (Israel) and see
  the order/ticket created;
- the buyer receives an email with a PDF e-ticket carrying a QR code
  and the organization's branding;
- an external scanner system subscribes to webhooks and receives
  `v1.ticket.created`, `v1.ticket.refunded`, `v1.ticket.revoked`,
  `v1.session.cancelled`, and posts scan results back to
  `POST /v1/scanner/scan-events`;
- every step above is fully usable by an `org_admin` / organizer / agent
  on a 360×640 mobile browser (Wave M acceptance gate). SuperAdmin-only
  flows remain desktop-first and need not satisfy the mobile gate.

Anything beyond that — assigned-seat selling, POS, BI builder, fiscal
receipts, RU acquirers, native mobile apps — is explicitly excluded
from this backlog.
