# Database, schema and transactions

SAVEPOINT rules, global unique indexes, audit actor, scan helpers, customer resolution races. Add a new trap here as a `- **Title.** ...` item and one title line for it under this topic in the AGENTS.md index.

- **"Best effort" writes inside a money transaction MUST sit behind a
  SAVEPOINT.** A statement that fails inside a pgx tx aborts the whole tx even
  if the Go code logs-and-swallows the error: the later COMMIT comes back
  25P02 "commit unexpectedly resulted in rollback" and the payment silently
  vanishes behind a generic error code. Found in `hbil24`'s PAY_ORDER
  (feature #494), where `customer_org_links.source` got the illegal value
  `bil24_gateway` — the CHECK from migration 0091 allows only
  ('order','import') — and a 23514 rolled back an otherwise-good payment.
  Pattern: wrap each non-critical section in a nested `tx.Begin(ctx)`
  (SAVEPOINT), have the helper RETURN its error, and roll back just that
  savepoint. See `Handler.payBestEffort` in `hbil24/cmd_order_pay.go`.
- **`payment_intents_provider_payment_id` is a GLOBAL unique index**
  (migration 0025), unscoped by org — same class of trap as
  `customer_identities_strong_uq`. Integration tests must randomize the
  external ref that feeds it per run, or leftovers from an interrupted run
  against the shared dev stand collide with 23505.
- **`audit_events.actor_id` is a nullable `uuid` column** and `audit.insertSQL`
  casts it with `NULLIF($3,'')::uuid`, so `audit.Event.ActorID` accepts only a
  UUID string or `""`. A non-UUID principal label (the Bil24 gateway's
  `gateway:<fid>`) aborts the whole enclosing transaction with SQLSTATE 22P02
  — which surfaces as a generic gateway `-99`. Pass such labels as audit
  metadata instead; see `htickets.CancelTicketParams.ActorLabel`.
- The hand-written `gen/*.sql.go` wrappers share scan helpers (e.g.
  `scanTicketRow`): widening a row struct means EVERY query that feeds that
  scanner must SELECT the new columns — including ones in other files
  (`superadmin.sql.go`, `refunds.sql.go`). Unit tests cannot catch a column-
  count mismatch; grep for the scanner's callers.
- **`audit.WithServiceActor` must keep `actor_id` a bare uuid.** It used to
  write `api_key:<uuid>`, which `audit_events.actor_id uuid` rejects with
  22P02 — every audited mutation under an organization API key silently lost
  its audit row (and would abort a WriteTx transaction). The human label now
  lives in `actor_type='api_key'` + `metadata.actor_label` (fix 871eeed).
- **`customers.Resolve` is race-safe via lookup-after-23505 inside a
  SAVEPOINT.** `customer_identities_strong_uq` (migration 0091) is a
  GLOBAL unique index, so two concurrent first-time resolves for the same
  brand-new email/phone both pass the initial lookup and race the insert;
  the loser used to surface a raw SQLSTATE 23505 (Bil24 CREATE_USER logged
  an ERROR and answered -1; a checkout-confirm transaction that treated
  the error as "non-fatal" was actually already aborted, one statement
  away from 25P02). `postgres_store.go`'s `InsertIdentity` now wraps the
  insert with `WithSavepoint` (a nested `pgx.Tx.Begin` — a real SAVEPOINT
  when the Store already runs inside an ambient tx, a fresh top-level
  transaction when it runs directly against a pool, detected via
  `gen.Queries.DB()` type-asserted against the narrow
  `Begin(ctx) (pgx.Tx, error)` both concrete types satisfy) and, on a
  unique violation, returns `ErrIdentityConflict` instead of the raw
  error. `resolve.go`'s `resolveAttempt` savepoints the WHOLE
  create-customer-and-attach-identity sequence (and every found-branch
  identity attach) through the same mechanism, so a loss rolls the
  customer row back together with the losing insert — no orphan customer
  survives — then re-runs itself (bounded, `identityRaceMaxRetries`),
  which finds the winner via the ordinary lookup. `customers.LinkOrg`
  (`UpsertCustomerOrgLink`) was already `ON CONFLICT DO NOTHING` and
  needed no change. **Any best-effort identity resolution/org-link that
  runs on a checkout or money transaction must STILL sit behind its own
  SAVEPOINT** (AGENTS.md pattern above, `Handler.payBestEffort` /
  `hfeed.Handler.bestEffort`) as defense in depth — `Resolve`'s internal
  retry only covers the identity race itself, not every other way that
  section could fail. Tests:
  `TestResolve_LostEmailRaceOnCreate_RecoversToWinningCustomer` /
  `_NoOrphanCustomerSurvives` / `TestResolve_LostWeakIdentityRace_ExistingCustomer`
  (`customers_test.go`, fakeStore's `injectConflict`),
  `TestPostgresStore_ConcurrentResolve_SameNewEmail_LiveDB`
  (`resolve_race_integration_test.go`, 20 real goroutines),
  `TestPublicFeedCheckout_ConcurrentSameNewBuyer_NoRaceAbortedTx`
  (`httpserver/public_feed_checkout_race_integration_test.go`),
  `TestBil24_CreateUser_ConcurrentSameNewEmail_AllSucceedSameUserID`
  (`tests/compat/bil24/create_user_race_test.go`).
- **Migration numbers are taken by whoever merges first, so renumber just before the merge (2026-10-31).** Every branch picks "next free" from its own base, and branches run in parallel: the venue-sharing migration was 0172 when master's head was 0171, then the shop (0172, 0173-0175, 0177-0180) and the staff invitations (first 0172, renumbered to 0176) merged and took the numbers, so it became 0182. goose then refuses the second file with the same number (and an out-of-order one on a database that already has a later one). Before merging a PR with a migration: `git fetch`, merge `origin/master`, compare `ls apps/backend/internal/migrations/sql | tail` with the file, rename it to the next free number, and update `migrations_head_test.go`, the "migration NNNN" comments, the AGENTS entry and the generated OpenAPI/TS files that quote it. Open PRs that add a migration: `gh pr list` then each PR's files under `migrations/sql/`.
