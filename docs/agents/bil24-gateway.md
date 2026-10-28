# Bil24 compatibility gateway

Wire protocol of the /compat/bil24 gateway: money units, tokens, order lifecycle, harness traps. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **Bil24 wire money is MAJOR units; the DB is MINOR units** (spec
  `08_architecture/20_bil24_gateway_money_units_spec_ru.md`, features
  #528–#530). Every `sum`/`price`/`charge`/`discount`/`totalSum`/
  `refundPrice` on the `/compat/bil24/json` wire is a JSON **number** in
  major units with ≤2 decimals and no trailing zeros (`18.9`, not `18.90`);
  `orders.total`, `ticket_tiers.price_amount` etc. are `bigint` minor units.
  All arithmetic (channel `fee_percent`, discounts, cart sums) runs in minor
  units and the conversion happens **exactly once**, only in
  `internal/adapters/bil24compat/money` (`money.Major` to emit,
  `money.Minor` to consume). Never hand-roll `/ 100` or `* 100` in
  `hbil24`/`macs`/`bil24wire` — a static guardrail in `tests/staticanalysis`
  rejects it. PAY_ORDER compares `money.Minor(amount)` against
  `orders.total` with a ±1 **minor**-unit tolerance; REFUND_TICKET and the
  Bil24 import convert incoming money the same way. (The earlier gotcha here
  claimed the gateway does no conversion at all — that was the pre-#528
  behaviour and is obsolete.)

- **The Bil24 cart ROUNDS the service charge, `hcheckout` FLOORS it.**
  `hcheckout.ComputePricingLines` computes the basis-point platform fee as
  `discounted * rate / 10_000` (integer floor) — the platform-wide contract
  for the REST/widget checkout, documented on `CheckoutPricing.platform_fee`
  in openapi.yaml. The gateway's cart projections (`feeChargeMinor`) round
  half away from zero, so a 5 % fee on 1890 is 95, not 94. CREATE_ORDER_EXT
  therefore re-states the fee with `applyGatewayCharge` (`cmd_cart_view.go`)
  before persisting, or the buyer would be billed 19.84 for a cart that
  displayed 19.85 and PAY_ORDER's ±1 tolerance would sit one unit off centre.
  Do not "fix" this by changing `hcheckout`.

- **Bil24 harness: `{{categoryPriceId}}` resolves to a platform UUID by
  default, and a UUID on the wire answers `-2`.** `resolveGolden`'s fallback
  for that placeholder is `st.AssignedTierID` (a UUID), but since feature #476
  `resolveCategoryPriceID` rejects UUIDs. Any scenario posting a category
  price must mint the int64 wire id with `compatids.Ensure(ctx, pool,
  compatids.KindCategoryPrice, id)` and override the placeholder through the
  scenario's runtime map. Related: `cleanupHarnessWireRows` must sweep
  `orders` before `checkout_sessions` before `reservations` — `orders` FKs
  both parents and neither cascades.

- **The Bil24 compat gateway has TWO independent "disabled" switches at two
  different HTTP-status levels, and only one of them 404s.** The
  `BIL24_COMPAT_ENABLED` env var un-mounts the whole `/compat/bil24/*`
  subtree (real `404`) — an all-channels, deploy-time kill switch. A
  per-channel `sales_channels.settings.gateway.enabled=false`
  (`hbil24/auth.go`) does NOT 404: it answers `HTTP 200` with JSON envelope
  `resultCode=-4` ("unknown fid or channel disabled"), same as every other
  compat command. Only `GET /compat/bil24/image` uses a real `404` status
  for every "you may not see this" case by design (spec §8: an enumerable
  404 there would leak which failure mode applies). Don't probe HTTP status
  to detect a disabled channel — check `resultCode`. Documented in
  `docs/ops/bil24_gateway.md` §5 (feature #521).

- **Every gateway token check must go through `hbil24.parseGatewaySettings`.**
  `sales_channels.settings` carries the hash in two shapes: legacy top-level
  `gateway_token_hash` and the W1 shape `settings.gateway.token_hash` written
  by `PUT .../channels/{id}/gateway-credential`. `authenticateCommand` used
  the parser, but the older `validateGatewayToken` path (RESERVATION, cart
  commands, CREATE_ORDER_EXT, PAY_ORDER, GET_TICKETS_BY_ORDER) decoded only
  the legacy key, so a freshly provisioned channel passed GET_ALL_ACTIONS and
  then got `-1 channel is not configured for gateway access` from the ticket
  picker on the live stand (fixed 9c350f3). Never hand-roll a settings
  decode for the hash again; the unit test
  `TestBil24_ValidateGatewayToken_AcceptsNestedGatewayShape` guards it.

- **The order's wire id is `orders.system_id` everywhere** (spec 18 §4 line
  "orderId (ответ CREATE_ORDER_EXT) → orders.system_id", §9.3 example
  `"id": 1000000500`). CREATE_ORDER_EXT `orderId`, the `order.paid` webhook
  `data.id` and every `ticketList[].orderId` must be the SAME integer, because
  the WordPress receiver (`bil24-notification-receiver.php`) stores the
  CREATE_ORDER_EXT value in `bil24_external_order_id` and later matches
  `data.id` against it. Until 2026-09-13 CREATE_ORDER_EXT answered the
  platform UUID and `orderexport.Order.ID` was `min(system_ticket_id)`, so the
  first real purchase through the stand paid fine but the site logged
  "WC order not found for Bil24 #1" and never received its tickets.
  `orderexport` now LEFT JOINs `orders` and uses `orders.system_id` (legacy
  fallback to the min ticket id only when `tickets.order_id` is NULL);
  integration tests resolve a wire orderId back to the row with
  `SELECT id FROM orders WHERE system_id=$1`.

- **Bil24 gateway token verification is cached in-process, keyed on the
  stored bcrypt hash** (`hbil24/token_cache.go`, perf fix — uncached, every
  `/compat/bil24/json` command paid a fresh ~50-190ms bcrypt compare; a load
  test showed 3-4 CPU cores at 44 req/s and 1.5-3s waits on a 200-reservation
  burst). Only SUCCESSFUL verifications are cached — a wrong guess always
  re-runs bcrypt, so caching can never make brute-forcing cheaper. The cache
  key IS the bcrypt hash string (not the channel id): both call sites
  (`authenticateCommand`'s inline check and the legacy `validateGatewayToken`
  helper used by RESERVATION/cart/CREATE_ORDER_EXT/PAY_ORDER/
  GET_TICKETS_BY_ORDER) go through the shared `Handler.verifyGatewayToken`,
  but only `authenticateCommand` resolves a full channel row —
  `validateGatewayToken` only ever receives the settings blob. bcrypt's
  random salt makes every hash effectively unique per channel, so keying by
  hash gives the same rotation-invalidates-immediately property as keying by
  channel id would (`PUT .../gateway-credential` writes a new hash, so the
  next request simply misses the cache under the old key and the cache never
  holds a stale credential), without needing a channel id at every call
  site — the disabled-channel / no-hash-configured gates still run BEFORE
  the cache is ever consulted, unchanged. TTL defaults to 5 minutes,
  configurable via `BIL24_TOKEN_CACHE_TTL`. **The cache must live on the
  Server, not the Handler:** `Server.bil24Handler()` builds a fresh
  `hbil24.Handler` per request, so the first version (cache as a Handler
  field) never hit and the load test showed no CPU change. `New` builds one
  `hbil24.TokenCache` and every per-request Handler receives it through
  `WithTokenCache`; the same trap applies to any other per-process state
  added to a per-request handler.
  Any NEW auth path must call `verifyGatewayToken`, never
  `bcrypt.CompareHashAndPassword` directly — a static call would bypass both
  the cache and the singleflight cold-cache dedup that collapses a burst of
  identical concurrent requests onto one bcrypt call.

- **CREATE_ORDER_EXT must never expire a customer's open order without
  checking the hold behind it is actually dead.** `orderWriteAggregate`
  (`hbil24/cmd_order_create.go`) used to call `ordering.Expire` unconditionally
  the moment `FindOpenOrder` found a second open order for the same
  customer+session pointing at a different reservation, on the assumption
  that a second open order can only mean "the first expired". Two gateway
  buyers who share the checkout identity the WordPress plugin sent (email/
  phone — whatever the buyer typed, not a stable account id) reserving
  separately for the same session let buyer B's CREATE_ORDER_EXT silently
  steal buyer A's still-live hold; A's PAY_ORDER then failed after
  WooCommerce had already charged them (reproduced under `ops/loadtest`,
  2026-09-13). Fixed: the existing order's reservation is now loaded and only
  expired when its state is not `draft`/`active` or its `expires_at` has
  already passed (`orderOldHoldIsLive`); otherwise CREATE_ORDER_EXT answers
  `101 bil24.open_order_exists` and writes nothing. The same-session repeat
  case (`existing.ReservationID == res.ID`, bounce off WooCommerce payment
  and come back) is a separate branch and is unaffected.

- **PAY_ORDER runs under a fixed payment window and never parks anything in
  `manual_review` — owner decision 2026-09-13.** A buyer has
  `settings.gateway.payment_window_seconds` (default 1200) plus
  `payment_grace_seconds` (default 120) to pay, timed from the most recent
  `CREATE_ORDER_EXT` (which sets `orders.expires_at = reservations.expires_at`
  to that instant and returns `paymentDeadline`/`paymentTimeout`); a plain
  cart `RESERVE`/`UN_RESERVE` never shortens that. PAY_ORDER takes a row lock
  on the order (`LockOrderForUpdate`) as its first statement and decides
  everything — pay, expire, or cancel — from the row it reads UNDER that
  lock, so concurrent calls for the same order serialize instead of racing a
  read-then-write status decision. Inside the window a live or reacquirable
  hold pays normally (`resultCode 0`); a hold lost to someone else within the
  window is CANCELLED automatically (`ordering.Cancel`,
  `order_events.cancelled` payload.reason=`hold_expired`, `101
  bil24.hold_expired`); past the window the order is EXPIRED automatically
  (mirroring `order.expire_sweep`'s own per-row step,
  `ordering.ExpireIfStillPending` + `hcheckout.ExpireHoldForOrderTx`, `101
  bil24.order_expired`) — NO revival, ever. The earlier revival machinery
  (`ordering.ReviveForPayment`, `ErrOpenOrderConflict`,
  `EventRevivedForPayment`, `payParkManualReview`) is removed: a fixed window
  makes it unreachable. A `manual_review` order in a live database predates
  this change and PAY_ORDER still answers it `101 bil24.hold_expired` with no
  writes so a stale row cannot retry-loop, but nothing new will ever create
  one. Tests: `apps/backend/tests/compat/bil24/order_pay_window_integration_test.go`,
  `apps/backend/internal/platform/ordering/lifecycle_test.go`. Runbook:
  `docs/ops/bil24_gateway.md` §9.2.

- **A 'sold' `session_seats` row with `reservation_id IS NULL` is a seat sold
  in the system the session was imported from.** The Bil24 import marks
  every `sbt:state="4"` seat that way (`MarkSessionSeatSoldUpstream`,
  warning `import.seats_sold_upstream`), while `state="0"` / `available:false`
  stays an operator-reopenable 'unavailable' block — an organizer withholds
  most of a hall and reopens it later, right next to seats whose tickets live
  in the old system. The state list travels as `SBTPlan.SoldSeatIDs`, outside
  `Geometry`, so an upstream sale never changes the geometry checksum. No
  ticket, order or `inventory_ledger.capacity_sold` stands behind such a row:
  guards that ask "does this session have sales" by counting tickets miss it,
  which is why the manual re-bind guard (`hseating/bind.go`) also counts sold
  seats. There is no operator action to release one yet.
