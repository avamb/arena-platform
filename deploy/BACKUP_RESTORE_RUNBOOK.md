# Arena — Backup / Restore Runbook

> **Scope:** PostgreSQL 17 + Redis 7  
> **Environment:** Dokploy-hosted production + optional staging  
> **Maintained by:** Platform / SRE team

---

## Table of Contents

1. [Overview](#1-overview)
2. [Backup Strategy](#2-backup-strategy)
3. [RPO / RTO Assumptions](#3-rpo--rto-assumptions)
4. [Nightly pg_dump Backup](#4-nightly-pg_dump-backup)
5. [WAL Archive (PITR) — Optional](#5-wal-archive-pitr--optional)
6. [Restore from pg_dump](#6-restore-from-pg_dump)
7. [Point-In-Time Recovery (PITR)](#7-point-in-time-recovery-pitr)
8. [Redis — No Backup Required](#8-redis--no-backup-required)
9. [Staging Dry-Run Procedure](#9-staging-dry-run-procedure)
10. [Monitoring & Alerting](#10-monitoring--alerting)
11. [Verification Checklist](#11-verification-checklist)

---

## 1. Overview

Arena's data is stored **exclusively in PostgreSQL 17**.  Redis is used only for
operational state (distributed locks, hot-cache) that is always rebuilt from
PostgreSQL on startup.  Therefore:

| Component  | Backup needed? | Reason |
|---|---|---|
| PostgreSQL | **Yes** — nightly pg_dump + optional WAL | Source of truth for all business data |
| Redis      | **No** | Operational state only; rebuilt from DB on restart |

---

## 2. Backup Strategy

### Tier 1 — Nightly pg_dump (mandatory)

- **Tool:** `pg_dump` custom-format (`-Fc`) with compress=9
- **Frequency:** Nightly at 02:00 UTC (configurable via cron)
- **Storage:** Local path **and** optionally S3-compatible object storage
- **Retention:** 7 days rolling (configurable via `BACKUP_RETENTION`)
- **Verification:** SHA-256 checksum written alongside each dump

### Tier 2 — WAL Archive / PITR (optional, recommended for production)

- **Tool:** `pg_basebackup` + `archive_command` WAL shipping
- **Recovery granularity:** Recover to any second within the WAL retention window
- **Enable:** Set `ENABLE_WAL_ARCHIVE=true` + `WAL_ARCHIVE_DEST` in backup environment

---

## 3. RPO / RTO Assumptions

| Metric | Tier 1 (pg_dump) | Tier 2 (WAL/PITR) |
|---|---|---|
| **RPO** (max data loss) | ~24 hours (time since last dump) | Seconds to minutes (last WAL flush) |
| **RTO** (time to restore) | 15–60 min (depends on DB size) | 30–90 min (base backup + WAL replay) |
| **Complexity** | Low | Medium |
| **Cost** | Storage only | Storage + WAL stream overhead (~1–3% I/O) |

### Assumptions

1. **Nightly dump window:** Backup starts at 02:00 UTC.  If a failure occurs at
   01:59 UTC the worst-case data loss is ~24 hours (one full day of transactions).
2. **Restore time (pg_dump):** Estimated at ~15 minutes for a 10 GB database on a
   4-core restore host.  Scales linearly with database size.
3. **WAL PITR recovery time:** Depends on the distance from the base backup to the
   target recovery timestamp.  A 2-hour distance at 10 MB/s WAL rate ≈ 7 minutes
   of replay after the restore finishes loading the base backup.
4. **Single-node assumption:** This runbook covers a single-node Dokploy deployment.
   Multi-node read-replica setups extend RTO through promotion; consult the
   Infrastructure Hardening milestone runbook when replicas land.
5. **Redis state is ephemeral:** After any restore (whether pg_dump or PITR), Redis
   must be **flushed** (`FLUSHALL`) or replaced with a fresh empty instance.
   The application rebuilds all cache and lock state from PostgreSQL on boot.

---

## 4. Nightly pg_dump Backup

### Schedule Setup (Cron on Dokploy host)

```cron
# /etc/cron.d/arena-backup
# Run nightly at 02:00 UTC
0 2 * * * root \
  DATABASE_URL="postgres://arena_user:SECRET@db-host:5432/arena?sslmode=require" \
  BACKUP_DEST="/var/backups/arena" \
  BACKUP_RETENTION="7" \
  S3_BUCKET="s3://my-arena-backups" \
  AWS_REGION="eu-west-1" \
  /opt/arena/deploy/backup.sh >> /var/log/arena-backup.log 2>&1
```

### Script location

```
deploy/backup.sh
```

### Verify last backup ran successfully

```bash
# Check recent backups
ls -lh /var/backups/arena/

# Verify today's dump exists and is non-empty
ls -lh /var/backups/arena/arena_$(date -u '+%Y%m%d')*.dump

# Tail the backup log
tail -50 /var/log/arena-backup.log
```

---

## 5. WAL Archive (PITR) — Optional

### Enable WAL archiving in PostgreSQL

Add the following to `postgresql.conf` (or via environment in Docker):

```conf
wal_level = replica
archive_mode = on
archive_command = 'test ! -f /var/lib/postgresql/wal_archive/%f && cp %p /var/lib/postgresql/wal_archive/%f'
archive_timeout = 300   # Archive at most every 5 minutes even if no activity
```

Or with S3 (using `wal-g` or `pgbackrest`):

```conf
archive_command = 'wal-g wal-push %p'
```

### Take a base backup

```bash
# Required before WAL replay can start
ENABLE_WAL_ARCHIVE=true \
WAL_ARCHIVE_DEST=/var/backups/arena/wal \
DATABASE_URL="postgres://replica_user:SECRET@db-host:5432/arena" \
deploy/backup.sh --wal-only
```

---

## 6. Restore from pg_dump

> **Use this procedure when:** you need to restore from a nightly dump.
> Acceptable data loss: up to 24 hours of transactions.

### Step 1 — Locate the dump file

```bash
# List available dumps
ls -lht /var/backups/arena/arena_*.dump

# Or from S3
aws s3 ls s3://my-arena-backups/ --recursive | grep "\.dump$" | sort
```

### Step 2 — Download from S3 (if applicable)

```bash
aws s3 cp s3://my-arena-backups/arena_20260622T020000Z.dump /tmp/arena_restore.dump
aws s3 cp s3://my-arena-backups/arena_20260622T020000Z.dump.sha256 /tmp/arena_restore.dump.sha256
```

### Step 3 — Verify the checksum

```bash
sha256sum --check /tmp/arena_restore.dump.sha256
# Expected output: /tmp/arena_restore.dump: OK
```

### Step 4 — Stop the arena-api application

In Dokploy dashboard: **arena-api** → **Stop** (prevents new writes during restore).

### Step 5 — Restore the database

```bash
# Method A: Using restore.sh (recommended)
DATABASE_URL="postgres://arena_user:SECRET@db-host:5432/arena?sslmode=require" \
RESTORE_DROP_FIRST=true \
deploy/restore.sh --dump-file /tmp/arena_restore.dump

# Method B: Manual pg_restore
PGPASSWORD=SECRET pg_restore \
  --host=db-host --port=5432 \
  --username=arena_user \
  --dbname=arena \
  --verbose \
  --exit-on-error \
  /tmp/arena_restore.dump
```

### Step 6 — Run migrations (schema catch-up)

The dump contains the schema at backup time.  If code is newer than the dump,
run migrations to apply any missing schema changes:

```bash
docker run --rm \
  -e DATABASE_URL="postgres://arena_user:SECRET@db-host:5432/arena?sslmode=require" \
  -e APP_ENV=production \
  -e ENABLE_DEV_AUTH=false \
  --entrypoint /app/arena-migrate \
  your-registry/arena-api:latest up
```

### Step 7 — Reset Redis

```bash
# Flush ALL Redis data — it will be rebuilt from PostgreSQL
redis-cli -u redis://redis-host:6379 FLUSHALL

# Or restart the Redis container
docker restart arena_redis
```

### Step 8 — Start arena-api

In Dokploy dashboard: **arena-api** → **Start**.

### Step 9 — Verify

```bash
curl https://your-domain.example.com/healthz   # → 200 {"status":"ok"}
curl https://your-domain.example.com/readyz    # → 200 {"status":"ok"}
```

Check application logs for errors:

```bash
# In Dokploy Logs tab, look for:
{"level":"INFO","msg":"server started","addr":":8080"}
```

---

## 7. Point-In-Time Recovery (PITR)

> **Use this procedure when:** you need sub-24-hour recovery granularity.
> Requires WAL archiving to be enabled (see §5).

### Step 1 — Determine the recovery target timestamp

Identify the exact timestamp to recover to (e.g., one minute before the failure):

```
TARGET_TIME="2026-06-23 01:58:00+00"
```

### Step 2 — Stop arena-api

Dokploy: **arena-api** → **Stop**.

### Step 3 — Restore the base backup

```bash
# Take the most recent base backup before TARGET_TIME
ls /var/backups/arena/wal/

# Restore PostgreSQL data directory from base backup
pg_restore_from_base_backup() {
  local base_backup_dir="$1"
  local pg_data_dir="/var/lib/postgresql/data"
  systemctl stop postgresql
  rm -rf "${pg_data_dir}"
  mkdir -p "${pg_data_dir}"
  tar -xzf "${base_backup_dir}/base.tar.gz" -C "${pg_data_dir}"
}
```

### Step 4 — Configure recovery target

Create `postgresql.auto.conf` (PostgreSQL 12+) or `recovery.conf` (older):

```conf
# postgresql.auto.conf (PostgreSQL 12+)
restore_command = 'cp /var/lib/postgresql/wal_archive/%f %p'
recovery_target_time = '2026-06-23 01:58:00+00'
recovery_target_action = 'promote'
```

### Step 5 — Start PostgreSQL and allow WAL replay

```bash
systemctl start postgresql
# Monitor WAL replay
tail -f /var/log/postgresql/postgresql.log | grep -E "(recovery|redo|consistent)"
```

### Step 6 — Continue with Steps 6–9 from §6 (migrate, flush Redis, start app)

---

## 8. Redis — No Backup Required

> **Design decision:** Redis in arena_new holds **operational state only**.
> It is NOT the source of truth for any business data.

### What Redis stores

| Key pattern | Purpose | Source of truth |
|---|---|---|
| `lock:<resource>` | Distributed advisory locks | Locks expire; rebuilt on reconnect |
| `cache:<entity_id>` | Hot-read cache | PostgreSQL (rebuilt on cache miss) |
| `session:<user_id>` | Auth session tokens (future) | PostgreSQL sessions table |

### Restore procedure for Redis

1. Start a **fresh, empty** Redis 7 instance.
2. Do **not** restore any Redis dump file (`.rdb` / `.aof`).
3. Start `arena-api` — all cache entries are repopulated on first access.
4. Distributed locks in flight at backup time are gone; any transactions that held
   them will time out and be retried by the retry logic in the worker.

### Why we do NOT backup Redis

- **Consistency risk:** A Redis snapshot from backup time contains locks that
  reference PostgreSQL rows that may have changed (or been rolled back) since
  the snapshot.  Restoring a Redis snapshot against a PostgreSQL restore from a
  different point in time would corrupt the consistency model.
- **Operational complexity:** Synchronising two independent point-in-time snapshots
  (PostgreSQL + Redis) to the exact same moment is fragile and not worth it for
  purely ephemeral state.

---

## 9. Staging Dry-Run Procedure

> **Goal:** Verify the backup → restore → boot cycle before you need it in
> production.  Run this monthly, or before any major release.

### Automated dry-run (recommended)

```bash
# Build the arena image first
docker compose build

# Run the full dry-run
ARENA_IMAGE=arena_new/arena-api:dev deploy/staging-dryrun.sh
```

**Expected output:**

```
✅ Source database started
✅ Migrations applied to source
✅ Test organization seeded: DRYRUN_TEST_ORG
✅ Backup created: /tmp/arena-dryrun-XXXXX/arena_20260623T020000Z.dump
✅ Target database started
✅ Restore complete
✅ Migrations applied to restore target (idempotent)
✅ Seeded row found in restored database (count=1)
✅ /healthz → 200 OK
✅ /readyz → 200 OK (DB reachable)
✅ ALL CHECKS PASSED — backup/restore cycle verified
```

### Manual staging dry-run checklist

Use this when automated dry-run is not available:

- [ ] `deploy/backup.sh --dry-run` completes without errors
- [ ] A real backup: `deploy/backup.sh --dump-only` completes and produces a
  `.dump` file with a matching `.sha256` file
- [ ] `sha256sum --check arena_*.dump.sha256` passes
- [ ] `deploy/restore.sh --dump-file arena_*.dump` completes without errors
  on a staging database
- [ ] `arena-migrate up` runs successfully on restored database (0 errors)
- [ ] `/healthz` returns 200 after starting arena-api against restored DB
- [ ] `/readyz` returns 200 (proves DB is reachable and schema is valid)
- [ ] A known entity (e.g., created during backup source seeding) is retrievable
  via the API against the restored database

---

## 10. Monitoring & Alerting

Add the following checks to your monitoring stack (e.g., Prometheus + AlertManager,
UptimeRobot, or Grafana):

| Check | Alert threshold | Suggested tool |
|---|---|---|
| Last successful backup age | > 26 hours | `find` + cron wrapper that exits non-zero on failure |
| Backup file size anomaly | < 10% of previous day's size | Shell comparison in backup.sh |
| PostgreSQL WAL lag (if PITR enabled) | > 5 minutes behind | Prometheus `pg_stat_archiver` exporter |
| Restore test last run | > 35 days ago | Alert from CI/cron test results |

### Recommended cron alerts

```bash
# Add to backup.sh exit trap — posts a PagerDuty/Slack alert on failure:
on_failure() {
  curl -s -X POST "${SLACK_WEBHOOK:-}" \
    -H 'Content-type: application/json' \
    --data "{\"text\":\"❌ Arena nightly backup FAILED on $(hostname) at $(date -u)\"}"
}
trap on_failure ERR
```

---

## 11. Verification Checklist

Run this checklist after every restore (production or staging):

### Post-restore verification

- [ ] `sha256sum --check` passed on the dump file before restore
- [ ] `pg_restore` exited 0 with no errors
- [ ] `arena-migrate up` applied 0 new migrations (schema was current) **or**
  applied expected delta migrations (if restoring to older version)
- [ ] `/healthz` → `200 OK`
- [ ] `/readyz` → `200 OK`
- [ ] Redis flushed / restarted with empty state
- [ ] Application logs show no `ERROR` lines within the first 60 seconds of boot
- [ ] Spot-check: At least one entity (organization, venue, etc.) is queryable via
  the API and returns expected data
- [ ] If PITR: Recovery log shows `LOG: recovery stopping before commit of transaction`
  at the expected target timestamp

### RPO validation

Record the following after each restore:

| Field | Value |
|---|---|
| Failure/incident timestamp | |
| Backup dump timestamp used | |
| Actual data loss window (delta) | |
| Target RPO (< 24 h for Tier 1) | Met / Not Met |

### RTO validation

| Field | Value |
|---|---|
| Incident detected at | |
| Restore started at | |
| Application back online at | |
| Total downtime | |
| Target RTO (< 60 min for Tier 1) | Met / Not Met |
