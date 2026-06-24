# Bil24 Compatibility Gateway — Behavior Differences from Legacy Bil24.pro API

This document records **intentional differences** between the Arena platform's
Bil24 compatibility gateway (`/compat/bil24/json`) and the original Bil24.pro
API (`https://api.bil24.pro/json`). The Vino&Co integration relies on this
gateway; these differences must be understood before the migration is complete.

---

## Summary Table

| Behavior | Legacy Bil24.pro | Arena Gateway | Impact |
|---|---|---|---|
| HTTP status on protocol error | 200 | 200 ✅ | None — same |
| HTTP status on application error | 200 | 200 ✅ | None — same |
| Result code for unknown command | -1 | -1 ✅ | None — same |
| Result code for invalid input | -2 | -2 ✅ | None — same |
| Result code for not found | -3 | -3 ✅ | None — same |
| Result code for internal error | -99 | -99 ✅ | None — same |
| Result code for success | 0 | 0 ✅ | None — same |
| Command echo in response | ✅ | ✅ | None — same |
| `fid` / `token` field type | Ulong (numeric) | String (JSON) | Low — JSON coerces OK |
| Legacy numeric IDs (actionId, etc.) | Resolved via catalog | Returns -2 (not found) | **Medium** — ID translation table not yet implemented |
| `GET_ACTIONS_V2` command | Supported | Returns -1 (unknown) | **Medium** — use `GET_ALL_ACTIONS` instead |
| `CREATE_ORDER_EXT` real order creation | Full checkout flow | Scaffold stub (orderId="pending") | **High** — returns placeholder until checkout flow is wired |
| `CANCEL_ORDER` real cancellation | Cancels checkout | Scaffold stub (no DB change) | **High** — returns OK without actually cancelling |
| `GET_ORDER_INFO` financial fields | Legacy currency codes | Platform currency codes | Low — both use ISO 4217 strings |
| `GET_SEAT_LIST` seat availability | Per-seat status | Per-tier availability count | Medium — arena does not model individual seats yet |
| `SCAN_TICKET` barcode resolution | Numeric ticket IDs | Requires `legacy_bil24` barcode authority | **High** — barcode authority must be seeded before scanning works |
| Authentication via fid/token | Validated per request | Not validated (scaffold) | Medium — scaffold trusts any fid/token value |
| Locale format accepted | `ru-RU` (BCP 47) | Any non-empty string | Low — gateway uses locale as-is for DB query |

---

## Detailed Differences

### 1. Legacy Numeric ID Translation (HIGH IMPACT)

**Bil24.pro behavior:** `actionId`, `actionEventId`, `orderId`, `ticketId` are
all **opaque integers** (e.g., `4827361`, `779234`). The legacy server resolves
them via its internal catalog.

**Arena gateway behavior:** The `TranslateLegacyID()` function accepts numeric
IDs but immediately returns `ErrLegacyIDNotFound` (`resultCode=-2`) because
the `compatibility_id_map` table is not yet implemented.

**Migration path:** When a Vino&Co request arrives with a legacy numeric ID:
1. The gateway returns `-2` with description "must be a valid ... identifier".
2. The Vino&Co client must be updated to send platform UUIDs.
3. Alternatively, implement the `compatibility_id_map` DB table to translate
   legacy IDs → platform UUIDs.

**Commands affected:** `GET_SEAT_LIST`, `GET_ORDER_INFO`, `CANCEL_ORDER`.

---

### 2. CREATE_ORDER_EXT — Scaffold Stub (HIGH IMPACT)

**Bil24.pro behavior:** Full checkout creation: reserves seats, prices the
order, links it to a userId/sessionId, returns a real `orderId`.

**Arena gateway behavior:** Returns a scaffold response:
```json
{
  "resultCode": 0,
  "command": "CREATE_ORDER_EXT",
  "orderId": "pending",
  "status": "scaffold_stub",
  "message": "order creation requires reservation flow; use POST /v1/checkout/reservations"
}
```

The `orderId: "pending"` is **not a real order**. No record is created in the
database.

**Migration path:** Implement the full reservation flow (features #131, #129,
#132) and wire `CREATE_ORDER_EXT` to `POST /v1/checkout/reservations`.

---

### 3. CANCEL_ORDER — Scaffold Stub (HIGH IMPACT)

**Bil24.pro behavior:** Cancels the checkout session, releases reserved seats,
may trigger a refund.

**Arena gateway behavior:** Validates the `orderId` UUID format, then returns a
scaffold response without touching the database:
```json
{
  "resultCode": 0,
  "command": "CANCEL_ORDER",
  "orderId": "<uuid>",
  "status": "scaffold_stub",
  "message": "cancellation requires checkout state machine; use POST /v1/checkout/{id}/cancel"
}
```

**Migration path:** Wire to the checkout state machine once the checkout cancel
endpoint is implemented.

---

### 4. SCAN_TICKET — Requires Barcode Authority Setup (HIGH IMPACT)

**Bil24.pro behavior:** Accepts any numeric barcode registered in the Bil24
catalog; validates and marks as scanned.

**Arena gateway behavior:** Looks up the `legacy_bil24` barcode authority in
the `barcode_authorities` table. If this record does not exist:
```json
{
  "resultCode": -3,
  "description": "legacy_bil24 barcode authority not registered..."
}
```

**Setup required before Vino&Co can use SCAN_TICKET:**
```http
POST /v1/barcodes/authorities
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "name": "Bil24 Legacy Scanner",
  "authority_type": "legacy_bil24",
  "config": {}
}
```

---

### 5. GET_ACTIONS_V2 Not Supported

**Bil24.pro behavior:** `GET_ACTIONS_V2` returns an extended catalog with
genre lists, min/max prices, and rich metadata.

**Arena gateway behavior:** Returns `resultCode=-1` (unknown command). Use
`GET_ALL_ACTIONS` instead, which returns similar catalog data.

**Migration path:** Add `GET_ACTIONS_V2` as an alias for `GET_ALL_ACTIONS`
if Vino&Co cannot be updated to use `GET_ALL_ACTIONS`.

---

### 6. fid/token Authentication Not Validated

**Bil24.pro behavior:** Every request's `fid` and `token` are validated against
the registered interface credentials. Invalid credentials return an error.

**Arena gateway behavior (scaffold):** The `fid` and `token` fields are
**logged but not validated**. Any value (or no value) is accepted.

**Security implication:** The `/compat/bil24/*` subtree is only mounted when
`BIL24_COMPAT_ENABLED=true` (env var, default false). In production,
ensure this is disabled unless you have implemented fid/token validation.

---

## Vino&Co Migration Checklist

Before Vino&Co can fully migrate from Bil24.pro to the Arena gateway:

- [ ] Update Vino&Co client to send UUID `actionEventId` instead of numeric
      Bil24 event IDs (or implement `compatibility_id_map` table)
- [ ] Implement `CREATE_ORDER_EXT` → real checkout reservation flow
- [ ] Implement `CANCEL_ORDER` → checkout state machine cancel
- [ ] Seed the `legacy_bil24` barcode authority in the database
- [ ] Implement `fid`/`token` validation middleware
- [ ] Set `BIL24_COMPAT_ENABLED=true` in Vino&Co-facing deployment

---

*Last updated: 2026-06-24 by feature #158 (Bil24 compatibility regression tests)*
