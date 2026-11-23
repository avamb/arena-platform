# Arena Event Center — small release: complete GA event creation
Owner authorizes delegating implementation to Claude, preparing reviewed changes for the next release. No merge or production deploy is authorized by this task.

Repository: C:\Projects\arena_new; GitHub avamb/arena-platform, default branch master.
Known latest deployed event-center/master: d9a1f6875c53f7c41628b66929f52c14ce9d736b.
Backend production: 02039d0acd976414285ee468815c79f1e416c2b9, schema 156.
Released #190 includes event status (#168), category management (#172), session sale times (#175).
User sees no Create event button. We need a complete working creation flow, not another preparatory phase.

WORKSPACE SAFETY
Root checkout is old feat/ec-01-bot-dialogs and dirty. Never reset, clean, stash, switch it, stage its changes or commit its untracked files.
Read applicable AGENTS.md/CLAUDE.md. Use a separate worktree from verified current origin/master; branch codex/event-center-ga-create. Do not assume a worktree created by tooling starts from master: explicitly verify ancestry/base. Do not touch Venue_SVG_Generator, credentials, billing settings, production, SSH or customer data. No paid Anthropic/OpenAI calls from application tests or nested runners; no additional agents.

REUSE
Existing unmerged #173 feat/w-6-1a-wizard-event-venue: Event/Venue UI, build flag VITE_FEATURE_WIZARD=1, currently default off.
#186 feat/w-6-1b-wizard-dates-tickets based on #173: dates/tickets validation, bundle parity; EventWizardScreen.tsx explicitly says saving is 6-1c.
Reuse these changes selectively or preserve commits where appropriate; do not merge stale branches blindly. Inspect current GitHub status first so completed work is not duplicated.
Paths: apps/event-center/src/screens/EventWizardScreen.tsx; components/Wizard*; lib/wizard/*; lib/features.ts; OrgLayout.tsx.
Existing capability import.bil24_session gates entry; verify actual backend create/import permission contract before deciding permissions. Do not invent a new permission model.

PRODUCT SCOPE
A single complete release: create a GA (unnumbered seating) event using existing platform bundle/create APIs.
Forms: title, supported description fields, existing venue selection, session date/time/timezone and GA categories with currency, price and capacity according to current contract. Retain existing useful forms; do not add unrelated materials/poster editing workflows or new venue creation unless essential.
Complete validation -> explicit create -> authoritative saved result -> navigate to event card and show it in organization list.
New event must be draft and off sale; no automatic publication, sales or notifications.
Verify server guarantees draft/off-sale. If current API cannot provide it safely, report concrete blocker; do not fake draft in UI or silently publish.
Owner/manager may create only as current permissions allow; other roles cannot use unauthorized write APIs. Server enforces organization boundaries.
Prevent double submission; use existing idempotency support. On network ambiguity preserve draft and prevent blind duplicate retry; report limits if backend lacks safe reconciliation.
Keep form data on validation/network failure; map server field errors, support correction and retry. On success clear only successfully saved draft.
Use existing i18n conventions for all supported locales, accessibility, mobile layout, timezone/DST handling.
Make feature available in intended release build with deliberate tested gating; enabling a flag alone is not completion.
Do not implement assigned seating, session move/cancel (#174), general event metadata editor, refunds, billing or unrelated open PRs.
If supporting multiple dates is atomic only per session, explicitly handle partial creation and prevent duplication; do not claim atomic success for incomplete event bundles. Prefer smallest coherent existing architecture, document any actual limitation.

ACCEPTANCE — ALL REQUIRED
1. Literal documented test/build commands pass.
2. Unit tests for create success, field/server failure, in-flight duplicate click, failed/ambiguous retry, draft retention and permission/gate routing.
3. Real offline backend integration and browser E2E: authorized GA event creation, persisted draft/off-sale, list/card/session visible, exact category prices/currency/capacities, timezone correct. Check direct unauthorized write and cross-org rejection. Mock tests alone insufficient.
4. No writes to production. Use existing local test database/seed/isolated runtime. No real purchases, emails, payments, customer source data or model API calls.
5. Existing event status/category/session sale-time regression remains passing; typecheck, lint if prescribed, build; no unexplained required skips.
6. Test evidence records commands, exit codes, PASS/FAIL/SKIP and HEAD/diff identity. Never treat absence/SKIP as PASS.
7. Inspect final diff for secrets/runtime/fixtures/unrelated changes; no user files in commit.
8. Prepare clean Conventional Commits and push ordinary feature branch if authorized GitHub credentials work, no force push. Open a focused PR to master with implementation, test evidence, risks and scope exclusions; no automatic merge/deploy. Attach resulting PR to this chat when tools permit.
9. Leave concise Russian handoff: actual branch/worktree/commit/PR, what implemented, exact tests and limitations, deployment flag/API dependency/rollback, READY or BLOCKED. Do not claim independent review of your own work.

Continue autonomously through implementation and local checks. If real blocker prevents safe completion, stop with exact evidence and minimal next action; do not create infrastructure phases.

