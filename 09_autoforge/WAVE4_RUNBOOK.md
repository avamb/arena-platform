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
| 1 | interactive (Fable 5) | AB-36, AB-37, AB-38 + `blocked→unavailable` rename | **DONE 2026-08-04** — commits a8fae78 + 87ddc20 + aa7acfb; migrations verified live (78→81: venue/currency backfill, 0080 trigger, tier-currency cascade + mismatch rejection); **CI run 30891031484 fully green** (`gh run view`), image published by the Docker job. **Stand redeployed 2026-08-04 on aa7acfb**: migrate 78→81 clean, api/worker healthy, backfill verified in the stand DB, admin rebuilt from Git (bundle carries first_session_at/venue_names/capacity_override). Follow-up a7e79a5 (CI run 30905497551 green): Geo Registry country create/update now carries the ISO-4217 currency (was broken by 0081's NOT NULL); stand geo data fixed live — CZ=CZK, Prague added, Palac Akropolis linked, its session re-derived to CZK with the tier following via the FK cascade. a7e79a5 is NOT yet on the stand — deploy together with pass 2 |
| 2 | AutoForge | AB-42, AB-47, AB-43, AB-44, AB-46 | **implemented 2026-08-04, REVIEW IN PROGRESS** — all 5 features passes=1; 6 local commits on top of bfdea96 (8322863 AB-43, 1b8ecf6 AB-46, c3cb458 docs, 80cc281 AB-42 publish gate, 5f4318f AB-47 + migration 0082 + head-pin bump, 0b5adf6 AB-44). **NOT PUSHED.** See 'Pass 2 review — handoff state' below for exactly what remains |
| 3 | interactive (Fable 5) | AB-40 A/B/C, AB-51 | not started |
| 4 | AutoForge | AB-39, AB-40D | in script, not imported |
| 5 | interactive (Fable 5) | AB-49, AB-48, AB-41 | not started — NOTE: the `blocked→unavailable` rename part of AB-49 already landed with pass 1 (folded into 0081) |
| 6 | AutoForge | AB-50, AB-45 | in script, not imported |

Keep this table current — it is the one place a new session learns where things stand.

Baseline at the time of writing: repo at `fe0e445`, stand deployed on `01eeafb`,
**migration head 0078**, **queue head (423, 533)**.
After pass 1: **migration head 0081**. Pass-1 implementation notes a fresh
session may need:
- Seat-status wire surface: statuses are `available|held|sold|unavailable`;
  PATCH seats **action** values stay `block`/`unblock` (verbs), outcome enum
  is now `unavailable|available|noop|skipped`.
- Session create runs the SEAT-B2 bind inline for assigned_seats/hybrid
  (hseating.BindSeatingForSessionCreate via a callback injected in
  catalog_shims.go); tiers are auto-created per SVG category in the session
  currency.
- Tier `currency` is no longer an operator input anywhere; the composite FK
  `ticket_tiers_currency_matches_session` (ON UPDATE CASCADE) enforces
  one-currency-per-session at the DB level.
- Events API: `first_session_at`/`last_session_at` (trigger cache) +
  `venue_names[]`; public feed events follow the same shape.

---

## Pass 2 review — handoff state (2026-08-04, mid-review)

AutoForge ran features 424–428 in two batches (a UI/server restart killed the
first #424 attempt mid-flight; its dangling diffs were folded into 8322863 and
finished by 80cc281 — the agent documented this honestly in claude-progress.txt).

**Review done so far:**
- Fabrication check PASSED: every feature has a real diff (stats: 8322863
  events.tsx +1099 incl. the AB-42 EventWizard UI; 1b8ecf6 +926 lines of
  domain tests across 8 packages; 80cc281 +56 publish gate; 5f4318f AB-47
  incl. migration 0082 + mediastore allowlist + head pin → 0082; 0b5adf6 +34).
- claude-progress.txt entries exist for every feature; #429 correctly
  NOT ATTEMPTED (gate #433 worked — agent released it).
- Migration 0082 read and sane: `session_poster` added to the media
  owner_type CHECK, `sessions.poster_media_id` FK with resolution order
  session ?? event ?? none.
- Gates already green on the local tree: `go test ./...` exit 0 (54 pkgs ok);
  admin type-check + 1095/1095 tests + build + test:mobile (chain exit 0).

**Review REMAINING (do these before push):**
1. Spec-vs-diff AB-42: inspect the EventWizard in events.tsx (inside commit
   8322863) against backlog steps 1–5 — three steps, capacity read-only +
   currency-derived display, resumability ("reopening a draft lands on the
   first incomplete step"), and the publish-gate error codes in
   events.go (80cc281): refuse publish without session / without priced tier.
2. Spec-vs-diff AB-47 step 4: poster resolution must reach the PUBLIC FEED,
   the WIDGET and ticket PDF/email. The 5f4318f diffstat did not obviously
   touch apps/widget — verify the full stat (`git show --stat 5f4318f`) and
   close the gap or record it as follow-up. Also confirm
   `TestAllowedOwnerTypes_MatchMigrationCheckConstraint` was extended and the
   WordPress contract doc update (spec step 4) — likely NOT done.
   AB-47 spec step 5 is an OPEN OWNER QUESTION (two image slots) — do not
   guess, ask.
3. Spec-vs-diff AB-44: commit is only +34 lines vs 4 spec items (modal
   dismiss/Escape+confirm via ResponsiveDrawer, venue column name+number,
   pricing-mode help, Activity tab) — check which items actually landed.
4. golangci-lint (`GOLANGCI_LINT_CACHE=.golangci-cache go run
   github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run` from
   apps/backend) + gofmt.
5. Codegen drift check (openapi30gen + oapi-codegen + gen-ts-client, then
   `git status` must stay clean).
6. Push, CI via `gh run view`, THEN redeploy the stand (owner decision:
   stand updates only after pass 2; it is still on aa7acfb and the a7e79a5
   geo fix is not deployed either — one deploy covers both, same procedure:
   Raw-compose tag ×4 + admin Deploy from Git).

**AutoForge queue state:** 424–428 passes=1; 429/430 blocked by human-gate
feature #433 (interactive pass 3), 431/432 by #434 (pass 5). The gates are
features with needs_human_input=1 — resolve #433 (boolean confirm) only after
pass 3 lands. NOTE: gate rows' `human_input_request` column MUST be valid
JSON ({prompt, fields[]}) — a raw string there 500s the whole Kanban API
(learned the hard way).
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
