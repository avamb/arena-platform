"""Wave W1 (AutoForge) - WP sites (Lampyris, Vino&Co) on arena via the
Bil24-compatible gateway. Features #450-#469, priorities 1007-1026.

Design authority: 08_architecture/18_bil24_compat_wave1_specification_ru.md
Backlog (human-readable, same ids): 09_autoforge/wp_bil24_compat_backlog.md

No human gates: owner decision 2026-09-04 (evening) - the whole wave runs
unattended. #450 builds the contract harness + fixtures FROM THE SPEC (the PHP
wire shapes and the pseudonymized real order sample are already in the repo);
#465 (MACS fixes) is verified against macs/stub; live MACS verification is a
later interactive checklist item, not a queue blocker. All features are
complexity 3 so model routing picks the strongest model.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_wp_bil24_compat_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (449, 1006)
CATEGORY = "WP Bil24 Compat W1"
START_ID = 450
START_PRIORITY = 1007

SPEC = "08_architecture/18_bil24_compat_wave1_specification_ru.md"
BACKLOG = "09_autoforge/wp_bil24_compat_backlog.md"

TAIL = (
    f" READ FIRST: {SPEC} (design authority; section numbers below refer to it) and "
    f"{BACKLOG} (feature entry with file:line facts). Definition of done = the golden "
    "fixtures named in the feature are green under apps/backend/tests/compat/bil24 with "
    "STRICT key-set comparison; never edit a golden file to get green - stop and ask. "
    "Gates before marking passing: go test ./... FULL and WITHOUT pipelines "
    "(go test ./... > log 2>&1; echo EXIT:$?), golangci-lint incl. gofmt "
    "(GOLANGCI_LINT_CACHE must be an absolute path), npm run admin:test, npm run type-check, "
    "admin build, codegen drift (openapi30gen + oapi-codegen@v2.4.1 "
    "--config=apps/backend/openapi/oapi-codegen.yaml + node scripts/gen-ts-client.mjs, then "
    "remove .compat30.gen.yaml), migration head pin bumped, DB tests behind "
    "//go:build integration, local migrate against Docker PG "
    "(DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable, "
    "JWT_SIGNING_SECRET set). Every new route in openapi.yaml (3.1, no nullable:) and in "
    "buildDriftTestServer; top-level httpserver/*.go <= 400 lines; bil24compat must not "
    "import internal/platform/httpserver. NEVER git add . / git add -A - stage by path and "
    "check git status --short. Commit AND push, then verify CI with gh run view. Never "
    "weaken a test to get green."
)


def gate(prompt: str, label: str) -> str:
    return json.dumps(
        {
            "prompt": prompt,
            "fields": [
                {
                    "id": "confirm",
                    "label": label,
                    "type": "boolean",
                    "required": True,
                    "placeholder": None,
                    "options": None,
                }
            ],
        }
    )


FEATURES = [
    {
        "id": 450,
        "name": "W1-0 [MAJOR]: contract fixtures + golden harness + wpstub from the spec (K6)",
        "description": (
            "Spec sections 15.1-15.3. tests/compat/bil24 today has 20 synthetic fixtures "
            "(vinoandco_fixtures.json: no RESERVATION/GET_CART/CREATE_USER/PAY_ORDER) and a "
            "stale BEHAVIOR_DIFFERENCES.md that claims CREATE_ORDER_EXT returns 0. Deliver the "
            "contract harness every later feature is judged by: (1) request bodies for all 15 "
            "commands and the 4 RESERVATION shapes under testdata/wp/requests/<COMMAND>/"
            "<case>.json, taken VERBATIM from spec section 7 (they are the shapes the PHP "
            "sends: fid int, both promo keys, string and int orderId, UN_RESERVE_ALL without "
            "actionEventId); (2) golden responses under testdata/wp/golden/<COMMAND>/<case>.json "
            "per spec section 7 with placeholders {{actionEventId}}, {{seatId:Parter-3-12}}, "
            "{{orderId}}, {{now+ttl}} resolved by the harness from seeded data, STRICT key-set "
            "comparison after canonical JSON normalization (a missing or extra key fails); "
            "(3) harness_test.go (//go:build integration) that boots httpserver with real "
            "gen.Queries + pool, seeds org/channel (display_number = fid, bcrypt token)/venue "
            "with timezone/event with an assigned_seats session (Palac Akropolis fixture) and a "
            "GA session with units/tiers/promo code/subscribers, and runs scenarios 1-10 of spec "
            "15.3 as t.Run steps that call t.Skip with the feature id while the command is not "
            "implemented yet (so the harness is green now and each later feature un-skips its "
            "steps); (4) wpstub package replaying bil24-notification-receiver.php (400 without "
            "type/data, 200 {ok:true} otherwise, dedup ticket.refunded by data.id, stores "
            "bil24_tickets); (5) a golden SVG fixture skeleton for spec section 8 built from the "
            "Akropolis geometry (synthetic until the owner supplies a real sbt SVG - say so in "
            "the file header); (6) a BINDING key-set test that testdata/wp/"
            "bil24_orders_pseudonymized.json matches the 36/17/14-key inventory of spec 9.3 "
            "(this documents the target shape for #463); (7) BEHAVIOR_DIFFERENCES.md rewritten "
            "from the spec. Do NOT implement any command in this feature. Done when the harness "
            "and wpstub tests are green in the Integration job and every scenario step is "
            "present (skipped with a feature id)." + TAIL
        ),
        "steps": [
            "Request + golden fixtures for 15 commands from spec section 7.",
            "Integration harness with seeding and scenario skeleton (skips carry feature ids).",
            "wpstub receiver + SVG fixture skeleton + binding key-set test on the sample.",
            "BEHAVIOR_DIFFERENCES.md rewritten.",
        ],
        "complexity": 3,
        "dependencies": [],
    },
    {
        "id": 451,
        "name": "W1-A1 [MAJOR]: per-channel gateway, integer fid, token on every command, credential provisioning (K1, K2)",
        "description": (
            "Spec sections 5 and 3.1. Today hbil24/bil24_compat.go:311 serves every org's "
            "events to any caller, fid must be a UUID (bil24compat/translate.go:37) while the "
            "site sends (int), read commands skip validateGatewayToken (:1245) and SCAN_TICKET "
            "resolves the channel globally (:1399). Deliver: sales_channels.settings.gateway "
            "{enabled, token_hash, token_rotated_at, default_locale} with legacy "
            "gateway_token_hash fallback; fid = sales_channels.display_number -> channel -> "
            "org_id on EVERY command incl. GET_ALL_ACTIONS/GET_SEAT_LIST/GET_SCHEMA/"
            "GET_ORDER_INFO (disabled/unknown -> -4); all gateway queries org-scoped; "
            "SCAN_TICKET resolves org via barcodes->tickets->sessions->events and rejects "
            "cross-org; delete GetSalesChannelByIDGlobal; PUT/GET/DELETE "
            "/v1/organizations/{org_id}/channels/{id}/gateway-credential (channel.update, "
            "X-Admin-Reason, token shown once, response {fid, token, base_url, image_url, "
            "rotated_at}, audit v1.channel.gateway_credential.rotated); admin-web section on "
            "the channel page modelled on the MACS webhook section (organizations.tsx:1041-1060). "
            "Tests: tenant isolation, token on reads, SCAN_TICKET cross-org -> -3, provisioning "
            "200/403/404, admin tests. Done when golden GET_ALL_ACTIONS/isolation.json and "
            "SCAN_TICKET/cross_org.json are green." + TAIL
        ),
        "steps": [
            "Channel gateway settings + fid=display_number + token on all commands.",
            "Org scoping of every gateway query; SCAN_TICKET fix.",
            "gateway-credential endpoints + OpenAPI + codegen + admin-web section.",
            "Golden isolation/cross_org green.",
        ],
        "complexity": 3,
        "dependencies": [450],
    },
    {
        "id": 452,
        "name": "W1-A2 [MAJOR]: migration 0090 compatibility_id_map, compatids package, integer IDs on the wire (K3)",
        "description": (
            "Spec sections 3.1 and 4. Every ID the gateway emits is a UUID string "
            "(bil24_compat.go:324, :568, schema.go:141); the site casts (int) -> 0. Deliver: "
            "migration 0090_compatibility_ids.sql EXACTLY as spec 3.1 (sequence "
            "compatibility_system_id_seq START 1000000000, table compatibility_id_map(kind, "
            "system_id, platform_id, source), session_seats.system_seat_id_source), head pin "
            "bump, gen queries; package internal/platform/compatids with Ensure/EnsureMany/"
            "Resolve/RegisterExternal (RegisterExternal rejects system_id >= 1e9, all "
            "transactional, lazy minting via INSERT ... ON CONFLICT DO NOTHING RETURNING); "
            "bil24compat.TranslateLegacyID parses int64 (string or number) and resolves "
            "through the map; NAMED response structs in bil24compat for every command of spec "
            "section 7 with int64 ids replacing map[string]any; rewire GET_ALL_ACTIONS (still "
            "flat), GET_SEAT_LIST, GET_SCHEMA, RESERVATION, UN_RESERVE, SCAN_TICKET so seatId "
            "= session_seats.system_seat_id and ticketId = tickets.system_ticket_id. Tests: "
            "idempotent minting, external registration, ranges, "
            "bil24compat_layout_188_test.go sentinels still satisfied. Done when no UUID "
            "appears in any gateway response (golden strict) and numeric requests resolve." + TAIL
        ),
        "steps": [
            "Migration 0090 + head pin + gen queries.",
            "compatids package with tests.",
            "Named wire structs; int64 ids in all existing commands.",
            "Golden strict: no UUID anywhere.",
        ],
        "complexity": 3,
        "dependencies": [451],
    },
    {
        "id": 453,
        "name": "W1-A3 [NORMAL]: result codes 1/101/-1 and localized descriptions ru/en/he/cs (K5)",
        "description": (
            "Spec section 6. -1 currently means unknown command (bil24_compat.go:268-277) "
            "while the site treats 1 as stale session and shows description to the buyer; only "
            "i18n/locales/en.toml exists. Deliver: ResultCodeSessionExpired=1, "
            "ResultCodeUserVisible=101, ResultCodeTransient=-1, unknown command -> -2; update "
            "result_codes.go header and the tests that pin -1 for unknown command "
            "(bil24_compat_157_test.go, bil24_374_test.go) - this is the documented decision of "
            "spec section 6, not a weakening; locales ru.toml/he.toml/cs.toml with every "
            "bil24.* key of section 6 (seat_taken {sector,row,number}, category_sold_out "
            "{name,available}, session_expired, sales_closed, promo_invalid, hold_expired, "
            "order_not_found, currency_mismatch, pricing_mode_unsupported, line_wrong_session, "
            "order_cancelled, use_refund_ticket); locale negotiation ru-RU->ru, unknown -> "
            "channel default -> en; description OK for 0. Table-driven test that every key "
            "exists in every locale. Done when golden RESERVATION/seat_taken_ru.json and "
            "seat_taken_he.json are green." + TAIL
        ),
        "steps": [
            "New result-code constants; unknown -> -2; tests updated.",
            "Locale files + negotiation + completeness test.",
            "Golden localized errors green.",
        ],
        "complexity": 3,
        "dependencies": [452],
    },
    {
        "id": 454,
        "name": "W1-A4 [MAJOR]: customers (0091), identity resolution, CREATE_USER, gateway sessions (C6 part 1)",
        "description": (
            "Spec sections 3.2, 7.3, 12.2, 12.3. No customer exists; buyer name/phone are "
            "validated then dropped (hfeed/public_feed_checkout.go:308-338); the gateway has "
            "no userId/sessionId. Deliver: migration 0091_customers.sql EXACTLY as spec 3.2 "
            "(customers with system_id from compatibility_system_id_seq, customer_identities "
            "with strong/weak unique indexes, customer_consents, customer_org_links, "
            "customer_attributes, customer_merge_candidates, gateway_sessions, "
            "reservations.gateway_session_id/customer_id, permissions customer.read/"
            "customer.import), head pin, gen queries; package internal/platform/customers "
            "(email lower/trim, phone E.164 via github.com/nyaruka/phonenumbers with region "
            "from the org venue country, Resolve per spec 12.2: strong-key conflict -> "
            "merge candidate never auto-merge, Touch, MarkVerified, LinkOrg; 12+ unit cases "
            "incl. family-with-one-phone); CREATE_USER per spec 7.3 (gateway_sessions, 30-day "
            "sliding TTL, 43-char base64url token, userId = customers.system_id); shared "
            "session lookup returning result code 1 when missing/expired and refreshing "
            "last_seen_at/expires_at; public feed checkout persists Buyer{} through "
            "customers.Resolve and sets reservations.customer_id; GET /v1/organizations/"
            "{org_id}/customers and /{id} per spec 12.3 (customer.read), OpenAPI + codegen, "
            "admin-web minimal list/card in org scope. Done when golden CREATE_USER/* are "
            "green and a second CREATE_USER with the same email returns a new sessionId but "
            "the same userId." + TAIL
        ),
        "steps": [
            "Migration 0091 + head pin + gen queries.",
            "customers package with resolution tests.",
            "CREATE_USER + session lookup helper (code 1).",
            "Public feed persists buyer; customers endpoints + admin-web.",
        ],
        "complexity": 3,
        "dependencies": [453],
    },
    {
        "id": 455,
        "name": "W1-A5 [MAJOR]: session cart - mutable hold, 4 RESERVATION shapes, GET_CART, UN_RESERVE_ALL (K4)",
        "description": (
            "Spec sections 7.4 and 7.5. One RESERVATION = one immutable reservation with a "
            "delta response (bil24_compat.go:1049-1825); the site expects an accumulating "
            "per-session cart whose response lists the WHOLE cart and whose quantity is the "
            "row count. One order = one session stays (checkout_sessions.reservation_id single "
            "NOT NULL FK) - the cart is ONE mutable reservation per event session. Deliver: "
            "hcheckout/hold_api.go ExtendHold/ShrinkHold/RefreshHoldExpiry/ReacquireHold "
            "reusing LockSessionSeatsForHold, AllocateGAUnitsTx, "
            "IncrementSessionSeatStatusVersion and the reservation_ga_items price lock, with an "
            "integration test that concurrent extend/shrink never double-holds a seat/unit; "
            "RESERVATION per spec 7.4 (RESERVE/UN_RESERVE by categoryList or seatList, "
            "UN_RESERVE_ALL without actionEventId, currency guard -> 101, TTL refresh on every "
            "RESERVE, cartTimeout, response = whole cart across action events with "
            "sum/discount/charge/totalSum/currency, GA units as rows with their "
            "system_seat_id); GET_CART per spec 7.5 (totalSum only, per-actionEvent "
            "chargePercent = sales_channels.fee_percent, discountAmount 0 until #457); errors "
            "1/101/-2/-3 localized; pwyw -> 101; widget/public schema still see holds "
            "(seat_status_version bumps) - regression tests. Done when golden RESERVATION/"
            "{reserve_category,reserve_seat,unreserve,unreserve_all,seat_taken,sold_out}.json "
            "and GET_CART/* are green and harness scenario 3 passes." + TAIL
        ),
        "steps": [
            "Mutable hold primitives + concurrency test.",
            "RESERVATION 4 shapes with whole-cart response.",
            "GET_CART.",
            "Golden + harness scenario 3.",
        ],
        "complexity": 3,
        "dependencies": [454],
    },
    {
        "id": 456,
        "name": "W1-A6 [MAJOR]: Order aggregate (0092) and ordering package for all three checkout surfaces (A1)",
        "description": (
            "Spec sections 3.3 and 14. There is no orders table; GET /v1/admin/orders lists "
            "checkout sessions; holder_email is never written (htickets/tickets.go "
            "InsertTicket(..., nil)). Deliver: migration 0092_orders.sql EXACTLY as spec 3.3 "
            "(pg_trgm, orders/order_items/order_events, tickets.order_id, permissions "
            "order.read/order.write, partial unique index one pending_payment order per "
            "(customer_id, session_id)), head pin, gen queries; package internal/platform/"
            "ordering with CreateOrderFromCheckout, MarkPaid, Cancel, Expire, ReconcileLines "
            "and worker job order.expire_sweep (spec 14.1); wire into hcheckout confirm, "
            "hfeed.confirmPublicCheckout (public_feed_checkout.go:985) and the payment webhook "
            "(payment_intents.go:809) -> MarkPaid; IssueTicketsForCheckout sets "
            "tickets.order_id, order_items.ticket_id, holder_email = orders.buyer_email and "
            "emits v1.order.paid ONCE after the last ordinal; GET /v1/organizations/{org_id}/"
            "orders (q over buyer_* via trgm, status/session/date filters), GET .../orders/{id} "
            "(items, events, tickets), POST .../orders/{id}/cancel; GET /v1/admin/orders reads "
            "orders; OpenAPI + codegen; admin-web org-scoped Orders page (table + drawer with "
            "items and timeline) with permission-driven nav and tests. Integration: public "
            "feed purchase creates one order with buyer fields; expire sweep; one-open-order "
            "rule. Done when a public feed checkout produces orders + order_items with buyer "
            "name/phone/email and v1.order.paid appears exactly once per order in "
            "outbox_events." + TAIL
        ),
        "steps": [
            "Migration 0092 + head pin + gen queries.",
            "ordering package + expire sweep job.",
            "Wire three surfaces + issuance + v1.order.paid.",
            "Orders endpoints + admin-web page.",
        ],
        "complexity": 3,
        "dependencies": [455],
    },
    {
        "id": 457,
        "name": "W1-B1 [MAJOR]: CREATE_ORDER_EXT, ADD_PROMO_CODES, CHECK_KDP, GET_ORDER_INFO",
        "description": (
            "Spec sections 7.6, 7.7, 7.8. All four return -5 or a stub today "
            "(bil24_compat.go:249-267, :650). Deliver: ADD_PROMO_CODES / CHECK_KDP per spec "
            "7.6 (accept promoCodeList AND promoCodes, <=10, new/exist/error lists via "
            "hcheckout.ValidatePromoForLines against the cart, gateway_sessions.promo_codes, "
            "first applicable code wins - document the single-code limitation in "
            "BEHAVIOR_DIFFERENCES.md; discount now visible in GET_CART); CREATE_ORDER_EXT per "
            "spec 7.7 (gateway session -> reservation of the action event, create+fill from "
            "lines when absent, ReconcileLines so the cart == lines, customers.Resolve on "
            "fullName/phone/email, one-open-order rule returns the SAME orderId, checkout "
            "confirm with PricingRules{PlatformFeeBP: fee_percent*100} and promo, orders "
            "pending_payment with external_ref = request orderId, response {orderId, "
            "externalOrderId, sum, discount, charge, totalSum, currency, expiration}; client "
            "total/chargePercent/expectedPrice only logged in order_events.created.payload."
            "client_reported); GET_ORDER_INFO per spec 7.8 (strict mode, order object, "
            "userMessage). Errors: -2 empty lines/orderId, 101 wrong-session line, 101 sold "
            "out on reconcile. Done when golden CREATE_ORDER_EXT/{ga,seated,promo,"
            "repeat_same_order}.json, ADD_PROMO_CODES/*, CHECK_KDP/*, GET_ORDER_INFO/* are "
            "green and harness scenario 6 passes." + TAIL
        ),
        "steps": [
            "Promo commands + session promo codes + GET_CART discount.",
            "CREATE_ORDER_EXT with reconcile and one-open-order rule.",
            "GET_ORDER_INFO.",
            "Golden + harness scenario 6.",
        ],
        "complexity": 3,
        "dependencies": [456],
    },
    {
        "id": 458,
        "name": "W1-B2 [MAJOR]: PAY_ORDER, GET_TICKETS_BY_ORDER, SEND_TICKETS_TO_EMAIL, CANCEL_RESERVATION, CANCEL_ORDER",
        "description": (
            "Spec sections 7.9-7.12. No external-payment-confirmed path exists; "
            "payment_provider_configs has a manual slug (hpayments/payment_configs_types.go:30-36) "
            "that nothing uses. Deliver: PAY_ORDER per spec 7.9 (idempotent; hold reacquire on "
            "expiry via ReacquireHold, on failure manual_review + 101 bil24.hold_expired + "
            "audit alert; amount mismatch recorded in order_events not blocked; one tx: "
            "payment_intents provider=manual state=succeeded provider_payment_id=wc:<ref>:<method>, "
            "checkout completed, reservation converted, promo redemption, ordering.MarkPaid, "
            "customer_org_links + identities verified_at; after commit SYNCHRONOUS "
            "IssueTicketsForCheckout plus the fallback checkout.issue_tickets job; no platform "
            "delivery e-mail unless settings.gateway.platform_email); GET_TICKETS_BY_ORDER per "
            "spec 7.10 (ticketList with pdfUrl/downloadUrl/barcode/seatId/categoryPriceId, "
            "ticketIdList, string or int orderId, empty lists before issuance, pdfUrl built "
            "from new required PUBLIC_BASE_URL); SEND_TICKETS_TO_EMAIL (delivery jobs), "
            "CANCEL_RESERVATION (unpaid only, unknown id -> 0), CANCEL_ORDER (paid -> 101 "
            "bil24.use_refund_ticket); config: PUBLIC_BASE_URL required when "
            "BIL24_COMPAT_ENABLED with a config test, .env.example. Integration: pay -> tickets "
            "issued in-request; pay after expiry with free seats -> reacquired; with seats "
            "taken -> manual_review; double PAY_ORDER -> 0 and one ticket set. Done when golden "
            "PAY_ORDER/*, GET_TICKETS_BY_ORDER/*, CANCEL_*/* are green and harness scenarios 2 "
            "and 5 pass up to the outbox event." + TAIL
        ),
        "steps": [
            "PAY_ORDER with reacquire/manual_review and synchronous issuance.",
            "GET_TICKETS_BY_ORDER + PUBLIC_BASE_URL.",
            "SEND_TICKETS_TO_EMAIL, CANCEL_RESERVATION, CANCEL_ORDER.",
            "Golden + harness scenarios 2 and 5.",
        ],
        "complexity": 3,
        "dependencies": [457],
    },
    {
        "id": 459,
        "name": "W1-B3 [MAJOR]: GET_ALL_ACTIONS full nested catalog",
        "description": (
            "Spec section 7.1. Today a flat actionList without actionEventList, cities, "
            "venues, categories and with RFC3339 dates (bil24_compat.go:298-348). Deliver "
            "exactly spec 7.1: countryList/cityList/venueList via compatids, published events "
            "of the org with upcoming scheduled sessions (start_at > now()-6h), day DD.MM.YYYY "
            "and time HH:MM in venues.timezone (session skipped + warn bil24.venue_timezone_"
            "missing when NULL; // allow:timeformat: markers), sellEndTime = min tier "
            "sale_window_end else start_at (RFC3339 with offset), availability = remaining "
            "(session_seats available count, else ledger capacity-sold-held), "
            "categoryLimitList[0].categoryList = GA tiers ONLY with placement:false and "
            "tariffIdMap {}, seatingPlanId = actionEventId for seated/hybrid and 0 for GA, "
            "seatingPlanName, minPrice/maxPrice via priceresolve, posters from session media "
            "with events.image_url fallback, age from age_rating (NR -> empty), organizerId = "
            "organizations.display_number, localized name/description as today. One SQL "
            "round-trip per list (no N+1 on sessions); integration test 100 events x 3 "
            "sessions under 200 ms. Done when golden GET_ALL_ACTIONS/{ga,seated,hybrid,"
            "dst_jerusalem,no_timezone}.json are green." + TAIL
        ),
        "steps": [
            "Nested catalog query without N+1.",
            "Dates in venue TZ, availability, categories, plan fields.",
            "Golden 5 cases + perf test.",
        ],
        "complexity": 3,
        "dependencies": [452, 453],
    },
    {
        "id": 460,
        "name": "W1-B4 [NORMAL]: GET_SEAT_LIST full form (placement tri-state, location, available, GA pseudo-seats)",
        "description": (
            "Spec section 7.2. Today availableCount = capacity, no placement/available/"
            "location, BSS ints (bil24_compat.go:470-592). Deliver: currency; categoryList with "
            "tri-state placement (true seated tiers, false GA tiers inside a plan, key ABSENT "
            "for pure-GA sessions), availability = remaining, tariffIdMap {}; seatList with "
            "seatId = system_seat_id, available bool, location {sector,row,number}, GA units "
            "as pseudo-seats for hybrid sessions (location sector = tier name), [] for pure "
            "GA; availableOnly filters seatList only; -3 outside the channel org. Keep "
            "GET_SCHEMA consistent (int seatId). Done when golden GET_SEAT_LIST/{seated,hybrid,"
            "ga,available_only}.json are green." + TAIL
        ),
        "steps": [
            "categoryList tri-state placement + availability.",
            "seatList with location/available and GA pseudo-seats.",
            "Golden 4 cases.",
        ],
        "complexity": 3,
        "dependencies": [455],
    },
    {
        "id": 461,
        "name": "W1-B5 [NORMAL]: GET /compat/bil24/image?type=seatingPlan in sbt/1.0 format (F1)",
        "description": (
            "Spec section 8. Only /v1/event-sessions/{uuid}/layout.svg exists with namespace "
            "http://bil24.pro/sbt, swatch circles and states 0-5 (hseating/layout_svg.go:84-381); "
            "the site fetches by int actionEventId + fid without a token and reads namespace "
            "http://www.w3.org/2015/sbt/1.0 (bil24-seat-picker.js:389-394). Deliver: "
            "hseating.RenderSBT10SVG per spec 8 (<metadata> <sbt:category sbt:id sbt:index "
            "sbt:name sbt:color sbt:price sbt:class>, seats <circle sbt:id=system_seat_id "
            "sbt:state 1|4 sbt:cat=INDEX sbt:seat cx cy r fill> inside <g sbt:sect><g sbt:row>, "
            "viewBox mandatory, GA zones as decor without sbt:id); route GET /compat/bil24/"
            "image mounted in bil24_shims.go without JWT (type != seatingPlan -> 404), fid -> "
            "channel -> org, published session only, ETag = checksum:seat_status_version, "
            "Cache-Control no-cache, If-None-Match -> 304; OpenAPI; golden SVG test (canonical "
            "XML compare) plus a DOM-level test replaying the JS rules (attrNS, catByIndex, "
            "ancestor sbt:sect). Done when golden image/{seated,hybrid}.svg are green and GA "
            "sessions / other org get 404." + TAIL
        ),
        "steps": [
            "RenderSBT10SVG encoder.",
            "Route + auth by fid + caching.",
            "Golden SVG + JS-rules test.",
        ],
        "complexity": 3,
        "dependencies": [452],
    },
    {
        "id": 462,
        "name": "W1-B6 [NORMAL]: EAN-13 credentials, barcode federation, revoke fix (F2)",
        "description": (
            "Spec sections 3.4 and 11. No EAN-13 generator exists; EAN-13 is a literal over a "
            "64-hex token (macs/export.go:475-478); RevokeTicketArtifactsTx revokes \"qr\" "
            "(htickets/cancel.go:568) while the CHECK says static_qr, so QR credentials survive "
            "cancellation. Deliver: migration 0093_ean13_credentials.sql (spec 3.4, head pin); "
            "package internal/platform/barcodes/ean13 with Encode(\"21\", id) = 21 + zero-padded "
            "10-digit system_ticket_id + check digit and Valid() (tests against the real "
            "2402604868419 and 100 generated codes); issuance writes ticket_credentials(type=ean13) "
            "and barcodes(platform authority, external_ref=ean13, ticket_id); revoke fix for "
            "static_qr, pdf, ean13 and barcodes.status=revoked; SCAN_TICKET and "
            "/v1/scanner/validate accept EAN-13; arena PDF prints the EAN-13 number under the "
            "QR; backfill job tickets.backfill_ean13 for stand data. Done when every new ticket "
            "has a valid EAN-13 in ticket_credentials and barcodes and cancelling revokes all "
            "three credential types (test asserts revoked_at on each)." + TAIL
        ),
        "steps": [
            "Migration 0093 + ean13 package.",
            "Issuance + barcodes federation + scan paths.",
            "Revoke fix + backfill job + PDF number.",
        ],
        "complexity": 3,
        "dependencies": [452],
    },
    {
        "id": 463,
        "name": "W1-B7 [MAJOR]: orderexport projection, bil24wire encoder, bil24_wp webhook subscriber and dispatcher (F3, F4)",
        "description": (
            "Spec sections 3.5, 9.1-9.3. Webhooks exist only for MACS (int holderStatus, event "
            "UUID hash, no order envelope, macs/dispatcher.go); the site needs {type, data} "
            "with the 36-key Order / 17-key Ticket / 14-key actionEvent Bil24 shapes. Deliver: "
            "migration 0094_wp_webhook_subscribers.sql (spec 3.5, head pin); package "
            "internal/platform/orderexport = the DB projection moved out of macs/export.go "
            "(Order, Ticket, proration, sold price, discountReason) with macs becoming an "
            "encoder over it and NO behaviour change for MACS (sample_tickets.json golden stays "
            "binding); package internal/platform/bil24wire encoder per spec 9.3 (string "
            "holderStatus NEVER_USE/REFUND, category string, seatLocation null/object, naive "
            "showTime in venue TZ, actionEvent.id = session actionEventId, refundDate RFC3339 "
            "with offset, actionLegalOwner from organizations legal name) with a BINDING "
            "key-set test against testdata/wp/bil24_orders_pseudonymized.json; dispatcher per spec 9.2 "
            "(mapping v1.order.paid->order.paid, v1.order.cancelled->order.cancelled, "
            "v1.ticket.cancelled/refunded/revoked->ticket.refunded, v1.event.published/"
            "updated + v1.session.updated/cancelled->event.created/changed/deleted with "
            "data=[{actionEventId}], envelope {id, created, type, data}, X-Arena-Signature "
            "optional, 2xx = success, third member of multiDispatcher in cmd/arena-worker/"
            "main.go:173-176; new producers v1.order.cancelled in ordering and v1.event.*/"
            "v1.session.* in hcatalog); PUT/GET/DELETE /v1/organizations/{org_id}/channels/{id}/"
            "wp-webhook with a SYNCHRONOUS test delivery reported in the PUT response "
            "{test_delivery: {ok, http_status}}; OpenAPI + codegen; admin-web section next to "
            "the gateway credential section. Integration: real cancel handler -> outbox -> "
            "dispatcher -> wpstub receives ticket.refunded once (retry on 503, dedup on "
            "replay); order.paid carries all tickets of the order. Done when wpstub scenarios "
            "in testdata/wp/wp_receiver/ are green and the MACS golden is unchanged." + TAIL
        ),
        "steps": [
            "Migration 0094; orderexport extraction with MACS unchanged.",
            "bil24wire encoder + binding key-set test on real sample.",
            "Dispatcher + new producers + wp-webhook endpoints + admin-web.",
            "Integration through real handlers and outbox.",
        ],
        "complexity": 3,
        "dependencies": [456, 462],
    },
    {
        "id": 464,
        "name": "W1-B8 [NORMAL]: REFUND_TICKET gateway extension",
        "description": (
            "Spec section 7.13. Refund from the site's event centre ends today with a manual "
            "step in the Bil24 manager (lampyris-ops class-lops-tickets.php:6-13 four-state "
            "model). Deliver: command REFUND_TICKET {ticketId, reason?, refundPrice?} wrapping "
            "the cancel transaction of htickets/cancel.go:174-276 with refund_mode=manual, "
            "actor gateway:<fid>, org scoping through the ticket's order channel (-3 "
            "otherwise), idempotent on already-cancelled (0), refundPrice -> "
            "tickets.refund_price, orders.status refunded/partially_refunded, "
            "order_events.ticket_refunded, response {ticketId, refundDate}; then the standard "
            "v1.ticket.cancelled -> ticket.refunded reaches the site (bil24_wp) and MACS. Done "
            "when golden REFUND_TICKET/{ok,repeat,other_org}.json are green and harness "
            "scenario 4 passes (refund reaches both stubs once, replay deduped)." + TAIL
        ),
        "steps": [
            "REFUND_TICKET command over the cancel transaction.",
            "Order status + events.",
            "Golden + harness scenario 4.",
        ],
        "complexity": 3,
        "dependencies": [463],
    },
    {
        "id": 465,
        "name": "W1-M [MAJOR]: MACS envelope fixes M1-M5 over orderexport",
        "description": (
            "Spec section 10. MACS receives order.paid as a single ticket in data (from "
            "v1.scanner.ticket.issued), counts any 2xx as delivered although MACS answers 200 "
            "with {status:Error}, and actionEvent.id is a UUID hash of the EVENT "
            "(17_macs_integration_contract.md:72). Deliver: M1 macs.Dispatcher consumes "
            "v1.order.paid and sends order.paid with data = {id, status:PAID, ticketList[]} "
            "built by orderexport (single-ticket synthetic order for complimentary tickets "
            "issued without an order; stop mapping v1.scanner.ticket.issued); ticket.refunded "
            "unchanged; M2 success only when 2xx AND body {status:OK}, anything else is an error "
            "so the outbox retries; M3 actionEvent.id = session actionEventId and actionId from "
            "compatibility_id_map (compatids.Ensure); M4 barcode = EAN-13 credential with "
            "barcodeFormat {id:0,name:EAN-13}, subscriber callback_url must end with "
            "/api/_wh/tickets (422 on PUT otherwise); M5 macs/stub rejects order.paid without "
            "data.ticketList or data.status != PAID with 200 {status:Error} and the AB-50g "
            "round-trip + AB-50h/50i golden tests are updated to the new shape (update the "
            "synthetic sample_tickets.json accordingly and keep it BINDING); "
            "17_macs_integration_contract.md reconciled and a runbook note that the "
            "actionEvent.id key changes for previously imported test events. Live MACS "
            "verification is an interactive checklist item, not part of this feature. Done when "
            "the round-trip integration test delivers order.paid with N tickets to the stub "
            "once and a stub {status:Error} answer produces a retry (next_attempt_at set)." + TAIL
        ),
        "steps": [
            "M1: order.paid from v1.order.paid via orderexport.",
            "M2: body-level success check + retry.",
            "M3/M4: session actionEventId, EAN-13, callback URL validation.",
            "M5: stub tightened, round-trip and golden tests updated, contract doc.",
        ],
        "complexity": 3,
        "dependencies": [463],
    },
    {
        "id": 466,
        "name": "W1-C1 [MAJOR]: organization API keys and service principal (C1, ADR-029/038)",
        "description": (
            "Spec sections 3.6 and 13.1. No server-to-server credential exists except feed "
            "tokens (agent_feed_tokens); auth.ActorType service has no issuer; permissions are "
            ".create/.read/.update/.delete/.publish (there is NO event.write). Deliver: "
            "migration 0095_api_keys.sql (spec 3.6, head pin, permissions api_key.manage and "
            "import.bil24_session); package internal/platform/apikeys (issue ak_<prefix12>_"
            "<secret43>, bcrypt, lookup by key_prefix, scope validation excluding platform.*/"
            "admin.*/api_key.manage, expires/revoked, last_used_at throttled to once a minute); "
            "middleware in applyAuth producing Actor{Type: service, ID: key id} with "
            "permissions = scopes; enforceOrgMembership (server_orgauth.go:56) and ALL "
            "sub-package twins (hcatalog/orgauth.go:46, hiam:44, hpayments:43, "
            "hbankaccounts:45, hseating/authz.go:33) accept a service actor only for "
            "api_keys.org_id - table-driven test over all five; rate limit 600/min per key via "
            "platform/ratelimit; audit actor api_key:<id>; POST/GET/DELETE /v1/organizations/"
            "{org_id}/api-keys (api_key.manage, X-Admin-Reason, key shown once); OpenAPI + "
            "codegen; admin-web API keys tab (issue with scope picker, shown once, revoke). "
            "Integration: a key with the spec 13.1 scope set creates event -> session -> tiers "
            "-> media -> publish; org A key gets 403 on org B; revoked -> 401. Done when the "
            "spec 13.4 no-seats flow runs end-to-end under a key and the event appears in "
            "GET_ALL_ACTIONS for the channel." + TAIL
        ),
        "steps": [
            "Migration 0095 + apikeys package.",
            "Middleware + service actor in every org-auth twin (table test).",
            "Endpoints + OpenAPI + admin-web tab.",
            "Integration: no-seats event flow under a key.",
        ],
        "complexity": 3,
        "dependencies": [459],
    },
    {
        "id": 467,
        "name": "W1-C3 [MAJOR]: sbt-SVG importer and POST /v1/organizations/{org_id}/imports/bil24-session (C3-arena)",
        "description": (
            "Spec sections 13.2 and 13.3. domain/seating/svg_import.go parses Inkscape "
            "conventions only; nothing can register Bil24 ids for an imported session; a "
            "rebind retires seat ids (migration 0088:72-77). Deliver: seating.ImportSBTSVG "
            "per spec 13.3 (metadata categories, <circle sbt:id sbt:state sbt:cat sbt:seat>, "
            "ancestor sbt:sect / sbt:row or <title>, duplicates and missing sbt:cat as "
            "validation errors, decor for everything else; geometry gains seats[].external_id "
            "and categories[].external_id, Canonicalize/Checksum aware; tests on the real "
            "fixture from #450 and a synthetic one); materialization uses external_id as "
            "system_seat_id with system_seat_id_source=bil24 and re-applies it on rebind; "
            "endpoint POST /v1/organizations/{org_id}/imports/bil24-session per spec 13.2 "
            "(all external ids < 1e9 else 422 compat.external_id_out_of_range; venue by "
            "external_bil24_id or created with mandatory timezone -> 422 venue.timezone_required; "
            "city/country via geo; event/session/tiers upsert through compatids.RegisterExternal; "
            "plan + version + bind with category_tier_map by category index; seats with "
            "available:false -> blocked with a warning; publish flag through the standard "
            "publish gate; response {event_id, session_id, tier_ids, seating_plan_version_id, "
            "seats_materialized, warnings, created}; 409 import.session_has_sales when a "
            "session with sales would change its seat set), permission import.bil24_session, "
            "OpenAPI + codegen. Integration: import twice -> created:false and identical ids; "
            "GET_SEAT_LIST then returns the Bil24 seatIds; image returns the same sbt:ids; the "
            "imported hybrid session sells through the #457/#458 flow with original ids in "
            "order.paid. Done when harness scenario 8 passes." + TAIL
        ),
        "steps": [
            "ImportSBTSVG + geometry external ids.",
            "Materialization keeps Bil24 seat ids across rebind.",
            "Import endpoint with idempotent upserts and publish.",
            "Harness scenario 8.",
        ],
        "complexity": 3,
        "dependencies": [466, 461],
    },
    {
        "id": 468,
        "name": "W1-C7 [NORMAL]: customer database import tool with dry-run (C7)",
        "description": (
            "Spec sections 3.7 and 12.4. Owner decision #11 requires loading Bil24/WooCommerce/"
            "GSheets/Brevo customer bases per organizer; no tool exists (running it on real "
            "data stays interactive - this feature builds the tool and tests it on the "
            "committed sample only). Deliver: migration 0096_customer_imports.sql (spec 3.7, "
            "head pin); worker job customer.import with modes dry_run/apply and parsers for "
            "bil24_orders_json (fixed mapper: fullName/phone/email/user.email, frontend.name -> "
            "org via mapping.frontends, actionEvent.actionName -> interests[], discountReason -> "
            "promo_codes_used[], date -> first/last_order_at, ticketQuantity -> tickets_count) "
            "and CSV via mapping.columns with org_rule; every row through customers.Resolve "
            "without auto-merge, row_hash idempotency, consents source=import:<label> never "
            "verified, customer_org_links(source=import) + aggregates, customer_attributes; "
            "endpoints POST /v1/admin/customer-imports, POST .../{id}/dry-run, POST .../{id}/"
            "apply, GET .../{id}, GET .../{id}/rows?action= (platform.superadmin, "
            "X-Admin-Reason); OpenAPI + codegen; admin-web page under Platform (media upload, "
            "mapping form, report). Tests on testdata/wp/bil24_orders_pseudonymized.json (68 orders): "
            "dry-run report counts, apply twice -> all matched. Done when dry-run and apply on "
            "the sample produce the expected report, are idempotent, and no marketing consent "
            "is verified after import." + TAIL
        ),
        "steps": [
            "Migration 0096 + job with two parsers.",
            "Endpoints + OpenAPI + admin-web page.",
            "Idempotency and consent tests on the real sample.",
        ],
        "complexity": 3,
        "dependencies": [456],
    },
    {
        "id": 469,
        "name": "W1-D [NORMAL]: documentation, OpenAPI completeness, ADR-034..038, gateway ops runbook",
        "description": (
            "Spec section 17 and backlog #469. Deliver: 08_architecture/11_architecture_decision_"
            "log_ru.md rows ADR-034 (compat gateway primary for the two owned WP sites, "
            "guardrail #6 exception), ADR-035 (orders + ordering package, partial reversal of "
            "ADR-033), ADR-036 (platform customer), ADR-037 (integer id rule), ADR-038 "
            "(organization API keys); 01_api_compatibility_gateway_ru.md answers its Open "
            "Questions from the spec; tests/compat/bil24/BEHAVIOR_DIFFERENCES.md current "
            "(single promo code, seatingPlanId = actionEventId, holderStatus spelling, "
            "manual_review after PAY_ORDER); 17_macs_integration_contract.md reconciled after "
            "#465; docs/ops/bil24_gateway.md runbook (provision a channel credential, register "
            "the WP webhook, rotate, read dead-lettered outbox rows, switch a site by option, "
            "roll back by URL, stand organizations lampyris-staging/vino-staging); AGENTS.md "
            "conventions learned in this wave; openapi.yaml covers /compat/bil24/json (oneOf by "
            "command) and /compat/bil24/image; .env.example + deploy/ for PUBLIC_BASE_URL. "
            "Done when docs tests (openapi docs, guardrail greps) are green." + TAIL
        ),
        "steps": [
            "ADR rows + gateway doc + BEHAVIOR_DIFFERENCES.",
            "Ops runbook + AGENTS.md.",
            "OpenAPI completeness for compat routes.",
        ],
        "complexity": 3,
        "dependencies": [458, 464, 465, 467, 468],
    },
]


def validate() -> None:
    ids = [f["id"] for f in FEATURES]
    assert ids == list(range(START_ID, START_ID + len(FEATURES))), ids
    for f in FEATURES:
        for dep in f["dependencies"]:
            assert dep == 450 or START_ID <= dep < f["id"], (f["id"], dep)
        assert "gate" not in f, f["id"]


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
            f"arena_new_features_before_w1_compat_{datetime.now():%Y%m%d_%H%M%S}.db"
        )
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as backup_connection:
            connection.backup(backup_connection)

        connection.execute("BEGIN IMMEDIATE")
        for offset, item in enumerate(FEATURES):
            is_gate = "gate" in item
            connection.execute(
                """
                INSERT INTO features (
                    id, priority, category, name, description, steps,
                    passes, in_progress, dependencies, needs_human_input,
                    human_input_request, human_input_response, complexity
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, NULL, ?)
                """,
                (
                    item["id"],
                    START_PRIORITY + offset,
                    CATEGORY,
                    item["name"],
                    item["description"],
                    json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]) if item["dependencies"] else None,
                    1 if is_gate else 0,
                    item["gate"] if is_gate else None,
                    item["complexity"],
                ),
            )
        connection.commit()
        print(
            f"Installed {len(FEATURES)} W1 features (ids {START_ID}-{START_ID + len(FEATURES) - 1}, "
            f"priorities {START_PRIORITY}-{START_PRIORITY + len(FEATURES) - 1}); backup: {backup}"
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
