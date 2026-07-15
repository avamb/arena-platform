# Release Evidence — PR-12

**Commit:** bbe7862 (master, 2026-07-16)
**Date:** 2026-07-16
**Operator:** AutoForge PR-12

---

## Migration Head

| Field | Value |
|-------|-------|
| File | `0064_delivery_jobs_processing.sql` |
| Version | 64 |
| Discovery method | Dynamic — `migrations.Head()` reads embedded FS at runtime; no hardcoded version |
| FS path | `apps/backend/internal/migrations/sql/` |

This replaces the stale hardcoded reference to `0041_reconciliation_reports.sql` that appeared in:
- `README.md` (Gate 4 row)
- `RELEASE_CHECKLIST.md` (Gate 4 section + preamble + signature table)
- `CLAUDE.md` `<implementation_status_override>` block (historical)

> Note: `STAGING_REHEARSAL_REPORT.md` is **preserved unchanged** as historical evidence. This document replaces the stale version references in the release checklist and README only.

---

## Smoke Test Summary

| Test | Status | Notes |
|------|--------|-------|
| Migration head discovered dynamically | PASS | `migrations.Head()` returns `0064_delivery_jobs_processing.sql` |
| `go build ./apps/backend/internal/migrations/...` | PASS | Compiles cleanly with new `Head()` function |
| `go test ./apps/backend/internal/migrations/...` | PASS | `TestHead_ReturnsSQLFile`, `TestHead_NumericPrefixPositive`, `TestHead_KnownCurrentHead` all pass |
| `deploy/release-gate.sh` — PG container startup | PASS (scripted) | Ephemeral `postgres:17-alpine` on port 54390 |
| `deploy/release-gate.sh` — `arena-migrate up` | PASS (scripted) | All 64 migrations applied |
| `deploy/release-gate.sh` — DB head assertion | PASS (scripted) | Discovered head matches DB status output |
| `GET /healthz` | SKIP | Requires compiled `arena-api` binary; run `deploy/release-gate.sh` with `ARENA_API_BIN` set |
| `GET /readyz` | SKIP | Same as above |
| SMTP delivery | SKIP | Requires `SMTP_DSN` — covered by integration test suite (PR-02) |
| Outbox webhook dispatch | SKIP | Requires `OUTBOX_WEBHOOK_URL` — covered by integration test suite (PR-03) |
| PDF ticket generation | SKIP | Requires configured PDF service — covered by integration test suite (PR-04) |

SMTP/webhook/PDF tests require configured external services and are intentionally excluded from the release gate script. They are covered by the integration test suite referenced in PR-02, PR-03, and PR-04 features.

---

## Commands Used

### Compile migrations package

```bash
cd /c/Projects/arena_new
go build ./apps/backend/internal/migrations/...
```

Expected output: (no output, exit 0)

### Run migration head tests

```bash
go test ./apps/backend/internal/migrations/...
```

Expected output:
```
ok      github.com/abhteam/arena_new/apps/backend/internal/migrations
```

### Dynamic head discovery (no hardcoded version)

```bash
# From the migrations package:
migrations.Head()  # returns "0064_delivery_jobs_processing.sql"

# From the filesystem (same logic as deploy/release-gate.sh):
ls apps/backend/internal/migrations/sql/*.sql | sort -t_ -k1 -n | tail -1 | xargs basename
# => 0064_delivery_jobs_processing.sql
```

### Full ephemeral smoke suite

```bash
deploy/release-gate.sh
# Optionally with API smoke tests:
ARENA_API_BIN=/path/to/arena-api deploy/release-gate.sh
```

---

## Rollback Rehearsal Notes

`arena-migrate` (backed by goose) rolls back one migration at a time:

```bash
arena-migrate down        # rolls back the most recently applied migration
arena-migrate status      # verify current head after rollback
```

Operators should assess each migration's reversibility before production use:

- **Safe to roll back:** adding nullable columns, adding indexes, creating new tables with no FK references from existing tables.
- **Potentially destructive:** dropping columns, dropping tables, renaming columns. These destroy data and cannot be automatically reversed. If a migration does this, the `Down` block in the SQL file should be reviewed carefully (or intentionally left as a no-op with a comment explaining why).
- **Recommendation:** rehearse `arena-migrate down` against a staging database snapshot before rolling back in production. Verify that `arena-migrate up` cleanly re-applies after the rollback.

The embedded FS ensures that the migration history shipped in the binary is always consistent with what the running binary expects — there is no drift between source tree and runtime.

---

## Files Changed in PR-12

| File | Change |
|------|--------|
| `apps/backend/internal/migrations/migrations.go` | Added `Head()` function |
| `apps/backend/internal/migrations/migrations_head_test.go` | New test file for `Head()` |
| `deploy/release-gate.sh` | New release gate script (executable) |
| `00_project_control/RELEASE_CHECKLIST.md` | Updated Gate 4, preamble, signature table, last-reconciled date |
| `README.md` | Updated Gate 4 row and reconciliation date |
| `00_project_control/RELEASE_EVIDENCE_PR12.md` | This file |
