# Bil24 Import — Operator Runbook

**Feature #387** — One-time snapshot import of current Bil24 events as native arena_new catalog entries.

This runbook describes how to export events from the Bil24 admin UI and import
them into the arena_new database using the `arena-bil24-import` CLI tool.

## Live venue, city, and country import

Feature #405 adds a separate live API mode. It makes authenticated JSON-RPC
calls to `GET_COUNTRIES`, `GET_CITIES`, and `GET_VENUES`; it does not require
an event snapshot. Country and city registry values are created when absent.
Venues retain their `external_bil24_id`, street address, and WGS-84
coordinates. Re-running the same import is safe: unchanged rows are skipped.

Credentials are supplied at run time and never written to the database or
printed. Use a production HTTPS endpoint and keep the token in the operator's
secret store:

```bash
export BIL24_FID=1271
export BIL24_TOKEN='...'
export BIL24_API_URL=https://api.bil24.pro/json

arena-bil24-import --venues --org-id <arena-org-uuid> --dry-run
arena-bil24-import --venues --org-id <arena-org-uuid>
```

`--bil24-fid`, `--bil24-token`, and `--bil24-url` override these environment
variables. `--dry-run` authenticates and reads the source but makes no
database writes. A successful run prints country/city and imported/updated/
unchanged counts; a non-zero exit status means the source call or transaction
failed.

---

## Overview

`arena-bil24-import` is a one-shot operator tool that reads a Bil24 event
catalog export (CSV or JSON file) and materialises each event as a native
arena_new `events` row. It is **not** an ongoing sync — it is a single
migration step run once by ops when transitioning away from Bil24.

Key properties:

* **Idempotent** — re-running against the same file is a no-op
  (`ON CONFLICT (external_bil24_id) DO NOTHING`).
* **Batch-safe** — one malformed row is rejected with a logged error; the rest
  of the batch continues.
* **Operator-only** — never compiled into `arena-api` or `arena-worker`.
* **Audit-traceable** — every imported event retains its Bil24 source ID in
  the `events.external_bil24_id` column.
* **Format-flexible** — accepts both CSV (native Bil24 admin export) and JSON
  (useful for scripted transformations and test round-trips); auto-detected by
  file extension.

---

## Step 1 — Obtain the Bil24 export (CSV, recommended)

1. Log in to the Bil24 admin panel.
2. Navigate to **Events → Export**.
3. Choose **CSV** format and click **Download**.
4. Save the file with a `.csv` extension (e.g. `bil24_events_2026-07-18.csv`).
5. Transfer the file to the operator machine that has access to the production
   database.

The exported CSV **must** contain the following column names in its header row
(Bil24 admin exports them in this order by default):

| Column               | Required | Notes                                             |
|----------------------|----------|---------------------------------------------------|
| `external_bil24_id`  | ✅        | Bil24 event ID; used as the idempotency key       |
| `title`              | ✅        | Event display name → `events.name`                |
| `starts_at`          | ✅        | RFC 3339 timestamp → `events.start_at`            |
| `ends_at`            | ✗        | Defaults to `starts_at + 3 hours` when blank      |
| `venue_name`         | ✗        | Appended to description; no FK resolution done    |
| `description`        | ✗        | → `events.description`                            |
| `poster_url`         | ✗        | → `events.image_url`                              |

Extra columns in the CSV are silently ignored.

Example CSV:

```csv
external_bil24_id,title,starts_at,ends_at,venue_name,description,poster_url
12345,Rock Night 2026,2026-09-15T19:00:00Z,2026-09-15T22:30:00Z,Main Hall,Annual rock festival,https://cdn.example.com/events/12345.jpg
67890,Jazz Evening,2026-10-05T20:00:00Z,,Studio Stage,Intimate concert,
```

---

## Alternative: JSON format

If you prefer JSON (or need to include `price_tiers` metadata), use a `.json`
file. The importer auto-detects format by extension:

```json
[
  {
    "external_bil24_id": "12345",
    "title":             "Rock Night 2026",
    "starts_at":         "2026-09-15T19:00:00Z",
    "ends_at":           "2026-09-15T22:30:00Z",
    "venue_name":        "Main Hall",
    "description":       "Annual rock festival.",
    "poster_url":        "https://cdn.example.com/events/12345.jpg",
    "price_tiers": [
      {"name": "Standard", "price_kopeks": 150000},
      {"name": "VIP",      "price_kopeks": 350000}
    ]
  }
]
```

`price_tiers` are logged in the import summary but **not** written to the
database — arena_new pricing is managed through the native catalog API.

---

## Step 2 — Ensure the migration is applied

The importer relies on the `external_bil24_id` column added by migration
`0070_external_bil24_id.sql`. Run migrations before the first import:

```bash
DATABASE_URL=postgres://... arena-migrate up
```

Verify:

```sql
SELECT column_name FROM information_schema.columns
WHERE  table_name = 'events' AND column_name = 'external_bil24_id';
```

---

## Step 3 — Dry run (strongly recommended)

Always perform a dry run first to review what would be imported:

```bash
DATABASE_URL=postgres://user:pass@host:5432/arena?sslmode=verify-full \
  arena-bil24-import \
    --input   /path/to/bil24_events_2026-07-18.csv \
    --org-id  <arena_new-organization-uuid> \
    --dry-run
```

The output lists:

* Every row that would be inserted (with ID, title, venue, price tiers)
* Every row that would be rejected, with the specific validation error
* Total counts

No database changes are made during a dry run.

---

## Step 4 — Run the import

```bash
DATABASE_URL=postgres://user:pass@host:5432/arena?sslmode=verify-full \
  arena-bil24-import \
    --input   /path/to/bil24_events_2026-07-18.csv \
    --org-id  <arena_new-organization-uuid>
```

Or build the binary first and run it directly:

```bash
go build -o bin/arena-bil24-import ./apps/backend/cmd/arena-bil24-import
DATABASE_URL=postgres://... bin/arena-bil24-import \
  --input  /path/to/bil24_events_2026-07-18.csv \
  --org-id <uuid>
```

### Flags

| Flag        | Description                                                   |
|-------------|---------------------------------------------------------------|
| `--input`   | Path to the export file (`.csv` or `.json`) **(required)**    |
| `--org-id`  | UUID of the target arena_new organization **(required)**      |
| `--dry-run` | Print plan without touching the database                      |
| `--db-url`  | PostgreSQL DSN; overrides `DATABASE_URL` env var              |

### Example output

```
arena-bil24-import summary
  imported: 42
  skipped (already present): 0
  rejected (validation failed): 1
    row[7] id="": external_bil24_id is required; title is required; starts_at is required
```

Exit code is **0** on success (even if all rows were skipped); **1** on fatal error.

---

## Step 5 — Verify

After the import, confirm rows in the database:

```sql
SELECT id, name, start_at, end_at, external_bil24_id
FROM   events
WHERE  external_bil24_id IS NOT NULL
ORDER  BY start_at;
```

Expected: N rows matching the number of valid events in the source file.

---

## Re-running (idempotency)

Running the importer a second time against the same source file is completely
safe:

```
arena-bil24-import summary
  imported: 0
  skipped (already present): 42
  rejected (validation failed): 0
```

All rows hit the `ON CONFLICT (external_bil24_id) DO NOTHING` clause and are
counted as "skipped".

---

## Credential handling

* `DATABASE_URL` must point at the **production** database.
  Always use `sslmode=verify-full` for production connections.
* The importer binary has **no knowledge** of Bil24 credentials. There is no
  network call to any Bil24 API endpoint inside `arena-bil24-import`.
* Keep the exported CSV/JSON file on a secure, operator-controlled machine.
  Delete it after the import is complete.

---

## Build isolation

`arena-bil24-import` is compiled as a separate binary in
`cmd/arena-bil24-import/`. The `arena-api` and `arena-worker` binaries have no
dependency on this package. Verify at any time:

```bash
grep -r "arena-bil24-import" \
  apps/backend/cmd/arena-api \
  apps/backend/cmd/arena-worker
# must return no output
```

---

## Schema reference

Migration `0070_external_bil24_id.sql` adds:

```sql
ALTER TABLE events ADD COLUMN external_bil24_id TEXT;
CREATE UNIQUE INDEX events_external_bil24_id_uidx
    ON events (external_bil24_id)
    WHERE external_bil24_id IS NOT NULL;
```

Events without a Bil24 origin have `external_bil24_id = NULL` and are not
affected by the unique index.
