"""Wave W1 split (2026-09-04, evening) - fixes the #451 claim/release loop.

AutoForge sessions #2-#45 each claimed batch 451/452/453, judged #451 too
large for one context window, released it and re-queued (37 no-op commits).
This script:

  1. inserts 53 SUB-FEATURES #470-#522 (priorities 1027-1079), each sized for
     ONE coding session, with package-level gates;
  2. turns the 19 MAJOR features #451-#469 into VERIFICATION-ONLY epics that
     depend on their sub-features and run the FULL gate suite once.

Design authority stays 08_architecture/18_bil24_compat_wave1_specification_ru.md;
backlog 09_autoforge/wp_bil24_compat_backlog.md (section 8 = this split).

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_w1_split_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (469, 1026)
CATEGORY = "WP Bil24 Compat W1"
START_ID = 470
START_PRIORITY = 1027

SPEC = "08_architecture/18_bil24_compat_wave1_specification_ru.md"
BACKLOG = "09_autoforge/wp_bil24_compat_backlog.md"

# Package-level gate for ONE sub-feature (the full suite runs in the epic).
SUB_TAIL = (
    f" READ FIRST: {SPEC} (the section numbers below) and the parent epic entry in "
    f"{BACKLOG}. This is ONE sub-feature sized for ONE session: deliver exactly its "
    "Steps, nothing from sibling sub-features. Golden files under "
    "apps/backend/tests/compat/bil24/testdata/wp are corrected ONLY to match the spec "
    "text (spec section 7/8/9 wins over the #450 skeleton), never to match the code. "
    "Gates before marking passing: go build ./... and go vet ./... in apps/backend; go "
    "test of every package you touched plus ./tests/compat/bil24/... ./tests/"
    "staticanalysis/... ./internal/migrations/...; gofmt -l on changed files; DB tests "
    "behind //go:build integration and run once against Docker PG "
    "(DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable, "
    "JWT_SIGNING_SECRET set); if you touched openapi.yaml: run the codegen (openapi30gen + "
    "oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml + node scripts/"
    "gen-ts-client.mjs, remove .compat30.gen.yaml) and commit the generated files; if you "
    "added a migration: bump internal/migrations/migrations_head_test.go and run "
    "arena-migrate locally. Keep top-level httpserver/*.go <= 400 lines; bil24compat must "
    "not import internal/platform/httpserver. NEVER git add . / -A: stage by path, check "
    "git status --short, commit AND push. If the sub-feature still does not fit: implement "
    "the first Steps items, commit by path, push, and write a progress note naming exactly "
    "which step remains - NEVER release the claim as too large, NEVER fabricate."
)

# Full-suite gate for a verification epic.
EPIC_TAIL = (
    " VERIFICATION-ONLY EPIC: do not implement anything new here. All sub-features listed "
    "in the dependencies are passing. Your job: (1) re-run the epic's golden fixtures and "
    "harness scenarios named in its backlog entry and confirm they are green; (2) run the "
    "FULL gate suite: go test ./... WITHOUT pipelines (go test ./... > log 2>&1; echo "
    "EXIT:$?), golangci-lint incl. gofmt (absolute GOLANGCI_LINT_CACHE), npm run admin:test, "
    "npm run type-check, admin build, codegen drift check, migration head pin, local "
    "migrate; (3) verify the latest CI run with gh run view; (4) fix any drift you find "
    "in small commits (by path, pushed); (5) mark passing only when everything is green. "
    "Write a short progress note listing the commands run and their exit codes."
)


def sub(id_, name, desc, steps, deps):
    return {"id": id_, "name": name, "description": desc + SUB_TAIL, "steps": steps,
            "dependencies": deps, "complexity": 3}


SUBS = [
    # ---------------------------------------------------------------- epic 451
    sub(470, "W1-A1a: harness seeding + #450 defect fixes (seed_test.go, scenario ids, binding inventory)",
        "Parent epic #451; spec sections 15.1-15.3, 9.3. Feature #450 left three defects: "
        "harness scenarios cite wrong feature ids, binding_test.go invents order keys "
        "(orderNumber, notes, returnedSum, cardMask) and misses paymentRRN/paymentTerminalId/"
        "paymentCardPAN/paymentCardBank, and setupHarness has no seeding. Deliver: "
        "tests/compat/bil24/seed_test.go (integration tag) that seeds via real gen.Queries: "
        "org (legal name), sales channel (display_number read back as fid, bcrypt token in "
        "settings.gateway.token_hash, fee_percent 5), venue with timezone Europe/Prague and a "
        "city, event published, assigned_seats session bound to the Palac Akropolis plan "
        "fixture (06_venue_maps_and_seating/Palac_Akropolis.svg via seating.ImportSVG), a GA "
        "session with 50 units (AB-51 path) and two tiers, a promo code; harnessState filled "
        "(SeatIDs keyed Parter-3-12 style from session_seats.system_seat_id). binding_test.go "
        "rewritten to the EXACT 36/17/14 inventory now written in spec 9.3 and made strict "
        "(fails on any drift; run it and prove it passes on the pseudonymized sample). Scenario "
        "skips retargeted: 1->#497, 2->#495, 3->#484, 4->#509, 5->#494, 6->#492, 7->#501, "
        "8->#518, 9->#514, 10->#520. Done when the harness boots and seeds against Docker PG "
        "and all tests in tests/compat/bil24 pass.",
        ["seed_test.go with real queries and Akropolis + GA sessions.",
         "binding_test.go exact spec 9.3 inventory, strict.",
         "Scenario skip ids retargeted; harness green on Docker PG."],
        [450]),
    sub(471, "W1-A1b: settings.gateway, fid = display_number, token on every command, org-scoped reads",
        "Parent epic #451; spec section 5 items 1-3 and section 7.1/7.2 scoping. Today reads "
        "are unauthenticated and cross-tenant (hbil24/bil24_compat.go:311, events.sql:110-111) "
        "and fid must be a UUID (bil24compat/translate.go:37). Deliver: sales_channels.settings"
        ".gateway {enabled, token_hash, token_rotated_at, default_locale} read by a small "
        "helper in hbil24/auth.go with legacy gateway_token_hash fallback; fid parsed as int64 "
        "(number or numeric string) -> sales_channels.display_number -> channel -> org_id "
        "(new gen query GetSalesChannelByDisplayNumber); every command validates fid+token "
        "when requireToken (disabled/unknown/missing hash -> -4); GET_ALL_ACTIONS filters "
        "events by the channel org (new query or org param on ListEvents) and status "
        "published; GET_SEAT_LIST/GET_SCHEMA/GET_ORDER_INFO/RESERVATION/UN_RESERVE verify the "
        "session/reservation belongs to the org (-3 otherwise). Split bil24_compat.go: move "
        "auth into hbil24/auth.go (keep the file under 1,000 lines; full split continues in "
        "#476). Tests: unit tests with fakes for fid parsing and token gate; golden "
        "GET_ALL_ACTIONS/isolation.json (org B events absent for org A fid) via the harness. "
        "Done when a channel with enabled=false gets -4 on every command and the isolation "
        "golden is green.",
        ["settings.gateway helper + display_number lookup + token on all commands.",
         "Org scoping of all read commands and hold commands.",
         "Unit tests + golden isolation green."],
        [470]),
    sub(472, "W1-A1c: SCAN_TICKET org resolution via barcodes -> tickets -> sessions -> events",
        "Parent epic #451; spec section 5 item 3 and 7.14. SCAN_TICKET resolves the channel "
        "with GetSalesChannelByIDGlobal (channels.sql:17-24, bil24_compat.go:1399). Deliver: "
        "channel by fid (from #471), barcode lookup across all barcode authorities, then "
        "tickets -> sessions -> events.org_id must equal the channel org else -3 with a "
        "localized description; delete GetSalesChannelByIDGlobal and its query; keep the "
        "existing scanned/revoked -2 semantics. Tests: unit (fakes) for cross-org, golden "
        "SCAN_TICKET/cross_org.json and SCAN_TICKET/basic.json via the harness.",
        ["Org-scoped SCAN_TICKET; global channel query deleted.",
         "Unit + golden tests."],
        [471]),
    sub(473, "W1-A1d: gateway-credential PUT/GET/DELETE endpoint + OpenAPI + audit",
        "Parent epic #451; spec section 5 item 4. Deliver PUT/GET/DELETE /v1/organizations/"
        "{org_id}/channels/{id}/gateway-credential in a new hiam or hcatalog sub-package file "
        "(<= 400-line shim rule): permission channel.update, X-Admin-Reason required, PUT "
        "generates a 32-byte secret, stores bcrypt in settings.gateway.token_hash, sets "
        "enabled=true, rotated_at, returns {fid, token, base_url, image_url, rotated_at} with "
        "the token ONCE (base_url/image_url from PUBLIC_BASE_URL config, empty string if unset); "
        "GET returns {fid, enabled, rotated_at}; DELETE sets enabled=false; audit event "
        "v1.channel.gateway_credential.rotated; OpenAPI 3.1 paths + schemas with descriptions, "
        "drift harness wiring, Go types + TS client regenerated and committed. Handler tests: "
        "200/403 (non-member)/404 (other org channel)/400 (no reason).",
        ["Endpoint + audit.", "OpenAPI + codegen + drift wiring.", "Handler tests."],
        [471]),
    sub(474, "W1-A1e: admin-web gateway credential section on the channel page",
        "Parent epic #451; spec section 5 item 4. In apps/admin-web add a 'Bil24-compatible "
        "gateway' section to the sales channel detail (same file family as the MACS webhook "
        "section in routes/organizations.tsx:1041-1060): shows fid/enabled/rotated_at, "
        "'Issue / rotate token' button with X-Admin-Reason prompt, token shown once with copy, "
        "'Disable' button; uses the generated client types. Vitest tests for render, rotate "
        "flow and secret-once behaviour; npm run type-check and admin build green.",
        ["Section UI + API calls.", "Vitest + type-check + build."],
        [473]),
    # ---------------------------------------------------------------- epic 452
    sub(475, "W1-A2a: migration 0090 compatibility_id_map + compatids package",
        "Parent epic #452; spec sections 3.1 and 4. Deliver migration 0090_compatibility_ids"
        ".sql EXACTLY as spec 3.1 (sequence from 1e9, map table with kinds action/action_event/"
        "category_price/venue/city/country, session_seats.system_seat_id_source), head pin "
        "bump, canonical SQL in queries/compat_ids.sql + hand-written gen file in the "
        "bank_accounts.sql.go style; package internal/platform/compatids with Ensure, "
        "EnsureMany, Resolve, RegisterExternal (rejects >= 1e9 with a typed error), all taking "
        "a gen.DBTX/tx so callers stay transactional; unit tests with fakes and an "
        "integration test for idempotent minting (two Ensure calls -> same id) and external "
        "registration/collision. Run arena-migrate against Docker PG.",
        ["Migration 0090 + head pin + gen queries.", "compatids package + tests.",
         "Local migrate smoke."],
        [471]),
    sub(476, "W1-A2b: named wire structs, int64 ids in existing commands, bil24_compat.go split",
        "Parent epic #452; spec sections 4 and 7 (struct names). Deliver: named response "
        "structs in internal/adapters/bil24compat (GetAllActionsResponse, GetSeatListResponse, "
        "ReservationResponse, ... as listed in spec section 7) with int64 id fields and "
        "omitempty only where the spec says so; TranslateLegacyID parses int64 and resolves "
        "via compatids (UUID input from clients rejected with -2); GET_ALL_ACTIONS (still "
        "flat), GET_SEAT_LIST, GET_SCHEMA, RESERVATION, UN_RESERVE, SCAN_TICKET emit ints: "
        "actionId/actionEventId/categoryPriceId via compatids.Ensure in the same tx, seatId = "
        "session_seats.system_seat_id, ticketId = tickets.system_ticket_id; finish the split "
        "of hbil24/bil24_compat.go into cmd_catalog.go, cmd_cart.go, cmd_order.go, "
        "cmd_tickets.go (no file over 700 lines) keeping all existing tests green; the "
        "bil24compat_layout_188_test.go sentinels stay. Tests: no UUID in any gateway "
        "response (a test that walks every golden and every handler output for uuid "
        "patterns).",
        ["Named structs + int ids.", "Handlers rewired + file split.",
         "No-UUID test + existing tests green."],
        [475]),
    # ---------------------------------------------------------------- epic 453
    sub(477, "W1-A3a: result codes 1/101/-1, unknown command -> -2",
        "Parent epic #453; spec section 6. Deliver constants ResultCodeSessionExpired=1, "
        "ResultCodeUserVisible=101, ResultCodeTransient=-1, unknown command -> -2; update the "
        "result_codes.go header comment, the tests pinning -1 for unknown command "
        "(bil24_compat_157_test.go, bil24_374_test.go) and hbil24 error mapping: DB/pool "
        "errors -> -1, validation -> -2, scope -> -3, business -> 101 with a description "
        "key (localization lands in #478, English strings now).",
        ["Constants + mapping + tests updated."],
        [476]),
    sub(478, "W1-A3b: localized descriptions ru/en/he/cs and locale negotiation",
        "Parent epic #453; spec section 6. Deliver internal/platform/i18n/locales/ru.toml, "
        "he.toml, cs.toml (+ keys in en.toml) with every bil24.* key of spec section 6 "
        "(seat_taken {sector,row,number}, category_sold_out {name,available}, session_expired, "
        "sales_closed, promo_invalid, hold_expired, order_not_found, currency_mismatch, "
        "pricing_mode_unsupported, line_wrong_session, order_cancelled, use_refund_ticket, "
        "unknown_command, invalid_request, not_found, unauthorized, internal); negotiation "
        "ru-RU->ru, en-GB->en, he-IL->he, cs-CZ->cs, unknown -> channel default_locale -> en; "
        "hbil24 uses the bundle for every non-OK description; table-driven test that every "
        "key exists in every locale; goldens RESERVATION/seat_taken_ru.json and "
        "seat_taken_he.json added per spec (descriptions in the target language).",
        ["Locale files + negotiation.", "Wiring + completeness test + goldens."],
        [477]),
    # ---------------------------------------------------------------- epic 454
    sub(479, "W1-A4a: migration 0091 customers/gateway_sessions + gen queries",
        "Parent epic #454; spec section 3.2. Deliver migration 0091_customers.sql EXACTLY as "
        "spec 3.2 (customers with system_id from compatibility_system_id_seq, "
        "customer_identities with strong/weak partial unique indexes, customer_consents, "
        "customer_org_links, customer_attributes, customer_merge_candidates, gateway_sessions, "
        "reservations.gateway_session_id/customer_id, permissions customer.read/"
        "customer.import), head pin bump, queries/customers.sql + queries/gateway_sessions.sql "
        "and gen files; migration tests; local migrate smoke.",
        ["Migration 0091 + head pin.", "Gen queries for customers, identities, links, sessions."],
        [478]),
    sub(480, "W1-A4b: customers package - normalization and Resolve",
        "Parent epic #454; spec section 12.2. Deliver internal/platform/customers: "
        "NormalizeEmail, NormalizePhone (github.com/nyaruka/phonenumbers, default region from "
        "the org's first venue country, invalid phone -> attribute not identity), Resolve per "
        "spec 12.2 (strong keys email/phone, weak device/wc_customer per channel; strong-key "
        "conflict -> customer_merge_candidates, never auto-merge; display_name rules), Touch, "
        "MarkVerified, LinkOrg (upsert customer_org_links with counters). Unit tests with a "
        "fake store: 12+ cases including the family-with-one-phone case; one integration test "
        "against Docker PG for the unique indexes.",
        ["Normalization + Resolve + helpers.", "Unit cases + integration uniqueness test."],
        [479]),
    sub(481, "W1-A4c: CREATE_USER and gateway session lookup (result code 1)",
        "Parent epic #454; spec section 7.3. Deliver CREATE_USER (optional email/firstName/"
        "lastName/phone -> customers.Resolve, new gateway_sessions row with 43-char base64url "
        "token, expires_at now+30d, locale) returning CreateUserResponse{userId int64 = "
        "customers.system_id, sessionId}; shared hbil24 helper requireGatewaySession(userId, "
        "sessionId) returning result code 1 when missing/expired/other org and refreshing "
        "last_seen_at/expires_at otherwise; used by RESERVATION/UN_RESERVE now (other commands "
        "adopt it as they land). Goldens CREATE_USER/basic.json corrected to spec 7.3 and a "
        "second case same_email_new_session (same userId). Unit + harness tests.",
        ["CREATE_USER + session helper.", "Goldens + tests."],
        [480]),
    sub(482, "W1-A4d: public feed persists the buyer; customers read endpoints; admin-web list",
        "Parent epic #454; spec sections 12.2 (call sites) and 12.3. Deliver: hfeed/"
        "public_feed_checkout.go stops discarding name/phone (:308-338) and calls "
        "customers.Resolve, sets reservations.customer_id; GET /v1/organizations/{org_id}/"
        "customers?q= (customer.read, only customers linked to the org, search by exact "
        "normalized email/phone and name ILIKE) and GET .../customers/{id} (identities masked "
        "unless verified, org consents, org attributes); OpenAPI + codegen; admin-web minimal "
        "Customers list + card in org scope with Vitest tests. Integration test: public feed "
        "checkout with name/phone creates the customer and link.",
        ["Public feed -> customers.Resolve.", "Endpoints + OpenAPI.", "Admin-web list/card + tests."],
        [480]),
    # ---------------------------------------------------------------- epic 455
    sub(483, "W1-A5a: mutable hold primitives (ExtendHold, ShrinkHold, RefreshHoldExpiry, ReacquireHold)",
        "Parent epic #455; spec section 7.4 (cart semantics). In hcheckout/hold_api.go add "
        "ExtendHold(tx, reservationID, seats|ga), ShrinkHold(tx, reservationID, seats|ga), "
        "RefreshHoldExpiry(tx, ids, ttl), ReacquireHold(tx, reservationID) reusing "
        "LockSessionSeatsForHold, AllocateGAUnitsTx, IncrementSessionSeatStatusVersion and "
        "writing reservation_ga_items unit_price via priceresolve; an empty reservation after "
        "Shrink becomes cancelled. Integration test (Docker PG): 20 goroutines extend/shrink "
        "the same session and no seat/unit is ever held twice; unit tests for the state "
        "transitions. No gateway wiring in this sub-feature.",
        ["Primitives with locking discipline.", "Concurrency integration test + unit tests."],
        [481]),
    sub(484, "W1-A5b: RESERVATION four shapes over the session cart",
        "Parent epic #455; spec section 7.4. Rewrite handleBil24Reservation: RESERVE/"
        "UN_RESERVE by categoryList or seatList, UN_RESERVE_ALL without actionEventId, one "
        "active reservation per (gateway session, event session) created on first RESERVE and "
        "extended/shrunk afterwards, currency guard (101 currency_mismatch), TTL refresh on "
        "every RESERVE, cartTimeout int seconds, response = whole cart across action events "
        "with sum/discount/charge/totalSum/currency (charge from sales_channels.fee_percent), "
        "GA units as rows with their system_seat_id, pwyw -> 101; errors 1/101/-2/-3 localized. "
        "Goldens RESERVATION/{reserve_by_category,reserve_by_seat,un_reserve,un_reserve_all,"
        "seat_taken,sold_out}.json corrected/added per spec 7.4 (cartTimeout integer!); "
        "harness scenario 3 un-skipped and green; widget seat-status tests still green.",
        ["RESERVATION over ExtendHold/ShrinkHold.", "Goldens + scenario 3."],
        [483]),
    sub(485, "W1-A5c: GET_CART",
        "Parent epic #455; spec section 7.5. Deliver GET_CART: totalSum only (no "
        "totalAmount/estimatedTotal), sum, discountAmount (0 until #491), chargeAmount, "
        "currency, cartTimeout, actionEventList[] with chargePercent = int(fee_percent) and "
        "seatList rows {seatId, categoryPriceId, tariffPlanId:null, price, discount}; empty "
        "cart -> empty list, zeros, resultCode 0. Golden GET_CART/basic.json corrected per "
        "spec 7.5 plus GET_CART/empty.json; unit + harness tests.",
        ["GET_CART handler.", "Goldens + tests."],
        [484]),
    # ---------------------------------------------------------------- epic 456
    sub(486, "W1-A6a: migration 0092 orders/order_items/order_events + gen queries",
        "Parent epic #456; spec section 3.3. Deliver migration 0092_orders.sql EXACTLY as spec "
        "3.3 (pg_trgm, orders with CHECK total = subtotal - discount + charge, order_items one "
        "row per unit, order_events, tickets.order_id, permissions order.read/order.write, "
        "partial unique index one pending_payment order per (customer_id, session_id)), head "
        "pin bump, queries/orders.sql + gen file (insert/update status/list by org with trgm "
        "search/get with items+events/find open order), migration tests, local migrate smoke.",
        ["Migration 0092 + head pin.", "Gen queries."],
        [485]),
    sub(487, "W1-A6b: ordering package + order.expire_sweep job",
        "Parent epic #456; spec section 14.1. Deliver internal/platform/ordering with "
        "CreateOrderFromCheckout(tx, in) (from a pricing_confirmed checkout session + "
        "reservation: order row, one order_item per seat/unit with unit_price from "
        "reservation_ga_items and prorated discount/charge, order_events.created), MarkPaid, "
        "Cancel, Expire, ReconcileLines(tx, reservationID, lines) (spec 7.7 step 3 using "
        "ExtendHold/ShrinkHold), FindOpenOrder(customer, session); worker job "
        "order.expire_sweep (pending_payment with expires_at < now() and no succeeded "
        "payment_intent -> expired). Unit tests for proration and reconcile; integration test "
        "for the sweep.",
        ["ordering package API + tests.", "Expire sweep job registered in arena-worker."],
        [486]),
    sub(488, "W1-A6c: wire ordering into hcheckout/hfeed/payment webhook; issuance sets order fields; v1.order.paid",
        "Parent epic #456; spec sections 14.1 and 9.1. Deliver: hcheckout confirm "
        "(/checkout/{id}/confirm) and hfeed.confirmPublicCheckout (public_feed_checkout.go:985) "
        "call ordering.CreateOrderFromCheckout in the same tx with buyer fields and "
        "customer_id; the payment webhook (payment_intents.go:809) calls ordering.MarkPaid; "
        "htickets.IssueTicketsForCheckout sets tickets.order_id, order_items.ticket_id and "
        "holder_email = orders.buyer_email, and publishes outbox event v1.order.paid "
        "(aggregate order, payload {order_id, org_id, channel_id, session_id, ticket_count}) "
        "exactly once after the last ordinal. Integration test: public feed purchase -> one "
        "order with buyer fields, items = tickets, one v1.order.paid row.",
        ["Three call sites + webhook.", "Issuance fields + v1.order.paid once.", "Integration test."],
        [487]),
    sub(489, "W1-A6d: org-scoped orders endpoints; admin orders reads orders; OpenAPI",
        "Parent epic #456; spec section 14.2. Deliver GET /v1/organizations/{org_id}/orders "
        "(q via trgm over buyer_*, status/session_id/from/to filters, paging), GET .../orders/"
        "{id} (items, events, tickets), POST .../orders/{id}/cancel (order.write, only "
        "pending_payment/manual_review, releases the hold via ordering.Cancel); GET /v1/admin/"
        "orders reads orders instead of checkout_sessions (keep response keys backward "
        "compatible); enforceOrgMembership on all; OpenAPI + codegen + drift wiring; handler "
        "tests incl. tenant 404.",
        ["Endpoints + admin list switch.", "OpenAPI + codegen + tests."],
        [488]),
    sub(490, "W1-A6e: admin-web Orders page in org scope",
        "Parent epic #456; spec section 14.2. Deliver an org-scoped Orders route in "
        "apps/admin-web: table (number = system_id, buyer, session, status, total in currency, "
        "created), search box, status filter, drawer with items and timeline, cancel action "
        "with reason; permission-driven nav entry (order.read); generated client types; "
        "Vitest tests; type-check + build green.",
        ["Orders route + drawer.", "Nav + tests."],
        [489]),
    # ---------------------------------------------------------------- epic 457
    sub(491, "W1-B1a: ADD_PROMO_CODES, CHECK_KDP, discount in GET_CART",
        "Parent epic #457; spec section 7.6. Deliver ADD_PROMO_CODES (union of promoCodeList "
        "and promoCodes, <= 10, case-insensitive match on promo_codes.code of the cart org, "
        "new/exist/error lists via hcheckout.ValidatePromoForLines over cart lines, persist in "
        "gateway_sessions.promo_codes, description = localized first error) and CHECK_KDP "
        "(singular promoCode; 0 or 101); GET_CART applies the first code of the session that "
        "yields a non-zero discount, prorated per row (remainder on the last row). Goldens "
        "ADD_PROMO_CODES/{basic,exist,error}.json, CHECK_KDP/{ok,invalid}.json, "
        "GET_CART/with_promo.json corrected/added per spec; BEHAVIOR_DIFFERENCES.md notes the "
        "single-code limitation.",
        ["Two commands + session promo codes.", "GET_CART discount + goldens."],
        [488]),
    sub(492, "W1-B1b: CREATE_ORDER_EXT",
        "Parent epic #457; spec section 7.7. Deliver CREATE_ORDER_EXT: gateway session (1), "
        "session in scope (-3), sales open (101), reservation of the action event (create "
        "empty + fill from lines when absent), ordering.ReconcileLines so cart == lines, "
        "customers.Resolve(fullName/phone/email) + verified flags untouched, one-open-order "
        "rule (existing pending_payment order with live hold -> same orderId updated; expired "
        "-> old order expired, new created), checkout session insert + confirm with "
        "PricingRules{PlatformFeeBP: fee_percent*100} and the first applicable promo code "
        "(request promoCodes union session codes), orders pending_payment with external_ref = "
        "request orderId, source bil24_gateway; response CreateOrderResponse {orderId, "
        "externalOrderId, sum, discount, charge, totalSum, currency, expiration}; client "
        "total/chargePercent/expectedPrice only in order_events.created.payload."
        "client_reported; -2 for empty lines/orderId, 101 line_wrong_session. Goldens "
        "CREATE_ORDER_EXT/{basic,string_orderid,ga,seated,promo,repeat_same_order}.json "
        "corrected/added per spec 7.7; harness scenario 6 un-skipped and green.",
        ["CREATE_ORDER_EXT through ordering.", "Goldens + scenario 6."],
        [491]),
    sub(493, "W1-B1c: GET_ORDER_INFO strict mode",
        "Parent epic #457; spec section 7.8. Deliver GET_ORDER_INFO returning {order: <Bil24 "
        "Order without ticketList>} (encoder shared with #505 later - for now a local "
        "projection of orders + org + channel in the spec 9.3 key set minus ticketList) and "
        "userMessage on errors; order of another channel/org -> -3. Golden GET_ORDER_INFO/"
        "basic.json corrected per spec.",
        ["GET_ORDER_INFO + golden."],
        [492]),
    # ---------------------------------------------------------------- epic 458
    sub(494, "W1-B2a: PAY_ORDER with hold reacquire, manual_review and synchronous issuance",
        "Parent epic #458; spec section 7.9. Deliver PAY_ORDER: order by system_id in the "
        "channel scope (-3), paid -> 0 idempotent, cancelled/refunded -> 101; expired hold -> "
        "hcheckout.ReacquireHold, failure -> orders/checkout manual_review + 101 hold_expired "
        "+ audit alert log; amount mismatch -> order_events.amount_mismatch only; one tx: "
        "payment_intents (provider manual, state succeeded, provider_payment_id wc:<ref>:"
        "<method>, amount = orders.total), checkout completed, reservation converted (convertjob "
        "logic inline), promo redemption, ordering.MarkPaid, customers.LinkOrg + MarkVerified; "
        "after commit synchronous IssueTicketsForCheckout with holder_email plus the fallback "
        "checkout.issue_tickets job; no platform delivery e-mail unless settings.gateway."
        "platform_email. Goldens PAY_ORDER/{basic,repeat,expired_reacquired,expired_manual_"
        "review}.json per spec; harness scenario 5 un-skipped and green.",
        ["PAY_ORDER transaction + issuance.", "Goldens + scenario 5."],
        [492]),
    sub(495, "W1-B2b: GET_TICKETS_BY_ORDER, SEND_TICKETS_TO_EMAIL, PUBLIC_BASE_URL; scenario 2 end-to-end",
        "Parent epic #458; spec sections 7.10, 7.11, 16. Deliver config PUBLIC_BASE_URL "
        "(required when BIL24_COMPAT_ENABLED, config test, .env.example, deploy/ compose "
        "comment); GET_TICKETS_BY_ORDER (string or int orderId, ticketList {ticketId, pdfUrl, "
        "downloadUrl, barcode, seatId, categoryPriceId}, ticketIdList, empty lists before "
        "issuance; pdfUrl = PUBLIC_BASE_URL + /v1/public/checkout/<token>/tickets/<uuid>/pdf; "
        "barcode = EAN-13 credential when present else system_ticket_id string until #502); "
        "SEND_TICKETS_TO_EMAIL enqueues delivery jobs with recipient_email. Goldens "
        "GET_TICKETS_BY_ORDER/{basic,before_issuance}.json, SEND_TICKETS_TO_EMAIL/basic.json "
        "per spec; harness scenario 2 (CREATE_USER -> RESERVE x2 -> GET_CART -> "
        "ADD_PROMO_CODES -> CREATE_ORDER_EXT -> PAY_ORDER -> GET_TICKETS_BY_ORDER) un-skipped "
        "and green up to the v1.order.paid outbox row.",
        ["Config + two commands.", "Goldens + scenario 2."],
        [494]),
    sub(496, "W1-B2c: CANCEL_RESERVATION and CANCEL_ORDER",
        "Parent epic #458; spec section 7.12. Deliver CANCEL_RESERVATION {reservationId?, "
        "orderId?} (unpaid order -> cancelled via ordering.Cancel, hold released; unknown -> 0; "
        "paid -> 101) and CANCEL_ORDER {orderId} (same; paid -> 101 use_refund_ticket). "
        "Goldens CANCEL_RESERVATION/{basic,unknown}.json, CANCEL_ORDER/{basic,paid}.json per "
        "spec; unit + harness tests.",
        ["Two commands + goldens."],
        [494]),
    # ---------------------------------------------------------------- epic 459
    sub(497, "W1-B3a: GET_ALL_ACTIONS nested catalog - core query, dates in venue TZ, availability, categories",
        "Parent epic #459; spec section 7.1. Deliver the nested GetAllActionsResponse: "
        "published events of the channel org with upcoming scheduled sessions (start_at > "
        "now()-6h), countryList/cityList/venueList via compatids (one query per list, no N+1), "
        "actionEventList with day DD.MM.YYYY and time HH:MM in venues.timezone (session "
        "skipped + warn when NULL; // allow:timeformat: markers), sellEndTime RFC3339 with "
        "offset, availability remaining, categoryLimitList = GA tiers only with placement:false "
        "and tariffIdMap {}, seatingPlanId = actionEventId for seated/hybrid else 0, "
        "seatingPlanName, currency, chargePercent. Goldens GET_ALL_ACTIONS/{basic,ga,seated,"
        "hybrid,no_timezone}.json corrected/added per spec 7.1 (the #450 skeleton uses wrong "
        "keys id/name/legalOwner - fix to actionId/actionName/...); harness scenario 1 "
        "un-skipped and green.",
        ["Nested query + TZ dates + availability.", "Goldens + scenario 1."],
        [476, 478]),
    sub(498, "W1-B3b: GET_ALL_ACTIONS remaining fields, DST golden, performance test",
        "Parent epic #459; spec section 7.1. Deliver the remaining action fields: "
        "fullActionName, description (localized), smallPosterUrl/bigPosterUrl from session "
        "media with events.image_url fallback, minPrice/maxPrice via priceresolve, age from "
        "age_rating (NR -> empty), organizerId = organizations.display_number, organizerName, "
        "firstEventDate/lastEventDate DD.MM.YYYY; golden GET_ALL_ACTIONS/dst_jerusalem.json "
        "(venue Asia/Jerusalem across a DST boundary); integration performance test 100 events "
        "x 3 sessions under 200 ms; eTicket true.",
        ["Remaining fields.", "DST golden + perf test."],
        [497]),
    # ---------------------------------------------------------------- epic 460
    sub(499, "W1-B4: GET_SEAT_LIST full form",
        "Parent epic #460; spec section 7.2. Deliver GetSeatListResponse: currency; categoryList "
        "with tri-state placement (true seated tiers, false GA tiers inside a plan, key absent "
        "for pure-GA sessions - use a *bool with omitempty), availability remaining, price via "
        "priceresolve, tariffIdMap {}; seatList with seatId = system_seat_id, available bool, "
        "location {sector,row,number}, GA units as pseudo-seats for hybrid (sector = tier "
        "name), [] for pure GA; availableOnly filters seatList only; -3 outside scope. "
        "Goldens GET_SEAT_LIST/{basic,seated,hybrid,ga,available_only}.json corrected/added "
        "per spec; GET_SCHEMA stays consistent.",
        ["Handler + goldens."],
        [484]),
    # ---------------------------------------------------------------- epic 461
    sub(500, "W1-B5a: RenderSBT10SVG encoder + golden SVG test",
        "Parent epic #461; spec section 8. Deliver hseating.RenderSBT10SVG(geometry, seats, "
        "tiers, categoryIds, statusVersion) emitting namespace http://www.w3.org/2015/sbt/1.0, "
        "<metadata> with <sbt:category sbt:id sbt:index sbt:name sbt:color sbt:price "
        "sbt:class>, <g sbt:sect><g sbt:row><circle sbt:id=system_seat_id sbt:state 1|4 "
        "sbt:cat=INDEX sbt:seat cx cy r fill>, viewBox, GA zones as decor without sbt:id, "
        "sbt:statusVersion; deterministic output. Tests: canonical-XML golden against "
        "testdata/wp/svg/palac_akropolis.sbt.svg regenerated from the seeded Akropolis "
        "session (header keeps the synthetic disclaimer), plus a DOM-level test replaying the "
        "JS rules (attrNS prefix + namespace, catByIndex, ancestor sbt:sect, viewBox parse).",
        ["Encoder.", "Golden + JS-rules test."],
        [476]),
    sub(501, "W1-B5b: GET /compat/bil24/image route, fid auth, caching, OpenAPI",
        "Parent epic #461; spec section 8. Deliver the route in bil24_shims.go (no JWT): "
        "type != seatingPlan -> 404, actionEventId via compatids, fid -> channel -> org, "
        "published session of that org only else 404, ETag = geometry_checksum:seat_status_"
        "version, Cache-Control no-cache, If-None-Match -> 304, Content-Type image/svg+xml; "
        "OpenAPI path + drift wiring; harness scenario 7 un-skipped and green (shape, 304, "
        "sbt:cat indices match metadata); GA session -> 404.",
        ["Route + auth + caching.", "OpenAPI + scenario 7."],
        [500]),
    # ---------------------------------------------------------------- epic 462
    sub(502, "W1-B6a: migration 0093, ean13 package, issuance writes EAN-13 credential + barcodes row",
        "Parent epic #462; spec sections 3.4 and 11. Deliver migration 0093_ean13_credentials"
        ".sql (widen CHECK to static_qr/pdf/ean13, shape CHECK), head pin bump; package "
        "internal/platform/barcodes/ean13 (Encode(prefix, n), Valid(s), tests against the "
        "real code 2402604868419 and 100 generated ones); IssueTicketsForCheckout writes "
        "ticket_credentials(type ean13, payload 21 + zero-padded 10-digit system_ticket_id + "
        "check digit) and barcodes(platform authority, external_ref = ean13, ticket_id); "
        "GET_TICKETS_BY_ORDER barcode = ean13; integration test on issuance.",
        ["Migration + ean13 package.", "Issuance + barcodes + test."],
        [476]),
    sub(503, "W1-B6b: revoke fix, scan paths accept EAN-13, PDF number, backfill job",
        "Parent epic #462; spec section 11. Deliver: RevokeTicketArtifactsTx revokes "
        "static_qr, pdf and ean13 (fix the \"qr\" typo at htickets/cancel.go:568) and sets "
        "barcodes.status = revoked; SCAN_TICKET and /v1/scanner/validate resolve EAN-13 through "
        "barcodes; arena PDF prints the EAN-13 number as text under the QR; worker job "
        "tickets.backfill_ean13 for tickets without the credential. Tests: cancel revokes all "
        "three credential types (revoked_at asserted on each), scan by EAN-13, backfill "
        "idempotent.",
        ["Revoke fix + scan paths.", "PDF number + backfill job + tests."],
        [502]),
    # ---------------------------------------------------------------- epic 463
    sub(504, "W1-B7a: migration 0094 + orderexport extraction (MACS unchanged)",
        "Parent epic #463; spec sections 3.5 and 9.3 (projection). Deliver migration "
        "0094_wp_webhook_subscribers.sql (channel_id + partial unique index), head pin bump; "
        "package internal/platform/orderexport holding the DB projection currently in "
        "macs/export.go (Order, Ticket, proration, sold price, discountReason, showTime in "
        "venue TZ) as neutral structs with a QueryOrder(ctx, pool, orderID) and "
        "QuerySession(ctx, pool, sessionID); macs becomes an encoder over orderexport with NO "
        "behaviour change (macs golden sample_tickets.json and all macs tests stay green "
        "unchanged).",
        ["Migration 0094.", "orderexport package; macs re-pointed; goldens unchanged."],
        [488, 502]),
    sub(505, "W1-B7b: bil24wire encoder with BINDING key-set test",
        "Parent epic #463; spec section 9.3. Deliver internal/platform/bil24wire: "
        "EncodeOrder(orderexport.Order, ctx) producing EXACTLY the 36/17/14 key sets of spec "
        "9.3 (string holderStatus NEVER_USE/REFUND, category string, seatLocation null or "
        "object, naive showTime, actionEvent.id = session actionEventId via compatids, "
        "actionId, refundDate RFC3339 with offset, charge prorated with remainder on the last "
        "ticket, discountReason 'Промокод <code>' or null, actionLegalOwner from the org legal "
        "name, frontend/agent/acquiring/paymentMethod filled per the spec example) and "
        "EncodeTicketRefunded; the BINDING test compares the encoder's key sets with "
        "testdata/wp/bil24_orders_pseudonymized.json inventories and FAILS on drift; "
        "GET_ORDER_INFO (#493) switched to this encoder minus ticketList.",
        ["Encoder + refunded shape.", "Binding test + GET_ORDER_INFO switch."],
        [504]),
    sub(506, "W1-B7c: bil24_wp dispatcher + new outbox producers",
        "Parent epic #463; spec sections 9.1 and 9.2. Deliver bil24wire.Dispatcher as the "
        "third member of multiDispatcher (cmd/arena-worker/main.go:173-176): subscriber by "
        "kind bil24_wp + channel_id (order/session channel; for events every bil24_wp "
        "subscriber of channels where the event is published), mapping v1.order.paid -> "
        "order.paid, v1.order.cancelled -> order.cancelled, v1.ticket.cancelled/refunded/"
        "revoked -> ticket.refunded, v1.event.published/updated + v1.session.updated/cancelled "
        "-> event.created/changed/deleted with data [{actionEventId}], envelope {id, created, "
        "type, data}, optional X-Arena-Signature, 2xx = success, non-2xx -> error for outbox "
        "retry; producers: v1.order.cancelled in ordering.Cancel, v1.event.published/updated "
        "and v1.session.updated/cancelled in hcatalog (status/PATCH handlers). Unit tests with "
        "an httptest receiver for every mapping and for skip-when-no-subscriber.",
        ["Dispatcher + mapping.", "Producers + unit tests."],
        [505]),
    sub(507, "W1-B7d: wp-webhook PUT/GET/DELETE with synchronous test delivery; admin-web section",
        "Parent epic #463; spec section 9.2 (registration). Deliver PUT/GET/DELETE /v1/"
        "organizations/{org_id}/channels/{id}/wp-webhook (channel.update, X-Admin-Reason; PUT "
        "deactivates the previous, creates kind bil24_wp with channel_id, sends {type:test, "
        "data:null} synchronously with a 10 s timeout and returns test_delivery {ok, "
        "http_status}); OpenAPI + codegen + drift wiring; handler tests with httptest; "
        "admin-web section next to the gateway credential section with Vitest tests.",
        ["Endpoints + test delivery.", "OpenAPI + admin-web + tests."],
        [506]),
    sub(508, "W1-B7e: integration round-trip through real cancel handler, outbox and wpstub",
        "Parent epic #463; spec section 15.3 scenario 4 (delivery part). Integration test "
        "(Docker PG, real handlers, real OutboxEventsDispatcher + multiDispatcher): a paid "
        "order's v1.order.paid reaches wpstub as order.paid with all N tickets; POST /v1/"
        "tickets/{id}/cancel -> ticket.refunded reaches wpstub once; wpstub answering 503 "
        "once -> retry with next_attempt_at set and success on the second attempt; replay of "
        "the same envelope -> deduplicated by wpstub. testdata/wp/wp_receiver/*.json scenarios "
        "drive the assertions.",
        ["Round-trip integration test."],
        [507]),
    # ---------------------------------------------------------------- epic 464
    sub(509, "W1-B8: REFUND_TICKET gateway command",
        "Parent epic #464; spec section 7.13. Deliver REFUND_TICKET {ticketId, reason?, "
        "refundPrice?} wrapping the cancel transaction of htickets/cancel.go (refactor the tx "
        "body into an exported function if needed) with refund_mode manual, actor gateway:<fid>, "
        "ticket must belong to an order of the channel org (-3), already cancelled -> 0, "
        "refundPrice -> tickets.refund_price, orders.status refunded/partially_refunded, "
        "order_events.ticket_refunded, response {ticketId, refundDate}. Goldens REFUND_TICKET/"
        "{ok,repeat,other_org}.json per spec; harness scenario 4 un-skipped and green (refund "
        "reaches wpstub and macs stub once, replay deduped).",
        ["Command + goldens + scenario 4."],
        [508]),
    # ---------------------------------------------------------------- epic 465
    sub(510, "W1-Ma: MACS order.paid from v1.order.paid; body-level success check (M1, M2)",
        "Parent epic #465; spec section 10 M1-M2. Deliver: macs.Dispatcher consumes "
        "v1.order.paid and sends order.paid with data {id, status:PAID, ticketList[]} from "
        "orderexport (single-ticket synthetic order for tickets issued without an order, e.g. "
        "complimentary, still triggered by v1.scanner.ticket.issued ONLY when tickets.order_id "
        "is NULL); ticket.refunded unchanged; success only when 2xx AND body {status:OK}, "
        "otherwise an error so the outbox retries. Stub (macs/stub) answers 200 "
        "{status:Error} for order.paid without data.ticketList or status != PAID; AB-50g "
        "round-trip test updated: N tickets delivered once, an Error body produces a retry.",
        ["Dispatcher mapping + success check.", "Stub + round-trip tests."],
        [508]),
    sub(511, "W1-Mb: MACS ids, EAN-13, callback URL validation, golden and contract doc (M3-M5)",
        "Parent epic #465; spec section 10 M3-M5. Deliver: actionEvent.id = session "
        "actionEventId and actionId via compatids in the MACS ticket, barcode = EAN-13 "
        "credential with barcodeFormat {id:0, name:EAN-13}, PUT macs-webhook rejects a "
        "callback_url not ending in /api/_wh/tickets with 422; macs golden sample_tickets.json "
        "regenerated to the new shape (still BINDING, synthetic disclaimer kept), AB-50h/50i "
        "tests updated; 17_macs_integration_contract.md reconciled with a runbook note that "
        "the actionEvent.id key changes for previously imported test events.",
        ["Ids + barcode + URL validation.", "Golden + tests + contract doc."],
        [510]),
    # ---------------------------------------------------------------- epic 466
    sub(512, "W1-C1a: migration 0095 api_keys + apikeys package",
        "Parent epic #466; spec sections 3.6 and 13.1. Deliver migration 0095_api_keys.sql "
        "(spec 3.6 incl. permissions api_key.manage and import.bil24_session), head pin bump, "
        "gen queries; package internal/platform/apikeys: Issue (ak_<prefix12>_<secret43>, "
        "bcrypt), Authenticate(raw) by key_prefix with revoked/expires checks, scope validation "
        "(reject platform.*, admin.*, api_key.manage), TouchLastUsed throttled to once a "
        "minute; unit tests + integration test for uniqueness.",
        ["Migration + package + tests."],
        [497]),
    sub(513, "W1-C1b: service actor middleware, org-auth twins, rate limit, audit",
        "Parent epic #466; spec section 13.1. Deliver: applyAuth accepts Authorization: Bearer "
        "ak_... producing auth.Actor{Type: service, ID: key id} with permissions = scopes "
        "(RBAC checker consults scopes for service actors); enforceOrgMembership/"
        "enforceMembershipInOrg (server_orgauth.go) and the twins hcatalog/orgauth.go, "
        "hiam/orgauth.go, hpayments/orgauth.go, hbankaccounts/orgauth.go, hseating/authz.go "
        "accept a service actor ONLY for api_keys.org_id - one table-driven test covering all "
        "five twins; rate limit 600/min keyed by api_key id via platform/ratelimit; audit "
        "actor api_key:<id> on mutations.",
        ["Middleware + RBAC scopes.", "Org-auth twins table test + rate limit + audit."],
        [512]),
    sub(514, "W1-C1c: api-keys endpoints, OpenAPI, admin-web tab, no-seats event flow under a key",
        "Parent epic #466; spec sections 13.1 and 13.4. Deliver POST/GET/DELETE /v1/"
        "organizations/{org_id}/api-keys (api_key.manage, X-Admin-Reason, key shown once, "
        "scopes validated), OpenAPI + codegen + drift wiring; admin-web 'API keys' tab (issue "
        "with scope picker, shown once, revoke) with Vitest tests; integration test: a key with "
        "the spec 13.1 scope set creates event -> session -> tiers -> media -> publish, the "
        "event then appears in GET_ALL_ACTIONS for the channel; org A key on org B -> 403; "
        "revoked -> 401. Harness scenario 9 un-skipped and green.",
        ["Endpoints + OpenAPI.", "Admin-web tab.", "Integration flow + scenario 9."],
        [513]),
    # ---------------------------------------------------------------- epic 467
    sub(515, "W1-C3a: ImportSBTSVG parser and geometry external ids",
        "Parent epic #467; spec section 13.3. Deliver seating.ImportSBTSVG(raw) in "
        "internal/domain/seating: metadata categories (sbt:id external id, sbt:index, name, "
        "color, price), seats = <circle> with sbt:id + sbt:state, sector from the nearest "
        "ancestor sbt:sect, row from ancestor sbt:row or <title>, number from sbt:seat, "
        "category by sbt:cat index; validation errors for missing sbt:cat, duplicate sbt:id, "
        "missing viewBox; everything else decor. Geometry gains seats[].external_id and "
        "categories[].external_id (int64, optional) included in Canonicalize/Checksum. Tests "
        "on testdata/wp/svg/palac_akropolis.sbt.svg and a synthetic error fixture per rule.",
        ["Parser + geometry fields + tests."],
        [514, 501]),
    sub(516, "W1-C3b: materialization keeps external seat ids (system_seat_id) across rebind",
        "Parent epic #467; spec sections 3.1 and 13.2 step 6. Deliver: session seat "
        "materialization (hseating/bind.go path) sets session_seats.system_seat_id = "
        "geometry.seats[].external_id when present (system_seat_id_source = bil24) and "
        "re-applies it on rebind; explicit values must not collide with the sequence "
        "(integration test: rebind twice, ids identical; a plan without external ids still "
        "gets sequence ids); GET_SEAT_LIST/GET_SCHEMA/image use those ids automatically.",
        ["Materialization + rebind test."],
        [515]),
    sub(517, "W1-C3c: POST imports/bil24-session - venue/event/session/tiers upsert, ids, publish",
        "Parent epic #467; spec section 13.2 steps 1-5, 7-10 (no plan yet). Deliver "
        "httpserver/himports with POST /v1/organizations/{org_id}/imports/bil24-session "
        "(permission import.bil24_session): validation of external ids < 1e9 (422 "
        "compat.external_id_out_of_range), venue by external_bil24_id or created with mandatory "
        "timezone (422 venue.timezone_required) and city/country via geo, event/session/tiers "
        "upsert through compatids.RegisterExternal (GA sessions only in this sub-feature: "
        "admission_mode general_admission with capacities from availability), currency, "
        "start_at from day+time in the venue TZ, sale window, poster side-load, publish flag "
        "through the standard gate, response {event_id, session_id, tier_ids, "
        "seating_plan_version_id: null, seats_materialized: 0, warnings, created}; idempotent "
        "on repeat (created:false, same ids); OpenAPI + codegen + drift wiring; handler tests.",
        ["Endpoint for GA sessions with idempotent upserts.", "OpenAPI + tests."],
        [516]),
    sub(518, "W1-C3d: imports/bil24-session seated path - plan, version, bind, blocked seats; scenario 8",
        "Parent epic #467; spec section 13.2 step 6 and 10. Extend the endpoint: with svg -> "
        "ImportSBTSVG -> seating_plans (plan_type by mode, name = seatingPlanName) -> version "
        "-> bind to the session with category_tier_map by category index -> materialization "
        "with Bil24 seat ids; seats with available:false -> blocked with a warning; hybrid "
        "detection (placement true + GA categories); 409 import.session_has_sales when a "
        "session with sales would change its seat set. Harness scenario 8 un-skipped and "
        "green: import twice -> created:false, GET_SEAT_LIST returns the Bil24 seatIds, image "
        "returns the same sbt:ids, and the imported hybrid session sells through RESERVATION "
        "-> CREATE_ORDER_EXT -> PAY_ORDER with original ids in order.paid.",
        ["Seated path + warnings + 409.", "Scenario 8."],
        [517]),
    # ---------------------------------------------------------------- epic 468
    sub(519, "W1-C7a: migration 0096 + customer.import job with bil24_orders_json and CSV parsers",
        "Parent epic #468; spec sections 3.7 and 12.4. Deliver migration 0096_customer_imports"
        ".sql, head pin bump, gen queries; worker job customer.import with modes dry_run/apply: "
        "parsers bil24_orders_json (fixed mapper of spec 12.4) and CSV via mapping.columns "
        "with org_rule; each row through customers.Resolve without auto-merge, row_hash "
        "idempotency, consents source import:<label> never verified, customer_org_links "
        "(source import) + aggregates, customer_attributes; dry_run_report/apply_report "
        "{rows, created, matched, merge_candidates, skipped, by_org, errors}. Tests on "
        "testdata/wp/bil24_orders_pseudonymized.json: report counts, apply twice -> all matched, "
        "no verified marketing consent.",
        ["Migration + job + parsers.", "Tests on the sample."],
        [488]),
    sub(520, "W1-C7b: customer-imports admin endpoints, OpenAPI, admin-web page; scenario 10",
        "Parent epic #468; spec section 12.4. Deliver POST /v1/admin/customer-imports "
        "{file_media_id, source_label, org_id?, mapping, legal_basis}, POST .../{id}/dry-run, "
        "POST .../{id}/apply, GET .../{id}, GET .../{id}/rows?action= (platform.superadmin, "
        "X-Admin-Reason), OpenAPI + codegen + drift wiring, handler tests; admin-web page under "
        "Platform (media upload, mapping form, dry-run report, apply) with Vitest tests; "
        "harness scenario 10 un-skipped and green.",
        ["Endpoints + OpenAPI.", "Admin-web page + scenario 10."],
        [519]),
    # ---------------------------------------------------------------- epic 469
    sub(521, "W1-Da: ADR-034..038, gateway doc, BEHAVIOR_DIFFERENCES, ops runbook, AGENTS.md",
        "Parent epic #469; spec section 17 and backlog #469. Deliver: ADR rows 034-038 in "
        "08_architecture/11_architecture_decision_log_ru.md; 01_api_compatibility_gateway_ru.md "
        "answers its Open Questions from the spec; tests/compat/bil24/BEHAVIOR_DIFFERENCES.md "
        "current (single promo code, seatingPlanId = actionEventId, holderStatus spelling, "
        "manual_review after PAY_ORDER, result codes); docs/ops/bil24_gateway.md runbook "
        "(provision a channel credential, register the WP webhook, rotate, dead-lettered outbox "
        "rows, switch a site by option, roll back by URL, stand orgs lampyris-staging/"
        "vino-staging); AGENTS.md conventions learned in this wave. Docs tests green.",
        ["ADRs + gateway doc + differences.", "Runbook + AGENTS.md."],
        [509, 511, 518, 520]),
    sub(522, "W1-Db: OpenAPI completeness for /compat/bil24/* and env/deploy for PUBLIC_BASE_URL",
        "Parent epic #469; spec section 16. Deliver openapi.yaml coverage of POST /compat/"
        "bil24/json (request oneOf by command with the spec 7 request shapes, response oneOf "
        "by command with the named structs) and GET /compat/bil24/image, drift harness "
        "wiring for both, codegen; .env.example and deploy/ compose document PUBLIC_BASE_URL "
        "and per-channel gateway settings; openapi docs tests green.",
        ["OpenAPI for compat routes + codegen.", "env/deploy docs."],
        [521]),
]

EPIC_SUBS = {
    451: [470, 471, 472, 473, 474],
    452: [475, 476],
    453: [477, 478],
    454: [479, 480, 481, 482],
    455: [483, 484, 485],
    456: [486, 487, 488, 489, 490],
    457: [491, 492, 493],
    458: [494, 495, 496],
    459: [497, 498],
    460: [499],
    461: [500, 501],
    462: [502, 503],
    463: [504, 505, 506, 507, 508],
    464: [509],
    465: [510, 511],
    466: [512, 513, 514],
    467: [515, 516, 517, 518],
    468: [519, 520],
    469: [521, 522],
}


def validate() -> None:
    ids = [f["id"] for f in SUBS]
    assert ids == list(range(START_ID, START_ID + len(SUBS))), ids
    known = set(ids) | {450}
    for f in SUBS:
        for dep in f["dependencies"]:
            assert dep in known and dep < f["id"], (f["id"], dep)
    covered = sorted(i for subs in EPIC_SUBS.values() for i in subs)
    assert covered == ids, covered


def install() -> None:
    validate()
    connection = sqlite3.connect(DB)
    try:
        head = connection.execute("SELECT MAX(id), MAX(priority) FROM features").fetchone()
        if tuple(head) != EXPECTED_HEAD:
            raise SystemExit(
                f"Unexpected queue head {tuple(head)}; expected {EXPECTED_HEAD}. Refusing insert."
            )
        backup = ROOT / ".autoforge" / "backups" / (
            f"arena_new_features_before_w1_split_{datetime.now():%Y%m%d_%H%M%S}.db"
        )
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as backup_connection:
            connection.backup(backup_connection)

        connection.execute("BEGIN IMMEDIATE")
        for offset, item in enumerate(SUBS):
            connection.execute(
                """
                INSERT INTO features (
                    id, priority, category, name, description, steps,
                    passes, in_progress, dependencies, needs_human_input,
                    human_input_request, human_input_response, complexity
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL, ?)
                """,
                (
                    item["id"], START_PRIORITY + offset, CATEGORY, item["name"],
                    item["description"], json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]), item["complexity"],
                ),
            )
        for epic_id, sub_ids in EPIC_SUBS.items():
            row = connection.execute(
                "SELECT name, description, dependencies, in_progress FROM features WHERE id = ?",
                (epic_id,),
            ).fetchone()
            if row is None:
                raise SystemExit(f"Epic #{epic_id} missing")
            name, description, deps_json, in_progress = row
            if in_progress:
                raise SystemExit(f"Epic #{epic_id} is in progress; stop the agent first")
            old_deps = json.loads(deps_json) if deps_json else []
            new_deps = sorted(set(old_deps) | set(sub_ids))
            new_name = name.replace("[MAJOR]", "[EPIC-VERIFY]").replace("[NORMAL]", "[EPIC-VERIFY]")
            new_desc = (
                f"EPIC (verification only) for sub-features {sub_ids}. Original scope, kept for "
                f"reference: {description.split(' READ FIRST:')[0]}" + EPIC_TAIL
            )
            connection.execute(
                "UPDATE features SET name = ?, description = ?, dependencies = ?, "
                "steps = ?, complexity = 3 WHERE id = ?",
                (new_name, new_desc, json.dumps(new_deps),
                 json.dumps(["Confirm sub-feature goldens/scenarios green.",
                             "Run the FULL gate suite and gh run view.",
                             "Fix drift in small pushed commits; mark passing."]),
                 epic_id),
            )
        connection.commit()
        print(
            f"Installed {len(SUBS)} sub-features (ids {START_ID}-{START_ID + len(SUBS) - 1}, "
            f"priorities {START_PRIORITY}-{START_PRIORITY + len(SUBS) - 1}); "
            f"{len(EPIC_SUBS)} epics converted; backup: {backup}"
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
