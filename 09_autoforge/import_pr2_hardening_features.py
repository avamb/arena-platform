"""Install the PR2 production-hardening wave into features.db.

Source: the 2026-07-17 independent multi-agent readiness audit (security,
domain logic, config/deploy, API contract, frontend, architecture). These
records capture confirmed defects that must be fixed before deployment plus
the highest-value refactors. Idempotent: refuses to overwrite or renumber an
unexpected queue, exactly like import_production_readiness_features.py.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (356, 438)  # (max id, max priority) before this wave
START_ID = 357
START_PRIORITY = 439


def feature(
    offset: int,
    code: str,
    severity: str,
    title: str,
    model: str,
    objective: str,
    steps: list[str],
    dependencies: list[int] | None = None,
) -> dict[str, object]:
    return {
        "id": START_ID + offset,
        "priority": START_PRIORITY + offset,
        "category": "Production Hardening PR2",
        "name": f"{code} [{severity}]: {title}",
        "description": (
            f"Model: {model}. Severity: {severity}. {objective} "
            "Source: 2026-07-17 multi-agent readiness audit. Do not mark "
            "passing from unit tests alone when the fix requires PostgreSQL, "
            "concurrent-access, webhook, container, or browser integration. "
            "Never weaken or delete an acceptance test to obtain green."
        ),
        "steps": steps,
        "dependencies": dependencies,
    }


FEATURES = [
    # ---- Security & tenancy -------------------------------------------------
    feature(
        0, "PR2-01", "BLOCKER",
        "Enforce org membership on every org-scoped route",
        "opus",
        "Close the cross-tenant authorization bypass: an org_admin in Org A can "
        "read/mutate Org B's bank accounts, payment-provider configs, channels, "
        "venues, and org settings via PATCH /v1/organizations/{orgB}/... because "
        "the RBAC check takes no org_id and roles are unioned across all orgs.",
        [
            "Thread the path org_id into the permission check (Check(ctx, action, resource, orgID)).",
            "Verify active membership of the caller in the path org, mirroring hseating/authz.go actorIsMemberOfOrg.",
            "Scope GetActiveRolesForUser and memberships queries by org_id (memberships.sql.go:119).",
            "Apply to hbankaccounts, hpayments payment-configs, channels, venues, and org update handlers.",
            "Add integration tests: Org A actor is denied on Org B resources across all listed surfaces.",
        ],
    ),
    feature(
        1, "PR2-02", "MAJOR",
        "Harden login rate-limiting against spoofed X-Forwarded-For",
        "sonnet",
        "The login limiter keys on ClientIP()+email but ClientIP() returns the "
        "first client-controlled X-Forwarded-For token unvalidated, so rotating "
        "the header defeats the 5/15min lockout.",
        [
            "Introduce a trusted-proxy / hop-count configuration for client IP resolution.",
            "Derive the rate-limit IP from RemoteAddr or the configured trusted hop, not raw XFF.",
            "Update httputil.ClientIP and hauth/login.go loginRateLimiterKey.",
            "Test that forged XFF values do not reset the per-email attempt counter.",
        ],
    ),
    feature(
        2, "PR2-03", "MAJOR",
        "Revoke sessions on reset; hash and rotate bearer tokens",
        "opus",
        "Password reset never revokes refresh tokens/sessions (30-day tokens "
        "survive account recovery); refresh, verification, and reset tokens are "
        "stored in plaintext; refresh has no rotation or reuse detection.",
        [
            "In PasswordResetConfirm, revoke all refresh tokens and clear Redis sessions for the user in-tx.",
            "Store SHA-256 hashes of refresh/verification/reset tokens; look up by hash.",
            "Rotate the refresh token on every Refresh and revoke the old one.",
            "Treat reuse of a revoked refresh token as full-session compromise.",
            "Test reset-then-old-token-rejected, tampered/expired tokens, and rotation replay.",
        ],
    ),
    # ---- Checkout / inventory / payments ------------------------------------
    feature(
        3, "PR2-04", "BLOCKER",
        "Convert held seats/capacity to sold on checkout completion",
        "opus",
        "SellReservationSeatsTx and ConfirmCapacity have zero production callers, "
        "so paid seats stay 'held'; the TTL worker later releases them back to "
        "available and the same seats are resold while valid tickets exist.",
        [
            "On paid and free checkout completion, in one tx call SellReservationSeatsTx + ConfirmCapacity + UpdateReservationState(id,'converted').",
            "Ensure GetExpiredReservations can never select a converted/paid reservation.",
            "Add integration test: complete checkout, run the TTL expiry worker, assert seats remain sold and are not resold.",
        ],
    ),
    feature(
        4, "PR2-05", "BLOCKER",
        "Add over-refund and double-refund protection",
        "opus",
        "HandleCreateRefund/approve never check intent state, currency, amount<=intent, "
        "or the sum of prior refunds, so N full-amount refunds can be created and "
        "each approved independently.",
        [
            "Lock the payment intent row and validate refundable = intent.amount - SUM(non-failed refunds).",
            "Reject refunds on non-succeeded intents, currency mismatches, and amounts exceeding the remaining refundable.",
            "Make approval re-validate against the current refunded total inside the tx.",
            "Test concurrent/duplicate full refunds and partial-then-full over-refund.",
        ],
    ),
    feature(
        5, "PR2-06", "BLOCKER",
        "Authenticate payment and refund webhooks",
        "opus",
        "The payment_intent and refund webhook endpoints are mounted in production "
        "wiring but accept unsigned JSON, so anyone who guesses a provider_payment_id "
        "can forge payment.succeeded to mint+email tickets or forge refunds to cancel them.",
        [
            "Verify the provider signature (Stripe/AllPay HMAC) before processing any webhook body.",
            "Reject unsigned/invalid-signature requests with 4xx and no state change.",
            "Keep a non-production mock path explicitly gated by config, never reachable in production.",
            "Test forged body rejection and a valid signed event happy path.",
        ],
        [345],  # depends on PR-01 production auth being in place (already passing)
    ),
    feature(
        6, "PR2-07", "MAJOR",
        "Make webhook processing atomic and durable",
        "opus",
        "InsertPaymentIntentEvent (idempotency row) commits before the state "
        "transition and inline ticket issuance; if those fail or the process "
        "crashes, provider redelivery hits the duplicate check and the transition "
        "and tickets are lost forever.",
        [
            "Record the idempotency event and apply the state transition in one transaction.",
            "Issue tickets via a durable job enqueued in the same tx, not inline best-effort.",
            "Ensure redelivery after a mid-flight failure still completes the transition and issuance.",
            "Test crash-after-event-insert recovery via provider retry.",
        ],
        [362],  # PR2-06 webhook auth
    ),
    feature(
        7, "PR2-08", "MAJOR",
        "Derive checkout pricing from the reservation, not client input",
        "opus",
        "HandleConfirmCheckout prices client-supplied req.TierID x req.Quantity with "
        "no cross-check against the linked reservation, so a buyer holding 10 VIP "
        "seats can confirm pricing for quantity=1 of a cheap tier and still receive "
        "10 seated tickets.",
        [
            "Load the reservation and build pricing lines from its session/tier/quantity/seats (reuse buildSeatedPricingLines).",
            "Reject or ignore client tier/quantity that disagrees with the reservation.",
            "Test mismatched tier/quantity is priced from the reservation, not the request.",
        ],
    ),
    feature(
        8, "PR2-09", "MAJOR",
        "Guard reservation state transitions against races",
        "sonnet",
        "UpdateReservationState issues an unconditional UPDATE with no state guard; "
        "concurrent cancels, or cancel racing the TTL worker, each release capacity "
        "and understate holds, enabling oversell.",
        [
            "Change transitions to conditional UPDATE ... WHERE id=$1 AND state=$expected RETURNING.",
            "Release capacity only when the guarded transition actually won the row.",
            "Re-check state inside expireReservation after re-acquiring the row (TTL worker drops SKIP LOCKED locks before per-item work).",
            "Test concurrent cancel+expire does not double-release capacity.",
        ],
    ),
    feature(
        9, "PR2-10", "MAJOR",
        "Make ticket issuance idempotent and quantity-aware",
        "opus",
        "Ticket issuance is list-then-insert with no tx/lock/unique constraint, so "
        "two concurrent triggers double-issue, and a partial failure poisons the "
        "state: the next retry returns the partial set as 'already issued'.",
        [
            "Add a unique constraint on (checkout_session_id, seat/ordinal) and wrap issuance in a tx or advisory lock.",
            "Make partial-issue detection quantity-aware so retries complete the missing tickets.",
            "Test concurrent double-trigger and mid-loop insert failure recovery.",
        ],
    ),
    feature(
        10, "PR2-11", "MAJOR",
        "Enqueue ticket delivery email exactly once",
        "sonnet",
        "IssueTicketsForCheckout enqueues delivery jobs, then payment_intents.go and "
        "checkout.go enqueue again for the same tickets; InsertDeliveryJob has no "
        "dedupe, so every ticket is emailed at least twice.",
        [
            "Enqueue delivery in exactly one place in the completion flow.",
            "Add a unique index on delivery_jobs(ticket_id) or ON CONFLICT DO NOTHING.",
            "Test a replayed webhook does not produce a second delivery job/email.",
        ],
    ),
    feature(
        11, "PR2-12", "MAJOR",
        "Enforce promo max_uses and record redemptions",
        "sonnet",
        "validatePromoCode checks only status/window/min-amount; usage counts are "
        "never consulted and redemptions are never recorded, so a single-use "
        "100%-off code is infinitely redeemable.",
        [
            "Record a redemption row at checkout completion in-tx.",
            "Enforce max_uses and max_uses_per_customer with row locking in validatePromoCode.",
            "Test that a single-use code is rejected on the second redemption under concurrency.",
        ],
    ),
    # ---- Outbox / worker ----------------------------------------------------
    feature(
        12, "PR2-13", "BLOCKER",
        "Make outbox delivery poison-safe and single-delivery",
        "opus",
        "ClaimNext always picks the oldest row with no max-attempts/dead-letter/backoff "
        "and continues without sleeping after failure, so one permanently failing event "
        "head-of-line blocks all delivery in a hot CPU loop; the claim tx also commits "
        "(releasing the lock) before dispatch, so a second dispatcher double-delivers.",
        [
            "Add an attempts cap with dead-letter and exponential next_attempt_at backoff.",
            "Sleep (waitOrStop) after a failed delivery instead of immediate re-claim.",
            "Order claims by next_attempt_at and skip rows not yet due.",
            "Hold the row claim so concurrent dispatchers cannot both deliver the same event.",
            "Test poison event does not block healthy events and is dead-lettered after the cap.",
        ],
    ),
    feature(
        13, "PR2-14", "MAJOR",
        "Add worker stale-claim reaper and retry backoff",
        "sonnet",
        "Worker claim sets status='claimed'; a crash/OOM between claim and completion "
        "strands the job forever (e.g. a ticket-delivery email), violating the "
        "documented at-least-once contract; MarkRetry re-marks pending with no backoff.",
        [
            "Add a visibility timeout: requeue jobs with status='claimed' AND claimed_at < now()-interval.",
            "Set scheduled_at backoff on MarkRetry.",
            "Test a simulated crash mid-job is reclaimed and eventually completes.",
        ],
    ),
    # ---- Config / deploy ----------------------------------------------------
    feature(
        14, "PR2-15", "MAJOR",
        "Close the production DB TLS validation hole",
        "sonnet",
        "unsafeDBTLS only rejects sslmode=disable/allow, so a DSN with sslmode=prefer "
        "or no sslmode (pgx defaults to prefer, plaintext fallback) boots in production "
        "despite docs mandating sslmode=require/verify-full.",
        [
            "In validateProduction, require the DSN to explicitly set sslmode=require, verify-ca, or verify-full.",
            "Reject prefer/absent sslmode in production with a clear fail-fast error.",
            "Add table-driven tests for each accepted/rejected sslmode.",
        ],
        [344],
    ),
    feature(
        15, "PR2-16", "MAJOR",
        "Validate APP_PUBLIC_URL and refuse debug flags in production",
        "opus",
        "APP_PUBLIC_URL is documented as boot-enforced but has no validation (empty "
        "value yields host-less auth-email links), and DEBUG_ROUTES_ENABLED / "
        "FAULT_INJECT_OUTBOX_AFTER_AUDIT are read via raw os.Getenv, bypassing the "
        "config contract (a stray Dokploy var mounts /v1/debug/panic in production).",
        [
            "Require a non-empty absolute https APP_PUBLIC_URL in production when EMAIL_MODE=smtp.",
            "Route DEBUG_ROUTES_ENABLED and FAULT_INJECT_* through config and hard-refuse them when IsProduction().",
            "Add the OTEL_EXPORTER_OTLP_INSECURE variable to .env.example/DOKPLOY.md and warn on insecure non-localhost OTLP.",
            "Test production rejection of each unsafe flag and empty public URL.",
        ],
        [344],
    ),
    feature(
        16, "PR2-17", "MAJOR",
        "Make Docker builds reproducible and purge committed binaries",
        "sonnet",
        "Dockerfile runs 'go mod tidy' at build time (defeats reproducibility and "
        "go.sum verification), and 55MB of stale Linux ELF binaries (arena-api, "
        "arena-worker) are committed to git with no .gitignore guard against "
        "recommitting arena-migrate/arena-healthcheck.",
        [
            "Replace 'go mod tidy' with COPY go.mod go.sum ./; RUN go mod download && go mod verify; build with -mod=readonly.",
            "git rm --cached arena-api arena-worker and delete local root binaries.",
            "Add /arena-api /arena-worker /arena-migrate /arena-healthcheck to .gitignore and **/*;C to .dockerignore.",
            "Verify two builds of the same commit embed identical dependency versions.",
        ],
    ),
    # ---- API / integration --------------------------------------------------
    feature(
        17, "PR2-18", "BLOCKER",
        "Stop Bil24 gateway from returning fake success and validate credentials",
        "opus",
        "Bil24 CREATE_ORDER_EXT and CANCEL_ORDER return resultCode=0 with a fabricated "
        "orderId and status 'scaffold_stub' though no order is created/cancelled, and "
        "the gateway never validates the fid/token credentials, so RESERVATION creates "
        "real inventory holds unauthenticated.",
        [
            "Return an honest Bil24 error (resultCode != 0, e.g. NOT_IMPLEMENTED) from the order/cancel stubs, or wire them to real hcheckout flow; never resultCode=0 from a stub.",
            "Validate the fid/token pair against stored channel credentials before any state-mutating command.",
            "Implement or explicitly gate off ADD_PROMO_CODES and the legacy numeric compatibility_id_map; feature-flag the gateway off in production if partial.",
            "Test unauthenticated/invalid token is rejected and stubs never report success.",
        ],
    ),
    feature(
        18, "PR2-19", "MAJOR",
        "Fix WordPress plugin session/tier fetch and error parsing",
        "sonnet",
        "The WP plugin calls GET /v1/public/feeds/{token}/sessions which does not "
        "exist (backend mounts .../events and .../events/{id}); the 404 is swallowed "
        "so tiers render empty in production, and the checkout proxy reads "
        "$data['error'] (an array) instead of $data['error']['message'].",
        [
            "Fetch sessions/tiers via the existing /public/feeds/{token}/events/{event_id} endpoint (or add the sessions endpoint + spec).",
            "Fix class-checkout.php error extraction to $data['error']['message'] ?? ... for the nested envelope.",
            "Surface fetch failures instead of returning an empty tier list silently.",
        ],
    ),
    feature(
        19, "PR2-20", "MAJOR",
        "Restore OpenAPI-first contract and add a client-drift CI gate",
        "opus",
        "The committed TypeScript client is stale (missing all 6 public checkout/feed "
        "paths and PricingLineItem), ~40 mounted admin routes are absent from "
        "openapi.yaml, and no CI job regenerates clients and fails on diff.",
        [
            "Document the missing admin and public paths (admin orders/refunds/tickets/organizations, billing, channels, reconciliation, consent, reports, barcode-batches, stripe/connect, public checkout/feed) in openapi.yaml.",
            "Run ./generate-clients.sh and commit the regenerated Go/TS clients.",
            "Have widget/admin-web consume the generated client types instead of hand-rolled ones where feasible.",
            "Add a CI gate that regenerates clients and fails on any diff.",
        ],
    ),
    # ---- Frontend -----------------------------------------------------------
    feature(
        20, "PR2-21", "BLOCKER",
        "Make the widget embeddable and wire live feed data",
        "opus",
        "The widget hardcodes relative /v1 fetches (works only behind the demo proxy, "
        "404s on any third-party/WP embed), fabricates a synthetic stub event because "
        "loadFromFeed is dead code (no tiers/buyer fields ever render), and leaves a "
        "blank widget when the resume-token path fails.",
        [
            "Add an api-base attribute (default from the script origin) and prefix every fetch in api.ts.",
            "Invoke the real feed load (fetchFeedEvent/session detail) so tiers, buyer fields, and event metadata render.",
            "After a dead/failed resume token, clear it and run normal init; always set a visible loadError on recovery failure.",
            "E2E against a non-proxied origin proves tiers render and a bad resume token shows a retry path.",
        ],
    ),
    feature(
        21, "PR2-22", "MAJOR",
        "Fix seat-map race, pin the CDN build, translate hardcoded strings",
        "sonnet",
        "SeatMapView's session-switch $effect has no stale-guard so a slow schema "
        "fetch for an old session can win; the WP plugin loads the widget from a "
        "mutable @master jsDelivr URL with no SRI/pin; SeatMapView hardcodes English "
        "strings untranslated for ru/cs/he including screen-reader labels.",
        [
            "Add a generation token to the SeatMapView session $effect and bail if superseded before applying schema/poller.",
            "Pin the WP widget src to a versioned tag (@vX.Y.Z) with SRI, or self-host the built asset.",
            "Move SeatMapView hardcoded strings/aria-labels into the i18n catalog for all supported locales.",
        ],
    ),
    # ---- Architecture / refactor debt --------------------------------------
    feature(
        22, "PR2-23", "MAJOR",
        "Burn down the golangci-lint debt blocking production-ready",
        "sonnet",
        "doc 14 still declares 563 golangci-lint issues as the production blocker; "
        "56 nolint directives sit in non-test code. The lint gate must be green "
        "before a production-ready claim.",
        [
            "Run golangci-lint with the root .golangci.yml and drive the issue count to zero.",
            "Justify or remove each nolint in hand-written code.",
            "Do not silence issues by broadening excludes; fix the underlying code.",
            "Record the clean lint run output in claude-progress.txt.",
        ],
    ),
    feature(
        23, "PR2-24", "MINOR",
        "Reconcile sqlc codegen and refresh stale architecture docs",
        "sonnet",
        "The gen/ tree claims sqlc v2.3.0 (never released) with 5 files marked "
        "hand-written, so the typed-query/schema-sync guarantee is unverifiable; "
        "doc 14 still claims migration head 0041 and 171 features while the real "
        "head is 0064; the declared domain/app/adapters layering has no ADR.",
        [
            "Run real sqlc generate against sqlc.yaml and reconcile drift (or replace the fabricated headers with an honest hand-written note where codegen genuinely cannot express the query).",
            "Refresh 08_architecture/14_current_implementation_overview_ru.md to migration head 0064 and current feature count/contexts.",
            "File the ADR that either accepts the handler-centric layout or plans extraction into internal/app.",
        ],
    ),
]


def install() -> None:
    connection = sqlite3.connect(DB, timeout=30)
    connection.execute("PRAGMA busy_timeout=30000")
    try:
        expected = [(item["id"], item["name"]) for item in FEATURES]
        existing = connection.execute(
            "SELECT id, name FROM features WHERE id >= ? ORDER BY id",
            (START_ID,),
        ).fetchall()
        if existing:
            if existing == expected:
                print("PR2 hardening features already installed.")
                return
            raise RuntimeError(f"Conflicting features at/above {START_ID}: {existing}")

        head = connection.execute(
            "SELECT MAX(id), MAX(priority) FROM features"
        ).fetchone()
        if head != EXPECTED_HEAD:
            raise RuntimeError(
                f"Unexpected queue head {head}; expected {EXPECTED_HEAD}. Refusing insert."
            )

        backup = ROOT / ".autoforge" / "backups" / (
            "arena_new_features_before_pr2_wave_"
            + datetime.now().strftime("%Y%m%d_%H%M%S")
            + ".db"
        )
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as backup_connection:
            connection.backup(backup_connection)

        connection.execute("BEGIN IMMEDIATE")
        for item in FEATURES:
            connection.execute(
                """
                INSERT INTO features (
                    id, priority, category, name, description, steps,
                    passes, in_progress, dependencies, needs_human_input,
                    human_input_request, human_input_response
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL)
                """,
                (
                    item["id"],
                    item["priority"],
                    item["category"],
                    item["name"],
                    item["description"],
                    json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]) if item["dependencies"] else None,
                ),
            )
        connection.commit()
        print(f"Installed {len(FEATURES)} PR2 features (ids {START_ID}-{START_ID + len(FEATURES) - 1}); backup: {backup}")
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
