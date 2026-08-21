"""Split AB-50 (#431) into four single-session sub-features (#437..#440).

Why: AutoForge agents refused #431 eighteen sessions in a row as "too
large for one turn" and asked for exactly this split. Each sub-feature
fits one session; #431 becomes the umbrella acceptance that depends on
all four and only verifies the round trip end to end.

Idempotent: refuses to insert unless the queue head is EXPECTED_HEAD and
backs the DB up first. Fix EXPECTED_HEAD deliberately if the queue moved;
do not delete the guard.

Run:  python 09_autoforge/import_ab50_split_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (436, 993)  # (max id, max priority) before this split
GATE_PASS5 = 434
UMBRELLA = 431
CATEGORY = "Reconstruction Wave 4"
START_PRIORITY = 994

COMMON_TAIL = (
    " Full context: 09_autoforge/admin_bootstrap_backlog.md, section AB-50 "
    "(under 'Wave 4 - reconstruction alignment') - read it first; it records "
    "the MACS source contract and marks which current behaviours are CORRECT. "
    "Build against MACS (the actual receiver), not the Bil24 docs, wherever "
    "they differ. Isolate ALL MACS-shaped mapping behind one adapter boundary "
    "(apps/backend/internal/platform/macs) - nothing MACS-specific may leak into "
    "the catalog or ticketing domain. Docker/PostgreSQL ARE available: "
    "DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable "
    "(migrated to head). Do not mark passing from unit tests alone when the "
    "change needs PostgreSQL; never weaken an acceptance test to get green. "
    "Any API change: update openapi.yaml and regenerate Go types + TS client in "
    "the same commit. Run the repo gates (go test ./..., golangci-lint, admin + "
    "widget tests) before marking passing; commit AND push."
)

FEATURES = [
    {
        "id": 437,
        "priority": START_PRIORITY,
        "name": "AB-50a [CRITICAL]: MACS ticket-system registration + integer id scheme (migration)",
        "description": (
            "Severity: CRITICAL. First of four AB-50 sub-features. MACS types id and "
            "seatId as INTEGER while ours are uuidv7. Register the platform as a ticket "
            "system and give every ticket/seat a stable, collision-free integer id via "
            "the SIMPLEST scheme that works (e.g. a bigint sequence column per entity); do "
            "not engineer an elaborate permanent scheme - widening MACS to string ids is the "
            "real long-term fix and belongs on the MACS backlog." + COMMON_TAIL
        ),
        "steps": [
            "New migration (next number after the current head): ticket_system registry (slug, name), tickets.system_ticket_id bigint (sequence-backed, unique), session_seats.system_seat_id bigint (sequence-backed, unique). Backfill existing rows. Bump the migration-head pin test.",
            "sqlc-style queries + hand-written gen wrappers for: resolve system ids for a ticket / seat / list of tickets; lookup ticket by system_ticket_id.",
            "Extend every SELECT that feeds a shared scan helper you touch (grep the scanner's callers - see AGENTS.md); run go test -tags integration against the local PG to prove the migration applies on real data.",
            "Keep the ids INTERNAL for now: no API surface changes in this sub-feature beyond the columns existing. Document the scheme in the migration header.",
        ],
        "complexity": 2,
        "dependencies": [GATE_PASS5],
    },
    {
        "id": 438,
        "priority": START_PRIORITY + 1,
        "name": "AB-50b [CRITICAL]: MACS JSON export (API endpoint + downloadable file)",
        "description": (
            "Severity: CRITICAL. Second AB-50 sub-feature. Ticket/order export in the EXACT "
            "shape of the owner's sample (orders at top level; tickets nested in ticketList; "
            "actionEvent per ticket with id, cityName, venueName, actionName, "
            "actionLegalOwner, showTime; seatId / id as integers from #437; barcode; "
            "holderStatus INTEGER 0 not used / 1 checked in / 2 checked out / 3 refunded; "
            "refundDate / refundPrice from the AB-49 ticket columns). MACS's importer "
            "silently fabricates missing values (random order ids, status PAID, 'Unknown "
            "City'/'Unknown Venue', EAN-13) - so every field must be present and truthful." + COMMON_TAIL
        ),
        "steps": [
            "Adapter package builds the export document from tickets + checkout_sessions + sessions/events/venues/cities; status collapses usage+refund only at the boundary (cancelled/revoked -> 3).",
            "GET /v1/organizations/{org_id}/sessions/{session_id}/macs-export (JSON) and the same with ?download=1 returning Content-Disposition attachment for the service's Import Tickets action. Permission: a new or existing export permission, org membership enforced.",
            "OpenAPI + Go types + TS client. Admin UI: a 'MACS export' download action on the session (events.tsx sessions tab).",
            "Tests: golden-file test against the owner's sample shape (field presence + types), integration test producing an export from seeded data on the local PG.",
        ],
        "complexity": 2,
        "dependencies": [437],
    },
    {
        "id": 439,
        "priority": START_PRIORITY + 2,
        "name": "AB-50c [CRITICAL]: MACS webhooks - order.paid + ticket.refunded, HMAC, outbox retry",
        "description": (
            "Severity: CRITICAL. Third AB-50 sub-feature. Webhook delivery with the MACS "
            "envelope {id:int, created, type, data} to POST /_wh/tickets. MACS handles ONLY "
            "order.paid and ticket.refunded - NOT order.cancelled and NOT the event.* "
            "triggers in the Bil24 docs. ACCEPTANCE: a ticket CANCELLED by the operator "
            "(AB-49, any refund_mode: automatic, manual or none) reaches MACS as "
            "ticket.refunded - admission has nothing to do with whether money moved; a "
            "whole-order cancellation is one ticket.refunded per ticket. Consume the "
            "existing outbox events (v1.ticket.cancelled, v1.ticket.refunded, "
            "v1.ticket.revoked, ticket issuance) - do not invent a second event source." + COMMON_TAIL
        ),
        "steps": [
            "Outbox consumer in the worker mapping platform events -> MACS envelope via the adapter; POST, success = HTTP 200, retry over 24 hours reusing 0068 next_attempt_at / dead_lettered_at - no second retry mechanism.",
            "Sign every webhook (HMAC over the body with webhook_subscribers.secret, header documented); verification must be optional on the receiver so MACS keeps working unchanged.",
            "Subscriber registration: reuse webhook_subscribers with a macs kind/flag; admin UI to register the MACS endpoint + secret for an org.",
            "Tests: envelope golden tests per event type; worker test proving retry + dead-letter reuse; integration test that a cancel via POST /v1/tickets/{id}/cancel with refund_mode=manual enqueues exactly one ticket.refunded.",
        ],
        "complexity": 2,
        "dependencies": [438],
    },
    {
        "id": 440,
        "priority": START_PRIORITY + 3,
        "name": "AB-50d [CRITICAL]: internal scanner status-gate + round-trip test against a stub MACS receiver",
        "description": (
            "Severity: CRITICAL. Fourth AB-50 sub-feature. (1) Minimum action on the three "
            "internal scan endpoints, which are NOT the product: POST /v1/scan, "
            "POST /v1/scanner/validate, POST /v1/scanner/scan-events must respect ticket "
            "status (the scan-events query already selects it and never reads it); mark "
            "them internal/testing-only in the OpenAPI description; build nothing further "
            "on them. (2) The round-trip acceptance: a stub MACS receiver (Go httptest "
            "server implementing the importer's fabrication behaviour) asserting what MACS "
            "STORED, not the HTTP status it returned." + COMMON_TAIL
        ),
        "steps": [
            "Status gate on the three endpoints: cancelled/revoked/transferred tickets are rejected with a specific code; regression tests per endpoint.",
            "Stub receiver package under internal/platform/macs/stub (or testutil): accepts /_wh/tickets and the import JSON, stores what a MACS importer would store, exposes it for assertions, mirrors the fabrication of missing values so an incomplete export is visibly garbage.",
            "Integration test (docker PG + worker + stub): seed order -> export -> import into stub -> cancel one ticket (refund_mode=manual) -> worker delivers ticket.refunded -> stub shows status 3 for that ticket and 0 for the others; order.paid on issuance.",
            "Document the MACS contract + ops runbook (how to register the receiver, rotate the secret, re-export) in 08_architecture.",
        ],
        "complexity": 2,
        "dependencies": [439],
    },
]

UMBRELLA_DESCRIPTION = (
    "UMBRELLA / ACCEPTANCE ONLY (split 2026-08-22 after eighteen single-session "
    "refusals). The work lives in #437 AB-50a (ids), #438 AB-50b (export), #439 "
    "AB-50c (webhooks), #440 AB-50d (scanner gate + round-trip stub test). This "
    "feature depends on all four and passes when the end-to-end round trip "
    "described in AB-50d is green in CI (Integration job) and the OpenAPI / "
    "codegen / admin / widget gates are green on the merged tree. Do NOT "
    "re-implement anything here; verify, fix integration seams if the pieces "
    "do not meet, and mark passing with the CI run id in claude-progress.txt."
)
UMBRELLA_STEPS = [
    "Confirm #437..#440 are passing and pushed; pull the tree.",
    "Run the AB-50d round-trip integration test against the local PG + stub receiver; run go test ./..., golangci-lint, openapi codegen drift, admin + widget tests.",
    "Fix only integration seams between the four pieces (no new scope); record the CI run id and mark passing.",
]


def install() -> None:
    with sqlite3.connect(DB) as connection:
        head = connection.execute("SELECT MAX(id), MAX(priority) FROM features").fetchone()
        if tuple(head) != EXPECTED_HEAD:
            raise SystemExit(
                f"Unexpected queue head {tuple(head)}; expected {EXPECTED_HEAD}. Refusing insert."
            )
        backup = ROOT / ".autoforge" / "backups" / (
            f"arena_new_features_before_ab50_split_{datetime.now():%Y%m%d_%H%M%S}.db"
        )
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as backup_connection:
            connection.backup(backup_connection)

        for item in FEATURES:
            connection.execute(
                """
                INSERT INTO features (
                    id, priority, category, name, description, steps,
                    passes, in_progress, dependencies, needs_human_input, complexity
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, ?)
                """,
                (
                    item["id"],
                    item["priority"],
                    CATEGORY,
                    item["name"],
                    item["description"],
                    json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]),
                    item["complexity"],
                ),
            )
        connection.execute(
            """
            UPDATE features
            SET description = ?, steps = ?, dependencies = ?, complexity = 1,
                in_progress = 0, passes = 0
            WHERE id = ?
            """,
            (
                UMBRELLA_DESCRIPTION,
                json.dumps(UMBRELLA_STEPS, ensure_ascii=False),
                json.dumps([f["id"] for f in FEATURES]),
                UMBRELLA,
            ),
        )
        connection.commit()
    print(
        f"Inserted #437..#440 (AB-50a-d), #431 is now the umbrella depending on them; "
        f"backup: {backup}"
    )


if __name__ == "__main__":
    install()
