# AutoForge backlog: assigned seating (seat maps) — Wave SEAT

Updated: 2026-07-10
Status: planning artifact for AutoForge. This file is not an implementation.
Owner decisions in §2 were confirmed by the owner on 2026-07-10.

## 1. Goal

Take the platform from GA-only to selling **assigned seats on a venue
seat map**, end-to-end: import an existing Bil24-convention SVG scheme →
versioned seating plan → bind to an event session → buyer selects
specific seats → reservation/checkout/ticket carry the seat → Bil24
gateway serves real seats. The GA flow must keep working unchanged and
both modes must coexist per session via `admission_mode`.

**First production venue: Palác Akropolis (Prague).** Its author-format
scheme already exists at
`06_venue_maps_and_seating/Palac_Akropolis.svg` (279 seats; sections
`Parter`, `Balcony left`, `Balcony center`, `Balcony right`; 15 price
categories `First`…`Fifteenth`; valid `PriceCategory`/`Legend` groups).
It is the acceptance fixture for the whole wave. The source PDF from the
venue (`BIGHALL_seating_pa_venuerider.pdf`) and the Bil24 rendering
reference (`palac_akropolis_shema_bil24.jpg`) sit next to it.

## 2. Confirmed owner decisions (2026-07-10)

These resolve clarification-register items Q11, Q28 and Q65:

1. **Geometry source of truth is structured JSON** stored in
   `seating_plan_versions.geometry` (JSONB). SVG is a derived /
   imported representation, never the canonical store. (Q11, Q28)
2. **Import-first, no visual editor in this wave.** Schemes are
   authored externally (Inkscape, Bil24 authoring conventions) and
   imported. A visual seat-map editor is explicitly out of scope. (Q65)
3. **The import pipeline must reproduce the Bil24 Editor conventions**
   (§6) so every SVG scheme already drawn for Bil24 — including the 23+
   files in `06_venue_maps_and_seating/svg_library/` — imports without
   modification.
4. GA remains the default. Assigned seating is opt-in per session via
   `admission_mode`.

## 3. Non-negotiable rules

- All guardrails in `09_autoforge/00_AGENT_GUARDRAILS.md` apply.
  Directly load-bearing here: #12 (Venue reusable, SeatingPlan a
  separate owned entity with versions and permissions), #13 (no editing
  another org's plan — fork/copy), #14 (idempotency + audit + permission
  checks on every mutation), #16 (static schema separated from dynamic
  seat status; keep the path to large-venue mode), #34 (this wave is the
  explicit owner decision that unlocks assigned seating).
- `08_architecture/04_large_venue_performance_strategy_ru.md` is
  MANDATORY reading before designing any seating endpoint. This wave
  implements the small/medium-venue path but must not close the door on
  large-venue mode (precomputed assets, bitsets, waiting room stay
  possible without schema changes).
- The GA commerce path (`inventory_ledger` counters, quantity
  reservations) must not regress: every existing test keeps passing.
- A seat can belong to at most one active hold/sale at any moment
  (domain invariant, `09_domain_state_machines_ru.md:48`); enforcement
  is transactional in PostgreSQL, not advisory.
- Financial totals continue to come from the platform (guardrail #15);
  seat selection changes *which* tier prices apply, never how totals
  are computed.
- No mock/sample data in production paths. The Akropolis fixture lives
  in testdata only.

## 4. Current implementation contract (post-refactor code map)

The codebase was heavily refactored in July 2026 — the layout AutoForge
remembers from Waves 1–20 has changed. Facts that bind this wave:

- Domain handlers live in sub-packages of
  `apps/backend/internal/platform/httpserver/`: `hcatalog` (events,
  sessions, tiers, venues, publications), `hcheckout` (checkout,
  reservations, payment intents, refunds, promo, pricing),
  `hinventory`, `htickets`, `hbil24`, `hfeed`, `hiam`, `hbankaccounts`,
  etc. Each follows the pattern: `handler.go` with a `Handler` struct +
  `New(...)` constructor-injection; thin `*Server` delegate methods in a
  top-level `<domain>_shims.go`; mount functions in `mount_*.go` call
  the lowercase `s.handleX` shims. **The new seating domain must be a
  new sub-package `hseating` following exactly this pattern** (study
  `hbankaccounts` + `bank_accounts_shims.go` as the freshest exemplar).
- Sub-packages never import package `httpserver` (no cycles). Shared
  HTTP helpers come from
  `apps/backend/internal/platform/httpserver/httputil`. Cross-domain
  calls go through the owning sub-package's exported API or a callback
  injected from the shim layer (see `hfeed.PromoValidator` and
  `hcatalog.SessionCancelledPublisher` precedents).
- Static gates that WILL fail your build if ignored:
  - file-size gate: every non-test file in the top-level `httpserver`
    directory must be ≤ 400 lines
    (`apps/backend/tests/staticanalysis/httpserver_file_size_175_test.go`,
    allowlist is empty and must stay empty — keep shims small, put logic
    in the sub-package);
  - `panic(` requires `// allow:panic:` + registration in
    `ops/codequality/panic-audit-176.md`; non-RFC3339 time formats
    require `// allow:timeformat:`;
  - `golangci-lint run ./...` must stay at **0 issues** (v2 config);
    gofmt clean;
  - structural grep tests: when tests reference a moved/new file by
    name, update `testfilehelpers_test.go` (`domainSubPackageFor`,
    `resolveFileInRepo`).
- OpenAPI: `apps/backend/openapi/openapi.yaml` is **pure OpenAPI 3.1**
  — `nullable:` is forbidden by tests; nullability is `type: [X,
  "null"]`. Every parameter and schema property must carry a
  `description`. Drift is enforced BOTH ways
  (`openapi_drift_test.go`): every documented route must be mounted in
  `buildDriftTestServer` (wire new `*Queries` options there) and every
  mounted route must be documented. Spec-first gaps go into
  `specPendingImplementation` with a feature reference. Go types are
  regenerated with `make gen-openapi`, which pipes the 3.1 spec through
  `apps/backend/tools/openapi30gen` (oapi-codegen cannot read 3.1) —
  regenerate and commit `types_gen.go` whenever the spec changes.
- Migrations: goose, embedded, sequential; **next free number is
  0057**. `timestamptz` only (a static gate scans for bare
  `timestamp`). Wiring pattern for a new domain's queries: Options
  field in `wire.go`, field in `server_struct.go`, `pickQueries` entry,
  mount gated on `stub enabled + queries + pool` (see
  `mountBankAccountRoutes` in `mount_iam.go`).
- Data access: sqlc-style hand-maintained gen files in
  `apps/backend/internal/adapters/postgres/gen/` + canonical SQL in
  `adapters/postgres/queries/`. Match the existing style exactly
  (`bank_accounts.sql.go` is the freshest exemplar).
- Dev commands run in Docker (`golang:1.24` image); CI is GitHub
  Actions on `avamb/arena-platform`, branch `master`.

## 5. Data model (new schema)

### 5.1 Migration `0057_seating_plans.sql`

```sql
CREATE TABLE seating_plans (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    venue_id                uuid NOT NULL REFERENCES venues(id),
    owner_org_id            uuid NOT NULL REFERENCES organizations(id),
    name                    text NOT NULL,
    plan_type               text NOT NULL CHECK (plan_type IN
                              ('assigned_seats','general_admission','tables','mixed')),
    visibility              text NOT NULL DEFAULT 'private' CHECK (visibility IN
                              ('private','shared_read','public_template','operator_verified')),
    status                  text NOT NULL DEFAULT 'draft' CHECK (status IN
                              ('draft','active','archived')),
    source_seating_plan_id  uuid NULL REFERENCES seating_plans(id), -- fork lineage
    current_version_id      uuid NULL,  -- FK added after versions table exists
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz NULL
);

CREATE TABLE seating_plan_versions (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    seating_plan_id     uuid NOT NULL REFERENCES seating_plans(id),
    version_number      integer NOT NULL,
    geometry            jsonb NOT NULL,        -- canonical model, §5.3
    geometry_checksum   text NOT NULL,         -- sha256 of canonical JSON
    svg_asset_media_id  uuid NULL,             -- original SVG via media storage (Wave G)
    capacity_seated     integer NOT NULL,
    capacity_standing   integer NOT NULL DEFAULT 0,
    locked_at           timestamptz NULL,      -- set on first session binding; locked = immutable
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (seating_plan_id, version_number)
);
```

Plus permission seeds (same INSERT style as earlier permission
migrations): `seating_plan.read`, `seating_plan.create`,
`seating_plan.update.own`, `seating_plan.fork`, `seating_plan.share`,
`seating_plan.verify`, `seating_plan.archive.own`,
`event_session.assign_seating_plan`. Grant to organizer/org_admin roles
consistent with how `venue.*` permissions are seeded.

Rules enforced app-side: a version referenced by any session with sales
or issued tickets is immutable (new version instead — architecture doc
`03_platform_management_api_and_permissions_ru.md:154`); modifying a
plan you do not own is impossible — fork instead (guardrail #13).

### 5.2 Migration `0058_session_seating.sql`

```sql
ALTER TABLE sessions
    ADD COLUMN admission_mode text NOT NULL DEFAULT 'general_admission'
        CHECK (admission_mode IN ('general_admission','assigned_seats','hybrid')),
    ADD COLUMN seating_plan_version_id uuid NULL REFERENCES seating_plan_versions(id),
    ADD COLUMN seat_status_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT sessions_seated_requires_plan CHECK
        (admission_mode = 'general_admission' OR seating_plan_version_id IS NOT NULL);

CREATE TABLE session_seats (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    session_id     uuid NOT NULL REFERENCES sessions(id),
    seat_key       text NOT NULL,             -- stable key from geometry, §5.3
    sector_name    text NOT NULL,
    row_name       text NOT NULL,
    seat_number    text NOT NULL,
    tier_id        uuid NULL REFERENCES ticket_tiers(id),  -- price category mapping
    status         text NOT NULL DEFAULT 'available' CHECK (status IN
                     ('available','held','sold','blocked')),
    reservation_id uuid NULL REFERENCES reservations(id),
    status_version bigint NOT NULL DEFAULT 0, -- session-scoped monotonic, for deltas
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, seat_key)
);
CREATE INDEX session_seats_status_idx  ON session_seats (session_id, status);
CREATE INDEX session_seats_version_idx ON session_seats (session_id, status_version);

CREATE TABLE reservation_seats (
    reservation_id  uuid NOT NULL REFERENCES reservations(id),
    session_seat_id uuid NOT NULL REFERENCES session_seats(id),
    PRIMARY KEY (reservation_id, session_seat_id)
);

ALTER TABLE tickets
    ADD COLUMN seat_key    text NULL,
    ADD COLUMN seat_sector text NULL,
    ADD COLUMN seat_row    text NULL,
    ADD COLUMN seat_number text NULL;
```

Concurrency contract (mirrors the `inventory_ledger` idiom): holds are
taken with `SELECT … FOR UPDATE` on the target `session_seats` rows in
`seat_key` order (deterministic lock order → no deadlocks), a
conditional `UPDATE … WHERE status='available'` per seat, and the whole
reservation aborts with 409 + the list of conflicting seats if any
update touches 0 rows. Every status change increments
`sessions.seat_status_version` and stamps the row's `status_version`.

### 5.3 Canonical geometry JSON (stored in `geometry`)

```json
{
  "schema_version": 1,
  "canvas": {"width": 1200, "height": 900},
  "categories": [
    {"index": 1, "name": "First", "color": "#e53935",
     "price_hint": "590", "currency_hint": "CZK"}
  ],
  "sections": [
    {"key": "parter", "name": "Parter", "rows": [
      {"key": "1", "name": "1", "seats": [
        {"key": "parter|1|5", "number": "5", "x": 123.4, "y": 56.7,
         "radius": 6.0, "category_index": 1, "barcode_hint": null}
      ]}
    ]}
  ],
  "standing_zones": [],
  "tables": [],
  "decor_svg": "<g>…</g>"
}
```

- `seat.key` = `<section.key>|<row.key>|<number>`, unique per version;
  it is the stable identifier copied into `session_seats.seat_key`.
- `categories[].price_hint`/`currency_hint` are import hints only —
  binding to real `ticket_tiers` happens per session (§7 SEAT-B2).
- `decor_svg` carries non-seat drawing elements (stage, walls, labels)
  extracted from the source SVG so the client can render the backdrop.
- `geometry_checksum` = sha256 over the JSON with sorted keys; it is
  the ETag for schema endpoints.
- `standing_zones` and `tables` are part of the schema for forward
  compatibility (plan types `tables`/`mixed`) but MAY be empty and no
  selling logic for them ships in this wave.

## 6. SVG import conventions (Bil24 authoring format)

The importer must accept exactly what the Bil24 Editor accepts (sources:
bil24.pro/create_schemes.html, bil24.pro/BSS.html — rules reproduced
here so no web access is needed):

Input requirements (validate, with precise error messages naming the
offending element):

1. Canvas at most **2000×2000 px** (viewBox or width/height).
2. Every seat is a **`<circle>`** (no other shapes) with a stroke.
3. Seats are grouped by row: the row `<g>` carries
   `inkscape:label="#<SectorName>"` and a `<title>` child (or
   `inkscape:title`) with the **row name**. The `#` prefix marks
   seating groups; a leading word `Сектор`/`Sector` in the name must be
   stripped.
4. Each seat `<circle>` has `<title>` = **seat number** (required) and
   optional label = barcode hint.
5. A group `id="PriceCategory"` contains one colored `<circle>` per
   price category, each labeled `#<CategoryName>`; sibling
   `id="PriceCategoryText"` may carry price text (import as
   `price_hint`).
6. A group `id="Legend"` contains the status swatches labeled `#Sold`,
   `#None`, `#Reserved`, `#MyTickets`. It is ignored for geometry but
   its presence is validated (warning, not error, if absent).
7. **Seat→category binding is by exact fill color match** between the
   seat circle and a `PriceCategory` swatch. A seat whose fill matches
   no category is a validation error listing the color and element.
8. Duplicate `(sector,row,number)` triples are an error; every section
   must contain ≥1 row, every row ≥1 seat.
9. Everything that is not a seat/PriceCategory/Legend element is
   collected verbatim into `decor_svg`.

Import output: canonical geometry JSON (§5.3) + capacity counts + the
original SVG stored via the media adapter (`svg_asset_media_id`).

Export (SEAT-D3): a **BSS-compatible SVG** generated from geometry +
live status, using the Bil24 wire attributes: seats carry
`sbt:seat` (number), `sbt:id` (platform seat id as string), `sbt:cat`
(category index), `sbt:state`; row groups carry `sbt:row`, `sbt:sect`;
category metadata carries `sbt:index/name/color/price/currency/sold/used`.
Status codes: `0 INACCESSIBLE, 1 AVAILABLE, 2 PRE_RESERVED, 3 RESERVED,
4 OCCUPIED, 5 REFUND`. Internal mapping: `blocked→0`, `available→1`,
`held→3`, `sold→4` (2 and 5 are reserved for future flows and never
emitted in this wave).

## 7. Waves and features

Suggested ordering: SEAT-A → SEAT-B → SEAT-C → SEAT-D → SEAT-E. Each
feature = migrations/code + OpenAPI + tests green before the next
starts. Every mutating endpoint: idempotency-key support, audit event,
permission check (Definition of Done, `03_SPECIFICATION_STARTER.md`).

### Wave SEAT-A — seating plans core (static geometry)

**SEAT-A1. Migration 0057 + gen queries.** Tables and permission seeds
from §5.1; canonical SQL in `queries/seating_plans.sql`; hand-written
gen additions matching `bank_accounts.sql.go` style. Migration tests
(sequential numbering, timestamptz gate) pass.

**SEAT-A2. Geometry model + SVG importer (pure Go, no HTTP).** Package
`apps/backend/internal/domain/seating` (or `internal/platform/seatinggeom`
— follow where pure domain logic lives today, e.g. `domain/inventory`):
types for the §5.3 JSON, canonicalization + checksum, and the §6 SVG
parser/validator built on `encoding/xml`. Table-driven tests: the full
rule list of §6, one fixture per error class, and the **Palác Akropolis
acceptance fixture** — copy `06_venue_maps_and_seating/Palac_Akropolis.svg`
to testdata; import must yield exactly **279 seats**, sections
`Parter`, `Balcony left`, `Balcony center`, `Balcony right`, **15
categories**, zero validation errors, and a stable checksum across two
runs (determinism).

**SEAT-A3. hseating sub-package: plan CRUD + versions + fork.**
Endpoints (all under the existing auth middleware, RBAC per §5.1
permissions):
- `GET/POST /v1/venues/{venue_id}/seating-plans`
- `GET/PATCH /v1/seating-plans/{id}` (PATCH: name/status/visibility;
  archive via status)
- `POST /v1/seating-plans/{id}/fork` (guardrail #13; copies latest
  version, records `source_seating_plan_id`)
- `POST /v1/seating-plans/{id}/versions` — body either
  `{"svg": "<data-uri or raw SVG string>"}` or
  `{"geometry": {…}}`; runs the importer/validator; response includes
  per-element validation errors on 422
- `GET /v1/seating-plans/{id}/versions/{n}` — full geometry
Wire `SeatingQueries` through `wire.go`/`server_struct.go`, mount in a
new `mount_seating.go`, shims in `seating_shims.go` (≤400 lines),
OpenAPI paths+schemas (3.1, all descriptions), drift harness wiring,
audit events `seating_plan.created/updated/forked/version.created`.

### Wave SEAT-B — session binding & seat inventory

**SEAT-B1. Migration 0058** (§5.2) + gen queries for `session_seats`,
`reservation_seats`, ticket seat columns.

**SEAT-B2. Bind a plan version to a session.**
`POST /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seating`
with `{seating_plan_version_id, admission_mode, category_tier_map:
{"1": "<tier_uuid>", …}}`:
- permission `event_session.assign_seating_plan`;
- validates every category index is mapped to an existing tier of that
  session (or auto-creates tiers from category name/price_hint when
  `"auto_create_tiers": true`);
- **materializes `session_seats`** (one row per seat, status
  `available`, tier from the map) in a single transaction;
- sets `locked_at` on the version (first use);
- recomputes `sessions.capacity_total` = seated capacity (reuse the
  documented capacity-propagation hook from `0016_sessions.sql:58`);
- rebinding is allowed only while the session has zero
  reservations/tickets; otherwise 409.
- GA sessions are untouched: `admission_mode='general_admission'` keeps
  the `inventory_ledger` path exactly as-is.

**SEAT-B3. Public schema + status endpoints** (performance doc
contract, small-venue tier):
- `GET /v1/event-sessions/{id}/schema` — geometry + category→tier/price
  resolution; strong ETag = `geometry_checksum`; `Cache-Control:
  public, max-age=86400, immutable` (new version ⇒ new checksum);
- `GET /v1/event-sessions/{id}/seat-status` — compact snapshot
  `{status_version, seats: {"<seat_key>": "available|held|sold|blocked"}}`;
- `GET /v1/event-sessions/{id}/seat-status?since_version=N` — delta:
  only rows with `status_version > N` plus the new `status_version`.
Both unauthenticated for published sessions (same visibility rules as
the public feed), included in the public feed session payload as
`schema_url`/`seat_status_url` when `admission_mode != 'general_admission'`.

**SEAT-B4. Manual seat open/close (operator control).** Bil24 parity:
an operator can close individual seats or whole rows/sectors for sale
and reopen them per session (tech seats, camera platforms, blocked
sightlines, house holds).
- `PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seats`
  with `{"action": "block"|"unblock", "seat_keys": [...]}` and/or
  `{"sectors": [...], "rows": [{"sector": ..., "row": ...}]}` selectors
  (selectors expand server-side; response reports per-seat outcome).
- Transitions allowed: `available → blocked` and `blocked → available`
  only. Seats currently `held` or `sold` are rejected per-seat (listed
  in the response as skipped with a reason), never silently.
- Permission: `event_session.assign_seating_plan` holders (same
  operational role); every call emits an audit event with the seat-key
  list and actor; idempotent (re-blocking a blocked seat is a no-op
  success).
- Blocked seats surface as `blocked` in seat-status endpoints, map to
  BSS `0 INACCESSIBLE` in the Bil24 gateway, are excluded from
  availability counters, and cannot be reserved (409
  `reservation.seats_conflict`).
- Capacity semantics: blocking does NOT shrink
  `sessions.capacity_total` (it is a sales hold, not a capacity
  change); available-count metrics derive from live status.

### Wave SEAT-C — commerce path with seats

**SEAT-C1. Seat reservations.** Extend `POST /v1/reservations`
(hcheckout): request gains mutually-exclusive `seats: ["<seat_key>",…]`
alongside `quantity` (validation: exactly one of them; `seats` requires
a seated session, `quantity` a GA session — hybrid sessions accept
both). Seated path: transactional hold per §5.2 concurrency contract,
writes `reservation_seats`, sets `session_seats.status='held'` +
`reservation_id`, bumps versions. Deterministic 409 body lists
conflicting seat keys (`reservation.seats_conflict`). Reservation
expiry/release (existing TTL worker path in
`hcheckout/reservation_processor.go`) must release seats back to
`available` in the same transaction that expires the reservation.
Idempotency-key behavior identical to the GA path.

**SEAT-C2. Checkout & pricing with seats.** Checkout over a seated
reservation prices each seat by its `tier_id` (sum of per-seat tier
prices; promo/fees pipeline via the existing
`hcheckout.ComputePricing` — extend to a multi-line breakdown, one line
per tier group). Confirm transitions seats `held→sold`; abandon/expire
releases them. State machine additions covered by tests (double-sell
attempt, expiry race, partial-conflict rollback).

**SEAT-C3. Tickets carry seats.** One ticket per seat with
`seat_key/sector/row/number` denormalized; PDF (`delivery/pdf`) and
email templates render "Sector / Row / Seat" lines for seated tickets
(all locales in `delivery/templates/`); GA tickets unchanged. Webhook
payloads for ticket lifecycle events include the seat fields (additive,
non-breaking).

### Wave SEAT-D — integrations

**SEAT-D1. Bil24 gateway real seats** (`hbil24`): for
`admission_mode != 'general_admission'` sessions `GET_SEAT_LIST`
returns one object per seat `{seatId: session_seat.id AS STRING
(ADR-005), price, sector, row, number, status: BSS code (§6),
categoryPriceId: tier UUID}`; gzip strongly recommended in handler docs.
GA sessions keep the current tier-facade behavior. `RESERVATION`
accepts `seatList` (mapped to SEAT-C1), keeps `categoryList` for GA.

**SEAT-D2. Bil24 `GET_SCHEMA`**: returns seat coordinates
(seatId→x,y) from geometry, joinable to `GET_SEAT_LIST` by seatId —
same split the legacy API used.

**SEAT-D3. BSS SVG export**: `GET /v1/event-sessions/{id}/layout.svg`
renders the §6 BSS-compatible SVG (geometry + decor + live `sbt:state`)
so legacy Bil24 widgets can consume the scheme unmodified.

### Wave SEAT-E — admin UI (import-first, minimal)

**SEAT-E1.** Venue drawer in `apps/admin-web`: "Seating plans" tab —
list plans, upload SVG (client sends raw SVG to SEAT-A3 versions
endpoint), show validation errors per element, preview the imported
scheme (render geometry JSON to SVG client-side), fork/archive actions.
**SEAT-E2.** Session editor: admission-mode selector, plan-version
picker, category→tier mapping table (with auto-create option), seat
counters.
**SEAT-E3. Interactive seat management on the session.** Render the
session's seat map (geometry JSON → SVG client-side, colored by live
status: available / held / sold / blocked, with the category legend).
Operator interactions backed by SEAT-B4: click a seat to toggle
block/unblock; multi-select via sector/row picker for bulk block;
skipped seats (held/sold) are reported inline. Read-only live counters
per sector and per category. This screen is the operational
"обменка"-equivalent: prices are edited on the mapped ticket tiers
(existing tier editor), seat availability is edited here, and category
layout changes are done by importing a new plan version (re-colored
SVG) — not by per-seat repainting (that is visual-editor scope, out of
this wave).
All three screens respect the Wave M responsive rules for organizer
presets.

## 8. Out of scope (this wave)

- Visual seat-map editor (drawing/moving seats) — explicitly deferred
  again; import-first only.
- Waiting room / queue, seat-status Redis cache, bitset encodings,
  MessagePack/SSE/WebSocket deltas — the schema above must not preclude
  them (`status_version` delta contract is the seam), but none ship now.
- `tables` and `standing_zones` selling logic (schema fields exist,
  plans of type `tables`/`mixed` can be imported but not yet sold).
- Seat-level external allocations (ADR-016 keeps quotas quantity-based).
- Migration of live Bil24 sales data.

## 9. Acceptance for the whole wave (Definition of Done)

1. `Palac_Akropolis.svg` imports with zero errors: 279 seats, 4
   sections, 15 categories; checksum deterministic.
2. Full API E2E (no UI needed): create plan → upload SVG version → bind
   to a session with a category→tier map → public schema + seat-status
   endpoints serve it → reserve 2 specific seats → concurrent duplicate
   reservation gets 409 with the conflicting keys → checkout + payment
   (test provider) → 2 tickets issued each carrying sector/row/number →
   seat-status shows them `sold` → refund releases per refund policy.
3. Reservation expiry returns seats to `available` (TTL worker test).
3a. Operator blocks a row via SEAT-B4 → seats show `blocked` in
   seat-status and BSS `0` in the gateway → reservation attempt on a
   blocked seat gets 409 → unblock restores sale; blocking a sold seat
   is rejected per-seat and audited.
4. `GET_SEAT_LIST`/`GET_SCHEMA`/`RESERVATION(seatList)` in the Bil24
   gateway behave per §6/§7 for a seated session; GA sessions unchanged.
5. Existing full test suite stays green: `go test ./...`,
   `golangci-lint` 0 issues, gofmt clean, OpenAPI drift both directions,
   file-size gate with an EMPTY allowlist.
6. All new mutations emit audit events and honor idempotency keys.
