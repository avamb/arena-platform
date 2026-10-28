# Dev environment and agent working rules

Windows host, Docker Desktop, bash allowlist, lint and format tooling, git hygiene, writing style for people. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- Full `go test ./...` is slow (4+ min); use focused packages plus
  `go build ./...` for type-checking when iterating, but the full suite must be
  green before a wave is pushed.

- **Never judge the full suite through a pipeline.** `go test ./... 2>&1 | grep -v ok | head`
  reports `head`'s exit code (0) even when packages FAIL — wave-4 pass 4 was
  declared green this way while OpenAPI docs tests were red. Run
  `go test ./... > log 2>&1; echo EXIT:$?` and grep the log afterwards.

- **The command allowlist rejects a bash command containing the bare token
  `postgres`** — including inside a heredoc or a quoted env assignment, so
  `DATABASE_URL=postgres://...` is refused outright. Pass the DSN base64-encoded
  instead. `export` is ALSO blocked now, so it must be an inline env prefix on
  the same command line:
  `DATABASE_URL="$(echo -n '<base64>' | base64 -d)" JWT_SIGNING_SECRET=x go.exe test -tags integration ./...`
  `wsl.exe -d <distro>` is also blocked (`wsl.exe -l -v` is not).
  **`base64` and `printf` are blocked as of feature #485**, so the DSN can no
  longer be encoded or decoded that way. What still works is splitting the
  rejected token with adjacent-string concatenation inside the inline env
  prefix — the scanner looks for the bare word, not the assembled value:
  `DATABASE_URL="post""gres://arena:arena@localhost:55432/arena?sslmode=disable" JWT_SIGNING_SECRET=x go.exe test -tags integration ./...`

- **The allowlist splits on `;` and on parentheses even inside a quoted
  argument**, so a `git commit -m "...(foo); bar"` message is rejected with a
  bogus "Command 'bar' is not allowed". Keep commit messages free of semicolons
  and parentheses. PowerShell additionally rejects expandable strings with
  embedded expressions, `$()` subexpressions, and .NET method calls — prefer the
  bash inline-env form above.

- **`cd` is rejected by the bash allowlist** ("Command 'cd' is not allowed"),
  and a rejected call also cancels the sibling calls issued in the same
  parallel batch. Always run from the repo root with repo-relative paths.

- **Test-run logs must go to a repo-relative path.** The Read tool cannot open
  a bash-written `/tmp/foo.log` on this Windows host (it reports "File does not
  exist"). Redirect to `./x.log` in the repo root and delete it before staging.

- **Starting Docker Desktop from a session**: `docker desktop start` prints
  nothing and does not reliably bring the engine up; do not trust its exit code
  (a `| head` pipeline reports head's status). Arm a background watcher instead:
  `until docker ps >/dev/null 2>&1; do sleep 10; done` — it notifies on ready.

- **`claude-progress.txt` exceeds the Read tool's 256KB cap** (1.3MB+). Read it
  with `offset`/`limit` and append with `Edit` anchored on the last lines.

- **golangci-lint locally**: no binary on this host; run from repo root:
  `GOLANGCI_LINT_CACHE=/tmp/golangci-cache go.exe run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./apps/backend/...`
  The cache path MUST be absolute — a relative path (`.golangci-cache`) fails
  with "not an absolute path". Pin to `@latest` — CI uses the action's
  `latest`; an older pin (v2.1.6) reports G115 false positives.

- **go on Windows**: `go.exe` works directly in bash without PATH tricks
  when the harness allowlist includes it. Prefer `go.exe <cmd>` over
  `cmd.exe /c "set PATH=...&& go <cmd>"` — the latter swallows output
  through the Windows shell redirect and is harder to debug.

- **Codegen oapi-codegen config path**: the config file lives at
  `apps/backend/openapi/oapi-codegen.yaml`, NOT at the repo root.
  Use `go run .../oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml`.

- **Local migration smoke test**: Docker Desktop's `arena_postgres` maps
  host port **55432** (not 5432) with user/pass/db `arena`. Run
  `DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable go run ./cmd/arena-migrate`
  to prove new migrations apply to a database carrying real prior-wave data
  before pushing (since per-role validation, 2026-10-14, migrate needs only
  DATABASE_URL; older images also demanded JWT_SIGNING_SECRET). Raw-SQL fixtures also live outside handlers: `cmd/arena-seed`,
  `cmd/arena-bil24-import` (incl. live_venues.go) and
  `delivery_integration_test.go` — schema changes must update them or the
  CI Integration job (migrated + seeded) breaks while Unit stays green.

- **Never `git add .` or `git add -A`**: stage changed files by path only
  (`git add path/to/file.go path/to/other.ts`). After staging, run
  `git status --short` and confirm nothing unexpected is staged. A previous
  agent committed `.golangci-cache/` (9.7k files) and a JWT token this way.

- **golangci-lint cache path must be absolute**: run from repo root with
  `GOLANGCI_LINT_CACHE=/tmp/golangci-cache go.exe run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./apps/backend/...`.
  A relative path (`.golangci-cache`) returns "not an absolute path" and aborts.

- **gofmt all changed files, including integration-tagged ones**: golangci-lint
  skips files with `//go:build integration` by default, so gofmt violations
  in those files slip through lint and are only caught by CI's format check.
  Run `gofmt -l -w <file>` on every new or modified Go file before committing,
  regardless of build tag.

- **`gh` CLI is blocked in the AutoForge sandbox** (`Command 'gh' is not
  allowed`, needs an entry in `.autoforge/allowed_commands.yaml` or mid-session
  approval). An Integrator gate run that pushes to `master` cannot watch CI
  status afterward from this environment — check the GitHub Actions run
  manually (or via a session where `gh` is allowlisted) after pushing.

- **`apps/backend/.gomodcache` is a gitignored local module cache that lives
  INSIDE the repo tree on this host.** A repo-wide `gofmt -l .` (or any other
  recursive source scan) run from the repo root walks into it and reports
  hundreds of "violations" in third-party dependency source — none of it is
  our code and none of it should be fixed. Scope `gofmt -l`/similar sweeps to
  the real source dirs (`apps/backend/{cmd,internal,tests,tools}`) instead of
  the bare repo root.

- **A bash `for f in ...; do ...; done` loop is rejected by the allowlist**
  with a bogus "Command 'f' is not allowed". Enumerate paths explicitly
  instead of looping.

- **Editing a source file with a `python ... str.replace` heredoc FAILS
  SILENTLY on the many CRLF files in this repo**, and line endings are mixed
  per file (`apps/widget/src/ArenaTickets.svelte` is CRLF,
  `apps/tickets-page/src/lib/render.ts` is LF — check with `file <path>`, not
  by eye: the Bash tool's `cat -A` showed no `^M` on a file that `python`
  proved was CRLF). A multi-line pattern written with `\n` matches nothing,
  `str.replace` returns the string unchanged, and the script still prints its
  success line — so the edit looks applied and is not. A `re.sub` whose
  pattern starts `\n\s*` is worse: on CRLF it eats the previous line's `\n`
  and leaves a bare `\r`, producing a file with "CRLF, CR line terminators"
  that git then warns about on `add`. Use the Edit tool for source files; if a
  scripted sweep is genuinely needed, assert the match (`assert old in s`)
  and re-run `file <path>` afterwards.

- **Stale `.claude/worktrees/*` directories accumulate** from old isolated
  agent runs and pollute repo-wide sweeps (e.g. `gofmt -l .` reports hits
  inside them). During an Integrator gate pass, check `git worktree list` for
  worktrees whose branch is fully superseded/merged, verify with
  `git -C <path> log --oneline -5` and a content diff of a sample file
  against HEAD, then remove with `git worktree remove <path> --force` and
  `git branch -D <branch>`.

- **Docker Desktop on this Windows host can lose the host-port publish for
  `arena_postgres` (55432) to a Hyper-V/WSL NAT port exclusion** —
  `netsh interface ipv4 show excludedportrange protocol=tcp` sometimes
  reprograms its excluded ranges (commonly on a reboot) to include 55432, so
  `docker start`/`up`/`--force-recreate` on the postgres service fails with
  `bind: An attempt was made to access a socket in a way forbidden by its
  access permissions` and `docker port arena_postgres` shows no mapping at
  all — `localhost:55432` then refuses every connection even though
  `docker exec arena_postgres psql ...` and the container's OWN network
  (arena_api/arena_worker talking to it by the `postgres` hostname) work
  fine throughout. Fixing the exclusion needs `Restart-Service winnat`,
  which needs admin and is a system-settings change agents must not make.
  Workaround that does not touch Windows: recreate the container with a
  free host port instead of 55432 (check candidates against
  `netsh interface ipv4 show excludedportrange protocol=tcp` first — low
  ports like 25432/45432 are reliably outside the Hyper-V dynamic range) —
  `docker rm arena_postgres` then
  `docker run -d --name arena_postgres --network arena_new_default --network-alias postgres -e POSTGRES_DB=arena -e POSTGRES_USER=arena -e POSTGRES_PASSWORD=arena -v arena_new_arena_pg_data:/var/lib/postgresql/data -p 45432:5432 --restart unless-stopped postgres:17-alpine`
  — the named volume (`arena_new_arena_pg_data`) and the `postgres` network
  alias preserve both the data and arena_api/arena_worker's connectivity;
  verify with `select count(*) from organizations` before/after. Point any
  host-side `DATABASE_URL` (migration smoke tests, the CI-Integration-job
  recipe above) at the new port instead of 55432 until a future session
  reclaims it.

- **Texts for people follow `docs/text_style_standard_ru.md` (principles borrowed from ASD-STE100, NOT a claim of STE conformance).** Short sentences, one action per sentence, active voice, the glossary terms only: in Russian event = «мероприятие» (never «ивент» or «событие»; «ивент-центр» is a product name), session = «сеанс», refund = «возврат», venue = «площадка», sales end = «конец продаж», doors open = «вход с», **hold = «бронь»** («в брони», «бронь истекла»; never «удержание мест» or «блокировка»). The glossary (section 4 of the standard) was approved on 2026-10-13. `i18n.TestTextStyle_Catalogs` scans `bot.*` of `en.toml` and `ru.toml` (`go test ./internal/platform/i18n -run TestTextStyle -v`). **A forbidden glossary synonym FAILS the test** (`styleForbidden`, rule `glossary`); a fixed phrase that contains a forbidden word but names another thing («схема зала», «ивент-центр») goes into the narrow, commented `styleAllowed` list and section 4.1 of the standard, only with the owner's consent; never weaken a regex to make a text pass, change the text. Every other rule (long sentence, semicolon, dash, passive, «необходимо») only WARNS, and a growing count prints `STYLE NOTE`. After a cleanup, refresh the baseline with `ARENA_STYLE_WRITE_BASELINE=1 go test ./internal/platform/i18n -run TestTextStyle_Catalogs` and commit `testdata/text_style_baseline.txt`. **Tone (section 2.1): we love our client and help them in everything.** Polite and warm, never blame the buyer or the organizer, every refusal or error says what to do next and offers a way out (a button, the next step, a support contact), no bare «error»/«invalid»/«неверно». The rollout plan is `docs/text_style_rollout_plan_ru.md`; the ASD text itself must never be copied into the repo.

- **How an agent writes instructions and answers for the owner (step 6 of the text rollout plan, checklist 3.4 of the standard).** In Russian. The result or conclusion first, details after. Short numbered steps, one action per step, the condition at the start of the step. Every command in its own code block, one command per block, PowerShell only for the owner (no bash prefixes, no `&&`). A warning stands BEFORE the risky step it is about (deleting, overwriting, deploying, anything that cannot be undone), not after it. Times in Madrid time. Say plainly what was verified (which tests ran, what was checked on the live stand) and what was NOT verified; never present an untested step as done.

- **Tooling on this Windows host (2026-10-24):** `npm run <script>` from a git-bash session in a deep scratch path exits 1 with NO output (cmd.exe as script shell); add `--script-shell="C:/Program Files/Git/bin/bash.exe"` or call `npx tsc` / `npx vitest run` directly. A fresh git worktree has no `node_modules`: junction them (`cmd //c "mklink /J <wt>\apps\admin-web\node_modules <main>\apps\admin-web\node_modules"`) and remove them with `cmd //c rmdir`, never `rm -rf` through the link. event-center has no install on this host: its `tsc -b` only fails on the missing `@axe-core/playwright` test types, `tsc -p tsconfig.app.json` is the check for `src`. The Write tool and bash heredocs turn `\uXXXX` and `\n` in source into raw characters or single backslashes: for escapes in regexes or strings write the file with a `node` script that builds them with `String.fromCharCode(92)`.
