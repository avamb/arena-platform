# Bil24 Compatibility — Behavior Differences (arena vs. legacy Bil24.pro)

Feature #450 (W1-0) rewrites this file from
`08_architecture/18_bil24_compat_wave1_specification_ru.md`. All numbered
sections below reference the spec; when the spec changes, this file must
change with it.

## Authority

- `apps/backend/tests/compat/bil24/testdata/wp/requests/<COMMAND>/<case>.json`
  is the wire-format request the legacy PHP plugin actually sends.
- `apps/backend/tests/compat/bil24/testdata/wp/golden/<COMMAND>/<case>.json`
  is the target response after arena implements the command. STRICT key-set
  comparison — a missing OR extra key fails the harness (spec §15.2). Never
  edit a golden file to make a test pass.
- Placeholders resolved by the harness: `{{actionEventId}}`,
  `{{seatId:Parter-3-12}}`, `{{orderId}}`, `{{now+ttl}}`, `{{sessionId}}`,
  `{{token}}`.

## Command inventory (spec §7)

15 commands supported by the gateway:

`GET_ALL_ACTIONS`, `GET_SEAT_LIST`, `CREATE_USER`, `RESERVATION`,
`GET_CART`, `ADD_PROMO_CODES`, `CHECK_KDP`, `CREATE_ORDER_EXT`,
`GET_ORDER_INFO`, `PAY_ORDER`, `GET_TICKETS_BY_ORDER`,
`SEND_TICKETS_TO_EMAIL`, `CANCEL_RESERVATION`, `CANCEL_ORDER`,
`SCAN_TICKET`.

`RESERVATION` has **4 request sub-shapes**:

1. `type=RESERVE|UN_RESERVE` with `seatList[]` — assigned-seats event.
2. `type=RESERVE|UN_RESERVE` with `categoryList[]` (GA units, quantity per
   `categoryPriceId` + optional `tariffPlanId`).
3. `type=UN_RESERVE_ALL` — **no `actionEventId`, no seatList, no
   categoryList**; the server clears every hold in `sessionId`.
4. Implicit reconciliation on `CREATE_ORDER_EXT`: any `lines[]` entry not
   already held is reserved as part of order creation.

## Behavior differences from legacy Bil24.pro

### 1. `CREATE_ORDER_EXT.orderId` accepts string OR int

Legacy Bil24 required a numeric `orderId`. WooCommerce sends the WC order id
which is a string like `"wc_string_2002"` for some site templates. The
gateway now accepts both; on the wire the response echoes
`externalOrderId` as the request value stringified. See
`testdata/wp/requests/CREATE_ORDER_EXT/string_orderid.json`.

### 2. `RESERVATION.seatList` is the **entire session cart**, not the delta

Legacy responses returned only the seats affected by the current call. The
arena implementation must return every seat currently held in
`sessionId` across every event session — the WP plugin recomputes cart
totals from this snapshot on every mutation.

### 3. `ADD_PROMO_CODES` accepts both `promoCodeList` and `promoCodes`

The PHP plugin sends both keys with identical values because different site
templates use different key names. The server must accept either
independently and merge into a single set before validation.

### 4. `UN_RESERVE_ALL` carries **no** `actionEventId`

Unlike other RESERVATION sub-shapes, `UN_RESERVE_ALL` releases every held
seat/unit across every event session for the gateway `sessionId`.

### 5. `resultCode = -2` (invalid request) never becomes HTTP 4xx

Legacy clients treat any HTTP status other than 200 as a transport failure
and re-queue the request. Malformed JSON, missing envelope keys and
unsupported HTTP methods therefore return `HTTP 200` with `resultCode=-2`
in the JSON envelope. `HTTP 404` is reserved for "compat gateway disabled"
(feature toggle off).

### 6. `resultCode = -1` means "transient failure, retry"

Feature #477 realigned the code map to spec section 6: `-1` now denotes a
retry-able transient failure (DB/pool errors, statement timeouts, worker
deadlocks). Unknown command names moved to `-2` (invalid request); the
`ResultCodeUnknownCommand` symbol is retained as a deprecated alias for
`ResultCodeInvalidRequest`.

Regression `TestCompatBil24_158_CommandDispatchIsNotUnknown` still guards
against dispatch drops but now matches on the `"unknown command"`
description substring rather than the raw code, since `-2` is also
emitted for genuine validation failures.

### 7. `fid` is an **integer** on the wire

Feature #451 pins `fid` to `sales_channels.display_number` (int64). The PHP
plugin sends `(int) $fid`; arena parses both `123` and `"123"` and rejects
anything else with `resultCode=-2`. Cross-channel access uses the token —
tenant isolation is enforced at the fid→org resolution step, not by the
caller-supplied `orgId` (which does not exist on the wire).

### 8. Money is a JSON `number` with ≤2 decimal places

The gateway must never emit currency strings, cents integers, or trailing
zeros beyond 2 decimals. `sum − discount + charge = totalSum` is a
platform-side invariant computed from the persisted cart, not accepted
from the client.

### 9. Show times are venue-local without offset; TTL fields carry offset

`actionEvent.showTime` = `"2028-02-15T19:00:00"` (no timezone) in the venue
timezone. Everything TTL-bound (`sellEndTime`, `expiration`,
`cartTimeout`, `refundDate`, `processing`) is RFC3339 with an offset,
`+01:00` for Prague, `+03:00` for Jerusalem (DST-aware).

### 10. One active promo code per session

`ADD_PROMO_CODES` enforces one active promo per `sessionId` at platform
level. Attempts to add a second are rejected via `errorPromoCodeList`;
`existPromoCodeList` reports codes that were already active.

### 11. One open order per session

`CREATE_ORDER_EXT` is idempotent per `(sessionId, actionEventId)` — a
repeated call with the same body must return the same `orderId`, not
create a second row.

### 12. `SCAN_TICKET.ticketId` accepts EAN-13 barcode OR internal UUID

The scanner apps use EAN-13; admin tooling passes the internal
`tickets.id` UUID. The gateway resolves both against `barcodes` with
`authority='platform'` for EAN-13 issued by arena, or
`authority='legacy_bil24'` for barcodes imported by feature #461. Rows are
org-scoped so a scan for another tenant's ticket returns `resultCode=101`,
not the ticket.

### 13. `image?type=seatingPlan` returns `sbt/1.0` XML (spec §8)

Not SVG in the general sense — it is the schema the WP plugin ships with:
`xmlns:sbt="http://www.w3.org/2015/sbt/1.0"`, `<metadata><sbt:category/>`,
`<g sbt:sect><g sbt:row><circle sbt:id sbt:state sbt:cat sbt:seat cx cy r/>`.
`sbt:cat` is the category **index**, not id. ETag is
`<geometry_checksum>:<seat_status_version>` and honours 304.
See `testdata/wp/svg/palac_akropolis.sbt.svg` for the on-disk skeleton.

### 14. Webhooks to WordPress use the `{type,data}` envelope

Spec §7/§9: POST `application/json` `{type, data}`. Empty `type` or empty
`data` object → `400 {"ok":false,"error":"..."}`. Any other body →
`200 {"ok":true}`. `ticket.refunded` is deduplicated by `data.id`. The
receiver stores accepted payloads to `bil24_tickets`. The stub replay
lives in `apps/backend/tests/compat/bil24/wpstub/`.

### 15. Order/Ticket JSON binding: 36/17/14 keys (spec §9.3)

`bil24wire` (feature #463) encoder must emit EXACTLY:

- 36 keys at the Order top level.
- 17 keys per Ticket in `order.ticketList[]`.
- 14 keys per `ticket.actionEvent`.

`TestCompatBil24_450_PseudonymizedFixture_KeySets` documents these
inventories; feature #468 regenerates the pseudonymized fixture from the
internal projection to un-skip that test.

## Rules for this file

1. New behaviour differences MUST be added here with a spec section reference.
2. Removing a difference (i.e. arena matches legacy again) requires a code
   change + spec update in the same commit.
3. This file is a `TestCompatBil24_158_BehaviorDifferencesDocExists`
   dependency — it must exist and be non-trivial.
