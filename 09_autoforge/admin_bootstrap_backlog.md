# Admin Bootstrap Backlog — findings from first real deploy walkthrough (2026-07-29)

Source: first production-like deploy on Dokploy (api/app.arenasoldout.com, staging mode)
and a superadmin walkthrough of the admin UI by the product owner. Nothing below is
implemented yet — this file is a staging area for AutoForge feature import (see
import_*.py scripts for the format used previously).

STATUS UPDATE 2026-07-29 (implemented directly in-session, see commit "fix(admin):
bootstrap walkthrough fixes"): AB-2 (perm cache TTL 60s + tests), AB-13 (client
resolves relative signed_url against API base), AB-14 (migration 0071 grants all
permissions to platform_superadmin), AB-15 (root cause found: server rotates
refresh tokens per PR2-03 but the SPA discarded the rotated token — now persisted;
openapi AuthRefreshResponse gained the missing refresh_token field), AB-17 (org
picker select with UUID-input fallback). Remaining open: AB-1 (audit beyond
superadmin), AB-3, AB-4, AB-5, AB-6, AB-7, AB-8, AB-9, AB-10, AB-11, AB-12, AB-16.

IMPORTED 2026-07-29: all open items installed into the AutoForge queue as
features #392-#408 (category "Admin Bootstrap AB") via
import_admin_bootstrap_features.py. AB-18 is first (CRITICAL). AB-11 (#408) is
blocked on Brevo SMTP credentials from the owner.

Context notes for the implementing agent:
- Live DB was hand-patched during bootstrap: `platform_superadmin` was granted
  `org.create/read/update/delete` manually; org `abhteam` and network `aso` created.
  Features AB-1/AB-2 must make those hand-patches unnecessary/reproducible.
- Organization legal/contact fields ALREADY exist (migration 0049 + org detail UI,
  feature #256) — do not re-create them.

---

## AB-1. RBAC: platform_superadmin must receive org.* by migration

**Category:** Database / RBAC
**Problem:** Seed migrations grant `org.create/update/delete` only to `admin` and
`org_admin` roles (0009_organizations.sql). `platform_superadmin` (0034) cannot create
organizations on a fresh database — the very first bootstrap action fails with 403.
Was hand-patched in the live DB on 2026-07-29.
**Steps:**
1. New goose migration: INSERT role_permissions for `platform_superadmin` ×
   (`org.create`, `org.read`, `org.update`, `org.delete`) ON CONFLICT DO NOTHING.
2. Review other permission families for the same gap — superadmin should be able to
   bootstrap a tenant end-to-end from an empty database. CONFIRMED missing so far:
   `org.*` (0009) and `media.*` (0053 — logo upload 403'd for the owner on day one;
   hand-granted 2026-07-29). Audit venue.*, channel.*, payment_config.write, event.*.
3. Integration test: fresh DB → create superadmin → POST /v1/organizations → 201.

## AB-2. RBAC: permission cache invalidation

**Category:** Backend / Auth
**Problem:** DBChecker caches role→permission sets in-process with NO invalidation
(rbac_checker.go, documented milestone simplification). After editing
role_permissions the API keeps returning 403 until the container is restarted, while
/v1/me (direct DB read) shows the new permission — confusing split-brain.
**Steps:**
1. Add TTL (e.g. 60s) to the permCache entries, or a LISTEN/NOTIFY / version-counter
   bust on role_permissions change.
2. Ensure /v1/me and RequirePermission resolve from the same source of truth.
3. Test: grant permission in DB → within TTL the endpoint authorizes without restart.

## AB-3. SuperAdmin User Directory (list/search users)

**Category:** SuperAdmin UI + API
**Problem:** /users page is create-only. There is no GET /v1/admin/users at all —
a superadmin cannot see any user, including themself. Roles/memberships of existing
users are invisible and uneditable.
**Steps:**
1. API: GET /v1/admin/users with pagination + search (email substring), returning
   id, email, created_at, email_verified_at, global roles, org memberships.
2. UI: table on /users (reuse ResponsiveTable), row → detail drawer with roles and
   memberships; keep the existing create form as a modal/section.
3. Gate by superadmin.read; mutations stay behind membership.grant.

## AB-4. User role management UI

**Category:** SuperAdmin UI + API
**Problem:** Roles can only be assigned at user creation (first role). No UI/API to
add/remove roles or memberships of an existing user, change org_admin ↔ organizer,
deactivate a user.
**Steps:**
1. API: POST/DELETE membership + global role endpoints (audit-logged, X-Admin-Reason).
2. UI: on user detail (AB-3) — add/remove role with org selector; confirmation flow.
3. Forbid removing your own last superadmin role (lockout guard) + test.

## AB-5. Organization create/edit form UX (country & locale pickers)

**Category:** SuperAdmin UI
**Problem:** Country is a raw 2-letter ISO text input — owner typed "EST" (3-letter)
and got a validation dead-end; locale is a raw BCP-47 input. Geo registry module
already exists in the platform but is not wired into this form.
**Steps:**
1. Replace country input with a searchable select fed from the geo registry
   (name + flag + code), storing alpha-2.
2. Replace locale input with a curated select (en/ru/et/uk/... + free-entry escape).
3. Same controls on the legal-address country field of the org detail form.

## AB-6. Mobile-friendly admin pass (product priority)

**Category:** SuperAdmin UI / Responsive
**Problem:** Product owner intends to operate the admin from desktop AND phone.
Layout components (ResponsiveTable/ResponsiveDrawer, a11y tokens) exist, but the
walkthrough surfaces were not audited for small screens (org create dialog, scope
bar with audit-reason chip, wide meta tables, sidebar).
**Steps:**
1. Audit every registered route at 375×812: no horizontal scroll of the page body,
   tap targets ≥44px, dialogs fit viewport with internal scroll.
2. Collapse sidebar into a drawer on <768px if not already; sticky scope bar wraps.
3. Add a CI viewport smoke (Playwright) for /organizations, /users, /networks.

## AB-7. Server-side search & pagination for organizations list

**Category:** API / SuperAdmin UI
**Problem:** /v1/organizations returns every org in one response; the UI states
"the controls below filter locally — there is no server-side search API today".
Fine for 1 org, breaks at scale.
**Steps:**
1. Add limit/offset (or cursor) + q= name/slug filter to the list endpoint.
2. UI: wire filter input to the server query, debounce, keep local fallback.

## AB-8. SPA permission refresh without relogin

**Category:** SuperAdmin UI
**Problem:** Permissions load once per app boot (AuthProvider.loadMe). After a grant,
the operator sees stale gating until a hard reload; combined with AB-2 this cost the
owner three confusing retry loops on day one.
**Steps:**
1. Refetch /v1/me on window focus and on any 403 from a mutation; surface a
   "permissions changed — refreshed" toast instead of a dead-end error.

## AB-9. Email: Reply-To organizer contact (cheap first step)

**Category:** Worker / Delivery
**Problem:** All outgoing mail uses one global SMTP_FROM; buyer replies go to the
platform, not the organizer. OrgContactEmail already exists in the delivery payload.
**Steps:**
1. Set Reply-To: org contact_email on ticket delivery emails when present.
2. Test: delivery handler emits Reply-To header; absent when org has no contact.

## AB-10. Email: per-organizer sender identity (Brevo domain verification)

**Category:** Worker / Delivery / SuperAdmin UI
**Problem:** Organizers with their own domain want tickets sent From their address.
Requires verified sender domains (SPF/DKIM via Brevo) — never organizer SMTP creds.
**Steps:**
1. Org fields: sender_email + verification status; superadmin UI to manage + show
   required DNS records (from Brevo API).
2. Worker: use org sender_email as From when status=verified, else global SMTP_FROM
   (+ AB-9 Reply-To fallback).
3. Verification poller job re-checking DNS/Brevo status.

## AB-12. Superadmin access to org-scoped resources (membership bypass or act-as-org)

**Category:** Backend / Auth
**Problem:** Org-scoped endpoints (bank-accounts CRUD and everything using
hiam/orgauth.go) enforce active membership with NO superadmin bypass: a
platform_superadmin gets `org.access_denied: caller is not a member of this
organization` on tenants they administer. Impersonation (#167) doesn't help when the
org has no members yet (bootstrap). Worked around on 2026-07-29 by inserting a
membership row (role platform_superadmin) for the owner into org abhteam.
**Steps:**
1. Decide the model: (a) superadmin.read bypasses actorIsMemberOfOrg (audit-logged
   with X-Admin-Reason), or (b) explicit time-boxed "act as org" grant surfaced in UI.
2. Implement in orgauth.go + tests (member, non-member, superadmin, suspended).
3. Remove the need for hand-inserted membership rows; document in runbook.
4. Note: memberships.role CHECK does not include 'org_admin' — membership roles
   (organizer/agent/...) vs RBAC roles (org_admin) is a confusing split; document or
   unify.

## AB-13. Media signed URLs are relative — logo previews broken cross-origin

**Category:** Backend / Media
**Problem:** mediastore builds local signed URLs as a RELATIVE path
(`/v1/media-files/{id}?expires=…&sig=…`) because `Options.DownloadURLBase` is never
wired in arena-api (main.go mediastore.New call). The admin SPA on
app.arenasoldout.com renders <img src> against its own origin → broken logo preview,
while the file actually lives on api.arenasoldout.com. Upload itself works (201).
**Steps:**
1. Wire DownloadURLBase from config (new env MEDIA_DOWNLOAD_URL_BASE, default =
   empty/relative for same-origin setups; docs in .env.example + DOKPLOY.md).
2. Alternative/additionally: admin client should resolve relative signed_url against
   VITE_API_BASE_URL (ImageUpload preview).
3. Test: cross-origin admin renders uploaded org logo preview.

## AB-14. Superadmin role must be granted ALL permissions by migration (generalizes AB-1)

**Category:** Database / RBAC
**Problem:** Walkthrough kept hitting 403s family by family: org.*, media.*,
membership.read (admin members list), venue.read (Venues tab), channel.read
(Channels tab) — every seed wave granted new permissions to admin/org_admin but
forgot platform_superadmin. Hand-fixed 2026-07-29 by CROSS JOIN grant of ALL 122
permissions to platform_superadmin on the live DB.
**Steps:**
1. Migration: grant all existing permissions to platform_superadmin (cross join,
   ON CONFLICT DO NOTHING) + add the same grant line to every future permission-seed
   migration template/checklist.
2. Consider a DB trigger or CI check ("no permission without a platform_superadmin
   grant") to prevent regressions.

## AB-15. SPA: silent access-token refresh

**Category:** SuperAdmin UI / Auth
**Problem:** Access token TTL is 1h; after expiry panels show raw
`auth.token_expired — The bearer token has expired; request a fresh one` with a
manual Retry button (seen on Payments tab). The SPA holds a refresh_token but does
not use it automatically.
**Steps:**
1. API client: on 401 auth.token_expired, POST /v1/auth/refresh once, retry the
   original request; on refresh failure → redirect to login.
2. Remove operator-facing raw error codes for this path.

## AB-16. Invite-by-email flow for org members

**Category:** SuperAdmin UI + API / Identity
**Problem:** "Add member" requires an already-existing user: entering a new email
fails with "no user exists with that email". Operator must first create the user on
/users (set locale/role there), then return and add membership — two disconnected
steps; and once real SMTP lands the natural flow is an invitation.
**Steps:**
1. API: POST invite (email, org, role) → creates user in invited state + sends
   invitation email with password-set link (requires Brevo SMTP; until then,
   create-with-temp-password fallback).
2. UI: Add member accepts any email; if user absent, offers "Invite".
3. Audit-log the invite; invitation token TTL + resend.

## AB-17. User-provisioning form: organization picker instead of raw UUID input

**Category:** SuperAdmin UI
**Problem:** /users create form has a free-text "Organization ID" input that
validates "must be a UUID". Owner naturally typed the slug ("abhteam") and hit a
dead-end; nothing on the page explains where to obtain the UUID (it lives on the
Organizations page → Details → ID). Same anti-pattern as the raw country/locale
inputs (AB-5).
**Steps:**
1. Replace the input with a searchable organization select (name + slug label,
   UUID value) fed from the orgs list endpoint; hide it entirely for global roles
   that need no org.
2. Preselect the org when the form is opened from an organization detail context
   (deep-link /users?org=<id> from the org Users tab / AB-16 invite flow).
3. Keep a "paste UUID" escape hatch for scale (combobox accepts raw UUID too).

## AB-18. Legal-entity fields: backend PATCH/read was never implemented (SILENT DATA LOSS)

**Category:** Backend / API — HIGH PRIORITY
**Problem:** The org detail "Legal & billing" form (feature #256 UI) PATCHes
/v1/organizations/{id} with legal_name, tax_id(+scheme), registration_number,
legal_address_*, contact_email, contact_phone, website_url, kyb_status. The server
returns 200 but HandleUpdateOrg only persists name/slug/country/locale/TTL and
silently drops everything else; NO handler in httpserver references legal_name /
contact_email at all (grep-verified 2026-07-29). Columns exist since migration 0049.
Owner filled the form twice and lost the data twice; only bank accounts (real
endpoint) and logo (media endpoint) survived.
**Steps:**
1. Extend PATCH /v1/organizations/{id} (or a dedicated /legal-entity endpoint) to
   persist all 0049 fields; sqlc query + validation (kyb transitions need
   legal_name; country alpha-2; tax_id per scheme as the UI copy promises).
2. Return the fields in the org read/list endpoints the admin form hydrates from
   (/v1/admin/organizations and single-org GET) — today they are also absent.
3. OpenAPI schema + regenerate; integration test: PATCH → GET roundtrip.
4. Guardrail: API must reject unknown body fields or the UI must detect
   no-op saves — a 200 that drops fields must never happen again.

## AB-19. Org detail tabs are read-only shells — no create/edit from org context

**Category:** SuperAdmin UI
**Problem:** Organization card tabs (Users/Venues/Channels/Payments) render raw
GET output with no create/edit actions; owner concluded "нет добавления площадки,
каналов, платежей". Real CRUD lives on the global sidebar pages (Venues, Sales
Channels, Payment Configs), which is not discoverable from the org context.
**Steps:**
1. Each org tab: embed the scoped list + primary create action prefilled with the
   org (or at minimum a deep link "Create venue for this organization →").
2. Users tab: link to /users?org=<id> (pairs with AB-17 preselect).

## AB-20. Venue creation: geocoding-first flow (address → name/city/coords)

**Category:** SuperAdmin UI + API
**Problem:** New-venue form demands raw UUIDs (Organization ID, City ID — "City ID
must be a UUID"); owner typed "Prague" and dead-ended. Real-world flow (owner's
words, mirrors the legacy Bil24 editor): organizer sends an address, operator
verifies it on a map and the tool fills name/address/city/coordinates.
**Steps:**
1. Org picker (reuse AB-17 pattern) + city picker fed from the geo registry with
   inline "create city" (admin geo endpoints already exist).
2. Address autocomplete via a geocoding provider (Google Places or
   Nominatim/Mapbox — decide licensing) that fills address lines, postal code,
   city (mapped/created in geo registry), country, and lat/long; map pin preview
   with manual adjustment; store coordinates on the venue.
3. Fallback: fully manual entry stays possible (no hard dependency on the
   geocoder).

## AB-21. Bil24 live import: venues (and cities) via Bil24 API

**Category:** Integration / Operator tooling
**Problem:** arena-bil24-import (features #386/387) imports EVENTS from CSV/JSON
snapshots only. Owner has an existing Bil24 account with venues (e.g. [10549]
Palac Akropolis with address, coords, seating plan) and wants them imported
directly via the Bil24 API instead of retyping. Official Bil24 API docs live in
01_official_bil24_docs/.
**Steps:**
1. Extend the importer (or new arena-bil24-import-venues mode) to call the Bil24
   API: auth via operator credentials/fid+token, fetch countries/cities/venues.
2. Map Bil24 country/city to the geo registry (create-if-absent), venues into
   venues table with external_bil24_id-style mapping column, coordinates and
   address preserved; idempotent re-runs.
3. Optional phase 2: seating plan import (Bil24 seating plan JSON → seating
   assets), leveraging the existing Palac Akropolis asset format in
   05_widgets_and_site_templates / 06_venue_maps_and_seating.
4. CLI contract mirrors the snapshot importer (dry-run, summary, exit codes).

## AB-22. Human-readable identifiers platform-wide (name-first UI + short display numbers)

**Category:** SuperAdmin UI + API / Design principle — owner priority
**Problem:** Internal UUIDv7 PKs leak into every operator surface (Organization ID,
City ID, member lists showing raw UUIDs). Legacy Bil24 shows short numeric ids +
names ("[267438] Lampyris s.r.o.", "[10549] Palac Akropolis") and operators are
used to that. UUIDs stay internally correct (non-enumerable, sortable, no central
counter) — the fix is presentation, not a PK migration.
**Steps (layered):**
1. UI RULE: no form ever asks for a raw id — name-first pickers/typeahead
   everywhere (generalize AB-17; applies to AB-5 country/locale, AB-20 venue org/
   city, event→promoter/org binding, member lists show email+name not UUID).
2. Short display numbers: secondary per-entity sequence column (org, venue,
   event, channel, user) surfaced as "Palac Akropolis · #23" in pickers, tables,
   support conversations, printed docs. UUID remains the PK and API identifier.
3. Slugs in admin URLs where entities have them (org/venue) instead of UUIDs.
4. UUID visible only in a collapsed "developer info" block with copy button.
5. Global admin search by name (header omnibox: orgs/venues/events/users) —
   phase 2.

## AB-23. Password-reset confirm flow broken end-to-end (feature #409)

**Category:** SuperAdmin UI + API / Auth — found live 2026-07-29 after Brevo SMTP
went live. The reset email links APP_PUBLIC_URL (SPA origin) + /v1/... (API path):
the link opens the admin SPA shell, and no set-new-password page exists at all.
Steps captured in AutoForge feature #409.

## AB-11. Ops: production-mode readiness checklist

**Category:** Deploy / Config
**Problem:** Current deploy runs APP_ENV=staging because production validation
requires SMTP (EMAIL_MODE=smtp) and DATABASE_URL sslmode=require (internal
docker-network Postgres has no TLS).
**Steps:**
1. Wire Brevo SMTP env into worker; flip EMAIL_MODE=smtp.
2. Decide TLS story for in-network Postgres (ssl in postgres:17 container, or relax
   requirement for private-network DSNs with explicit override flag) — then
   APP_ENV=production.
3. Document the flip procedure in deploy/DOKPLOY.md §10.

---

## Wave 2 findings — 2026-07-30 walkthrough of the deployed AB wave (queued as #410-#413)

## AB-24. User lifecycle: deactivate/block/delete from the user drawer (#410)
Owner: drawer manages roles but a user cannot be blocked or removed. Soft-deactivate
with session revocation + audit; hard delete only for never-active accounts;
last-superadmin guard.

## AB-25. Venue seating plans: versions with SVG upload (#411) — RE-SPECIFIED 2026-07-30

First attempt was marked passing with zero code written (no seating diff, no
progress note); reopened and re-specified from a live diagnosis:

- The backend is COMPLETE (migration 0057 + mount_seating.go: list/get/create/
  patch/versions/fork/bind). Verified live: GET list 200, POST create 201.
- "Create plan does nothing" = silent-disabled submit in
  venueSeatingPlans.tsx (~L862): disabled until Name is typed, with no reason
  shown. Plus the raw "Owner org UUID" field (AB-17/AB-22 violation).
- The REAL gap: no UI to create a plan VERSION, and a plan without a version has
  current_version_id NULL — hence "no seating plan, no SVG". POST
  /v1/seating-plans/{id}/versions exists (geometry jsonb, svg_asset_media_id,
  capacity_seated/standing) and is unused by the admin.
- Media blocks it: mediastore owner_type allowlist is {org_logo, event_poster,
  artist_photo} and SVG is not an advertised/accepted type.
- Visual seat editor remains DEFERRED; this is upload/attach/list/preview only.
  Reference assets: Palac Akropolis package (05_/06_ dirs), Bil24 plan model.

## AB-26. Sales Channels + Payment Configs: UUID gate and broken create (#412)
Both pages still demand a pasted org UUID and the New channel / New payment config
buttons do not lead to a working create flow. Org picker + fixed modal forms.

## AB-27. Geo Registry page is a shell (#413)
Page still shows the SAUI placeholder while the admin geo API already works (used
via curl to seed CZ/Prague). Wire countries/cities list + create forms.

Answered in-session (not a defect): "Verified" flips when the user confirms their
email — via the verification link or by completing the provisioning password-setup
link; the owner's own account was seeded pre-verified.

## AB-28. Seating owner_org_id guard has no superadmin path (#416) — reproduced 2026-07-31

POST /v1/venues/{id}/seating-plans with an owner_org_id the caller does not belong
to returns 403 `seating_plan.owner_org_forbidden` even for platform_superadmin with
a valid X-Admin-Reason; the same call for an org the caller belongs to returns 201.
The seating handler duplicates membership logic instead of using
server_orgauth.go's enforceMembershipInOrg, which already honours the audited
superadmin bypass from #395/AB-12.

## AB-29. SPA hangs on "Restoring session…" after full reload (#417)

Reported independently by two implementing agents as a blocker they worked around
(#410 checkpoint, AB-25 verification). AuthGate shows that screen while
auth.status === "initializing", so some bootstrap path never reaches a terminal
status. Blocks operators and blocks browser verification of any feature.

## CLAUDE.md poisoning (fixed 2026-07-31, commits a310b4d + BOM strip)

CLAUDE.md had been overwritten with a chat-assistant system prompt that told its
reader "you cannot modify source code — you have NO Write/Edit/Bash tools".
Claude Code loads CLAUDE.md as project instructions, so every coding agent booted
with an instruction not to write code — a plausible contributor to the #411
fabrication (feature marked passing with zero diff). Restored to `@AGENTS.md`.
Follow-up during review: the restored file carried a UTF-8 BOM before the `@`
import directive (the PowerShell encoding trap AGENTS.md itself documents), which
risks silently breaking the AGENTS.md import; BOM stripped.

---

## Wave 3 findings — 2026-07-31 walkthrough of cfd058e (queued as #418-#423)

Positive: AB-25 verified live by the owner — SVG upload produced v1 with 90 seated
+ 500 GA, preview rendered.

- **AB-30 (#418)** — terminology: "standing" is called **General admission** on the
  market; rename all operator-facing strings (presentation only, DB/API fields
  unchanged). GA-capacity input must appear only for plan_type
  general_admission/mixed, never for assigned_seats.
- **AB-31 (#419)** — /events directory and the scope bar still show raw org UUIDs;
  render name + short display number (#397 infra) per the AB-22 rule.
- **AB-32 (#420, CRITICAL)** — no way to create an event from the UI ("later wave"
  copy). Core product loop blocked: event -> session -> seating bind -> channel ->
  widget sale. Deliver event+session create/edit with status transitions.
- **AB-33 (#421)** — new-channel dialog incomprehensible even to the owner;
  "Settings (JSON object)" is provider-specific config (0045: Stripe statement
  descriptor, AllPay terminal id) — replace with structured per-provider fields +
  advanced JSON escape hatch + field guidance.
- **AB-34 (#422)** — Stripe asks for a webhook URL; surface the copyable
  POST /v1/payment-intents/webhook URL and the signing-secret flow in Payment
  Configs (webhook_secret already tracked as required there).
- **AB-35 (#423)** — one Bil24-style cascading flow country -> city -> venue with
  inline create at each level; Geo Registry repositioned as optional/advanced
  maintenance (owner: "зачем она нужна" as a standalone page).

---

# Wave 4 — reconstruction alignment (deep audit 2026-07-31, after AB-28..AB-35 rollout)

This wave is different in kind from waves 1-3. Those were UI gaps. This one corrects
**four structural deviations from the Bil24 model** that the project was commissioned to
reconstruct. They were found by walking the owner's live event-creation attempt on
01eeafb back through `01_official_bil24_docs/`, `04_legacy_screenshots/`,
`08_architecture/` and the schema.

Every complaint the owner raised in that walkthrough (session not auto-created, capacity
asked twice, currency not derived, no per-seat category, feed-token UUID) is a symptom of
one of these four. Fix the model and most of the UI backlog collapses.

## The four deviations

| Concern | Bil24 + our own spec | What was built |
|---|---|---|
| Time | only the session (`ActionEvent`) carries date/time | `events.start_at/end_at` are first-class own fields |
| Venue | bound to the **session** | `events.venue_id` — bound to the event |
| Price | `Session x Category`, Category == a zone/seat group | `ticket_tiers` on session, **no link to any zone** |
| Currency | follows venue geography | `ticket_tiers.currency`, unvalidated free text |

Source evidence (do not re-litigate these — they are settled):

- `01_official_bil24_docs/api/bil24_ticket_agent_api_notes_ru.md`: "Важно различать
  само событие (название, описание) и его сеансы (конкретные даты и время)… ключевым
  идентификатором является не actionId, а actionEventId".
- `TixEditor.jar` dialog inventory: `AddActionDialog` / `AddActionEventDialog` /
  `AddActionEventPriceDialog` / `AddActionEventLimitDialog` — event, session, session
  price, session quota are four separate things.
- `04_legacy_screenshots/tix_editor/2026-06-12_editor_audit/`: the session dialog owns
  `Seating plan` and `ETS`, and refuses to save with "start of session not set". The
  session label carries the currency: `[ILS][7697][GA2] 2 Nov 2026, 20:30` vs
  `[EUR][3895][VBS1]` in the same installation.
- RESERVATION accepts `seatList` (assigned) and `categoryList` (GA quantity) in one
  call — mixed halls are a protocol-level guarantee, not an edge case.
- Our own spec already says the same: `08_architecture/03_platform_management_api_and_
  permissions_ru.md:156-166` defines `EventSessionVenueAssignment` = (`event_session_id`,
  `venue_id`, `seating_plan_version_id`, `admission_mode`, `capacity_override`), and
  `08_architecture/02_wordpress_integration_contract_ru.md:181` puts `city, currency` in
  the **session** cache. The deviation is implementation-only; nothing needs re-deciding.

Owner decisions taken 2026-07-31 (binding for this wave):

1. **No visual scheme editor.** Schemes stay authored in Inkscape to the Bil24 SVG
   convention and imported. Q65 (2026-07-10) stands. BUT assigning a **price category to
   each seat** on an imported scheme is mandatory — that is Bil24's `Change category...`,
   not an editor.
2. **payment_provider_configs must be wired to Stripe checkout**, not deleted.
3. **Stand data is disposable** — migrations may be destructive, no backfill required.

Added 2026-08-01:

4. **The in-platform scanner is not the product.** Gate control is the external MACS
   service; the platform feeds it (AB-50). Check-in/check-out is MACS's concern, not ours.
5. **GA places are materialized with real identity** — one row, one id per place (AB-51).
6. **Posters bind to the session**, deliberately diverging from the reference (AB-47).
7. **One order = one SESSION (owner-confirmed 2026-08-01). Multi-event and multi-session
   carts are OUT OF SCOPE.**
   The reference supports them — the Reporter fans one order into ticket rows carrying
   different Session/Venue/Event ids, the export nests `actionEvent` inside each
   `ticketList` item rather than at order level, and the order carries `filtered*` twins of
   every money field (`filteredSum`, `filteredDiscount`, `filteredCharge`,
   `filteredTotalSum`, `filteredTicketQuantity`) which only earn their keep when an order
   spans more than a report filter selects. We are not building that now.
   Our schema already enforces the restriction: `checkout_sessions.reservation_id` is a
   single `NOT NULL` FK and `reservations.session_id` is a single `NOT NULL` FK, so one
   order can only ever cover one session. **Leave it that way** — do not "fix" it as an
   apparent limitation.
   Two consequences to honour anyway:
   - The export/webhook format is per-ticket by design. Emit `actionEvent` per ticket and
     emit the `filtered*` fields even though they will always equal their plain twins. A
     receiver written against the reference must not have to special-case us.
   - **One session, not merely one event** — confirmed as the intended constraint, so a
     buyer wanting two dates of the same run places two orders. That is the accepted
     behaviour, not a gap to close.

Sequencing is load-bearing: AB-36 -> AB-37 -> AB-38 must land before AB-42, or the event
wizard gets written twice. Migration head is **0078**; this wave takes 0079-0081.

---

## AB-36. Session owns venue and seating (migration 0079) — CRITICAL, foundation

**Category:** Database / Catalog
**Problem:** `events.venue_id` binds a venue to the event. Bil24 and our own spec bind it
to the session. Consequences visible today: a tour (one event, several cities) is
unrepresentable; the session form cannot derive capacity because it does not know the
venue; the seating-bind panel is only reachable in session *edit* mode
(`events.tsx:2891`), so creating a session shows a bare capacity box.
**Steps:**
1. Migration 0079: add `sessions.venue_id uuid NOT NULL REFERENCES venues(id)` and
   `sessions.capacity_override integer NULL`; drop `events.venue_id`. Destructive — stand
   data is disposable, no backfill.
2. Session create/patch handlers accept and validate `venue_id` (must belong to the
   event's org, or superadmin bypass per AB-12/AB-28).
3. Seating bind moves into the session **create** path, not only edit: choosing
   `admission_mode` + `seating_plan_version_id` at creation.
4. Capacity resolution order becomes: bound plan version (seated / seated+GA) ->
   `capacity_override` -> `venues.capacity_default`. `venues.capacity_default` currently
   has **zero readers** anywhere in the codebase; this makes it live.
5. Event list/detail render venue from the event's sessions (distinct venues, or "N
   venues" for a tour).
**Done when:** `go test ./...` green; migration-head pin updated; `openapi.yaml`,
`types_gen.go` and the TS client regenerated with no drift; no handler references
`events.venue_id`.

## AB-37. Event dates derived from sessions (migration 0080)

**Category:** Database / Catalog
**Problem:** `events.start_at/end_at` are independently editable and already drift from
sessions. Live proof on the stand: the events list showed "Next session 2026-08-31 20:51"
for an event with **zero** sessions, because the column renders `event.start_at`
(`events.tsx:1379-1382`, with an honest code comment admitting the shortcut). The
deviation leaked into the compatibility layer: `GET_ALL_ACTIONS` returns `firstEventDate`
from `events.StartAt` (`hbil24/bil24_compat.go:297`) — a Bil24-shaped response computed
from a non-Bil24 model.
**Steps:**
1. Migration 0080: drop `events.start_at/end_at`; add `events.first_session_at` and
   `events.last_session_at` (nullable cache), maintained by trigger on `sessions`
   insert/update/delete. Cache rather than a view — the events list filters and sorts on
   these and must not take a subquery per row.
2. Rewrite the `starts_on_or_after` / `starts_on_or_before` filters and list ordering onto
   the cached columns.
3. `firstEventDate` in the Bil24 gateway reads `first_session_at`.
4. The events list "Next session" column shows the true next future session, or an empty
   state for an event with no sessions.
5. Event create no longer collects dates at all — dates belong to step 2 of the wizard
   (AB-42).
**Done when:** an event with no sessions renders no date anywhere in UI or API; the Bil24
compat contract test still passes.

## AB-38. Currency derived from geography (migration 0081)

**Category:** Database / Pricing
**Problem:** `ticket_tiers.currency text NOT NULL DEFAULT 'USD'` has **no CHECK, no
ISO-4217 validation and no whitelist** — the handler does `strings.TrimSpace` and defaults
to `USD` (`hcatalog/ticket_tiers.go:154,179-181`); a grep for `iso4217|validCurrenc` across
the whole backend returns nothing. Any string persists. Worse, **two tiers of the same
session may carry different currencies**, producing an unsellable mixed-currency cart.
`countries` has no currency column at all, so nothing could derive it even if it wanted to.

**Owner rule (final): currency is DERIVED from the venue's country/city, and OVERRIDABLE
on the session.** The reference agrees — the Bil24 "Add sessions…" dialog carries a
currency dropdown showing `EUR` for a Prague venue, and the resulting session label reads
`[CZK]…` in the default case. Two legitimate reasons for the override: a country with more
than one currency in circulation, and an organizer who simply chooses to settle a run in
another currency (touring). The invariant that actually matters is **one currency per
session**, not "derivation can never be overruled".

**Steps:**
1. Migration 0081: `countries.currency char(3) NOT NULL` (seed all 10 seeded countries);
   `cities.currency_override char(3) NULL` for the rare intra-country divergence; CHECK
   `^[A-Z]{3}$` on every currency column touched by this wave.
2. Add `sessions.currency char(3) NOT NULL`. Resolution at session create:
   `venue -> city.currency_override ?? country.currency`, then the operator may override
   the resolved value explicitly.
3. Track **how** the value was set — `sessions.currency_source` (`derived` | `override`) —
   so the UI can show "EUR — from venue country" vs "EUR — set manually", an override is
   visible in audit, and a later venue change can safely re-derive only the `derived` ones.
4. `ticket_tiers.currency` stays for wire compatibility but is constrained to equal its
   session's currency; tier create/patch stops accepting it as an operator input. Changing
   a session's currency after tiers exist must either rewrite them all or be refused with a
   clear reason — never leave a session with mixed currencies.
5. UI: currency is a **prefilled** select, not a blank one. Default = derived value, with
   the source shown next to it. Changing it is a deliberate act, not the path of least
   resistance.
6. Delete the hardcoded `PROVIDER_CURRENCIES` map in `events.tsx:588` as the source of
   truth; keep provider support only as a *validation* ("this org's Stripe account cannot
   settle CZK") — warn on override, do not silently allow an unsettleable currency.
**Done when:** a tier cannot be created in a currency differing from its session; a session
in Prague defaults to CZK and can be deliberately switched to EUR with the change recorded
as an override; invalid ISO codes are rejected at the API with a 422, not persisted.

## AB-39. Per-seat price category assignment — OWNER-MANDATORY

**Category:** Seating / Admin UI
**Problem:** Bil24's core pricing gesture is missing: select seats on the hall map, assign
them a price category. The legacy dialog is literally "Change the category of the selected
seats (3 seats)" (`04_legacy_screenshots/raw_misc/legacy_change_category.jpg`), with a
sibling `Price order` dialog listing `First..Fifteenth` each with its own price. Owner
called this obligatory. Without it a mixed hall cannot be priced at all.
**Good news — most of the machinery already exists and is unused:**
`session_seats.tier_id` exists (0058); `hseating/bind.go:637 autoCreateTier` already mints
a tier per SVG price category at bind time; `sessionSeats.tsx` already renders the seat map
(`renderSeatMapSVG`) and already supports seat selection for holds/blocks. What is missing
is the ability to **re-assign**.
**Steps:**
1. `PATCH /v1/event-sessions/{session_id}/seats/category` — bulk assign one `tier_id` to a
   list of `seat_key`s (n=1 is the single-seat case).
2. Reject reassignment of seats in `sold`/`held` status with 409 listing the conflicting
   seat keys; allow `available`/`blocked`.
3. Bump `seat_status_version` so widget/feed caches invalidate.
4. After a successful bind, every seat must carry a non-null `tier_id` (today it is
   nullable in practice).
5. Admin UI — **two equivalent surfaces over one selection model**, as in the reference
   (owner: "может работать как через табличный метод, так и через графический"):
   - **Table**, mirroring the Bil24 seat-management dialog:
     `ID | Sector | Row | Seat | Category | Price | Barcode | Status`, multi-select with a
     live "Selected seats: N" counter, and actions `Category…`, `Unavailable`,
     `Available`, `Price order`. Rows are colour-coded by status (available / unavailable /
     sold), and GA rows appear in the same table with empty Sector/Row/Seat.
   - **Map**, side by side with the table: click and shift-drag rubber-band selection, seat
     fill colour = category, legend above the map.
   Selecting in one surface selects in the other. The table is what makes bulk work
   practical on a 590-unit hall; the map is what makes it comprehensible.
6. Tier price editing stays where it already is (Ticket tiers tab) — that is the
   equivalent of Bil24's `Price order`.
**Done when:** on the Palac Akropolis seated plan an operator can select an arbitrary set
of balcony seats and move them to another category, and the widget reflects the new price.

## AB-40. Combined seating plans: GA as a first-class price category, end to end

**Category:** Seating / Import / Widget — DATA LOSS + missing core capability

### Model, confirmed against the live Bil24 Editor

Owner supplied a screenshot of BIL24 Editor 2.6 build 7921 (Mar 10 2026) showing the real
production setup of *IVO DIMCHEV in Prague* at Palác Akropolis. It settles the model — do
not re-derive it:

1. **Venue is hard-bound to address / city / country.** One venue, one address.
2. **A venue owns MANY seating plans.** In the editor the Seating plan is a dropdown under
   the venue with its own `+` / `−` (`[43130] Palac Akropolis GA`). Our schema already
   supports this (`seating_plans.venue_id`) — confirmed correct, do not "fix" it.
3. **A session is hard-bound to exactly one seating plan**, chosen when the session is
   created. The session label carries it verbatim:
   `Session: [CZK][7040243][Palac Akropolis GA] Oct 9, 2026 7:30 PM` — currency, id, plan,
   datetime. (Currency on the session: see AB-38.)
4. **A plan version declares its type.** The screenshot reads
   **"Seating plan version: 1.1 (combined)"**. *Combined* is Bil24's word for what our
   schema calls `plan_type='mixed'`. A plan is either fixed (assigned seats only) or
   combined (assigned seats **plus** a GA area such as a dance floor).
5. **GA is an ordinary row in the plan's category table**, not a parallel concept. The
   plan-level table is `Category | Seats | Starting price` and reads:
   `Fourteenth | 0 | ` / `Fifteenth | 0 | ` / **`General admission | 500 | 1`**.
   So a category either enumerates coordinate-bearing seats (row + number) or carries a
   **bulk capacity** and no coordinates. Same table, same price grid.
6. **Up to 15 price categories** (`First`..`Fifteenth`) — matching the 15 swatches the SVG
   convention already carries. They are renameable: the session table shows
   `SEATING - SEZENÍ | 1,890` where `Third` used to be.
7. **Prices are per session**, per category: the right-hand `Category | Price` table
   belongs to the session, while `Starting price` on the plan is only a default.

### Plan authoring — three plan types (second screenshot set, "Add seating plan…")

The create dialog is `Venue | Name` plus a three-way radio, and the rest of the form
**changes with the choice**:

| Type | Map file | Category table | Notes |
|---|---|---|---|
| **Assigned seats** | `Choose file…` required | none | categories come from the file's swatches |
| **Combined** | `Choose file…` required | `No. \| Category \| Seats \| Starting price`, add/remove rows | file supplies seats, table supplies the GA rows |
| **General admission** | **none** (`Upload spl…` optional) | required, hand-entered | a GA-only plan is just a list of named capacities |

Two consequences we had wrong:

8. **A GA-only plan needs no SVG at all.** The operator types categories directly —
   screenshot shows `1 | R1 | 10 | 10` and `2 | R2 | 20 | 15`. Our create flow assumes an
   SVG upload is the only path to a usable plan; it is not.
9. **GA is not a single blob — a plan can carry SEVERAL GA categories**, each with its own
   capacity and starting price (`R1`, `R2` above; `General admission | 500` was simply a
   one-row case). Model GA capacity per category, never as one number on the version.
   (`seating_plan_versions.capacity_standing` as a scalar is therefore the wrong shape.)
10. A plan may render **several level diagrams in one canvas** (the assigned-seats plan
    `[43129] Palac Akropolis` previews FLOOR and BALCONY as two framed diagrams). Verify our
    single-`Canvas` geometry handles this before assuming it does.

### Session ↔ plan binding (fifth screenshot, "Add sessions…")

The session dialog picks `Seating plan` and `Event` from dropdowns, then takes
`Start of session / Start of sales / End of sales`, with `+` to add **several sessions in
one pass**. Owner's rule: usually one session uses one plan, but **different sessions of
the same event must be able to use different plans** — our `sessions.seating_plan_version_id`
already allows exactly this (confirmed correct; see AB-36). Note also `Use quota`, `ETS`
and `Mobile electronic ticket (MET)` as per-session flags we do not model yet.

The follow-up dialog "Setting session prices…" sets `prices by category` for the selected
sessions and displays the seat count each category actually holds (`Third: EUR 30,
Seats: 260`, `Total seats: 260`), with a session multi-select (`set →` / `← remove`) so one
price grid can be applied to **many sessions at once**. Worth carrying into AB-39/AB-42 as
a bulk-pricing step rather than per-session retyping.

This reframes the defect. `Palac_Akropolis_GA.svg` is **not** a damaged copy of
`Palac_Akropolis.svg` — it is a legitimate *second, combined* plan of the same venue
(side/balcony seats + a 500-capacity ground floor). Both must import.

### What is broken today

The importer recognises only `<circle>` elements inside `#`-labeled groups, and
`Geometry.StandingZones` is **hardcoded to an empty slice** (`svg_import.go:98`); nothing
in the import path can ever populate it. In `Palac_Akropolis_GA.svg` the ground floor is an
unlabeled dashed `<rect>` with no `inkscape:label`, no fill and no capacity, so it is not
even a candidate.

Traced end to end: `ImportSVG(Palac_Akropolis_GA.svg)` returns **zero errors** and a
"successful" geometry of 90 balcony seats — **the entire 500-capacity floor is silently
discarded** into `decor_svg` as inert background art. The acceptance fixture exercises only
the seated file, so no test catches it. A silent success is the bug; the missing feature is
secondary.

### Part A — plan model: GA becomes a category

1. Give geometry categories a kind: `seated` (seats bind to it by fill colour, as today) or
   `general_admission` (carries its own `capacity`, plus an **optional** polygon for
   hit-testing — present in a combined plan, absent in a GA-only plan). Several GA
   categories per plan must be supported. Keep both kinds in the single category list so
   the admin renders one `Category | Seats | Starting price` table exactly like Bil24's.
2. `Seats` for a seated category = count of bound seats; for a GA category = its declared
   capacity. `Starting price` is a plan-level default; the sellable price is the
   per-session tier price (AB-39).
3. Enforce the 15-category ceiling. Allow a per-plan display-name override so renaming
   `Third` to `SEATING - SEZENÍ` does not require re-importing the SVG.
4. Retire `Geometry.StandingZones` as a separate parallel array, or keep it strictly as the
   serialised form of GA-kind categories — one representation, not two.

### Part B — importer

1. Document the GA authoring convention in `seating_backlog.md`: a GA area is an element
   labeled `#GA <name>` whose `<title>` carries the capacity, with fill colour matching one
   of the 15 `PriceCategory` swatches. Stays inside the existing Bil24/Inkscape convention
   — no new authoring tool (owner decision Q65 stands).
2. Parse those elements into GA-kind categories (id, name, capacity, category binding by
   fill colour, polygon geometry).
3. **Hard-fail the import** with a 422 when `plan_type` is `general_admission` or
   `combined`/`mixed` and no GA area was found.
4. Re-author `Palac_Akropolis_GA.svg` to the convention (ground floor = 500) and add it as a
   **second acceptance fixture** beside the seated one. Its imported capacity must equal
   the real venue capacity.
5. Cross-check `seating_plans.plan_type` against `sessions.admission_mode` at bind time —
   `bind.go` never reads `plan_type` today, so an `assigned_seats` plan can be bound as
   `hybrid`.
6. Session capacity = bound seat count + sum of GA category capacities, read-only (AB-36).

### Part C — admin

1. Create-plan form becomes **type-aware**, mirroring the Bil24 dialog: a
   `General admission / Assigned seats / Combined` choice that reshapes the form —
   GA-only needs **no file**, just a hand-entered category table; Assigned needs the file
   only; Combined needs both. Today the flow assumes an SVG is the only path to a usable
   plan, which makes a GA-only venue impossible to set up.
2. Session create picks the seating plan **version**, mirroring "Seating plan" in the
   editor's session form, and supports adding several sessions in one pass.
3. The plan screen shows the Bil24 table — `Category | Seats | Starting price` — with GA
   rows inline.
4. Per-seat category assignment for coordinate-bearing seats is AB-39. GA categories have
   no seats to assign; only capacity and price.

### Part D — widget: one surface, no mode switching

Owner requirement, and the widget spec already mandates it —
`08_architecture/16_ticket_widget_ux_and_technology_ru.md:144-164`: «Гибридные залы
(GA + места) — одна поверхность, ноль переключателей… одна корзина смешивает позиции обоих
типов». The current widget does not do this: the buyer is forced to switch between GA and
assigned-seat modes. The owner names the existing Bil24 widget as the reference and this
switching as the thing to beat.

1. GA areas render **on the same hall map** as seats, as clickable polygons.
2. Clicking a GA area opens a small inline quantity popover ("how many tickets"), bounded
   by that area's remaining capacity out of its shared pool. Clicking a seat still selects
   that seat.
3. **One cart** holds both kinds of line item simultaneously. No mode toggle anywhere in
   the UI.
4. Remaining GA capacity is a shared pool, decremented by reservations — it is not a set of
   pseudo-seats the buyer picks from.
5. Backend already supports the shape: `admission_mode: hybrid` accepts `seats` and
   `quantity` in one reservation (SEAT-C1), and `reservation_ga_items` exists. This is
   presentation + reservation wiring, not new inventory mechanics.
6. Cover with the widget e2e suites (mock + `:real`). Reminder from AGENTS.md: the
   Playwright dev server needs `VITE_API_BASE_URL` in the config env or the app fails on
   startup.

**Import note:** Part D lives in `apps/widget` with its own e2e suites and should be
imported as its own feature id; Parts A-C are one backend+admin feature.

**Done when:** the combined Palác Akropolis plan imports with capacity 500 on the ground
floor plus its seated categories; a GA-typed plan with no GA area is a 422; and a buyer can
put two floor tickets and one balcony seat in one cart without ever switching modes.

## AB-41. payment_provider_configs is never read by checkout (Stripe)

**Category:** Payments — SILENT NO-OP
**Problem:** The whole table, its CRUD API, and the Payment Configs UI (AB-34) are a
dead end. `grep` for `paymentConfigQueries|PaymentProviderConfig` outside
`hpayments/` returns only wiring stubs. The checkout path resolves the provider from
`sales_channels.provider/payment_mode` (`domain/payments/routing.go`), and
`POST /v1/payment-intents` takes `provider` as a **free-text string from the client**
(`hcheckout/payment_intents.go:216-219`). The operator fills in a form — including the
`secrets` jsonb — that influences nothing.
**Steps:**
1. `PaymentRoutingPolicy` resolves against `payment_provider_configs` for the org:
   provider + `is_active` + `status='configured'`, taking `provider_account_id` and
   secrets from there. `sales_channels` keeps deciding *which* provider; the config
   supplies *the credentials*.
2. `POST /v1/payment-intents` stops trusting a client-supplied `provider`.
3. Stripe reads its account and keys from the config rather than process env.
4. Test: checkout against an org whose config is `is_active=false` or
   `status='missing_required_fields'` must fail with a clear error, never fall through to
   a default.
5. Related dead flag, decide in the same pass: `organizations.kyb_status` gates nothing —
   an `unverified` org can take payments and payouts today.

## AB-42. Event creation wizard (replaces the current single modal)

**Category:** Admin UI
**Problem:** Create Event is one flat modal that posts only the event
(`events.tsx:1950`), leaving an event with no session, no tier and no way to sell. The
owner's live attempt produced exactly that. Bil24's order is
Country -> City -> Venue -> Event -> Seating plan -> Session(s) -> categories/prices.
Depends on AB-36/37/38.
**Steps:**
1. Step 1 — event identity: org, name, visibility, description. Creates immediately in
   `draft`. No dates, no venue (they moved to the session).
2. Step 2 — first session: venue (cascading country/city/venue per AB-35), seating plan
   version, admission mode, start/end. Capacity **derived and shown read-only**; currency
   shown as derived text.
3. Step 3 — categories and prices; per-seat assignment per AB-39.
4. **Publish gate:** `event.publish` refuses an event with no session, or a session with
   no priced tier, with a specific reason. This is what the owner meant by "finalisation" —
   achieved by a gate, not by deferring creation, so a half-finished event stays
   resumable.
5. The wizard is resumable: reopening a draft event lands on the first incomplete step.
**Done when:** an operator can go from "no event" to a sellable published event without
leaving the wizard, and cannot publish an unsellable one.

## AB-43. Publications: channel picker, derived city, honest errors

**Category:** Admin UI / Backend
**Problem:** Three separate defects in one small tab.
(a) "Feed token ID" is a free-text UUID box. There is **no UI anywhere** to create or list
feed tokens, although `hfeed/feed_tokens.go` implements full CRUD under a sales channel.
The owner pasted the event's own id — the only id visible on screen.
(b) That produces a Postgres FK violation (23503) on
`event_publications.feed_token_id`, which `hcatalog/publications.go:138-145` collapses
into a generic 500 `publication.internal`, surfaced as "The server failed to apply the
publication change." Every DB error of any kind maps to that one string.
(c) "City scope" asks the operator for a city the system already knows from the venue.
Note the concept is sound — Bil24's model is an explicit allow-list of trusted agents per
organizer (`Frontends -> Trusted agents`, add/remove) — the UI just exposes the wrong end
of it.
**Steps:**
1. Replace the UUID input with a sales-channel picker; the feed token is resolved (or
   created) behind it. Add feed-token management to the Channels screen.
2. Map 23503 to 404 `publication.feed_token_not_found` (and the city FK likewise);
   document both in `openapi.yaml` — the spec currently documents only the 500.
3. Default city scope from the session's venue city; expose an override only under
   "advanced". Keep the column — global (NULL) publication stays meaningful.

## AB-44. Events UI hygiene (small, independent)

**Category:** Admin UI
1. Create/Edit modals must not close on outside click (`events.tsx:1976` backdrop
   `onClick={onClose}`); Escape closes with a confirm when the form is dirty. There is no
   Escape handler at all today despite `aria-modal="true"` — reuse
   `components/layout/ResponsiveDrawer.tsx`, which already does both correctly.
2. Venue column renders a raw shortened UUID (`events.tsx:1373`) — name + display number
   per the AB-22 rule.
3. Pricing-mode select has no help text; add per-mode descriptions (fixed / free / pwyw
   with min-max).
4. Activity tab is an honest placeholder — leave it, or wire it to `audit_events` if
   cheap.

## AB-45. Dead schema: wire it or drop it

**Category:** Database hygiene
Found during the audit; each is schema with no consumer. Decide per item, do not leave
them ambiguous:
1. Migration 0051 event metadata — `slug`, `short_description`, `genre`, `age_rating`,
   `duration_minutes`, `poster_media_id`, `teaser_url`, `trailer_url`, `meta_*` **plus the
   whole `event_artists` table**: zero readers, not referenced by any sqlc query or
   handler. These are exactly the fields a public event page needs, so likely "wire".
   Note the Bil24 editor confirms most of them as real operator inputs (`Age limit 16+`,
   `Duration 100 min`, `Genres`, `Promoter`, `Short title` / `Full title` / `Header`,
   `Rating`, `Min. service fee %`) — so "wire", not "drop", for that group.
   **`poster_media_id` is already decided by AB-47** (becomes the event-level default that
   sessions inherit) — do not drop it.
2. `promo_codes.applies_to_tier_ids` — a tier-restricted promo code applies to **any**
   tier at checkout; the restriction is never read.
3. Organisation branding on ticket PDFs/emails — 10 `delivery.Payload` fields are
   declared, rendered and unit-tested but **never populated** in production
   (`htickets/delivery_enqueue.go`), so every ticket ships with platform-default
   branding. `organizations.logo_media_id` additionally has no write path at all.

## AB-46. Domain layer has no tests

**Category:** Testing
`internal/domain/catalog`, `inventory`, `tickets`, `billing`, `reporting` contain the
state machines and invariants (event/session lifecycles, reservation TTL precedence,
discount math, ticket transitions) and have **zero test files** between them. The only
tests referencing them are `tests/staticanalysis/*_layout_*` import-restriction checks.
The 423/423 figure is overwhelmingly HTTP-boundary tests against the composed server —
the layer that was split out (#183-#187) specifically to be unit-testable never was.
Add table-driven tests per aggregate for the transition matrices and guards.

## AB-47. Posters belong to the session, not the event — deliberate divergence from Bil24

**Category:** Catalog / Media
**Problem:** Bil24 attaches artwork to the **event** (the editor's Event pane carries
`Upload…` and the poster thumbnails, while the Session pane has none). Owner's field
experience says this is wrong: organizers put the **specific date and venue on the poster**,
so one image per event does not survive contact with a multi-date run. Decision: **posters
bind to sessions**, with a one-click way to reuse the same image across sessions when the
artwork happens to be date-neutral. This is an intentional departure from the reference —
record it as such so nobody "corrects" it back later.

**Steps:**
1. Add `session_poster` to the `media_objects.owner_type` CHECK **and** to
   `mediastore.AllowedOwnerTypes` in the same migration. AGENTS.md documents this exact
   trap: widening the Go allowlist without the migration makes `POST /v1/media` stream the
   bytes to storage and *then* fail the INSERT with 23514.
   `TestAllowedOwnerTypes_MatchMigrationCheckConstraint` guards the pair — extend it.
2. `sessions.poster_media_id uuid NULL REFERENCES media_objects(id)`.
3. Reuse without a join table: keep `events.poster_media_id` (migration 0051, currently
   dead — see AB-45) and **repurpose it as the event-level default**. A new session
   inherits it; a session may override it. The admin shows one checkbox on upload — "use
   for all sessions of this event" — which writes the image as the event default *and*
   clears per-session overrides. This gives both "apply to all now" and "apply to sessions
   created later" without extra schema.
4. Resolution order everywhere a poster is rendered: `session.poster_media_id` ??
   `event.poster_media_id` ?? none. Applies to the public feed, the widget, the WordPress
   cache contract (`08_architecture/02_wordpress_integration_contract_ru.md` currently
   lists poster URLs under the *event* — update the contract) and ticket PDF/email.
5. Open question for the owner, do not guess: the Bil24 event pane shows **two** image
   slots (plus a separate `big image 640x670` drop area on the venue). Confirm whether we
   need one poster per session or a small set (e.g. portrait + landscape) before fixing the
   column shape.

**Done when:** an organizer can upload one poster, tick "use for all sessions", and later
override it on a single session; every surface resolves session-first with event fallback.

## AB-48. Category pricing: defined categories only, three-level cascade, scheduled prices

**Category:** Pricing / Catalog
**Depends on:** AB-38 (currency), AB-39 (categories), AB-40 (GA categories)

### Fix the reference's defect, don't copy it

The Bil24 "Setting session prices…" dialog lists **all fifteen** categories regardless of
how many the plan uses: the screenshot shows `First: EUR 10, Seats: 0` … `Third: EUR 30,
Seats: 260` … `Fifteenth: EUR 150, Seats: 0`, `Total seats: 260`. Fourteen of the fifteen
rows are noise the operator has to zero out. Owner: if the organizer defined 5 categories,
ask for 5 prices.

### Where a price comes from — three levels, each overriding the previous

1. **Plan** — `Starting price` per category in the seating-plan category table. A seed.
2. **Session** — set when sessions are created, appliable to several sessions at once.
3. **Live** — edited in the session editor after sales have already started.

### Beyond Bil24 — scheduled (dynamic) pricing

The organizer sets prices per category **for date ranges**: early-bird until a date, then
the standard price, switching automatically with no operator action. Defined while creating
sessions, and editable mid-sale — both the amounts and the dates they apply to.

Do not confuse this with `ticket_tiers.sale_window_start/end`, which already exists and
governs **when a tier is sellable**, not what it costs. Both must coexist.

### Steps — A. only the categories that exist

1. Price forms offer exactly the categories the plan defines: seated categories with at
   least one seat bound, plus every GA category. Never a fixed 15-row grid.
2. Keep 15 as a ceiling, not as a shape.
3. Show each category's seat count / GA capacity beside its price — the reference's one
   genuinely useful touch here (`Third: … Seats: 260`, `Total seats: 260`).

### Steps — B. the cascade

4. Plan `starting_price` seeds the session tier price; after creation the session price is
   independent of the plan.
5. Session-creation pricing applies one grid to several sessions in a pass (the
   `set →` / `← remove` multi-select in the reference).
6. Post-sale edits are permitted but **audited**: who changed which category, from what to
   what, when. This is money — an untracked edit is unacceptable.

### Steps — C. scheduled pricing

7. New table `ticket_tier_prices` (`tier_id`, `valid_from`, `valid_to` nullable,
   `price_amount`), with a **database-level guarantee of non-overlapping windows per
   tier** — a GiST exclusion constraint over `tstzrange` (needs `btree_gist`). Overlap
   resolved in application code will eventually produce two prices for one moment.
8. **One resolver**, used by the pricing quote, checkout, widget and public feed alike:
   the window containing `now()`, falling back to the tier's base `price_amount` when none
   matches. Do not reimplement per surface — divergence here is a pricing incident.
9. **The quoted price is locked at reservation creation** for the reservation's TTL. A cart
   held across a boundary must not silently reprice. Issued tickets are never repriced.
10. Mid-sale editing: future windows are freely editable; a change to the currently active
    window applies to **new carts only** and never rewrites tickets already sold.
11. Expose the current price and, where the next change is known, the date it changes — the
    widget may show "price rises on <date>"; the data must be available even if the UI
    ships later.
12. Decide and document the gap policy **once**: a schedule that leaves a period uncovered
    either falls back to the base price or is refused at save time. A silently zero price is
    the failure mode to design out.

**Done when:** an organizer who defined 5 categories is asked for exactly 5 prices; an
early-bird price switches to standard on the configured date with no operator action; a
cart held across that switch keeps the price it was quoted; and every post-sale price edit
appears in the audit log.

## AB-49. Ticket cancellation: the operator action that does not exist

**Category:** Inventory / Ticketing — CRITICAL, permanent inventory loss

### The invariant being tested (owner, confirmed against the original Bil24 design)

> A seat and a ticket are separate entities. We do not sell seats — we sell tickets, and a
> ticket *reserves* a seat. Over the life of a session several tickets may exist for the
> same seat (sold → cancelled → sold again); only one is valid at a time. When a ticket is
> cancelled the seat returns to sale **immediately**.

### Cancellation and money refund are two different things (owner, 2026-08-01)

This governs the whole item — read it before anything else.

**Ticket cancellation is the primary operator action and the sole driver of inventory and
gate state.** The organizer cancels a ticket in the admin panel. That act, by itself and
immediately, and regardless of any money movement:
- invalidates the ticket,
- returns its seat to sale,
- restores capacity,
- notifies MACS so the ticket stops admitting at the door.

**The money refund is a separate, optional, possibly-later action.** At cancellation the
organizer picks one of:
- **automatic** — where the payment provider supports it and is configured: the organizer
  confirms the amount (**full or partial**) and the platform calls the provider;
- **manual** — the organizer will refund from the provider's own dashboard later; the
  platform records the obligation and performs no financial operation;
- **none** — nothing is owed (comp ticket, no-refund policy).

**The money outcome must never gate inventory or admission.** A cancelled ticket frees its
seat and stops admitting the moment it is cancelled — whether or not money has moved,
whether or not it ever will, and whether or not the provider call succeeds. Wiring the seat
release behind a successful refund would reintroduce the exact failure this item exists to
remove.

**Today the code has this backwards.** The *only* path that cancels a ticket is the inbound
payment-refund webhook (`hcheckout/refunds.go` → `CancelTicketsByCheckoutSession`) — money
drives cancellation, and **there is no admin "cancel ticket" action at all**. Undo the
inversion: cancellation becomes the operator-facing primitive; refund becomes its optional
financial consequence. Keep the inbound-refund path, but demote it to a *defensive
secondary trigger* — a refund initiated directly in Stripe must also cancel the ticket, so
the two systems cannot drift apart.

**The structural half is correct — do not "fix" it.** `tickets` and `session_seats` are
separate tables; `tickets` holds no FK to `session_seats`, only denormalized
`seat_key/seat_sector/seat_row/seat_number` copies taken at issuance
(`0058_session_seating.sql:180-184`, rationale at `:43-47`). There is **no** unique
constraint on `(session_id, seat_key)` in `tickets`, so several ticket rows for one seat
are permitted exactly as required; the only unique constraint on the table is
`tickets_checkout_ordinal_uq (checkout_session_id, ordinal)` from `0066`, which is about
idempotent issuance and is orthogonal to seats. Ticket states are
`active | cancelled | transferred | revoked` (`0026` widened by `0038`), all non-`active`
terminal.

### The lifecycle half is not implemented at all

1. **The SQL primitive does not exist.** `ReleaseSessionSeat`
   (`queries/session_seats.sql:141-155`) is the only "give the seat back" query and its
   predicate is `WHERE ... status = 'held'` — it structurally cannot touch a `sold` row. No
   query anywhere in the codebase transitions `session_seats.status` from `sold` back to
   `available`.
2. **Refund path releases nothing.** `CancelTicketsByCheckoutSession`
   (`queries/refunds.sql:64-69`) updates `tickets` only. `hcheckout/refunds.go` never
   touches `session_seats`, never revokes barcodes, never decrements
   `inventory_ledger.capacity_sold`. **A refunded assigned seat stays `sold` forever and can
   never be resold.**
3. **Complimentary revocation is closer but still incomplete.**
   `htickets/complimentary.go:474-687` revokes tickets, barcodes and credentials and calls
   `RestoreSoldCapacity` — but also never touches `session_seats`. A revoked comp ticket
   leaves its assigned seat permanently `sold`.
4. **GA capacity is never returned on a paid refund.**
   `0020_inventory_ledger.sql:91` says `capacity_sold` is "never decremented (refunds
   handled via separate domain events)" — that consumer does not exist. The only caller of
   `RestoreSoldCapacity` outside tests is complimentary revocation.
5. Seat release exists **only pre-issuance**: `releaseReservationSeatsTx` shared by manual
   hold-release, reservation cancel and the TTL reaper. A fundamentally separate mechanism
   that cannot be reused post-issuance.

### Steps

### Status ownership and the seat state machine (owner-confirmed 2026-08-01)

**`available` / `unavailable` / `sold` belong to the SEAT. `refunded` belongs to the
TICKET.** This was explicitly confirmed and is not open for reinterpretation: a refund is
recorded on the ticket as `refund_date` / `refund_price` — the exact shape the Bil24 export
already carries — and the seat it held returns to `available` immediately, so the place is
resellable at once. There is no `refunded` seat status; any "refunded" view in reporting is
derived by joining tickets, never a state that blocks resale.

Seat status set: **`available | held | sold | unavailable`**. `held` is ours (cart TTL); the
reference expresses that through reservations rather than a seat state, but we need it.

**Rename — DECIDED 2026-08-01, do it:** the current DB value is `blocked`; the domain word
everywhere else — reference UI, owner, this spec — is `unavailable`. **Rename the CHECK
value in the database**, do not maintain a `blocked`↔`Unavailable` translation layer. It is
a wire-visible change to the seat-status endpoints, but this wave already breaks those and
the cost only grows later. (Contrast AB-30, where `capacity_standing` was deliberately left
alone because renaming would have rippled through stable API contracts; a status enum value
in one CHECK is a far smaller blast radius.) Update the CHECK, every query, the OpenAPI
schema, the TS client and the admin UI in one commit — a half-renamed enum is worse than
either end state.

**Transition table — implement exactly this, reject everything else:**

| From | To | Allowed | Trigger |
|---|---|---|---|
| `available` | `held` | yes | cart hold |
| `available` | `unavailable` | yes | operator withdraws from sale |
| `held` | `available` | yes | hold released / TTL expiry |
| `held` | `sold` | yes | checkout confirmed |
| `unavailable` | `available` | yes | operator releases to sale |
| `sold` | `available` | yes | **only** via ticket refund/void |
| **`sold`** | **`unavailable`** | **NO** | must go `sold → available` (refund) first |
| `unavailable` | `held` / `sold` | no | — |
| `held` | `unavailable` | no | release the hold first |

The forbidden edge is the one to get right: **a sold seat can never be taken out of sale
directly.** The operator must refund the ticket, which frees the seat to `available`, and
only then may they mark it `unavailable`. Rejecting `sold → unavailable` must return a
specific, actionable error ("refund the ticket on this seat first"), not a generic 409 —
this is a real workflow an operator will attempt.

**Steps:**
0. Implement the status set and transition table above as guarded conditional UPDATEs (one
   query per legal edge, `WHERE status = <from>`), so an illegal transition is impossible at
   the SQL layer rather than merely discouraged in a handler.
1. **Build the missing operator action: `POST /v1/tickets/{id}/cancel`.** Body: a reason,
   plus the refund decision — `mode: none | manual | automatic` and, for `automatic`, the
   confirmed amount (defaulting to the full paid amount, editable down for a partial).
   Permission-gated and audited: who cancelled what, when, why, and which refund mode was
   chosen. Admin UI: a Cancel action on the ticket, with the refund choice presented as a
   deliberate step, not a hidden default.
2. In **one transaction**, cancellation performs: ticket → `cancelled`; seat
   `sold -> available`; capacity restored; barcodes and credentials revoked; a MACS
   notification enqueued (AB-50). None of these may be conditional on the money.
3. New query `ReleaseSoldSessionSeat`: conditional `sold -> available`, clearing
   `reservation_id` and bumping `status_version`. Guard it so it cannot fire while a valid
   active ticket still references that seat.
4. Record the financial side on the ticket without letting it block anything:
   `cancelled_at`, `cancellation_reason`, `refund_mode`, and — for `automatic` — a link to
   the `refunds` row (0028). `manual` is an **outstanding obligation**, visible in the
   admin as "refund pending, handled outside the platform"; it must not read as done.
5. Only for `mode = automatic`: call the provider with the confirmed amount. A provider
   failure leaves the ticket cancelled and the seat on sale, and surfaces as a retryable
   financial task — never as a rolled-back cancellation.
6. Wire seat release into the **other** terminal transitions too: complimentary revocation,
   and the inbound refund webhook in its new defensive role.
   **A Stripe-initiated refund is ORDER-level** (owner, 2026-08-01): the money comes back
   for the order, and an order may hold several tickets — so a full inbound refund must
   move **every ticket of that order** to refunded, releasing every one of their seats.
   The existing `CancelTicketsByCheckoutSession`
   (`WHERE checkout_session_id = $1 AND status = 'active'`) already has the right scope,
   since one order is one checkout session (scope decision 7); it simply never released the
   seats. Keep the scope, add the release.
   **Open question — partial inbound refunds.** A partial amount refunded directly in
   Stripe cannot be attributed to particular tickets: the platform has no way to know which
   two of five seats the organizer meant. Cancelling all of them would be destructive and
   cancelling none would be silently wrong. **Recommendation: do not auto-cancel anything on
   a partial inbound refund — record it and raise it for operator review**, so a human
   decides which tickets it covers. Confirm before implementing; do not let an agent invent
   a proportional-allocation rule.
7. Decrement `inventory_ledger.capacity_sold` on every cancellation (GA and seated alike),
   using the existing `RestoreSoldCapacity`. Delete the stale "separate domain events"
   comment in 0020 or implement what it promises.
8. Re-selling a released seat must work end to end: cancel → seat available → new
   reservation → new ticket, with both ticket rows retained in history.
9. Regression tests per path: admin cancel of an assigned seat, admin cancel of a GA place,
   comp revocation, inbound Stripe refund, and a full sell → cancel → resell cycle. Plus the
   one that matters most: **cancel with `mode = manual` must free the seat and notify MACS
   with no provider call at all.**

**Done when:** an organizer can cancel a ticket from the admin, the seat is purchasable
again within the same request, MACS stops admitting it, and the money decision — automatic,
manual or none — is recorded without ever having gated any of the above.

## AB-50. Integrate the EXTERNAL scanning service (JSON export + Bil24-shaped webhooks)

**Category:** Integration / Ticketing — CRITICAL (a refunded ticket must stop working)

### Scope correction — the in-platform scanner is NOT the product

Owner decision 2026-08-01: **we do not need an internal scanner wired to our own database.**
Gate control is a separate, already-operating product — a ticket-scanning service with its
own backend, admin panel and iOS/Android apps, capable of holding tickets from many
different organizers (screenshot: `macs.arenasoldout.com`, "ArenaSoldOut", with
Events / Users / Imports, an `Import Tickets` action, and per-event stats
`Total Tickets | Not used | Check in | Check out | Ticket was refunded` over a
`BARCODE | SECTOR | ROW | SEAT | STATUS | VALIDATED` table).

The service is **Max Mobil Access Control System (MACS)**. So the platform's job is **to feed
it accurately**, by two routes:
1. **JSON export/upload** — the owner supplied a working sample (`sample_tickets.json`).
2. **HTTP webhook notifications** — Bil24-compatible, per
   `bil24.pro/manager.html#83`, `/notification_order_data.html`,
   `/notification_ticket_data.html`, `/notification_session_data.html`.

### The admission decision is TICKET-level, not order-level (owner-confirmed)

MACS decides admission from the **ticket's** state — is this ticket valid, has it been
refunded. **Order status is not the gate criterion.** Two consequences for us:

- Per-ticket refund state must be exact and independently propagated. `order.cancelled`
  alone is not sufficient: a partial refund of one ticket in a five-ticket order must reach
  MACS as `ticket.refunded` for that ticket, and the other four must remain valid.
- Anything MACS derives on its own — check-in / check-out, entry/exit counting, re-entry
  policy — is **out of scope for the platform**. We do not model it, do not mirror it, and
  do not need `scan_events` for this product. We supply ticket validity; MACS owns what
  happens at the door.

**The security requirement does not disappear — it relocates.** Today a refunded ticket
still scans as valid (evidence below). Under the new architecture the equivalent failure is
"the platform never told the scanning service about the refund", and the ticket works at the
door just the same. The external service already tracks `Ticket was refunded`, so it can
represent this — we simply have to send it.

### The current internal endpoints, for the record

They stay out of scope for investment, but must not be left as a live hole:
- `POST /v1/scan` (`hbarcode/barcodes.go:408-542`) rejects only
  `barcode.Status == "revoked"`; it never loads the ticket row.
- `POST /v1/scanner/validate` (`hscanner/scanner_snapshot.go:380`): `Valid: barcode.Status
  == "active"`.
- `POST /v1/scanner/scan-events` selects `t.status AS ticket_status`
  (`queries/scan_events.sql:48-59`) and **never reads it**
  (`hscanner/scanner_callback.go:224-336`) — admits unconditionally, sets `used_at`.
- `hcheckout/refunds.go` never revokes barcodes, so a refunded ticket's barcode stays
  `active`.
Minimum action: make all three respect ticket status (the `scan-events` query already
returns it), then mark them internal/testing-only in the OpenAPI description. Do not build
anything further on them.

### The export contract — confirmed from the owner's sample

Top level is an **order**, tickets nested inside it:
`id, date, user{id,email}, agent{id,name}, frontend{id,agentId,name,type{id,name}},
currency, paymentMethod, longReservation, expiration, processing, ticketList[], seatList[],
gatewayOrderList[], sum, discount, charge, totalSum, ticketQuantity, status ("PAID"),
acquiring{id,systemId,name,systemName:"Stripe",agentId,agentName}, paymentBankId,
paymentBankStatus, paymentBankMessage, paymentRRN, paymentTerminalId, paymentCardPAN,
paymentCardBank, email, emailSent, phone, fullName`

Each `ticketList[]` entry:
`id, seatId, orderId, seatLocation{sector,row,number}, category, tariff, price, discount,
charge, totalPrice, discountReason, barcode, barcodeFormat{id,name:"EAN-13"},
actionEvent{...}, holderStatus, refundDate, refundPrice`

Two things this settles beyond doubt:
- **`seatId` is a required, first-class field on every ticket** — the ticket points at a
  seat identity. This is the same invariant AB-49/AB-51 are about, visible in the wire
  format.
- **`refundDate` / `refundPrice` live on the ticket**, which is exactly the shape proposed
  for our own refund record in AB-49 step 0.

`actionEvent` carries `id, cityId, cityName, venueId, venueName, actionId, actionName,
actionLegalOwner, actionLegalOwnerInn, actionKind, currency, showTime, eTickets, gateway{}`
— note it denormalizes city/venue/organizer onto every ticket, so the scanning service needs
no lookups.

### The webhook contract — from the Bil24 docs

Envelope: `{ id: Ulong, created: ISO-8601 UTC, type: string, data: object|array }`.
Delivery: **POST, expect HTTP 200, retry for 24 hours.**

| Trigger | Payload |
|---|---|
| `order.paid` | order object as above, with `ticketList` |
| `order.cancelled` | **same shape but `ticketList` is absent and `seatList` replaces it**, identical field structure |
| `ticket.refunded` | a single ticket object (with `refundDate`, `refundPrice`) |
| `event.created` / `event.changed` / `event.deleted` | session records |

Session payload fields: `actionEventId, externalEventId, cityId, cityName, venueId,
venueName, seatingPlanId, seatingPlanName, organizerId, organizerName, actionId, actionName,
gatewayId, currency, showTime (local, no timezone), sellStartTime, sellEndTime (both UTC),
eTicket, fullNameRequired, phoneRequired, fanIdRequired, visitorDocRequired,
visitorBirthdateRequired, sellEnabled`.

Note `seatingPlanId` on the session payload — third independent confirmation that a session
is bound to one plan (AB-36/AB-40). And note the per-session buyer-data flags
(`fullNameRequired`, `phoneRequired`, `fanIdRequired`, `visitorDocRequired`,
`visitorBirthdateRequired`) plus `sellEnabled` / `sellStartTime` / `sellEndTime` — **we do
not model any of these yet**; they also appear in the editor's session form. Add them to the
session schema as part of this item or spin them out, but do not silently drop them from the
payload.

### Steps

1. Ticket/order export in the sample's exact shape, both as a downloadable JSON file (to
   feed the service's `Import Tickets`) and as an API endpoint.
2. Webhook subscriptions producing the Bil24 envelope and the five trigger types above. We
   already have `webhook_subscribers` (0040) and a `v1.*` catalog
   (`08_architecture/15_webhook_event_catalog.md`) — add a **Bil24-compat flavour** rather
   than renaming our native events.
3. **Cancellation propagation is the acceptance criterion.** The trigger is the operator
   **cancelling a ticket** (AB-49), not a money refund — a ticket cancelled with
   `refund_mode = manual` or `none` must reach MACS exactly like one refunded
   automatically, because admission has nothing to do with whether money moved. Emit
   `ticket.refunded` per ticket — that is MACS's name for "no longer valid", and its gate
   check is `status == 3`, not "was money returned". Deliver through the existing outbox
   with retry; a whole-order cancellation becomes one `ticket.refunded` per ticket, since
   MACS does not consume `order.cancelled`.
4. Delivery semantics per the docs: POST, success = HTTP 200, retry over 24h. Our outbox
   already has `next_attempt_at` / `dead_lettered_at` (0068) — reuse it, do not invent a
   second retry mechanism.
5. **Sign our webhooks** even though the reference does not. Unsigned webhooks were already
   raised as a BLOCKER in the PR2 audit; `webhook_subscribers` carries a secret. Signature
   verification must be optional on the receiver so the existing service keeps working.
6. Status enum — **RESOLVED from the MACS source** (owner supplied the repo 2026-08-01;
   FastAPI + MongoDB, `app/models/tickets.py`, `app/api/tickets.py`). See the MACS contract
   section below. In short: it is an **integer**, not a string — `NEVER_USE` is the *Bil24*
   wire value and is not what MACS stores.
7. Per-ticket report fields, confirmed from the Reporter's ticket pane — the export must be
   able to produce all of them:
   `Order ID | Ticket ID | Seat ID | Sector | Row | Seat | Category | Tariff | Price |
   Discount | Cause of discount | Service fee | Total | Barcode | Barcode format |
   Owner status | Refund date | Session ID | Start of session | Venue ID | Venue name |
   Event ID | Event name`.
   `Cause of discount` carries the promo-code name (`Промокод CatDaniel`) or
   `Внешняя система` for externally-allocated/comp tickets — this is the `discountReason`
   field, and it must be populated, not left null.
   Note also that category names are free-form commercial constructs — the report shows
   `Входной билет` (GA) alongside `Раннее бронирование` (early booking). The reference
   models early-bird as a **separate category**; AB-48 gives us scheduled prices on a single
   category instead. Both must be expressible — do not assume one category means one price
   forever.
7. End-to-end test against a stub receiver: sell → export/notify → refund → assert the
   refund notification is delivered and retried on a non-200.

### The MACS contract — read from its source, not inferred

Owner supplied the MACS repo (`backend-develop`: FastAPI + MongoDB + Pydantic, ~26 files).
Everything below is quoted behaviour, not a guess. **Build against this, not against the
Bil24 docs, wherever the two differ** — Bil24 is the format's ancestor, MACS is the actual
receiver.

**Ticket status is an INTEGER on one field** (`app/models/tickets.py`, `TicketStatusStats`):

| value | meaning |
|---|---|
| `0` | not used |
| `1` | checked in |
| `2` | checked out |
| `3` | **refunded** |

`NEVER_USE` from the Bil24 sample is *not* what MACS stores. Note the consequence, which
contradicts the guidance previously written here: **MACS deliberately conflates usage and
refund in one field** — marking a ticket refunded (`3`) overwrites its check-in state. Keep
them separate in *our* model (usage vs `refund_date`/`refund_price`) and collapse to the
integer only at the boundary.

**The gate already does the right thing** (`app/api/tickets.py:385-391`): on validate, if
`current_status == 3` it returns `400` with `"Ticket was refunded <date>"`. So propagating a
cancellation is *sufficient* to close the admission hole — MACS needs no change, only a
truthful feed. This is the concrete mechanism AB-50 exists to deliver.

**"Refunded" is the deliberate word at the door — keep it** (owner, 2026-08-01). It is a
customer-service decision, not a technicality: told "invalid ticket", a holder argues *"but
I bought it"* and the door staff have no answer. Told **"this ticket was refunded"**, the
conversation is already settled — yes, you bought it; it was subsequently returned. Use
"refunded" in operator- and customer-facing text; do not "correct" it to
*cancelled* / *void* / *invalid* anywhere on the admission path.

One honest limitation to accept: MACS has exactly four integer statuses, so a ticket
cancelled with `refund_mode = none` (a revoked comp, say) still reaches the door as
"refunded". That is the best available message and matches the reasoning above. If we ever
need to distinguish "cancelled, nothing owed" at the gate, that is a **MACS-side** change,
not something to work around here (see the MVP note above).

**Webhook receiver:** `POST /_wh/tickets`, envelope
`{id: int, created: datetime, type: str, data: Ticket | TicketList | null}`.
Handled `type` values are **only `order.paid` and `ticket.refunded`**
(`app/api/tickets.py:674-686`) — MACS does **not** consume `order.cancelled` or the
`event.*` triggers the Bil24 docs describe. A whole-order cancellation must therefore be
emitted as one `ticket.refunded` per ticket, which lines up with the owner's rule that
admission is decided per ticket. (`/_wh/test` and `/_wh/reprocess/{id}` exist for
debugging.)

**Required ticket fields** (Pydantic, so a missing one is a validation error):
`id: int`, `seatId: int`, `barcode: str`, `actionEvent: Event` — and `Event` requires
`id`, `cityName`, `venueName`, `actionName`, `actionLegalOwner`, `showTime`.
Optional: `seatLocation{sector,row,number}`, `barcodeFormat`, `category`, `refundDate`,
`sold_at`, `ticket_system`, `system_ticket_id`.

**Identifier mismatch — plan for it.** `Ticket.id` and `Ticket.seatId` are typed `int`;
our ids are `uuidv7`. (`Event.id` is `Union[int, str]` and so is tolerant, but the ticket
fields are not.) MACS is explicitly multi-system — `ticket_system: str` (slug),
`system_ticket_id: str`, `BsonEvent.system_ids: Dict[slug, external_event_id]`, and a
generic `POST /tickets/import/{ticket_system_slug}` endpoint alongside per-vendor ones for
TicketTailor and TicketsCloud. So: **register as a ticket system with our own slug, carry
our UUIDs in the string fields (`system_ticket_id`, `system_ids`), and supply a stable
integer for `id`/`seatId`.** Decide that integer scheme deliberately — it must be stable
across re-imports and unique per ticket/seat, because MACS keys on it.

**The importer is dangerously permissive — send complete data.** `app/importers/
json_importer.py` silently repairs anything missing rather than rejecting it: a missing
order `id` becomes `random.randint(1000000, 9999999)`; missing `status` becomes `"PAID"`;
missing `seatId` is copied from the ticket `id`; a missing `actionEvent` is either borrowed
from a sibling ticket or fabricated as `Unknown City` / `Unknown Venue` /
`Event #<id>`; a missing `barcodeFormat` defaults to `EAN-13`. An incomplete export will
therefore import "successfully" and produce plausible garbage. Our export must be complete
by construction, and the round-trip test must assert on what MACS *stored*, not on the HTTP
status it returned.

Also note the import UI can bind an upload to a pre-selected event, in which case it
**overwrites `actionEvent` on every ticket** in the file — useful, and a footgun worth
knowing when reconciling.

**MACS is an MVP and will be developed further; we do not touch it in this wave**
(owner, 2026-08-01). It works today and stays as-is. Four consequences for how we build
against it:

1. **Isolate the contract behind an adapter.** All MACS-shaped mapping (int status,
   int `id`/`seatId`, envelope, field names) lives in one boundary package. Nothing
   MACS-specific leaks into the catalog/ticketing domain. When MACS changes, one file
   changes.
2. **Do not invent an elaborate permanent numbering scheme** for the int `id`/`seatId`
   mismatch. Pick the simplest stable, collision-free mapping that survives re-import, and
   record that the cleaner long-term fix is widening MACS to accept string ids — a change
   for the MACS backlog, not something to engineer around forever here.
3. **Do not compensate on our side for MACS defects.** The silently-fabricating importer is
   a MACS bug; the fix belongs there. Our job is to send complete, correct data and to fail
   loudly if we cannot — not to build workarounds that will outlive the bug.
4. Findings about MACS that we hit while integrating go into a MACS-side note for the owner,
   not into this repo's code.

## AB-51. GA units get real identity — one row per place, decided

**Category:** Inventory — model change, decision taken

**Owner decision 2026-08-01:** every General Admission place gets **its own unique ID and
its own database row at session setup** — "так же, как в API Bil24". This closes the fork
previously recorded here; option "keep the counter" is rejected.

GA rows use the **same seat status set and the same transition table as assigned seats**
(`available | held | sold | unavailable`, with `sold → unavailable` forbidden — see AB-49).
That uniformity is the point of the change: one inventory concept, one state machine, one
admin table.

The reference makes it concrete. In the seat-management dialog for
`Oct 9, 2026 7:30 PM IVO DIMCHEV in Prague`, assigned seats and GA places sit in **one
table**, distinguished only by whether the coordinate columns are filled:

```
ID             Sector          Row  Seat  Category           Price  Barcode  Status
2,874,435,893  Balcony center  1    5     SEATING - SEZENÍ   1,890           Sold
2,874,435,979                              General admission    590           Unavailable
2,874,436,059                              General admission    590           Sold
2,874,436,066                              General admission    590           Available
```

GA ids are allocated contiguously at setup time (…,979 through …,100+), long before any
sale — they exist as inventory, not as a by-product of purchase.

**What we have today (to be replaced):** GA is a bare counter.
`reservation_ga_items` (0063) stores `quantity` keyed `(reservation_id, tier_id)`;
availability is `capacity_total - capacity_held - capacity_sold` on a single
`inventory_ledger` row (`hinventory/inventory.go:62-66`); `bind.go:34-36` states GA sessions
are never touched by seat materialization; GA tickets carry `seat_key/sector/row/number` as
explicit `nil` (`htickets/tickets.go:257-260`); and `getSeatListGA`
(`hbil24/bil24_compat.go:448-479`) can only return an aggregate `availableCount` with **no
`seatId`** — while the export format requires `seatId` on every ticket.

**Steps:**
1. Materialize GA capacity as `session_seats` rows at session setup — one row per place per
   GA category, with `sector_name`/`row_name`/`seat_number` empty and `tier_id` set to that
   category. Same table, same status machine, same block/unblock/hold/sell paths as
   assigned seats.
2. Retire `reservation_ga_items` in favour of `reservation_seats`, or keep it strictly as a
   denormalized rollup — one source of truth, not two.
3. `inventory_ledger` becomes a derived counter (or is dropped for seated/GA sessions);
   availability is `count(status='available')`. Whichever is chosen, it must not be possible
   for the counter and the rows to disagree.
4. GA tickets carry a real `seatId`; the Bil24 export and `GET_SEAT_LIST` emit per-unit ids,
   restoring compat parity.
5. Individual GA places become blockable/voidable exactly like seats — this is what makes the
   AB-39 table work uniformly for both.
6. **Volume and contention are the real risk.** Palác Akropolis is ~590 rows per session;
   a large arena is far more. Materialization must be a single bulk insert (the existing
   `InsertSessionSeats` path), and GA hold/sell must not serialise the whole category —
   benchmark an on-sale burst before this ships, and record the numbers.
7. The widget's GA quantity popover (AB-40 Part D) now allocates N concrete units from the
   pool rather than decrementing a counter; the buyer-facing behaviour is unchanged.

---

**Repo gates for every item in this wave** (waves 1-3 lost time to all of these):
`openapi.yaml` updated for every route change and Go types + TS client regenerated in the
same commit; migration-head pin in tests bumped; any test needing a live DB behind the
`integration` build tag; `gofmt`/`golangci-lint` over `apps/backend` before commit — the
AB-28..AB-35 wave was pushed claiming "all green" while CI Lint was red on gofmt.
