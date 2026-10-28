# Seating, GA inventory and holds

Places, categories, hold and quota lock order, plan geometry, seat layouts, venue generator, seated bot and web screens. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **A GA ticket carries the seat_key of its ga_unit (`ga|pool|000003`),
  so `tickets.seat_key != NULL` does NOT mean an assigned seat.** Since
  AB-51 issuance stamps the unit key on every GA ticket; the cancellation
  release used to send any seat_key to `ReleaseSoldSessionSeat`
  (`kind='seat'` only) and every GA ticket sold through a site failed with
  `ticket.release_failed` (found live 2026-09-13). Branch on the `ga|`
  prefix and release that exact unit with `ReleaseSoldGAUnitBySeatKey`; the
  reservation-scoped `ReleaseSoldGAUnitForReservation` is only for legacy
  NULL-seat_key tickets. Integration fixtures that insert GA tickets with a
  NULL seat_key test the legacy shape, not the real one — add the stamped
  variant too (`TestAB49Integration_GAUnit_ReleaseByTicketSeatKey`).

- **`reservation.expire_sweep` now releases TTL-expired reservation holds —
  this was a live production defect until the fix.**
  `hcheckout.ReservationProcessor.ProcessExpiredReservations` existed since
  feature #131 but nothing in arena-api or arena-worker ever called it; a
  buyer who reserved and walked away left `reservations` (draft/active),
  `session_seats`/`ga_unit` rows (status held) and
  `inventory_ledger.capacity_held` stuck forever, showing a session as sold
  out with nothing sold. `internal/platform/reservationexpiry` is the
  self-scheduling worker job (cadence 30s, batch 500, registered in
  `cmd/arena-worker/main.go` next to `order.expire_sweep`) that finally
  drives it. `order.expire_sweep` (`internal/platform/ordering`) only closes
  the order aggregate — it never released inventory, despite an old comment
  in that file claiming otherwise. The audit also found the TTL processor's
  capacity-release branching had silently drifted from `ReleaseHold`'s (it
  was missing the legacy-GA-lines branch); both now share
  `releaseHoldCapacityTx` (`hcheckout/hold_api.go`) so the two release paths
  cannot diverge again. Any FUTURE new hold shape (a new `session_seats.kind`,
  a new capacity-accounting scheme, etc.) must stay releasable by
  `expireReservation` (`hcheckout/reservation_processor.go`) — mirror
  whatever `ReleaseHold` does for it, and extend the shared helper rather
  than hand-rolling a second branch.

- **Every hold-mutation transaction must lock `sessions` (the
  `seat_status_version` bump) BEFORE it touches `inventory_ledger`
  (`ReserveCapacity`/`ReleaseCapacity`/`ConfirmCapacity`), never the other
  way round.** `CreateGAHold` (`hcheckout/hold_api.go`) used to reserve
  capacity first and bump the version second — the opposite order from
  every sibling primitive (`CreateSeatedHold`, `ExtendHoldTx`,
  `ShrinkHoldTx`, `ReleaseHold`, `ConvertReservationInTx`) — so a buyer
  opening a brand-new GA cart via CreateGAHold deadlocked (Postgres
  `40P01`) against a buyer extending/shrinking an *existing* cart on the
  same session; the same inversion existed independently in
  `HandleCreateReservation`'s GA branch (`hcheckout/reservations.go`, the
  plain REST `POST /v1/reservations` quantity path). Both were fixed by
  reordering: version bump, THEN `ReserveCapacity`/`ReleaseCapacity`. See
  the lock-order comment on `hcheckout.createGAHoldTx`. Because two
  DIFFERENT reservations can still legitimately contend for the two locks
  even with the order fixed everywhere, `hcheckout/retry.go` adds
  `retryOnSerializationFailure` (3 attempts, 10-50ms jittered backoff,
  ctx-aware) wrapping the whole transaction body of every hold-mutation
  entry point (`CreateGAHold`, `CreateSeatedHold`, `ReleaseHold` directly;
  `ExtendHold`/`ShrinkHold`/`ReacquireHold` via the shared `inHoldTx`
  helper) — a retry MUST restart the whole transaction, never continue
  inside one Postgres already aborted. `hbil24`'s `writeCartHoldError`
  default branch (and CREATE_ORDER_EXT's hold-failure path, which routes
  through the same function) answers Bil24 resultCode **-1** (transient,
  retried by the WordPress plugin) for a Postgres error of a retryable
  SQLSTATE class — 40 (deadlock/serialization, incl. one that survived the
  retry), 08, 53, 57 (`isRetryablePgError`) — rather than **-99**. Integrity
  and data errors (23xxx, 22xxx) stay -99: they repeat on every retry and
  would make the plugin loop. Any NEW code opening a transaction that touches BOTH
  `inventory_ledger` and `sessions.seat_status_version` must follow the
  same order and go through (or mirror) `retryOnSerializationFailure`.

- **On a plan-less GA session a `ga_unit` row's `tier_id` is a sale stamp,
  not ownership.** `AllocateGAUnitsTx` stamps the line tier onto the NULL-tier
  pool units it holds and releases reset them to NULL, so a tier's own rows
  are exactly its held and sold units. Any per-tier availability must count
  the unbound pool (capped by `ticket_tiers.capacity` minus the tier's
  held+sold), never "available rows with this tier". `GET_SEAT_LIST` and
  `GET_ALL_ACTIONS` both got this wrong and showed every category that had
  sold one ticket as sold out while 56 pool units were free (staging,
  2026-09-14). The pool itself is being replaced by per-category quotas like
  Bil24 — plan `08_architecture/23_ga_category_quotas_plan_ru.md`; the WP site
  caches category availability in product meta, so re-run the catalog sync
  after a fix before judging the page.

- **Since migration 0101 a GA place with `tier_id IS NULL` is UNSELLABLE.**
  A General Admission category OWNS its places (plan
  `08_architecture/23_ga_category_quotas_plan_ru.md`): they are the
  `session_seats` rows of `kind='ga_unit'` carrying its `tier_id`, keyed
  `ga|t<ticket_tiers.unit_seq>|<n>`, and `AllocateGAUnitsForHold` filters on
  `tier_id = $tier` with no NULL fallback. Any fixture that still seeds the
  pre-0101 fungible `ga|pool|<n>` batch with a NULL tier produces a session
  that answers "sold out" on every surface — seed a `ticket_tiers` row (with
  `capacity`, `unit_seq`, `is_open`) and stamp its id on the places instead.
  Two production paths still create the dead NULL-tier pool and are marked
  KNOWN GAP in the source until plan steps 3/6 land: `hcatalog/sessions.go`
  session create and capacity edit. A closed category (`is_open=false`) or
  one outside `sale_window_start/end` refuses every NEW hold through
  `hcheckout.CheckCategorySellable` and reports availability 0 on the
  gateway wire (there is no "closed" flag in the protocol); an order that
  already exists is unaffected — `ReacquireHoldTx` and PAY_ORDER deliberately
  skip the gate.

- **TWO cascade-less FKs point at `session_seats`, not one:
  `reservation_seats.session_seat_id` AND `order_items.session_seat_id`.**
  Every "delete a free GA place" path must exclude both, or it dies with
  23503. The dev database has 32 AVAILABLE `ga_unit` rows across 6 sessions
  referenced by `order_items` (and 0 referenced by `reservation_seats`), so
  a guard that only checks `reservation_seats` — as the GA-quota plan
  originally specified — passes every hand-written query and then fails on
  real data. Guarded in `queries/ga_quota.sql`
  (`CountDeletableGAUnitsForTier` / `DeleteAvailableGAUnitsForTier`) and in
  migration 0101's conversion block. The pre-existing
  `DeleteAvailableGAPoolUnits` still checks neither.

- **A quota/inventory mutation must take the `sessions` row lock
  (`IncrementSessionSeatStatusVersion`) as its literally FIRST statement,
  before the reads it decides from — not just before the writes.** Under
  READ COMMITTED, reading the per-category place counts and then bumping
  the version lets two concurrent editors both decide from the same stale
  counts and write a quantity the places no longer match (seen 2026-09-14:
  60 stated against 70 real places in
  `TestGAQuota_SetQuantityVsGAHold_NoDeadlock`). The bump is both the lock
  order step 1 AND the serialization point; a later `return err` rolls it
  back with the rest of the transaction, so taking it early costs nothing.

- **A seat's cx/cy in a plan SVG is NOT its position — the ancestors'
  `transform` is.** Inkscape sources and the sbt plan Bil24 serves place a
  whole row with a transform on its `<g>` (all 90 seats of Palác Akropolis
  share one `cy`). Both importers (`seating.ImportSVG`, `ImportSBTSVG`)
  ignored transforms until 2026-09-21 and arena — which renders seats from
  the stored geometry — drew the hall as one line of stacked seats; nobody
  noticed because no test looked at coordinates. `seating/transform.go`
  (`resolveTransforms` + `placeCircle`) now resolves every seat and GA
  polygon to root coordinates; ground truth for a Bil24 plan is its own
  `GET_SCHEMA` (x/y per seatId). The harness golden
  `tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg` is rendered
  THROUGH the importer, so a geometry change means regenerating it
  (`ARENA_REGEN_SBT_GOLDEN=1`, then put the disclaimer comment back).

- **A chain of categories hands free places on; never re-mint a chain head.**
  `ticket_tier_chain` (migration 0112) links a category to the next one;
  `tier.chain_sweep` (`internal/platform/tierchain`, every 30 s) runs
  `gaquota.HandOver` for every link whose source `sale_window_end` passed:
  free, unreferenced-by-`reservation_seats` GA places are RE-TIERED and
  re-keyed (rows kept, so a place an expired order's `order_items` points at
  moves too), both capacities are re-synced, and on the FIRST hand-over the
  source closes and the target opens. The session's place count never
  changes. In an event-bundle, `categoryList[].nextCategoryIndex` sets the
  link (-1 removes, absent keeps), a successor declared with availability 0
  starts with NO places and closed (no `import.category_sold_out`), and the
  head's availability means the WHOLE chain's places — a repeat import
  applies the difference to the member selling now (`chainIndex.selling`).
  Re-applying it to a handed-over head would grow the hall. Judge the head by
  the WHOLE chain's places (`chainPlaces`), never its own: a head that sold
  nothing owns 0 places after the hand-over, and a repeat import that fell
  through to the new-category branch minted it a fresh set every save
  (staging 2026-09-28, 30 became 60 — `TestEventBundleChain_ReSaveAfterHeadHandedOverEverything`). Seated places
  never move; a seated target is refused (`ErrSeatedCategory`) — seats use
  `priceSchedule` instead.
  **Quantity steps (migration 0114):** `ticket_tier_chain.sell_limit` makes
  the selling category own exactly that many places; the rest of the hall
  waits in the next, CLOSED category (`gaquota.RebalanceStep`, run by
  `RebalanceSession` after every arena import and by `HandOver` for the new
  selling category). The sweep hands over when the date passes OR the step
  has no free place left (sold + held = limit), whichever comes first. The
  chain's place count is still the sum over all members, so a re-save keeps
  the hall. Bundle `sellLimit`: absent keeps, `0` removes, positive needs a
  `nextCategoryIndex` (422 `import.invalid_sell_limit`). A removed limit
  pulls the parked places back into the selling category.

- **`apps/venue-svg-generator` is a Python tree brought in by `git subtree add` (G1, 2026-10-14), and it has three traps.** (1) `git log --oneline apps/venue-svg-generator` shows ONE commit: the subtree merge rewrites the path, so history simplification finds no parent with the same tree there. The upstream history (18 commits, authorship kept, no `--squash`) is `git log e6263d7^2` (second parent of the merge) or `git log --follow` on one file; never "fix" it by re-adding the folder. After a subtree add, merge `origin/master` into the branch, never rebase: a rebase flattens the merge and the second parent is gone. (2) Upstream `.gitignore` (now `apps/venue-svg-generator/.gitignore`) keeps `ChatExport_*`, `*/results`, `seating_poc/`, `arena_importsvg_proof/`, `*.xlsx`, `*.pdf` out of git, so a clean checkout lacks the private client data the tests read: `benchmark_v1` imports the missing `seating_poc` package (not collected, see its `pytest.ini`) and nine tests read client XLSX or frozen results (skipped by `conftest.py` through its `NEEDS_PRIVATE_DATA` list when `ChatExport_2026-10-11/` and `seating_poc/` are absent, run for real when they are present). A NEW test that needs such a file must be added to that list, or CI goes red. `arena_importsvg_proof` (the proof that the authoring SVG passes `seating.ImportSVG`) is NOT in the repository, only on the owner's machine; the M3 contract in the generator's `ROADMAP.md` was written from `internal/domain/seating/doc.go`. (3) The `venue-svg-generator` CI job decides in its first step whether the folder (or `ci.yml`) changed (`git diff` against the PR base or the push's `before`) and gates every later step on it, because a path filter on the whole workflow would switch off every other job; on an unrelated PR the job is green and does nothing. Python is pinned in `apps/venue-svg-generator/requirements*.txt` (runtime: `openpyxl`; dev: numpy, pillow, pytest), the image is `python:3.12-slim`, and the compose service `venue_gen` sits under profile `venue-gen` with NO host port (the generator has no HTTP until G2a, so its container prints help and exits, `restart: "no"`).

- **A seat's category for every buyer render is `session_seats.tier_id`, never the plan anchor (S1a, 2026-10-15).** `PATCH .../sessions/{id}/seats/category` writes a new tier per seat and leaves the stored geometry alone, so the geometry's `category_index` is only the plan's original grouping. Until S1a the widget `/schema`, the BSS `layout.svg` and the Bil24 `/compat/bil24/image` took a plan category's tier from its FIRST seat (`resolveCategoryTiers`) and drew every seat with the geometry index, so a repainted seat (or, worse, a repainted first seat that relabelled its whole category) showed the old price while `GET_SEAT_LIST` already showed the new one. All three now go through `hseating.resolveSeatCategories` (`seat_categories.go`, pure): the HOME tier of a plan category is the tier of most of its seats (ties: first in `seat_key` order) and labels the category entry; a seat with another tier takes the index of the category that is home to that tier (lowest index), and a tier that is home to no plan category gets a SYNTHETIC category appended after the plan's last index (colour from a fixed palette until `ticket_tiers.color` exists, plan 26 S1d); a seat with no resolvable tier keeps its plan category. `SBT10CategoryTierIDs` includes the synthetic ones, so `hbil24/image.go` needed no change. **Response shape (additive, geometry stays byte-for-byte stored):** `/schema` gains `seat_category_index` (seat_key -> effective category index, ONLY for seats that left their plan category) and `extra_categories` (the synthetic ones, `category_prices` shape); `category_prices` keeps one entry per plan category. The widget folds both in once, in `fetchSessionSchema` (`lib/seatCategories.ts` `applySeatCategoryOverlay`: rewrites `seat.category_index`, appends the extra categories), so seat colours, price labels, the cart and quick-pick all read one number; an older server sends neither field and nothing changes. The strong ETag already contains `seat_status_version`, which the PATCH bumps, so caches revalidate. Do not add a fourth place that reads `geometry...category_index` for a price. Test: `TestS1a_CategoryPatch_OneSeatOnePriceInAllThreeRenders` (`tests/compat/bil24/seat_category_s1a_integration_test.go`, compares EVERY seat of the hall in all three renders and fails on the old code), unit twin `hseating/seat_categories_test.go`, widget `lib/seatCategories.test.ts`. Operator query for the live check (sessions where a plan category carries more than one tier, i.e. where the old and the new render can differ): see the S1a PR description. `price_hint` of a plan category (import-time hint) still wins over `price_amount` in `buildPriceLabel`, so an aria label of a plan category that carries an import hint shows the hint, not the tier price.

- **An operator's change of a seat's category or open/closed status goes through `seatlayout.ApplyBatch` and is recorded in `session_seat_batches` (task S1c, migration 0162).** `PATCH /v1/event-sessions/{id}/seats/category` (kind `paint`, `Strict`: any seat that became held/sold rolls the call back with the old 409) and `PATCH .../sessions/{id}/seats` block/unblock (kinds `close`/`open`, held/sold skipped with a reason) are thin wrappers; the new routes are `POST|GET /v1/organizations/{org_id}/sessions/{session_id}/seat-batches`, `POST .../seat-batches/{batch_id}/revert` and `GET .../seats/{seat_key}/history` (seat_key path-escaped). One transaction: `IncrementSessionSeatStatusVersion` FIRST (the batch's only bump), `LockSessionSeatsForBatch` (FOR UPDATE in seat_key order, the order `CreateSeatedHold` uses, so a batch and a buyer's hold serialize on the sessions row and never deadlock), one conditional UPDATE that stamps the new `status_version` on every changed row (a tier change too), then the batch row with `from`/`to` per seat and the skipped seats with reasons (`sold`, `held`, `seat_not_found`, `unknown_status`, `ga_unit`, `changed_since`, `tier_unavailable`). A GA place opens and closes but never changes category. A revert is a NEW batch (`kind='revert'`, `reverts_batch_id`) that only puts back a seat still exactly as the original left it; a batch is reverted at most once and a revert is never reverted. A revert that would put back NO seat (all sold, held, changed again or their category deleted) is refused with 409 `seat_batch.nothing_to_revert` and writes nothing (no bump, no row): the original stays revertable, so reverting the later batch first and then this one works. The batch list (`GET .../seat-batches`) carries only `changed_count`/`skipped_count`; the per-seat arrays are in `GET .../seat-batches/{batch_id}`. A block/unblock whose selectors expand past `seatlayout.MaxChanges` (10 000) answers 422 `seating.too_many_seat_keys`. A new writer of `session_seats.tier_id`/`status` for operator edits must go through `ApplyBatch`/`ApplyBatchTx`, not a hand-rolled UPDATE, or history and revert lose it. The 40P01/40001 retry loop lives in ONE place, `internal/platform/txretry` (hcheckout delegates; `gaquota/retry.go` is still a copy). The routes are gated by `session.seats.manage` (S1f, migration 0161, switched at the wave-C release merge); `new_tiers` answers 422 `seat_batch.new_tiers_unsupported`. Tests: `seatlayout/seatlayout_test.go`, `seatlayout/seatlayout_integration_test.go` (mixed sold/held batch, revert, history, a close batch racing 16 holds), `httpserver/seat_batches_integration_test.go`.

- **The seat-layout allocator is a registry of algorithms, and every call goes through `layoutgen.Run` (S1e, `internal/domain/seating/layoutgen`, routes `GET|POST /v1/organizations/{org_id}/sessions/{session_id}/seat-layout/{algorithms|suggest}`).** A new algorithm is ONE file with `func init() { layoutgen.Register(x) }`: no API, component or bot change (the list route serves its `ParamSchema`). `Run` checks the output contract after `Suggest` (every free seat assigned to a declared slot, no sold or held seat assigned, 1-15 slots) and recomputes the frozen list and the metrics with `Evaluate`, so revenue means "sold by fact + held at its price + open free seats at the new prices" for every algorithm; closed (`unavailable`) seats get a slot but no revenue. Parameters are validated generically by `ParamSchema.Validate` (unknown name or out of range = 400 `seat_layout.invalid_param`); a default must be stored as its decoded Go type (`int64(4)`, not `4`) or `Register` refuses the schema. A frozen seat's price is `order_items.total` of the latest order of its CURRENT reservation, else the tier's `price_amount` (`gen.ListSessionSeatLayoutStates`, reached through `orders`, because `order_items` has no index on `session_seat_id`). `label_key`/`hint_key` are i18n keys (`layoutgen.*`) that no catalog carries yet: the client that draws the form (S2c) adds them. The suggest route writes nothing; until S1d stores `seating_plans.annotations` the stage arrives in the body, and `desirability` answers 422 `seat_layout.annotation_required` without it (the stage is never guessed).

- **A plan version's places are written in ONE place, `httpserver/seatmint.Materialize` (task S1b, 2026-10-16).** The seating bind (`hseating/bind.go`, policy `Strict`), the Bil24 import's seated half (`himports/seating.go`, `Lenient`) and the arena event bundle's seated branch (`himports/import_arena_seated.go`, `Lenient`) all call it, so seat keys, Bil24 `system_seat_id`s and the `ga|c<index>` GA-zone keys cannot drift; `seatmint.AdmissionMode`, `PlanCapacity` and `PlacedCategories` are the shared rules. A new path that binds a plan to a session must call it, never copy the `InsertSessionSeats`/`InsertGAUnits` loop. A source=arena bundle creates a seated session from `actionEvent.arenaSeatingPlanVersionId`, matching `categoryList` to plan categories by `planCategoryIndex` or name (all-or-nothing, 422 with the field path), and NEVER re-binds or touches places of an existing session (409 `import.seating_plan_version_conflict`). An integration fixture that inserts a seating plan must delete its sessions before `seating_plan_versions` and the plan before the venue (`seatedFixture.cleanup` in `event_bundle_seated_integration_test.go`): `bundle525Fixture.cleanup` alone logs FK errors and leaves the venue and organization behind.

- **The venue generator speaks HTTP since G2a (`apps/venue-svg-generator/service`, port 8090, internal network only, NO authentication, never publish the port), and its authoring SVG is proven by a Go test.** `POST /generate` (XLSX + adapter profile id from the allowlist `hunter-v1`/`aribau-v1`/`synthetic-v1`, optional `target_profile` JSON that must say `commercial_data: false`) returns 202 and a job id; `GET /jobs/{id}` gives status and the `seating-json-v1` report with a `provenance` block (upload hash and size, never a host path or the client's file name), `/svg` answers only for `validation.status == PASS` (QUARANTINE is 409 `job.quarantined`, the diagnostic drawing is `/diagnostic.svg`). "Stateless" = jobs live in process memory for `VENUE_GEN_JOB_TTL_S` (1 h) after finishing, one job at a time, each in a spawned child process that is killed at `VENUE_GEN_JOB_TIMEOUT_S`; a restart forgets every job, so arena's G2b gateway must treat 404 as "resubmit". The exporter (`arena_export`, ported from the owner's `arena_importsvg_proof`, which is not in the repo) writes ONE temporary category (`PLACEHOLDER`, no price hint) and the grid cell as coordinates; the contract lives in `apps/backend/internal/domain/seating/venue_gen_authoring_test.go`, which reads `arena_export/testdata/synthetic_authoring.svg` + `.expected.json` generated by `VENUE_GEN_REGEN_FIXTURE=1 python -m pytest arena_export` from the SYNTHETIC `synthetic-v1` workbook (`xlsx_seating_adapter/synthetic.py`) — change the exporter, regenerate and commit both files or the Go test and the Python guard fail. A price hint in `PriceCategoryText` would become `Category.PriceHint`, which is why the bundled profile has no `price_label`. Run the generator's tests from `apps/venue-svg-generator` (`python -m pytest`); the service tests spawn real child processes (~20 s).

- **The web seat manager of a seated date (tasks S2b and S2c, `apps/event-center`, route `/o/$orgId/events/$eventId/sessions/$sessionId/seats`, permission `session.seats.manage`) is touch-first and writes ONLY one atomic `POST .../seat-batches`.** Brush modes: categories (paint) and sale (close/open); tools: seat, row, section, rectangle, lasso, pan; every touch is a stroke in a TRAY (`lib/seatManager/pending.ts`, kept in sessionStorage per session) and nothing reaches the server before Apply. Sold and held seats are drawn, never painted and never counted (`makeStroke` skips them; the server skips a seat sold meanwhile and the note says so). History and undo are the server's batches and `POST .../seat-batches/{id}/revert`. The map is the widget's own core, reached through the alias `@seatmap` (vite, vitest, tsconfig paths and the `COPY` lines of the Dockerfile): only `pan-zoom.ts`, `svg-sanitize.ts` and `types.ts` are imported. The hall SVG is built WITHOUT inline `style` (CSP `style-src 'self'`), colours and transforms are set through the CSSOM (`el.style.x = ...`). Category colours: `ticket_tiers.color` when the tier has one, else a deterministic palette by order (`lib/seatManager/colors.ts`, one function). The automatic layout is a generic form drawn from `GET .../seat-layout/algorithms` (a new algorithm needs no client change), previewed through `POST .../seat-layout/suggest` after a 450 ms pause, and "Move to the changes" turns each mapped slot into a stroke over an EXISTING category (the batch route refuses `new_tiers`); the stage point (`focal_point`) is asked once and kept in sessionStorage because S1d has not stored annotations yet. `?embed=1` (read by `useEmbedded`) drops the topbar, the title and the way back for the Telegram mini app. A "done" parity row needs the twin file: `live/seats.spec.ts` with the seed's `--seats-op reset|inspect|sell` (`seats_fixture.go`: a published event with 102 REAL seats minted by `seatmint.Materialize`, three sold, two held). The twin taps seats with Playwright clicks on `[data-k="<seat_key>"]`.

- **The bot's seated screens (S3c, 2026-10-27) are two separate things: the "Seat layout" dialog and a seated DATE in the wizard.** (1) `eventbot/seat_layout*.go`: dialog kind `seatlayout` (`sl:*` presses, owner/manager/operator), opened from the date card of a seated session. The bot names no algorithm: it reads `GET .../seat-layout/algorithms`, asks at most two enum/boolean params outside the common price/category set (`loOwnedParams` are filled from the date's open seated tiers, dearest slot to dearest tier, so a proposal is applied only with exactly one slot per category), asks the stage side (top/bottom/left/right → a focal point outside the seat box of the plan geometry, read from the venue's plan versions) when the algorithm requires `focal_point`, shows the proposal from `POST .../seat-layout/suggest` (writes NOTHING), and applies exactly that proposal as one `apply_layout` seat batch. The apply button carries a short fingerprint of what the preview showed (`loFingerprint`: algorithm, slot→tier counts, sorted assignments, sorted frozen keys - NOT `seat_status_version`), so a hall that changed since the preview shows the new picture and applies nothing. No typed word: a batch is reversible, the result screen and the start screen offer "Undo" (`POST .../seat-batches/{id}/revert`). (2) The "Scheme" button is a `url` or `web_app` button behind `BOT_SCHEME_BUTTON` (off|url|web_app, default off, bot role) plus `EVENT_CENTER_PUBLIC_URL`; private chats only; the screen is `/o/<org>/events/<event>/sessions/<session>/seats?embed=1` (embed=1 hides the header for the Mini App). Mode `url` opens `<base>` + that path (the person signs in with a password). Mode `web_app` opens `<base>/tg?next=<that path, url.QueryEscape>`: `/tg` signs the person in from the Telegram launch data (`POST /v1/auth/telegram-miniapp`, needs `TELEGRAM_MINIAPP_SECRET_KEY` on arena-api) and then goes to `next`; build it only through `loSchemeURL` (the path comes from three UUIDs, so it always starts with `/o/`; `TestLoSchemeURL` pins that). (3) `eventbot/wizard_seated.go`: a venue with an active plan of the organization (`PlanIO.VenuePlans`, the optional interface the real `ArenaClient` implements; the wizard fakes without it simply see no plans) asks "sell by the plan or without seats", then the price of each plan category (`stSPlan`, `stSPlanPrice`). The date carries `DraftSession.PlanVersionID` and its own `Tickets` (ModeParallel, `PlanIndex`/`GA`); `BuildBundle` then sends `arenaSeatingPlanVersionId` and `seatedCategories` (planCategoryIndex for a new date, arenaTierId for an existing one, `placement` true for seats, availability only for a standing zone, NO schedule and NO chain: nil keeps the stored ones). Never route a seated date through the GA ticket questions: `stSMore` "next" skips them only when EVERY date has its own categories, "Prices and categories" on a seated date goes to the price loop (`startSeatPrices`), "for all dates" is hidden while any date is seated, and `CopyDraft` of a seated event drops the categories (they are not a set to sell on new dates). `LoadEventDraft` no longer refuses a seated session: it loads the tiers as categories with their ids (`loadSeatedTickets`) and the edit changes prices only; a seated date's capacity is never rewritten by a price edit. Not done in the wizard: choosing the starting layout at creation (the importer assigns plan categories by index; re-cut afterwards with "Seat layout"), a plan for a venue created in the wizard, and editing the seats of an existing seated date. Tests: `seat_layout_test.go`, `wizard_seated_test.go`, e2e `bot_e2e_seat_layout_integration_test.go`, `bot_e2e_wizard_seated_integration_test.go` (create through the real import, then a price-only edit that leaves places and plan untouched).

- **Saved seat layouts, the colour of a category and plan annotations (task S1d, migration 0171, `seatlayout/layout.go`, `hseating/layouts.go`).** `seat_layouts` is per ORGANIZATION and per PLAN (not per version, applied by `seat_key`); `payload` is `{slots[{key,name,color,price_amount,currency,sort_order,share}], assignments{seat_key: slot_key}, closed_seat_keys[]}` with at most 15 slots and 10 000 distinct seats, stored in the form `LayoutPayload.Normalize` returns (colours lower-case, closed keys sorted, `frozen_seat_keys` dropped); `source` is `manual|geometry|allocator|copied_from_session` (a Go list and the CHECK are kept equal by `TestLayoutSources_MatchMigrationCheckConstraint`), `algorithm`/`params` record which allocator made it. **Applying is `seatlayout.ApplyLayout`: one retried transaction whose FIRST statement is the sessions lock (`lockSession`), then the plan check (a layout of another plan is 422 `seat_layout.plan_mismatch`), then slot resolution, then `applyChangesLocked` (the same body `ApplyBatchTx` runs) with kind `apply_layout`.** A used slot is tied to a category of the session by `slot_tiers` (explicit, must be live), else by name (case-insensitive; two live categories of one name are `ambiguous`, never a guess), else created when `create_missing_tiers` is set (`InsertTicketTier` + `SetTicketTierColor`, the session's currency, fixed price, or `free` at 0); otherwise the whole call is 422 `seat_layout.slots_unmapped` and nothing is written. Applying never reopens a seat the layout does not list as closed, never repaints a GA place and never edits an existing category. `dry_run` runs everything - including the category creation - in a transaction that is rolled back, so the diff is exact; the ids of categories it would create exist only inside it. **`ticket_tiers.color` is deliberately NOT a field of `gen.TicketTierRow`** (a dozen SELECT lists feed `scanTicketTierRow`): it is read/written by `ListSessionTierColors` / `GetTicketTierColor` / `SetTicketTierColor` and hydrated into the REST tier answers by `hcatalog.withColor`; a NEW tier-returning route must hydrate it the same way. The public feed and the Bil24 gateway do not carry it yet. `seating_plans.annotations` (same JSON as `layoutgen.Annotations`) is written only by the plan's OWNER (`PATCH .../seating-plans/{plan_id}/annotations`, tri-state per key, checked with `Annotations.CheckGeometry` against the plan's CURRENT version, which is why a plan whose `current_version_id` is NULL is not geometry-checked) and read by `suggest` when the request carries none (`annotations_source` says which); stored annotations are pruned to the session's version (`PruneTo`) instead of refused. A foreign organization reads a SHARED plan's annotations (`writable:false`) but never writes them (404, fork it). All eight routes are gated by `session.seats.manage` (no new permission, so no parity entry) and listed in `orgIsolationAuditedSafe`. Tests: `seatlayout/layout_test.go`, `layout_integration_test.go` (race with 16 holds), `httpserver/seat_layouts_integration_test.go` (built on the S1e Palác Akropolis fixture, which now also exposes `plan`, `event`, `tier`).
