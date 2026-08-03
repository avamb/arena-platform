# Wave 4 Runbook — how to actually run this wave

**Read this first in any new session.** It is self-contained: it says what is decided, what
is done, what is next, and what must not be done. Full feature specs live in
`09_autoforge/admin_bootstrap_backlog.md`, section *"Wave 4 — reconstruction alignment"*.

---

## The governing rule

The strongest model (**Fable 5**) is available **only in the interactive session**.
AutoForge cannot use it. Therefore this wave is **not** a one-time split of work — it is an
**alternation**:

> hardest passes here (Fable 5) → hand bounded breadth to AutoForge → come back → repeat.

**Never hand an interactive pass to AutoForge.** Those are the migrations that touch nearly
every catalog query, plus the inventory and money paths, where an error is silent and
expensive. `import_wave4_reconstruction_features.py` deliberately queues only the AutoForge
half; do not "complete" the wave by adding the rest to the queue.

Assignment principle, if a new feature ever needs placing: **blast radius and
reversibility**, not apparent difficulty. Interactive takes what fails silently and
expensively; AutoForge takes what fails visibly and locally.

Quality bar is **production, not MVP** (owner, explicit).

---

## Status board

| Pass | Where | Features | Status |
|---|---|---|---|
| 1 | interactive (Fable 5) | AB-36, AB-37, AB-38 + `blocked→unavailable` rename | **NOT STARTED — do this first** |
| 2 | AutoForge | AB-42, AB-47, AB-43, AB-44, AB-46 | queued in script, **not imported yet** |
| 3 | interactive (Fable 5) | AB-40 A/B/C, AB-51 | not started |
| 4 | AutoForge | AB-39, AB-40D | in script, not imported |
| 5 | interactive (Fable 5) | AB-49, AB-48, AB-41 | not started |
| 6 | AutoForge | AB-50, AB-45 | in script, not imported |

Keep this table current — it is the one place a new session learns where things stand.

Baseline at the time of writing: repo at `fe0e445`, stand deployed on `01eeafb`,
**migration head 0078**, **queue head (423, 533)**.

---

## Step 0 — import the AutoForge queue (once, before pass 2)

```bash
python 09_autoforge/import_wave4_reconstruction_features.py
```

Idempotent; refuses to insert if the queue head is not `(423, 533)` and backs the DB up
first. If it refuses because something else was queued meanwhile, **fix `EXPECTED_HEAD`
deliberately** — do not delete the guard.

Importing early is harmless (AutoForge is launched by hand), but AutoForge must not be
*run* until pass 1 has landed.

---

## Pass 1 — interactive, do this before anything else

Everything downstream is written against the model this pass creates. Getting it wrong
means rewriting passes 2 and 4.

**Scope:** migrations `0079`, `0080`, `0081`, plus the seat-status enum rename.

1. **0079 (AB-36)** — `sessions.venue_id NOT NULL`, `sessions.capacity_override`; drop
   `events.venue_id`. Seating bind moves into session *create*, not only edit. Capacity
   resolution: bound plan version → `capacity_override` → `venues.capacity_default`.
2. **0080 (AB-37)** — drop `events.start_at/end_at`; add `first_session_at` /
   `last_session_at` maintained by trigger; rewrite the list filters and ordering onto
   them; `firstEventDate` in the Bil24 gateway reads `first_session_at`
   (`hbil24/bil24_compat.go:297` currently reads `events.StartAt`).
3. **0081 (AB-38)** — `countries.currency`, `cities.currency_override`,
   `sessions.currency` + `sessions.currency_source` (`derived` | `override`); constrain
   `ticket_tiers.currency` to equal its session's; ISO-4217 CHECK everywhere.
4. **Enum rename** — `session_seats.status` value `blocked` → `unavailable`, in the CHECK,
   every query, the OpenAPI schema, the TS client and the admin UI, **in one commit**. It
   is folded into this pass so the CHECK is touched once, not twice.

Stand data is disposable — migrations may be destructive, no backfill needed.

**Done when:** the gate below is green and the stand is redeployed and smoke-tested.

---

## The gate — run between EVERY pass, no exceptions

Go is not on PATH on this host; prefix every Go command:
`$env:PATH = "C:\Program Files\Go\bin;$env:PATH"` (PowerShell).

- `go test ./...` — full suite, 4+ min. Must be green, not "green except".
- `golangci-lint` over `apps/backend` — **including gofmt**.
- `npm run admin:test`, `npm run type-check` (**not** `check-ts`), admin build.
- `test:mobile` (Playwright viewport smoke).
- Codegen drift check: `go run ./apps/backend/tools/openapi30gen openapi.yaml .compat30.gen.yaml`
  then oapi-codegen per `oapi-codegen.yaml`, plus `node scripts/gen-ts-client.mjs`.
  Remove the compat file after. `make` is not available on this host.
- Migration-head pin in tests bumped to the new head.
- Clean working tree, pushed.
- **CI verified with `gh run view <id>`** — never trusted from a commit message. Wave
  AB-28..AB-35 was pushed claiming "423/423, all green" while CI Lint was red on gofmt.

---

## Running an AutoForge pass

1. Confirm the preceding interactive pass is merged and its gate was green.
2. Launch AutoForge (owner does this by hand, as in previous waves).
3. Run **3–4 features, then stop and review** — do not run a whole pass unattended. This
   wave is almost entirely model work; drift compounds.

### Review protocol after every AutoForge pass

Four failure classes, all observed in waves 1–3. Green tests do **not** catch them:

1. **Fabrication.** A feature marked passing with zero lines of code (#411, wave 2). Check
   first, every time: `git log <base>..HEAD`, grep the range for the feature's keywords, and
   confirm a `claude-progress.txt` entry exists.
2. **No push.** The agent stops at a local commit and CI never sees the work.
3. **Repo-wide gates not run.** Package tests green while routes bypass `openapi.yaml`,
   Go/TS codegen drifts, the migration-head pin is stale, guardrail tests fail, gofmt/revive
   complain.
4. **Wrong CI environment assumptions.** A DB test without the `integration` build tag
   passes locally and fails in the Unit job (which has `DATABASE_URL` but no migrated
   schema). Playwright needs `VITE_API_BASE_URL` in the config env.

Then diff each delivered feature against its spec section in the backlog.

---

## Decisions already made — do not relitigate

These were settled with the owner against the Bil24 source material. A fresh session will be
tempted to reopen some of them; don't.

- **Four structural deviations are the subject of this wave**: time and venue belong to the
  session, not the event; price must link to a seating zone; currency follows venue
  geography. Evidence is quoted in the backlog preamble.
- **No visual seating-plan editor.** Schemes stay authored in Inkscape to the Bil24 SVG
  convention and imported (owner decision Q65, 2026-07-10). Assigning a *price category to
  each seat* is mandatory and is a different thing.
- **One order = one SESSION.** The schema already enforces it
  (`checkout_sessions.reservation_id` and `reservations.session_id` are single NOT NULL
  FKs). Leave it — it is not a gap to close.
- **GA places get real identity** — one row, one id per place, same status machine as
  assigned seats.
- **Posters bind to the session**, deliberately diverging from Bil24.
- **Cancellation drives inventory, not the refund.** Cancelling a ticket frees the seat and
  notifies MACS immediately, regardless of whether money moved. Money is a separate optional
  consequence (`automatic` / `manual` / `none`) and must never gate the seat release.
- **"Refunded" is the deliberate word at the door.** Do not "correct" it to
  *invalid* / *void* / *cancelled* anywhere on the admission path.
- **Partial inbound refunds escalate**, never auto-allocate. The review hold **flags, never
  blocks admission**, and is never sent to MACS as status 3.
- **The internal scanner is not the product.** MACS is; the platform feeds it. Check-in /
  check-out is MACS's concern.
- **MACS is an MVP we do not touch.** Build behind one adapter boundary, do not compensate
  for its defects, do not engineer permanently around its int-id limitation.
- **`sold → unavailable` is forbidden.** A sold seat must be refunded to `available` first.
  Implement transitions as conditional UPDATEs so illegal ones are impossible in SQL.

---

## Remaining open question

Only one, and it is small: `holderStatus` display values are known from the MACS source
(integer `0/1/2/3`), but if a *new* status is ever needed the enum must come from the MACS
team — do not invent values.

---

## Pointers

- Full specs: `09_autoforge/admin_bootstrap_backlog.md` → "Wave 4 — reconstruction alignment"
- Import script: `09_autoforge/import_wave4_reconstruction_features.py`
- Repo conventions that have cost time before: `AGENTS.md`
- Bil24 source material: `01_official_bil24_docs/`, `04_legacy_screenshots/`,
  `06_venue_maps_and_seating/`, `08_architecture/`
- MACS source (read-only reference, supplied by the owner):
  `C:\Projects\macs-arenasoldout-com\backend-develop.zip`
