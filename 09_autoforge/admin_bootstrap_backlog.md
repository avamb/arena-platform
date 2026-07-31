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
