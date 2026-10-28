# Payments, hosted checkout and payment webhooks

Stripe, Flitt and AllPay hosted checkout, payment modules, webhook state machine, cart pricing. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **The widget/public payment webhook MUST complete the checkout session in
  the same transaction as the payment-succeeded state transition, or a paid
  purchase reports "pending" forever.** Until 2026-09-13,
  `hcheckout.HandlePaymentIntentWebhook` (`payment_intents.go`) marked the
  payment intent succeeded, enqueued `checkout.issue_tickets`, marked the
  order paid (`ordering.MarkPaid`), and converted the reservation — but
  never called `CompleteCheckoutSession`. `checkout_sessions.state` stayed
  `pricing_confirmed`, so `hfeed.checkoutStatusToPublic` answered `pending`
  on `GET /v1/public/checkout/{token}` even though the order was paid and
  tickets were issued underneath (a 251-purchase load test showed 251/251
  stuck this way). The fix adds a Step 2b inside the webhook's existing
  transaction: `CompleteCheckoutSession(ctx, csID, pi.ID.String(),
  pi.Provider)`, idempotent (an already-`completed` session — replay, or a
  completion that raced this one home first — is treated as success without
  re-running it), and NOT force-completed when the session can no longer
  reach `pricing_confirmed`→`completed` (hold TTL'd out before the webhook
  arrived, already `manual_review`, etc.) — that case parks the session (and
  the order, via `UpdateOrderStatus(..., "manual_review", ...)`) for an
  operator instead, mirroring `hbil24`'s `payParkManualReview`
  (PAY_ORDER, spec §7.9). Ticket issuance, the order mark-paid step, and
  reservation conversion are all gated on this completion succeeding
  (`checkoutCompleted`) so a parked session never ships tickets for seats
  that may no longer be held.

- **`validPaymentIntentTransitions` is too strict for a REAL provider
  webhook and must not be loosened directly** — Stripe commonly delivers
  `payment_intent.succeeded` (and `.payment_failed` /
  `.amount_capturable_updated`) straight from `created`, skipping
  `processing` entirely; the strict table answered `200 processed:false`
  ("state transition not valid") and silently dropped the payment. Fixed
  with a SEPARATE `validWebhookTransitions` table /
  `validWebhookTransition()` helper used ONLY by
  `HandlePaymentIntentWebhook` — the authenticated
  `POST /v1/payment-intents/{id}/transition` endpoint still enforces
  `validPaymentIntentTransitions` unchanged. Extend the webhook table, never
  the strict one, when a provider needs a new direct hop.

- **`POST /v1/payment-intents/webhook` must accept both the flat legacy body
  AND a genuine Stripe event envelope.** The pre-existing decoder only
  understood `{"provider_payment_id","event_type",...}` (used by the mock
  provider and tests); a real Stripe webhook is
  `{"id":"evt_...","type":"payment_intent.succeeded","data":{"object":{"id":"pi_...","status":"...","last_payment_error":{...}}}}`
  and failed with `webhook.missing_provider_payment_id`.
  `parseWebhookPaymentIntentRequest` (`payment_intents.go`) detects the
  envelope by a top-level `type` string TOGETHER WITH a top-level `data`
  object (the flat shape has neither), then maps `data.object.id` →
  `provider_payment_id`, `type` → `event_type`, and
  `data.object.last_payment_error.code`/`.message` → `failure_code`/
  `failure_message`. The existing `(provider_payment_id, event_type)`
  idempotency dedup (`payment_intent_events` UNIQUE constraint) is reused
  as-is for both shapes — the Stripe event's own `id` is kept in the raw
  `event_payload` for audit but is deliberately NOT folded into the dedup
  key (would need a migration; the existing key already makes a replay a
  no-op once the intent reaches its terminal state — see below).
  `verifyWebhookSignature`/`payments.VerifyStripeSignature` already
  implement Stripe's real scheme correctly (`Stripe-Signature:
  t=<ts>,v1=<hex>`, HMAC-SHA256 over `"<ts>.<raw body>"`, constant-time
  compare, 5-minute tolerance) — do not "fix" that path again.

- **A replayed webhook event for an already-terminal payment intent answers
  `200 processed:false`, NOT `204`.** The `204 No Content` path is the
  `InsertPaymentIntentEvent` `ON CONFLICT DO NOTHING` dedup, which only
  fires while the intent is still non-terminal (e.g. two deliveries racing
  in before the first commits). Once the first delivery commits, the intent
  is terminal (`succeeded`/`failed`), and EVERY later delivery — including
  an exact replay — is caught by the earlier `isTerminalPaymentIntentState`
  guard before the transaction even opens, which always answers `200`
  `{"processed":false,"reason":"payment intent is already in a terminal
  state"}`. A test asserting "replay is a no-op" against a real end-to-end
  flow must expect 200, not 204.

- **The payment webhook completes a checkout only from `pricing_confirmed`**
  (`CompleteCheckoutSession` guard). Nothing sets `payment_started` today;
  a future hosted-payment flow that does MUST extend the completion guard or
  every paid checkout in that state is parked in `manual_review`. The
  manual-review writes run in a SAVEPOINT (`parkCheckoutForManualReview`) and a
  non-NoRows completion error answers 500 so the provider redelivers — never
  swallow a failed statement inside that transaction.

- **The per-config payment webhook route is the one that may answer 200 to a
  payment arena does not own.** `POST /v1/payment-intents/webhook/{config_id}`
  (`hcheckout/payment_webhook_config_route.go`) names a
  `payment_provider_configs` row, verifies the signature ONLY against THAT
  row's `secrets.webhook_secret` — never the process-env fallback — and then
  requires the resolved payment intent to belong to the SAME org as the
  config. Statuses are deliberate and must not be "simplified": a config id
  that is not a UUID is 400, one that is unknown OR not usable (inactive,
  wrong provider) is 404 with no detail so the endpoint cannot be used to
  enumerate ids, a config with no signing secret is 401 (it exists, it just
  cannot authenticate anything), a bad signature is 401, and a VERIFIED
  event whose payment arena does not own — unknown id, or another org's
  intent — is 200 `{acknowledged:true, processed:false, reason:"not an
  arena payment"}`. That last one is the whole point: an organizer's Stripe
  account also serves their other sites, so foreign events arrive
  constantly and a 404 would make Stripe retry for days and mail the owner
  that our endpoint is broken. The LEGACY un-suffixed
  `POST /v1/payment-intents/webhook` keeps its old behaviour exactly
  (404 for an unknown payment) — it has no config id, so it cannot tell a
  foreign-but-legitimate event from a probe. Both routes share ONE body,
  `processPaymentWebhook`, parameterised by a `webhookRoute` struct; never
  fork the state machine to add a route.

- **The payment-webhook metrics are fed by an unauthenticated endpoint, so
  every label is bounded by construction.**
  `arena_payment_webhook_signature_failures_total{route_kind}` carries only
  `legacy` or `config` — never an org id, a config id or an IP, all of which
  an attacker controls the cardinality of.
  `arena_payment_webhook_events_total{event_type,outcome}` passes
  `event_type` through `observability.PaymentWebhookEventLabel`, which keeps
  the handful of types arena actually handles and collapses everything else
  to `other`. Stripe publishes well over a hundred event types and the body
  is caller-supplied, so a raw pass-through is a metrics-backend outage
  waiting to happen. Any new counter on this surface must do the same.

- **The widget takes money through a Stripe-HOSTED Checkout Session, and its
  `payment_intents.provider_payment_id` is the `cs_…` session id, NOT a
  `pi_…`.** `POST /v1/public/feeds/{token}/checkout/start` used to answer a
  hardcoded dead `redirect_url` (`/checkout/<uuid>`) and never called a
  provider at all. It now commits its own transaction first, then — AFTER the
  commit, never inside it — resolves the channel's provider
  (`sales_channels.provider`, only `stripe` is supported) and the org's
  credentials through `hcheckout.ResolveProviderConfig`, builds
  `stripe.New(...)` PER REQUEST from that row's `secrets.api_key` (every
  organizer has their OWN Stripe account — no Connect, no application fee),
  creates the hosted page and stores a `payment_intents` row keyed by the
  `cs_…`. That is what every `checkout.session.*` webhook event identifies
  the payment by, so keying it any other way 404s a real payment. The `pi_…`
  arrives later on `data.object.payment_intent` and is stored in
  `provider_charge_ref` (migration 0103) because a REFUND must be driven
  through the `pi_…`; `hosted_checkout_url` on the same row is what
  `GET /v1/public/checkout/{token}` returns as `payment_url` so a buyer who
  bounced off Stripe can resume. Failure codes are deliberately distinct:
  `checkout.payment_provider_unsupported` (422), `checkout.payment_not_configured`
  (422/503), `checkout.payment_start_failed` (**503, never 502 — see below**),
  `checkout.invalid_return_url` (400). A failure never releases the hold by
  hand — the existing sweeps do.
  **`checkout.payment_start_failed` must stay 503.** `api.arenasoldout.com`
  is fronted by Cloudflare, which REPLACES an origin 502 with its own 16-byte
  `error code: 502` text/plain page, so the JSON error envelope — and the
  error code the widget shows the buyer — never survives the hop (verified
  2026-09-20 by curling the origin directly with `--resolve`: correct JSON at
  the origin, Cloudflare's stub on the public URL). A 503 is passed through
  untouched. Any NEW public, Cloudflare-fronted route must pick 503 over 502
  for the same reason; the remaining 502s in the repo
  (`hbilling/stripe_connect.go`, `hbilling/stripe_billing.go`,
  `sender_identity.go`) are authenticated admin routes whose body nobody
  parses, and were deliberately left alone.
  **A paid cart is refused BEFORE it takes inventory.** The provider call
  itself still runs after the commit (never inside the money transaction),
  but the CONFIGURATION half — channel provider, `ResolveProviderConfig`,
  a non-empty `secrets.api_key` — now also runs as a pre-flight
  (`PaymentStarter.CheckPaymentConfigured`, implemented once in
  `StripePaymentStarter.resolveConfig` and called by `StartHostedCheckout`
  too, so the two cannot drift) from `hfeed.preflightPaidCheckout`, after
  pricing and before the hold transaction commits. Until 2026-09-20 a
  misconfigured org failed only after the commit, so every buyer attempt left
  a real hold that only the ~31-minute TTL sweep released — three failed
  attempts "sold out" a live 15-seat master class (availability fell 15 → 12).
  A zero-total cart skips the pre-flight entirely: a free checkout has no
  provider. The `return_url` check moved into the same pre-flight for the
  same reason; its code and status (400) are unchanged.
  `checkout.session.completed` is only acted on when `data.object.payment_status
  == "paid"`; Stripe fires it for async methods long before the money settles,
  and that gate runs BEFORE the idempotency insert so the later real event is
  not swallowed as a duplicate. Extend `validWebhookTransitions`, never the
  strict table.

- **The widget payment window must outlive the Stripe session, never the
  other way round.** `WIDGET_PAYMENT_WINDOW_SECONDS` (default 1860 — Stripe
  refuses a Checkout Session expiry closer than 30 minutes, so never go below
  1800) is the hosted session's own `expires_at`; the hold / order / checkout
  expiry is that PLUS `WIDGET_PAYMENT_GRACE_SECONDS` (default 120), set
  inside the checkout transaction with `hcheckout.SetHoldExpiryTx` exactly as
  `hbil24`'s CREATE_ORDER_EXT does, so `ordering.CreateOrderFromCheckout`
  copies `orders.expires_at` off the reservation and the three can never
  disagree. Invert the two and a buyer pays for seats arena already resold.

- **Per-org webhook secrets only work because the envelope branch exists.**
  `webhookSecretsFromOrgConfig` (`hcheckout/provider_config.go`) used to find
  the org ONLY from a flat body's `refund_id`/`payment_intent_id`. A genuine
  Stripe event carries neither, so per-org secrets were never consulted and
  every webhook fell back to the single process-env secret — which at most
  ONE of two organizers on two Stripe accounts can own. It now also reads
  `data.object.id` from the envelope (`providerPaymentIDFromEnvelope`) and
  resolves the org through `GetPaymentIntentByProviderID`. Guarded by
  `TestHostedCheckout_TwoOrgsUseTheirOwnWebhookSecrets`.

- **A zero-total public checkout completes inline; both it and the payment
  webhook run the SAME four writes.** `hcheckout.FulfillCompletedCheckoutTx`
  (`fulfillment.go`) enqueues `checkout.issue_tickets`, marks the order paid
  and enqueues `checkout.convert_reservation` on the CALLER's transaction;
  `CompleteFreeCheckoutTx` wraps it for a free order (payment_provider
  `'none'`, no payment intent). Any failure there now rolls the whole
  transaction back and answers 500 so the provider redelivers — the old
  "log the mark-paid failure and carry on" was never real, because a failed
  statement had already aborted the pgx transaction and the COMMIT died with
  25P02 anyway.

- **The widget's service charge is the CHANNEL's `fee_percent`, resolved per
  request by `hfeed.Handler.channelPricingRules` — never the process-wide
  `Options.PricingRules`, which nothing sets.** Until 2026-09-29
  `HandlePublicFeedCheckoutStart` and `HandlePublicCheckoutRecover` priced
  every cart with `h.pricingRules` alone, so a channel's fee reached the
  Bil24 gateway's carts (`feeChargeMinor`) but never a hosted checkout: a
  5 % channel sold 50 EUR tickets for 50.00 on the Stripe page, and
  `createPublicOrder`'s `ChargePercentBP(ch.FeePercent)` was only an audit
  snapshot on the order. The channel row is read on the pool BEFORE the
  hold transaction (the AGENTS.md pool-read rule) and only overrides
  `PlatformFeeRate` when the channel fee is > 0. The public feed session now
  carries `service_fee_percent` (`GetFeedTokenBuyerFlags` also selects
  `sc.fee_percent`) and the widget's `CartSheet` shows a "Service fee" line
  computed by `cartServiceFee` with the SAME basis-point floor as
  `ComputePricingLines`, so the sheet's total is the payment page's total.
  Guarded by the fee assertion in
  `TestHostedCheckout_StartCreatesStripeSessionAndWebhookPaysTheOrder`
  (fixture channel 1.25 %). There is no fixed (per-ticket) fee yet — only
  the percentage.

- **Flitt is a hosted-checkout provider like Stripe, with three differences
  that bit during the build (spec `08_architecture/29_flitt_payment_provider_ru.md`,
  migration 0120).** (1) The callback carries its signature IN THE BODY and is
  signed with the SAME `payment_key` that signs requests — there is no
  separate webhook secret, so `WebhookSecretFromConfig` reads `payment_key`
  for flitt, and `verifyConfigWebhookSignature` compares `merchant_id` before
  hashing. Decode the body with `json.Number` or `payment_id` is signed as
  `8.05e+08`. (2) Flitt matches payments by OUR `order_id` (the checkout
  session UUID), stored as `payment_intents.provider_payment_id`; its numeric
  `payment_id` goes to `provider_charge_ref`. (3) `declined` is deliberately
  NOT mapped to a failed intent — a declined card leaves the Flitt order open
  for another card, and a terminal intent would swallow the later `approved`
  (a paid purchase lost). Only `approved` and `expired` move an intent. A
  Flitt-shaped body on the LEGACY un-suffixed webhook route is answered 400
  `webhook.flitt_requires_config_route`; the per-config route sets
  `webhookRoute.ExpectedProvider="flitt"` so a Flitt signature can never move
  a Stripe intent of the same org. The published signature example in
  docs.flitt.com does not reproduce (computed on other data): the algorithm was
  proven against the live sandbox (`merchant_id 1549901`, key `test`, public
  in their docs) — wrong key answers 1014, unknown merchant 1016, unknown
  order 1018, which is exactly what `VerifyCredentials` keys on. `FLITT_API_BASE_URL`
  is the test seam and `config.Validate` refuses it in production. Currencies
  are NOT allow-listed in arena: the session currency goes to Flitt unchanged
  and Flitt's 1012/1007 is the verdict. `buyer_locale` accepts
  `templates.SupportedLocales` = `cs/de/en/es/fr/he/ru` (French e-mail templates
  added 2026-10-01, the PDF already had `fr`); the Flitt page gets `lang` from the
  same value. The widget forwards whatever `locale` the host site sets (its UI
  falls back to English for an unknown one), but the tickets PAGE only resolves
  `en/ru/cs/he/es`, so a French browser on a promoter page still sends `en`
  until `fr` joins `PAGE_LOCALES` (needs a French string table). Refunds are NOT driven through Flitt from arena (nor through Stripe:
  the refund flow only simulates provider submission today). **The buyer comes
  back from Flitt THROUGH arena, never straight to their page:** Flitt returns
  the buyer with the method set in its portal (default POST, switchable only
  after the director's identification), and a static tickets page answers a POST
  with 405. `response_url`/`cancel_url` therefore point at
  `GET|POST /v1/public/payment-return?r=<buyer page>&checkout_token=<token>`
  (`hfeed/payment_return.go`), which 303s to the buyer's page; the POST body is
  never read and `r` is re-validated by `ReturnURLPolicy` (an origin outside the
  allow-list falls back, never redirects). Do not point a provider's return URL
  at a buyer page directly.

- **Payment providers are MODULES since PAY-01 (spec `08_architecture/36_payment_modules_refunds_acquiring_ru.md` §5): the core never names a provider.** The contract is `internal/domain/payments/module.go` (`Descriptor` with `Secrets`/`Capabilities`, `Module`, optional `HostedCheckoutProvider`, `CredentialVerifier`, `WebhookParser`, `Refunder`, `RefundLookup`) and the factory `Registry` in `domain/payments/modules.go`; the LIST of providers is `internal/app/payments/modules.go` (`Modules()`, `Registry()`), because the adapters import the domain package and the list cannot live there. Modules are built PER REQUEST from one config's secrets (`payments.SecretsFromJSON(cfg.Secrets)`) plus `payments.Options` (the `StripeAPIBaseURL`/`FlittAPIBaseURL`/`AllPayAPIBaseURL` test seams, fed by `Options.StripeAPIBaseURL`, `STRIPE_API_BASE_URL`, `FLITT_API_BASE_URL`, `ALLPAY_API_BASE_URL`, plus `Options.Mode`, the config's test/live mode that hfeed sets per build). hfeed hosted checkout, the hpayments provider catalogue, required secrets (`Descriptor.RequiredSecretKeys`), key prefixes (`SecretField.Prefixes`/`ModePrefixes`/`Label`) and credential verification, hcheckout's webhook-secret lookup (`Descriptor.WebhookSecretKey`, fallback `webhook_secret`) and both webhook routes all go through it. Behaviour switches on CAPABILITIES, not names: `BuyerReturnsByPOST` routes the buyer through `/v1/public/payment-return`, `WebhookConfigRouteOnly` (Flitt, AllPay) sends the per-config callback URL, sets `webhookRoute.ExpectedProvider` and hands the module's `NormalizedEvent` to the state machine, and makes every other route answer 400 `webhook.<name>_requires_config_route` for a body the module recognizes. Stripe events are still parsed by the core's flat/envelope parser after the module verified the signature (moving them onto `NormalizedEvent` is PAY-04/05). The legacy route's env secrets are mapped to names by `paymodules.PlatformWebhookSecrets`. `tests/staticanalysis/payment_provider_literals_test.go` fails on a `"stripe"`/`"flitt"`/`"allpay"` literal (any case) in non-test code of hcheckout, hfeed, hpayments, htickets and eventbot; `"manual"`/`"none"` are allowed, and an exception needs a documented entry in its allowlist. **Adding a provider:** a package `internal/adapters/<name>` with `Name`, `ProviderDescriptor()`, `Entry()` and the optional interfaces it really implements (a capability flag without its interface fails the contract), a `module_test.go` calling `contracttest.Run`, one line in `app/payments/Modules()` (append, the order shows in error messages), and its name added to `providerNameLiteral` in the guardrail. Until PAY-02 a new provider ALSO needs a migration widening `sales_channels_provider_check`. **Before PAY-04/05 move Stripe onto `NormalizedEvent`, read the M1 warning under the backlog table of spec 36 §12** (decide purely from the event kind, paid gate on the normalized kind not `PaymentStatus`, collapse the two Stripe envelope parsers, assert the partial-refund amount in the contract test), or paid `checkout.session.completed` events are swallowed as unpaid. The per-config route runs the whole-body `json.Valid` check on the module-event path too (`TestFlitt_SignedCallbackWithTrailingGarbageIsRefused`).

- **A sales channel's `provider` is validated by the payment module registry, not by the database (PAY-02, migration 0132, 2026-10-10).** `sales_channels_provider_check` (the `('stripe','allpay','flitt')` list) is gone; the table keeps only `sales_channels_provider_format_check` (`^[a-z][a-z0-9_]{0,63}$`, the shape of a descriptor name). The rule lives in `paymodules.IsChannelProvider` / `Registry.IsModule`: a provider with a module behind it (not `Declared`), spelled EXACTLY as its descriptor names it (no case folding — `Stripe` is still 400). `hcatalog` create and PATCH call `ValidateChannelProvider` (`hcatalog/channel_provider.go`, same 400 `channel.invalid_config`, the message lists the registry's names), an empty provider on create still defaults to `paymodules.DefaultChannelProvider()` (stripe), `provisioning.CreateWorkspace` and the onboarding `payment_provider` check both go through `paymodules.ChannelProviderForChoice` (a module WITH a hosted page is taken as is — since PAY-14 that includes AllPay). A new provider therefore needs no migration, but it DOES need the `enum` of `CreateChannelRequest.provider` / `UpdateChannelRequest.provider` in openapi.yaml updated (and the clients regenerated): `tests/staticanalysis/channel_provider_mirror_test.go` pins that enum to the registry. The provider-literal guardrail (`payment_provider_literals_test.go`) now builds its regex from `paymodules.Registry().Names()` (minus the `manual` marker) and also scans `hcatalog` and `provisioning`, so a registered module is guarded the moment it is added. Not changed on purpose: the onboarding FORM's option list (`onboarding/schema.go`, form vocabulary with its own labels) and the seed data. Tests: `hcatalog/channel_provider_test.go`, `hcatalog/channel_provider_integration_test.go` (a fake module registered at test time is created, stored and PATCHed through the real handlers).

- **A public cart is priced in ONE place, whether it is quoted or started.** `POST /v1/public/feeds/{token}/checkout/quote` (`hfeed/public_feed_quote.go`) and `checkout/start` build their price from the same helpers in `hfeed/public_feed_pricing.go` (`priceGAItems`, `gaPricingLines`, `cartCurrency`, `findPromoCode`, `writePublicPromoError`) plus `seatPricingLines`, `channelPricingRules`, `ValidatePromoForLines`, `CheckPromoLimits` and `ComputePricingLines`. Change the rule in a helper, never in one handler, and never add a second calculation of a total: the quote is what the cart sheet shows and must equal what the payment page asks for (`TestPublicQuote_EqualsStartTotal_AndLeavesNothingBehind` compares the two, GA and seated). A quote takes no lock and writes nothing: seats are read with `ListSessionSeatsByKeys`, never `LockSessionSeatsForHold`, and no reservation, checkout, order or payment intent is created. A code the cart cannot use is answered on BOTH routes with the same small set of stable 422 codes: `promo.not_applicable` covers unknown, paused, not-yet-valid, expired and out-of-scope codes alike; `promo.minimum_not_reached`, `promo.exhausted` and (start only) `promo.per_customer_limit` are given only for a code that fits the cart (`publicPromoError`). The text a buyer types is matched without regard to case and surrounding spaces (`GetPromoCodeByCodeCI`). A code that is named but cannot be checked fails the request; it is never skipped. The per-customer cap needs the buyer's e-mail: the quote takes no buyer details and does not check it, `checkout/start` enforces it. Both routes use the same token and per-IP rate limits as every public feed route, plus a separate, tighter budget for refused codes (`PUBLIC_PROMO_FAIL_TOKEN_RATE_LIMIT`, `PUBLIC_PROMO_FAIL_IP_RATE_LIMIT`, `0` disables; answer 429 `promo.rate_limited`). The budget is a reservation, not a check followed by a count: a request that names a code takes a slot atomically (`ReservePromoAttempt`), and only a refused code keeps it, so a burst cannot all pass a check made before any of them is counted. The budget is checked before any database read, and an IPv6 client is counted by its /64 prefix (`ipRateKey`). All the public feed limiters keep their counters in the memory of one arena-api process: with several replicas each has its own, so the ceiling is the limit times the replica count. One cart is capped at 20 lines, 50 tickets a line and 100 seats (`checkCartCaps`). Several codes of one organization that differ only in case resolve by the exact spelling, then the oldest. The public feed session (`GET .../events/{event_id}`) carries `promo_codes_available`, read in the same query as the buyer flags (`GetFeedTokenBuyerFlags`, one read before any transaction): true when the organization has a code that is not deleted, has status `active`, is inside its date window and is not used up (`max_uses`). It deliberately ignores per-customer limits, scope and currency, because `checkout/quote` stays the authority on whether a code fits a cart. It reveals only that an active code exists, never which one. The widget shows its promo field only when it is true (collapsed behind a link), so a buyer of an organization without codes sees no change and no extra request. A new public route needs a row in `flatRouteFamilies` (`org_isolation_flat_guard_test.go`). Widget: every timing rule of the code lives in `lib/promoController.ts` (a Svelte-free state machine, tested in `promoController.test.ts`), not in the component. One request is current and an older answer never writes; an answer is accepted only for the cart it was asked about (`cartQuoteKey`), otherwise the new cart is quoted again; the wait is bounded (`QUOTE_TIMEOUT_MS`) and a failed or hung quote takes the applied code off with the reason and never blocks paying the list price; the pay button follows `promoPayBlocked` (only an applied code whose quote is not for the current cart blocks it, a pending Apply never does and is dropped when the buyer goes on to the form); the cart lines are locked while a quote is in flight; a code that takes nothing off (`promo_applied` false) is not applied. The sheet shows the platform's numbers only while the quote's key matches the cart (`sheetAmounts`), and takes the code off when `checkout/start` refuses it. The words are `lib/promoI18n.ts` (en, ru, cs, he, es, de, fr, looked up by language subtag like `PAYMENT_COMMIT_I18N`, not in `CHECKOUT_I18N`), the picker stops at 100 seats (`MAX_CART_SEATS`) and `lib/cartLimits.ts` holds the buyer texts for `checkout.cart_too_large` and `checkout.invalid_qty`; and every amount in the sheet goes through `formatAmount`/`formatDiscount`, which wrap it in a left-to-right isolate so a minus sign keeps its place in a Hebrew sheet. Vitest files must end `.test.ts`. The real-backend acceptance is `apps/widget/tests/palac-promo-real.e2e.ts` (promo codes seeded in `seed-palac-e2e.sql` section 13; locally point `ARENA_API_URL` at an arena-api on a free port such as `HTTP_LISTEN_ADDR=:18080`). The mock e2e is `apps/widget/tests/promo-code.e2e.ts`: if port 4173 is taken, run it through a temporary copy of `playwright.config.ts` with another `baseURL` and the `PORT` env for `serve-demo.cjs`.

- **AllPay is a hosted-checkout module like Flitt since PAY-14 (spec `08_architecture/39_allpay_payment_provider_ru.md`, no migration), and the code that existed before described an API AllPay does not have.** The old `adapters/allpay` (`api.allpay.co.il/v1`, Bearer token, an `X-AllPay-Signature` HMAC header) and the second copy `domain/payments/allpay.go` were invented; the real API (https://allpay.to/docs/api-reference.htm) is `POST https://allpay.to/app/?show=<method>&mode=api15` with a JSON body, errors come back as HTTP **200** `{"error_code","error_msg"}`, and every request and webhook carries a body field `sign` = SHA-256 of the VALUES (keys sorted A-Z at every level, `""`/null skipped, 0/false kept, numbers in shortest form `100.00`→`100`, true→`1`) joined with `:` plus `:<api key>`. Sign the JSON exactly as sent: build requests from `json.Number` and decode webhooks with `UseNumber` (`adapters/allpay/sign.go`); `TestSign_DocumentedWorkedExample` pins the documentation's own vector (its e-mail is hidden by Cloudflare in the HTML: `jason@company.io`). Secrets keep the old key names: `merchant_id` = API LOGIN, `secret_key` = API KEY (also verifies webhooks); optional `vat` (0/1/3, default 0), `max_installments` (2-12, ILS only) and `installments_min_amount` (shekels, the smallest total that offers installments) — `SecretField.Pattern` is now enforced on save by `hpayments.ValidateSecretFormats`. Traps: (1) amounts are MAJOR units (`123.5` shekels) — convert only through `allpay.MinorToMajor`/`MajorToMinor`; (2) `order_id` is the checkout session id and is BOTH `provider_payment_id` and `provider_charge_ref` (AllPay refunds and reads status by it, it mints no payment id); (3) a webhook with `status` 0 is ONE failed attempt on a page that stays open — `EventPaymentDeclined`, never a failure, like Flitt's `declined`; (4) billing currency is ILS/USD/EUR only and an account without USD/EUR permission is SILENTLY charged in ILS, so the module refuses any other currency before calling; (5) a config filed as `test` sends `test_mode=1` (`payments.Options.Mode`), so a test key can never charge a real card, and a config filed as `live` sends `test_mode=0`, which overrides an integration left in test mode in the dashboard (verified on the test integration 2026-10-13: accepted, no test banner) — neither the webhook nor `paymentstatus` says whether a payment was a test one; (6) AllPay does not document its redirect method, so the buyer returns through `/v1/public/payment-return` (`BuyerReturnsByPOST`); (7) there is no fail URL and no expiry webhook — `expire` is the payment window and arena's sweeps release the hold; (8) AllPay's `refund` has no idempotency key and returns no refund id, so `adapters/allpay/refund.go` (PAY-14b) NEVER calls it before reading `paymentstatus`: it compares AllPay's refunded total P with what other refunds of the payment are known to have returned (K, `payments.RefundRequest.OtherRefundedMinor`) and may have returned (U, `OtherUncertainMinor`), both computed by the engine's `claim` under `LockPayment` (`otherProviderRefunds`, `refunds/drive.go`); P-K = 0 sends, P-K = A with U = 0 on a REPEATED call (`Attempt` ≠ 1) is an earlier call of THIS refund whose answer was lost (succeeded, nothing sent), on a first call it is money refunded elsewhere and goes to a human, anything else is `NeedsReview` (a refund made in the AllPay dashboard shows up that way). The provider refund id is `<order_id>/<arena refund id>` (`allpay.RefundID`). Any other provider without idempotency must get the same two sums, never a blind retry. A REPEATED call (`RefundRequest.Attempt` > 1) that finds nothing unexplained still sends nothing until `allpay.SettleWait` (10 min) after `FirstAttemptAt`, because an earlier call may still be processed by AllPay; a status-4 answer without a partial `refunded`, an answer without amount or currency, or one about another `order_id` sends nothing either; (9) a PAID webhook is confirmed by reading `paymentstatus` before tickets (`confirmPaid`): unpaid or unreadable → the module error wraps `payments.ErrProviderUnreachable` and the per-config route answers 503 `webhook.provider_unconfirmed` (AllPay redelivers, nothing written); already refunded or an order this login does not hold → `EventPaymentManualReview`. Module options (provider API roots) reach the per-config webhook parser through `hcheckout.Handler.WithModuleOptions` plus the config's own mode — a webhook module built with `payments.Options{}` would call the real allpay.to from tests; (10) the core checks the paid amount and currency of every `WebhookConfigRouteOnly` module (Flitt, AllPay) against the payment intent (`hcheckout/payment_amount_guard.go`): a mismatch, or a paid event with no amount (0) or no currency, parks the intent in `manual_review` with `failure_code` `paid_amount_mismatch` (the webhook table now allows created/requires_action → manual_review, and `UpdatePaymentIntentState` stores the failure fields for `manual_review` too); (11) a phone in international form outside 972 sends `client_tehudat=000000000`, AllPay's documented value that hides the Israeli ID field. `ALLPAY_WEBHOOK_SECRET` is deprecated and verifies nothing (the legacy route never consults a config-route-only module); production now requires `STRIPE_WEBHOOK_SECRET` alone. The real-AllPay check is `adapters/allpay/live_test.go` (build tag `allpaylive`, credentials only from `ALLPAY_TEST_LOGIN`/`ALLPAY_TEST_KEY`, never in CI); AllPay's test cards: Visa `4557430402053431` succeeds, `4000000000000002` declines. Integration tests: `httpserver/allpay_checkout_integration_test.go` and `allpay_refund_integration_test.go` (the stub `stubAllPayServer` keeps a ledger of orders per API login, answers `paymentstatus`/`refund` from it and can lose a refund answer; a paid webhook needs `stub.markPaid` first).

- **A Flitt buyer's name and phone travel in `reservation_data`, not in their own fields (P1d, 2026-10-14).** `checkout/url` has only `sender_email` for the buyer (`sender_cell_phone` is a RESPONSE/callback field). Name and mobile phone are accepted as `customer_name` / `phonemobile` inside `reservation_data`, a BASE64 JSON string (docs.flitt.com/api/reservation_data), which is signed like any other non-empty parameter; `adapters/flitt/buyer_fields.go` builds it and leaves the key out entirely when nothing survives. The page demands Latin characters, so the name is folded (`García` → `Garcia`) and a Cyrillic/Hebrew/`ø`/`ł` name is NOT sent (one refused field would fail the whole checkout); the phone is reduced to `+digits` (00 → +, 7+ digits, 16 characters at most). Live probe 2026-10-15 (owner-approved, spec 29 §4.2): the public sandbox merchant accepts `checkout/url` WITH `reservation_data` (HTTP 200, `success`, checkout URL returned), same as without it; whether the page pre-fills the fields, a non-Latin name and the callback echo are unchecked. Rerun with `FLITT_TEST_MERCHANT=1549901 FLITT_TEST_KEY=test go.exe test -tags flittlive -run TestFlittLive -v ./apps/backend/internal/adapters/flitt/` (build tag `flittlive`, credentials from env only, never in CI). AllPay already sends `client_name`/`client_phone` only when the request carries them (`TestCreateCheckoutSession_BuyerDetailsPresenceFollowsTheRequest`).
