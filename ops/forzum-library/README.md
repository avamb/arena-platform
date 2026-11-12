# Forzum hall-plan library

Tools that copy the hall plans of the old Bil24/Forzum system into arena.
The platform owner runs them. Nothing here holds a secret, client data or a
downloaded plan: the repository is public.

| Step | Tool | Reads | Writes |
|---|---|---|---|
| 1-2 inventory | `inventory.py` | Forzum API | `<lib>/inventory/` |
| 3 download | `download.py` | Forzum API, inventory | `<lib>/plans/` |
| 3 Editor files | `ingest_editor.py` | `<lib>/editor-export/` | `<lib>/plans/` |
| 4 convert | `go run ./apps/backend/cmd/arena-plan-restyle --lib <lib>` (or `--plan <folder>`) | `<lib>/plans/` | `house.svg`, `convert.json` next to each plan |
| time zones | `go run ./apps/backend/cmd/arena-geotz-lookup` | `<lib>/load/venues-zone-request.json` | `<lib>/load/venues-zones.json` |
| 4 contact sheet | `contact_sheet.py` | `<lib>/plans/` | `<lib>/contact-sheet.html` |
| 5 load | `loader.py` | `<lib>/plans/`, arena API | arena venues and plans, `<lib>/load/` |

`<lib>` is the library folder, default `C:\Arena\forzum-library`
(`--lib` or `FORZUM_LIBRARY_DIR`).

## Rules

- Read-only on the Forzum side. `CREATE_USER` is never sent: it writes a
  buyer record.
- The token is read from a file at call time: `--token-file` or
  `FORZUM_TOKEN_FILE`, default `C:\Arena\forzum_token.txt`. It is never
  printed, logged or written to disk. A saved URL or request never holds it.
- Calls are sequential, with a pause (`--pause-ms`, default 400) and a
  growing wait after an error (5 tries, then the call is recorded as failed
  and the run goes on).
- Every tool can be stopped and started again. It skips what is already on
  disk.
- Russia is excluded. Test plans (name has `test` or `тест`) are skipped.
- The loader talks to one arena server, named on the command line. It asks
  for the operator's e-mail and password in the terminal. Staging first.

## Forzum API

`POST <api>` with JSON `{"command": ..., "fid": ..., "token": ..., "locale": ...}`.
Default API `https://api.forzum.ru/json`, FID 2558. An answer with
`resultCode` other than 0 is an error. The plan picture is
`GET <host>/image?type=seatingPlan&actionEventId=<id>&userId=0&fid=<fid>&locale=<locale>`
(no token in the URL).

The API has no command that lists the plans of a venue. A plan is seen only
through a session: `GET_ALL_ACTIONS` and `GET_ACTION_EXT` carry
`seatingPlanId`, `seatingPlanName` and `placementUrl` for each session.

The old system has no open session now, so these commands are not expected
to reach the plans. The inventory still lists countries, cities and venues,
and `inventory.py --probe-action-event <id>` asks once for a closed session
(`GET_ACTION_EXT`, `GET_SCHEMA`, `GET_SEAT_LIST`, the plan picture,
`GET_FILTER`) and writes what answered to `inventory/probe.json`.

`--api` may be given more than once. The default order is
`https://api.forzum.ru/json`, then `https://api.bil24.pro/json`. The first
address that answers `GET_COUNTRIES` with `resultCode` 0 is used for the
whole run and is written to `summary.json` as `apiBase`.

## Plans saved from the Forzum Editor (main source)

The owner signs in to the Forzum Editor and saves the plan pictures as SVG
files into `<lib>/editor-export/`. The main layout is files named
`<venueId>_<seatingPlanId>.svg` and a manifest `<lib>/editor-export/manifest.jsonl`
with one JSON object per line:

```json
{"country": "Czechia", "city": "Karlovy Vary", "venueId": 7401, "venue name": "Thermal", "seatingPlanId": "P1", "plan name": "Big hall", "plan version": 3, "file": "7401_P1.svg"}
```

Key names ignore case, spaces, `_` and `-`. `file` may be left out when
`venueId` and `seatingPlanId` are given: the name is then `<venueId>_<seatingPlanId>.svg`.
A line whose file name ids differ from its `venueId` or `seatingPlanId` is not copied.
If there is no `manifest.jsonl`, the older `manifest.json` or `manifest.csv` is used.
Venue data comes from `inventory/venues.json` by `venueId`; without an inventory, or without a
`venueId`, the venue, city and country names must be in the manifest.
`ingest_editor.py` copies each file into the plan folder layout below as
`source.svg` with a `plan.json`. A file that is not in the manifest, a
manifest line without a file, a Russian venue and a test plan are listed in
`<lib>/editor-export/ingest-report.json` and not copied. A plan without
`seatingPlanId` gets the id `ed-<first 12 characters of the SHA-256>`.
`ingest_editor.py --draft-manifest` writes a manifest draft from the file
names for the owner to complete.

## Inventory files (`<lib>/inventory/`)

- `raw/<COMMAND>[_<ids>].json`: each answer as received.
- `countries.json`, `cities.json`, `venues.json`: lists with the ids and
  names of the API. A venue has `countryId`, `cityId`, `venueId`,
  `venueName`, `address`, `geoLat`, `geoLon`, `type`.
- `sessions.json`: one item per session: `actionEventId`, `actionId`,
  `actionName`, `day`, `countryId`, `cityId`, `venueId`, `venueName`,
  `seatingPlanId`, `seatingPlanName`, `placementUrl`.
- `plans.json`: one item per distinct `seatingPlanId`: the plan name, the
  venue, the list of `actionEventId` and `newestActionEventId`.
- `summary.json`: counts of countries, cities, venues, venues with a plan,
  distinct plans, sessions, failed calls.
- `state.json`: what is done, for a restart.

## Plan folder (`<lib>/plans/<country>/<city>/<venueId>/<seatingPlanId>/`)

Folder names of country and city are the API names made safe for a path,
with the id as a suffix (`Czechia_203/Karlovy-Vary_1234`).

- `source.svg`: the plan as Forzum serves it (sbt dialect).
- `schema.json`: the `GET_SCHEMA` answer: seat id, sector, row, number, x, y.
- `plan.json`: `seatingPlanId`, `seatingPlanName`, `venueId`, `venueName`,
  `address`, `geoLat`, `geoLon`, `countryId`, `countryName`, `cityId`,
  `cityName`, `actionEventId` used, `sourceUrl`, `sha256` of `source.svg`,
  `seatCount`, `fetchedAt`, `status` (`downloaded`, `skipped`, `failed`) and
  `reason`.
- `house.svg`, `convert.json`: written by the converter (below).

`<lib>/plans/manifest.json` lists every `plan.json`. Plans of one venue that
are the same hall keep the newest one (`status: downloaded`); the others
stay listed as `skipped` with `reason: "older version of <id>"`. Two plans
are the same hall when their sets of (sector, row, seat) are equal, or when
their names are equal after the version mark is removed.

## House style (`house.svg`)

The converter reads `source.svg` with arena's own importer:
`seating.ImportSBTSVG` for the sbt dialect that the API serves (it has
`sbt:sect` attributes), `seating.ImportSVG` for the authoring dialect that
the Editor saves (`inkscape:label="#..."` row groups). It writes one style
for every plan, in the convention that `seating.ImportSVG` reads:

- `<g id="PriceCategory">`: one swatch per category that has a seat. Names
  and colours come from the source. Prices are not copied.
- `<g id="Legend">`.
- One `<g inkscape:label="#Сектор <sector>">` per row, with `<title>` (row
  name) and one `<circle>` per seat. A seat has `<title>` (seat number), the
  category fill and `data-sector`, `data-row`, `data-seat`.
- `<g id="HouseDecor">`: the source backdrop without its legend, its price
  table and its own row numbers.
- `<g id="HouseLabels">`: a sector title above each sector, the row name at
  both ends of each row, the seat number inside each seat.

The converter never repairs a plan. `convert.json` says what happened:
`status` (`ok`, `needs_review`, `failed`), `reasons`, seat counts per
(sector, row) in the source, in `schema.json` and in `house.svg`, whether a
stage label was found in the source, the scale if the canvas was reduced to
the 2000 limit, importer warnings and errors. `ok` needs: `ImportSVG` and
`ValidateForPlanType("assigned_seats")` without errors, equal seat counts in
every row, equal seat positions, a stage label found, a viewBox that starts
at 0 0, seat counts equal to `schema.json` when it is there, and no label
that had to be dropped. A `failed` plan has no `house.svg`.

Known flaw of arena's importers: a sector or row key keeps only `a-z0-9`, so
names that differ only in other characters (`Партер` and `Балкон`, `7` and
`*7`, `VYCHOD` and `Vychod`) are merged into one. The converter counts the
names in the source itself and marks such a plan `failed` with the reason
`importer merged sectors or rows`. The check is strict: it also stops a plan
when its own count and the importer's count differ in the other direction.

## Loader

Gates of every mode that talks to a server: `--api <base URL>` must be
https (plain http only for `localhost` or `127.0.0.1`); the host must have
`staging` in its name unless `--production`; a real run needs
`--i-have-the-owners-go-ahead`, `--dry-run` does not. `--org` is the
organization UUID. The e-mail and password are asked at run time. A dry run
logs in and reads, and sends no POST or PATCH other than the login.

Plans: `loader.py plans --api <base URL> --org <uuid> [--dry-run]` (the bare
form without `plans` works the same) takes every plan with `convert.json`
status `ok`, finds or creates the venue under the organization and uploads
`house.svg` as an unverified plan. A venue is matched by name and city. Zero
matches: create. One: reuse. More than one, or the same name in another city:
the plan is not loaded and goes to `<lib>/load/ambiguous.json`.
`<lib>/load/log.json` records each plan, keyed by the plan folder path
relative to `<lib>/plans`, so a second run does not load a plan twice.

Venues: loads every venue of `inventory/venues.json`, with or without a plan,
under one organization.

- `loader.py venues-report --lib <lib>` is offline (no login, no network).
  It writes `<lib>/load/venues-report.json` and
  `<lib>/load/venues-zone-request.json` (one item per distinct city: `key` =
  cityId, `iso2`, `city`) and prints the counts: venues, duplicates inside the
  inventory (same normalised name and city: ambiguous, never merged), venues
  without coordinates or address, distinct cities, countries with no ISO2
  code, cities with a time zone resolved, unresolved or not looked up yet.
- Time zones come from `<lib>/load/venues-zones.json`. Make it with
  `go run ./apps/backend/cmd/arena-geotz-lookup --in <lib>/load/venues-zone-request.json --out <lib>/load/venues-zones.json`.
- `loader.py venues --api <base URL> --org <uuid> [--dry-run] [--no-mark-verified]`.
  The country name becomes an ISO2 code through a table in `loader.py`; an
  unknown name skips the venue. The city is found in `GET /v1/geo/cities` or
  created through `POST /v1/organizations/{org}/cities` (idempotent by name).
  The time zone is the arena city's, else the zones file; with neither the
  venue is skipped as `timezone unresolved`. A venue is matched by name and
  city as in the plans mode: 0 create, 1 in the same city reuse, else
  ambiguous and not created. The venue is created with name, city, country,
  time zone, address and valid coordinates. The create route has no venue type
  field, so the type is never sent (all 171 are null today).
- A venue that this run created or matched gets `review_status` `verified`
  through `PATCH /v1/admin/venues/{id}/review`: the owner checked these
  venues by hand. `--no-mark-verified` turns this off. A plan is never
  verified by the loader.
- Files: `<lib>/load/venues-log.json` (by Forzum `venueId`, a rerun creates
  nothing twice), `venues-ambiguous.json`, and for `--dry-run`
  `venues-dry-run.json` (would create, would match, ambiguous, cities to
  create, time zone unresolved).
