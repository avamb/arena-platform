"""Pass 7 (AutoForge) — follow-ups from the interactive pass-6 review.

The interactive session closed the BLOCKERS of the pass-6 batch (tenant
holes on macs-export and event_artists, MACS webhook data = full Ticket,
refund sources resolved from the ticket, all three scan endpoints gated,
holderStatus enum, 24h retry window, nullable order columns in the export
query, the broken `tc.cred_type` join). What remains is bounded,
visible-on-failure work — AutoForge territory.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_pass7_fix_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (440, 997)
CATEGORY = "Reconstruction Wave 4"
START_PRIORITY = 998

TAIL = (
    " Read 09_autoforge/admin_bootstrap_backlog.md (AB-50 / AB-45) and the "
    "pass-6 review verdict in 09_autoforge/WAVE4_RUNBOOK.md first. Docker/PG "
    "are available (DATABASE_URL=postgres://arena:arena@localhost:55432/arena?"
    "sslmode=disable). Run go test ./..., golangci-lint, openapi codegen drift, "
    "admin + widget tests before marking passing; commit AND push; never weaken "
    "an acceptance test."
)

FEATURES = [
    {
        "id": 441,
        "priority": START_PRIORITY,
        "name": "AB-50e [MAJOR]: MACS stub receiver with importer fidelity + true round-trip test",
        "description": (
            "The pass-6 stub (internal/platform/macs/stub) only appends envelopes and "
            "returns 200 — it does not store what MACS would store. Make it a faithful "
            "importer: a ticket table keyed by ticket id with holderStatus; accept the "
            "export JSON (Import Tickets) and the /_wh/tickets envelopes; validate the "
            "MACS-required fields (id, seatId, barcode, actionEvent{...}) and respond "
            "422 on a missing one exactly like the Pydantic model; mirror the importer's "
            "fabrication of missing optional values (random order id, status PAID, "
            "'Unknown City'/'Unknown Venue', EAN-13) so an incomplete export is visibly "
            "garbage. Then the round-trip test must assert STORED state: seed an order "
            "with 3 tickets → export → import into stub → cancel ONE ticket through "
            "POST /v1/tickets/{id}/cancel with refund_mode=manual (the real handler, not "
            "a raw UPDATE) → run the worker dispatcher → stub shows holderStatus 3 for "
            "that ticket and 0 for the other two; order.paid on issuance; retry on a "
            "503 then success; HMAC verified by the stub when a secret is set." + TAIL
        ),
        "steps": [
            "Stub: ticket store + import endpoint + envelope endpoint + required-field validation (422) + fabrication of optional values + optional HMAC verification.",
            "Round-trip integration test (docker PG) through the real cancel handler and the real outbox dispatcher; assert stored holderStatus per ticket, retry + dead-letter reuse, HMAC.",
            "Per-endpoint regression tests for the three internal scan endpoints (POST /v1/scan, /v1/scanner/validate, /v1/scanner/scan-events): cancelled, revoked and transferred tickets are rejected.",
            "OpenAPI: mark the three internal scan endpoints internal/testing-only in their descriptions.",
        ],
        "complexity": 2,
        "dependencies": [440],
    },
    {
        "id": 442,
        "priority": START_PRIORITY + 1,
        "name": "AB-50f [MAJOR]: MACS export fidelity - sold price, discountReason, venue-local showTime, tests, audit",
        "description": (
            "Export correctness gaps from the pass-6 review: (1) Ticket.price is the "
            "tier's CURRENT price_amount — with AB-48 scheduled prices every early-bird "
            "ticket is misreported; use the price the ticket was sold for (the "
            "reservation_ga_items locked unit_price via checkout_sessions.reservation_id, "
            "falling back to the tier price only for legacy reservations) and make "
            "Order.sum consistent with charge/totalSum; (2) discountReason must be "
            "populated (promo code name, or 'Внешняя система' for imported sales), "
            "tariff set, discount/charge computed, not hardcoded; (3) showTime must be "
            "venue-local time (venues carry a timezone since wave V) — not the server "
            "zone; (4) GA tickets reuse the ticket sequence for seatId which can collide "
            "with a real seat's seatId — give GA tickets a seatId from the GA unit they "
            "hold (session_seats.system_seat_id of the unit) or a disjoint range; (5) "
            "export_test.go is an empty comment block — add a golden-file test against "
            "the owner's sample_tickets.json shape and an integration test from seeded "
            "data; (6) audit PUT/DELETE /organizations/{org_id}/macs-webhook (who "
            "registered/rotated the endpoint that decides admission); (7) keep MACS "
            "subscribers out of the WordPress-facing generic subscriber listing; (8) "
            "replace the raw-SQL lookups in macs/dispatcher.go with the gen querier "
            "methods that already exist in macs_webhook_subscribers.sql.go." + TAIL
        ),
        "steps": [
            "Sold price + consistent sums; discountReason/tariff/discount/charge.",
            "Venue-local showTime; GA seatId from the held unit.",
            "Golden-file + seeded integration tests for the export.",
            "Audit on MACS webhook registration; listing separation; gen querier in the dispatcher.",
        ],
        "complexity": 2,
        "dependencies": [441],
    },
    {
        "id": 443,
        "priority": START_PRIORITY + 2,
        "name": "AB-45b [MAJOR]: promo tier restriction on the public checkout + PATCH-safe event metadata + audit",
        "description": (
            "AB-45 follow-ups from the pass-6 review: (1) promo_codes.applies_to_tier_ids "
            "is enforced only in the authenticated hcheckout confirm — the customer-facing "
            "public feed checkout (hfeed/public_feed_checkout.go applyPromoDiscount / "
            "validatePromo), the pricing quote (hcheckout/pricing_calculator.go) and "
            "HandleValidatePromoCode still apply a tier-restricted code to any tier; "
            "enforce it everywhere through ONE helper, and limit the discount to the "
            "eligible lines rather than the whole subtotal; (2) UpdateEventMetadata "
            "(queries/events.sql) overwrites all nine columns with NULL when a partial "
            "PATCH arrives — make it PATCH-safe (COALESCE with existing values) and "
            "surface its failure as an error, not a swallowed Warn + 200; (3) "
            "organizations.logo_media_id: give the org-scoped PATCH a write path, allow "
            "clearing it, and verify the media object belongs to the org with "
            "owner_type='org_logo'; (4) audit event_artists create/update/delete and "
            "metadata updates like the other catalog mutations; (5) decide and record "
            "Promoter / Rating / Min. service fee % (Bil24 editor inputs) — wire or "
            "explicitly decline in the backlog." + TAIL
        ),
        "steps": [
            "One promo-tier helper used by public checkout, quote, validate and confirm; discount limited to eligible lines; tests on the public path.",
            "PATCH-safe UpdateEventMetadata + honest error; tests.",
            "logo_media_id org-scoped write/clear with owner_type + org check.",
            "Audit events for artists + metadata; backlog decision on the remaining Bil24 inputs.",
        ],
        "complexity": 2,
        "dependencies": [434],
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
            f"arena_new_features_before_pass7_{datetime.now():%Y%m%d_%H%M%S}.db"
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
    print(f"Inserted #441..#443 (pass 7 fix features); backup: {backup}")


if __name__ == "__main__":
    install()
