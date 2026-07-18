# Bil24 One-Shot Event Import

**Feature #386** — One-time snapshot import of current Bil24 events as native arena_new catalog entries.

## Overview

`arena-bil24-import` is a one-shot operator tool that reads a Bil24 event
catalog export (JSON file) and materialises each event as a native arena_new
`events` row. It is **not** an ongoing sync — it is a single migration step
run once by ops when transitioning away from Bil24.

Key properties:

* **Idempotent** — re-running against the same file is a no-op
  (`ON CONFLICT (external_bil24_id) DO NOTHING`).
* **Batch-safe** — one malformed row is rejected with a logged error; the rest
  of the batch continues.
* **Operator-only** — never compiled into `arena-api` or `arena-worker`.
* **Audit-traceable** — every imported event retains its Bil24 source ID in
  the `events.external_bil24_id` column.

---

## Step 1 — Obtain the Bil24 export

Choose one of the two supported source variants:

### Variant A — Export file from Bil24 admin (recommended)

1. Log in to the Bil24 admin panel.
2. Navigate to **Events → Export → JSON**.
3. Download the file (e.g. `bil24_events_2026-07-18.json`).
4. Transfer the file to the operator machine that has access to the
   production database.

### Variant B — Read-only HTTP pull from Bil24 API

Run **once** from an operator workstation (not from the production server):

```bash
# Replace <TENANT_TOKEN> and <TENANT_ID> with current Bil24 credentials.
curl -s -H "Authorization: Bearer <TENANT_TOKEN>" \
  "https://api.bil24.pro/v1/tenants/<TENANT_ID>/events?limit=1000" \
  -o bil24_export.json
```

The downloaded JSON may need to be reformatted to match the import schema
(see **Source format** below). A simple `jq` transform is usually sufficient.

---

## Step 2 — Prepare the JSON source file

The importer expects an array of objects at the top level:

```json
[
  {
    "external_bil24_id": "12345",
    "title":             "Rock Night 2026",
    "starts_at":         "2026-09-15T19:00:00Z",
    "ends_at":           "2026-09-15T22:30:00Z",
    "venue_name":        "Main Hall",
    "description":       "Annual rock festival.",
    "poster_url":        "https://cdn.bil24.pro/events/12345.jpg",
    "price_tiers": [
      {"name": "Standard", "price_kopeks": 150000},
      {"name": "VIP",      "price_kopeks": 350000}
    ]
  }
]
```

| Field               | Required | Notes                                                         |
|---------------------|----------|---------------------------------------------------------------|
| `external_bil24_id` | ✅        | Must be unique within the file; used as idempotency key       |
| `title`             | ✅        | Maps to `events.name`                                         |
| `starts_at`         | ✅        | RFC 3339 timestamp; maps to `events.start_at`                 |
| `ends_at`           | ✗        | Defaults to `starts_at + 3 hours` when absent                 |
| `venue_name`        | ✗        | Appended to description; no venue_id FK resolution performed  |
| `description`       | ✗        | Maps to `events.description`                                  |
| `poster_url`        | ✗        | Maps to `events.image_url`                                    |
| `price_tiers`       | ✗        | Logged in summary only; no `ticket_tier` rows are created     |

---

## Step 3 — Dry run (recommended)

Always do a dry run first to review what would be imported:

```bash
DATABASE_URL=postgres://... \
  arena-bil24-import \
    --source  /path/to/bil24_export.json \
    --org-id  <arena_new-organization-uuid> \
    --dry-run
```

The output lists every row that would be inserted and every row that would
be rejected, with reasons. No database changes are made.

---

## Step 4 — Run the import

```bash
DATABASE_URL=postgres://... \
  arena-bil24-import \
    --source  /path/to/bil24_export.json \
    --org-id  <arena_new-organization-uuid>
```

Or build first and run the binary:

```bash
go build -o bin/arena-bil24-import ./apps/backend/cmd/arena-bil24-import
DATABASE_URL=postgres://... bin/arena-bil24-import \
  --source /path/to/bil24_export.json \
  --org-id <uuid>
```

### Flags

| Flag          | Description                                               |
|---------------|-----------------------------------------------------------|
| `--source`    | Path to the Bil24 JSON export file **(required)**         |
| `--org-id`    | UUID of the target arena_new organization **(required)**  |
| `--dry-run`   | Print plan without touching the DB                        |
| `--db-url`    | PostgreSQL DSN; overrides `DATABASE_URL` env var          |

---

## Step 5 — Verify

After the import, confirm rows in the database:

```sql
SELECT id, name, start_at, external_bil24_id
FROM   events
WHERE  external_bil24_id IS NOT NULL
ORDER  BY start_at;
```

Expected: N rows matching the number of valid events in the source file.

---

## Re-running

Running the importer a second time against the same source file is safe:

```
arena-bil24-import summary
  imported: 0
  skipped (already present): <N>
  rejected (validation failed): 0
```

---

## Schema

The importer relies on the `external_bil24_id` column added by migration
`0070_external_bil24_id.sql`. Ensure migrations are up to date before running:

```bash
arena-migrate up
```

---

## Credential handling

* `DATABASE_URL` must point at the **production** database.
  Use `sslmode=verify-full` for production connections.
* Bil24 API credentials (Variant B only) are used only during the one-time
  pull and must not be stored in the arena_new configuration.
* The importer binary itself has no knowledge of Bil24 credentials; the
  operator is responsible for the pull step.

---

## Isolation guarantee

`arena-bil24-import` is compiled as a separate binary in `cmd/arena-bil24-import/`.
The `arena-api` and `arena-worker` binaries have no dependency on this package.
Verify:

```bash
grep -r "arena-bil24-import" \
  apps/backend/cmd/arena-api \
  apps/backend/cmd/arena-worker
# must return no output
```
