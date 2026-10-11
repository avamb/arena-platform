# Hand-off for a cloud Claude session (2026-10-11)

Read this first, then `AGENTS.md` (binding, long) and the two specs named below. The owner (Andrei) writes in Russian: **reply to him in Russian**, keep code, commits and PRs in English.

## State of the repository

- `master` = `b64f7d8` and it is what runs in production (deployed 2026-10-11 13:56 Madrid, migrations up to **0139**).
- Merged and deployed in the last day: SEC-1 (membership and admin-organization routes), SEC-2 (org isolation for feed tokens, inventory, allocations, complimentary, sessions, seating), PAY-03 (refund engine, migration 0138), EC-14 (promoter card in the Telegram bot, migration 0139), plus the earlier EC-02..16, PAY-00..02.
- Specs: `08_architecture/35_telegram_event_center_full_ru.md` (bot, EC-*), `08_architecture/36_payment_modules_refunds_acquiring_ru.md` (payments, PAY-*), plan `docs/plans/telegram_event_center_full_plan_2026-10-09_ru.md`.
- Security audit with the remaining holes: `docs/security/org_isolation_audit_2026-10-10.md`.

## What a cloud session CAN and CANNOT do

- CAN: change code, push branches, open PRs, read CI (`gh pr checks`), fix CI failures.
- CANNOT: deploy (needs the owner's Dokploy key on his computer), reach the production database or servers, run `docker`/Postgres-based tests locally. **The GitHub CI is the only place the integration tests run for you**: the `Integration (PostgreSQL + Testcontainers)` job. Write the tests anyway (build tag `integration`), `go build`, `go vet -tags integration` and unit tests locally, push, and iterate on CI output (`gh run view <id> --log-failed`).
- NEVER merge your own PR unless the owner said so in the chat; leave PRs open and green. Never `git add .`/`-A` (stage by path), never commit secrets. Commit messages: no semicolons and no parentheses.
- Next free migration number is **0140**. Never use a lower number (goose refuses out-of-order migrations). A new permission must be granted to `platform_superadmin` in the same migration.
- Codegen after ANY `openapi.yaml` change: see the "Codegen & spec" section of AGENTS.md (types and the TS client must be regenerated and committed).

## Work packages, in priority order

### 1. SEC-3: org isolation of the flat routes (security, do first)
Source: section "MEDIUM" and "SUSPECT" of the audit file. Routes without `{org_id}` that load a row by UUID and check no ownership: tickets and credentials, scanner snapshot/validate and barcodes, reservations, admin ticket delivery/scans, media, billing invoices, webhook subscribers, plus poster_media_id / promo tier scope / allocation tier_id. **Several are public or door-staff flows by design** (buyer widget holds a reservation token, scanner app is used by door staff): read how each is used BEFORE changing it and keep legitimate flows working. Use the existing helpers: `RowOrgAccess` (hcheckout), `orgread` (httpserver/orgread), `requireEventInOrg` / `requireSessionInOrg` (hcatalog/tier_org.go). A foreign row answers the route's own 404. Add a table-driven sweep test in the style of `httpserver/org_isolation_sweep_integration_test.go` (caller whose ONLY authority is an org-B membership with an EMPTY roles claim, plus an org-B API key) and extend `TestOrgIsolationCoverage_EveryOrgChildRouteIsSwept` or add a similar router walk for flat routes. Split into several small PRs (one per route family), each with its sweep rows.

### 2. PAY-03 leftovers (spec 36 section 12, "known follow-ups")
1. An alert refused more than 20 times and owed for more than 24 hours is given up after a Telegram outage over a day.
2. `repair_attempts` is not reset when a refund becomes accepted again.
3. If the ticket-cancel route failed to write `tickets.refund_id`, its refund publishes nothing.
4. A ticket-cancel refund writes the order event `refunded` instead of `ticket_refunded`.
Money code: each fix needs a failing test first and an adversarial self-review of lock order (payment advisory lock, then the refund row) and of "money first, ticket cancelled only after the provider accepted".

### 3. PAY-04 and next (spec 36)
Flitt and AllPay refund modules, organizer-side refund routes (PAY-06, which also fixes that `POST /v1/tickets/{id}/cancel` with automatic refund still cancels BEFORE money moves), the buyer letter (PAY-07), payment webhooks (PAY-05, including warning M1 in spec 36 §12), tariff switch (PAY-11). Take them one at a time from the backlog table in the spec.

### 4. Bot screens (spec 35)
EC-20..24 (money screens), HP-1..3, EC-30..32, EC-40 (move the session and onboarding dialogs from memory to `bot_dialogs`, see `eventbot/dialogs.go`). Bot rules: texts are `bot.*` keys in en, ru, es with the completeness tests; callback_data under 64 bytes; irreversible actions need a typed confirmation word; the stub Telegram in e2e tests records text only.

### 5. Small items
- Admin-web and the site plugins do not send the `X-Client-Channel` header yet (audit `via`).
- Flaky tests: `TestWorkerBoot109_...` under `-race`, `TestInfoP99Latency_*` (rerun alone before filing a fix).

## Do NOT touch
- PR #11 (tariff scenarios, docs): belongs to another session.
- Production configuration, Dokploy, Cloudflare, WordPress servers.
- Migration 0138/0139 contents (already applied in production).

## How to report
End every PR description with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`; list what is done, what is left, and the exact test commands. Leave one summary comment in the PR with the CI table.
