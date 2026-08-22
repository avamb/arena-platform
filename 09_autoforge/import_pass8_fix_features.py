"""Pass 8 (AutoForge) — follow-ups from the interactive pass-7 review.

The interactive session re-committed pass 7 without the 9.7k junk files
(and a local JWT) and repaired the money-correctness bugs: promo
discounts now use real per-line amounts on confirm, the validate
pre-flight computes the discount once, the recovery path uses the same
helper, Order.sum is no longer doubled, GA seatId is in a disjoint range.
What remains is bounded, visible-on-failure work.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_pass8_fix_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (443, 1000)
CATEGORY = "Reconstruction Wave 4"
START_PRIORITY = 1001

TAIL = (
    " Read the pass-7 review verdict in 09_autoforge/WAVE4_RUNBOOK.md first. "
    "Docker/PG are available (DATABASE_URL=postgres://arena:arena@localhost:55432/"
    "arena?sslmode=disable). NEVER `git add -A` / `git add .` — stage the files you "
    "changed by path; .golangci-cache, .tmp, scratch *.txt and tokens must never be "
    "committed (a pass-7 commit swept in 9.7k files and a JWT). Run go test ./..., "
    "golangci-lint, openapi codegen drift, admin + widget tests before marking "
    "passing; commit AND push; never weaken an acceptance test to get green - fix "
    "the fixture instead."
)

FEATURES = [
    {
        "id": 444,
        "priority": START_PRIORITY,
        "name": "AB-50g [MAJOR]: true MACS round-trip through the real cancel handler + worker, strict stub validation",
        "description": (
            "Pass-7 AB-50e was rejected: the round-trip test cancels via a raw UPDATE "
            "and calls Dispatch() by hand (no outbox row, no next_attempt_at / "
            "dead-letter path), and the stub's validation was WEAKENED (seatId 0 "
            "accepted, cityName/venueName optional) so a fixture without a city "
            "would pass. Required: (1) the integration test cancels ONE of three "
            "tickets through POST /v1/tickets/{id}/cancel with refund_mode=manual "
            "(the real handler, via the composed server or the exported handler with "
            "a real pool) and runs the REAL outbox dispatcher loop (OutboxEventsDispatcher "
            "with a MACS dispatcher; the stub first answers 503 then 200) asserting "
            "stored holderStatus 3 / 0 / 0, next_attempt_at set on the failed attempt "
            "and no dead-letter; (2) restore strict validation exactly like the MACS "
            "Pydantic Ticket model (id, seatId>0, barcode, actionEvent.cityName/"
            "venueName required) and seed a city for the fixture venue instead; the "
            "export must fail loudly (422 macs.export_incomplete) when a venue has no "
            "city rather than emitting 'Unknown City'; (3) fabricate random order id / "
            "status PAID / EAN-13 like the importer and STORE the fabricated values so "
            "an incomplete import is visibly garbage in assertions; (4) delete the dead "
            "panic() fixture helpers; move the DB-less stub validation test out of the "
            "integration-tagged file so it runs in the Unit job." + TAIL
        ),
        "steps": [
            "Round-trip through the real cancel handler and the real outbox dispatcher (503 then 200), asserting stored state + retry bookkeeping.",
            "Strict stub validation; fixture gets a city; export 422 on missing city.",
            "Importer fabrication stored and asserted; dead helpers removed; unit-level stub test untagged.",
        ],
        "complexity": 2,
        "dependencies": [441],
    },
    {
        "id": 445,
        "priority": START_PRIORITY + 1,
        "name": "AB-50h [MAJOR]: MACS export - golden file, discountReason, computed discount/charge, seated sold price",
        "description": (
            "Pass-7 AB-50f left: (1) add the owner's sample_tickets.json to the repo "
            "(ask for it under 01_official_bil24_docs or docs/macs/) and a golden-file "
            "test asserting field presence + types of our export against it, plus a "
            "seeded integration test of the export endpoint; (2) discountReason = promo "
            "NAME (not code) when a promo applied, or 'Внешняя система' for externally "
            "imported sales, never empty; discount/charge/totalPrice computed from the "
            "checkout's promo discount prorated per line, not hardcoded 0/price; tariff "
            "= category name; (3) seated tickets still report the CURRENT tier price - "
            "take the sold price from the reservation's locked reservation_ga_items "
            "line for the seat's tier (seated reservations write those lines since "
            "AB-48), falling back to tier price only for legacy reservations; untiered "
            "GA must not fall to 0 - use subtotal / quantity as the last fallback; (4) "
            "replace the remaining raw SQL in macs/dispatcher.go (getOrgIDForSession, "
            "getSystemTicketID) with gen querier methods; (5) the MACS webhook "
            "registration audit must not discard its write error (log Error)." + TAIL
        ),
        "steps": [
            "Golden-file + seeded integration tests for the export.",
            "discountReason / tariff / computed discount+charge.",
            "Seated sold price from locked lines; untiered GA fallback; raw SQL replaced; audit error logged.",
        ],
        "complexity": 2,
        "dependencies": [444],
    },
    {
        "id": 446,
        "priority": START_PRIORITY + 2,
        "name": "AB-45c [MAJOR]: promo-tier tests, metadata clear semantics, logo clear error, OpenAPI org logo, backlog decision",
        "description": (
            "Pass-7 AB-45b left: (1) tests - zero tests were added; cover "
            "ValidatePromoForLines (eligible-lines math, min-order on eligible subtotal, "
            "tier_not_applicable, unrestricted), the public checkout path with a "
            "tier-restricted code and mixed cart, the recovery path, and the validate "
            "pre-flight (discount computed once); (2) UpdateEventMetadata is now "
            "COALESCE-based so a field can never be CLEARED - introduce tri-state "
            "inputs (absent = keep, null = clear) like hiam's optionalString and test "
            "both; (3) PATCH organizations logo clear branch swallows the DB error and "
            "returns 200 with stale data - surface it as 500; accept JSON null as "
            "clear; (4) add logo_media_id to the Organization response + update "
            "request in openapi.yaml and regenerate Go types + TS client (codegen "
            "drift today); (5) remove the legacy validatePromoCode copies "
            "(hcheckout/promo_codes.go and httpserver/pricing_calculator.go) once no "
            "caller remains - ONE helper; (6) decide Promoter / Rating / Min. service "
            "fee % (Bil24 editor inputs) and record the decision in "
            "admin_bootstrap_backlog.md AB-45." + TAIL
        ),
        "steps": [
            "Promo-tier test suite (helper, public checkout, recovery, validate).",
            "Tri-state metadata PATCH with clear; logo clear error surfaced; OpenAPI + codegen for logo_media_id.",
            "Legacy promo helpers removed; backlog decision recorded.",
        ],
        "complexity": 2,
        "dependencies": [443],
    },
]


def install() -> None:
    with sqlite3.connect(DB) as connection:
        head = connection.execute("SELECT MAX(id), MAX(priority) FROM features").fetchone()
        if tuple(head) != EXPECTED_HEAD:
            raise SystemExit(
                f"Unexpected queue head {tuple(head)}; expected {EXPECTED_HEAD}. Refusing insert."
            )
        backup = ROOT / ".autoforge" / "backups" / (
            f"arena_new_features_before_pass8_{datetime.now():%Y%m%d_%H%M%S}.db"
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
                    item["id"], item["priority"], CATEGORY, item["name"],
                    item["description"], json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]), item["complexity"],
                ),
            )
        connection.commit()
    print(f"Inserted #444..#446 (pass 8 fix features); backup: {backup}")


if __name__ == "__main__":
    install()
