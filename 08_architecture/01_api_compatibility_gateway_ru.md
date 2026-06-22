# Bil24-Compatible API Gateway

Updated: 2026-06-21

## Goal

Existing integrations must be able to switch from the old Bil24 endpoint to the new platform endpoint with minimal or no business-logic changes. WordPress/Vino&Co is the main reference case: the domain and credentials may change, but the command flow and expected response structures should remain compatible.

The new platform core remains independent. Compatibility is implemented as a gateway/facade layer.

```text
Existing WordPress / widget / partner client
        |
        | JSON POST: { command, fid, token, locale, ... }
        v
Bil24-Compatible API Gateway
        |
        | command adapter + validation + response mapper
        v
New Platform Core APIs / Services
```

## Compatibility Levels

### Level 1: Wire Compatibility

The old client can send the same HTTP request shape:

- POST JSON.
- `command` field selects the operation.
- `fid` identifies the frontend/interface.
- `token` authenticates that interface.
- `locale` controls localized text where relevant.

The response keeps the Bil24-style envelope:

```json
{
  "resultCode": 0,
  "description": "OK",
  "command": "GET_ALL_ACTIONS"
}
```

### Level 2: Semantic Compatibility

The same command sequence must produce the same business result:

```text
GET_ALL_ACTIONS
GET_SEAT_LIST / GET_CART
RESERVATION
ADD_PROMO_CODES
CREATE_ORDER_EXT
GET_ORDER_INFO
CANCEL_ORDER / refund commands where needed
```

The gateway maps these commands to the new domain objects:

```text
actionId        -> event.id or compatibility_external_id
actionEventId   -> event_session.id
venueId         -> venue.id
seatId          -> seat.id
categoryPriceId -> ticket_type / inventory_class id
orderId         -> order.id
ticketId        -> ticket.id
fid             -> sales_channel / frontend id
```

### Level 3: Response Shape Compatibility

Fields used by old clients must be preserved even if the internal names are different:

- `actionList`
- `actionEventList`
- `firstEventDate`
- `bigPosterUrl`
- `smallPosterUrl`
- `currency`
- `sum`
- `discount`
- `charge`
- `totalSum`
- `statusExtStr`
- `statusExtInt`

All ID-like values should be returned safely for JavaScript clients. Prefer strings in new APIs; for strict legacy compatibility, support the numeric-looking format expected by existing clients and validate this with contract tests.

## Important Bil24 Behaviors To Preserve Or Intentionally Improve

### Financial Fields

The compatibility API must preserve Bil24's financial field semantics:

```text
sum      = base ticket amount before discounts and service charges
discount = discount amount
charge   = service charge
totalSum = sum - discount + charge
```

Existing integrations such as WooCommerce rely on `totalSum` as the payment total. The gateway must not recalculate this inconsistently from cart line items.

Source memory: `concepts/bil24-financial-fields-structure`.

### Promo Code Flow

Existing Bil24 behavior is sensitive to command order:

```text
RESERVATION -> ADD_PROMO_CODES -> CREATE_ORDER_EXT
```

For compatibility, old integrations should still work with this order. Internally, the new platform may implement a safer promo engine, but the gateway must return compatible responses for current clients.

Source memory: `concepts/bil24-promo-code-per-session-limitation`.

### Ticket Data

The old `GET_ORDER_INFO` limitation is known: it does not return `ticketList`. For compatibility, we can choose one of two policies:

1. Strict mode: match old behavior for clients that expect only order-level data.
2. Enhanced mode: optionally include ticket-level data for clients migrated to the new platform.

Default for drop-in migration should be strict mode unless a client is explicitly upgraded.

Source memory: `concepts/bil24-get-order-info-ticketlist-limitation`.

## Architecture Rules

1. The compatibility gateway is an adapter, not the platform core.
2. Internal services must not use Bil24 command names as their primary API.
3. Every command adapter must have contract tests based on captured real requests and expected responses.
4. Every legacy `fid/token` must map to a new `sales_channel`, `organization`, and permission scope.
5. Compatibility mode must be configurable per client/channel.
6. Response mappers must be versioned because legacy clients may depend on specific field quirks.
7. Unknown commands must be logged with full payload and return Bil24-style errors.

## Cutover Strategy

1. Collect fixtures from current integrations:
   - request JSON
   - response JSON
   - command order
   - edge cases: promo, service charge, failed payment, cancelled order, sold-out inventory
2. Build compatibility contract tests before implementation.
3. Run shadow mode:
   - old client continues using Bil24
   - same request is replayed against the new gateway in non-mutating mode where possible
   - compare response shape and core values
4. Switch a test frontend/fid to the new endpoint.
5. Switch production domain/endpoint when order, payment, ticket, and webhook flows pass.
6. Keep rollback by endpoint configuration, not code rollback.

## Recommended Endpoint Shape

Support at least one drop-in endpoint:

```text
POST /json
POST /api/bil24/json
```

The exact public URL can differ by domain, but the request body contract must remain stable.

## Open Questions

1. Which exact Bil24 commands are used by Vino&Co today?
2. Which response fields are actually consumed by the WordPress plugin?
3. Does the current WordPress flow call `PAY_ORDER`, or does payment confirmation happen through a separate provider/webhook?
4. Which commands must be strict-compatible and which can be enhanced?
5. Do we need legacy numeric IDs in responses, or can existing clients accept string IDs?

