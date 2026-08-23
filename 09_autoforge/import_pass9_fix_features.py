"""Pass 9 (AutoForge) — last follow-ups of the wave-4 MACS / AB-45 thread
from the interactive pass-8 review. Small, test-heavy, visible-on-failure.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_pass9_fix_features.py
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (446, 1003)
CATEGORY = "Reconstruction Wave 4"
START_PRIORITY = 1004

TAIL = (
    " Docker/PG are available (DATABASE_URL=postgres://arena:arena@localhost:55432/"
    "arena?sslmode=disable). NEVER `git add .` / `git add -A` - stage changed files by "
    "path and check `git status --short` before committing. Run go test ./..., "
    "golangci-lint, openapi codegen drift, admin + widget tests before marking "
    "passing; commit AND push; never weaken a test to get green."
)

FEATURES = [
    {
        "id": 447,
        "priority": START_PRIORITY,
        "name": "AB-50i [NORMAL]: MACS stub importer fabrication + export endpoint tests (422/200) + binding golden file",
        "description": (
            "Pass-8 leftovers on the MACS side: (1) the stub importer must FABRICATE "
            "like the real one (random integer order id when missing, status PAID, "
            "barcodeFormat EAN-13) and STORE the fabricated values so a test asserting "
            "stored state sees garbage when the export is incomplete - today "
            "fabricateOptionals was deleted and nothing is fabricated; (2) handler-level "
            "tests for GET .../sessions/{id}/macs-export: 200 with a complete fixture, "
            "422 macs.export_incomplete for a city-less venue, 404 for a session of "
            "another org (tenant isolation); (3) make the golden-file comparison "
            "BINDING: the test must FAIL (not t.Logf) when our export's field set or "
            "types diverge from macs/testdata/sample_tickets.json; ask the owner for the "
            "real sample (the current file is synthetic - say so in the file header) and "
            "replace it when provided; (4) the 50e round-trip test still cancels via raw "
            "UPDATE and hand-calls Dispatch() - either delete it as superseded by 50g or "
            "migrate it; (5) remove dead code in macs_ab50h_integration_test.go "
            "(`_ = orderByCS`, empty loops, duplicated promo subtest); (6) proration "
            "remainder: the last ticket of an order absorbs the rounding so the sum of "
            "ticket discounts equals the order discount exactly." + TAIL
        ),
        "steps": [
            "Stub fabrication stored + asserted; 50e test deleted or migrated.",
            "macs-export handler tests 200/422/404.",
            "Binding golden-file test; synthetic-sample disclaimer; proration remainder fix with a test.",
        ],
        "complexity": 2,
        "dependencies": [445],
    },
    {
        "id": 448,
        "priority": START_PRIORITY + 1,
        "name": "AB-45d [NORMAL]: tests for tri-state event metadata, logo clear, public-checkout promo tiers, recovery, validate endpoint",
        "description": (
            "Pass-8 AB-45c shipped code with zero tests for: (1) PATCH event metadata "
            "tri-state (absent = keep, JSON null = clear, value = set) for slug / "
            "short_description / genre / age_rating / duration_minutes / teaser_url / "
            "trailer_url / meta_* - handler tests for all three cases and for the honest "
            "500 on a failed update; (2) PATCH organizations logo: null clears, bad UUID "
            "400, wrong owner_type / other org's media 422, DB failure 500 "
            "(org.logo_clear_failed) - and make the superadmin path "
            "(hiam/admin_orgs.go) honour null=clear the same way instead of silently "
            "ignoring it; (3) public feed checkout with a tier-restricted promo and a "
            "mixed cart (eligible + ineligible lines): discount limited to eligible "
            "lines; same for the hold-recovery path; (4) HandleValidatePromoCode: "
            "discount computed exactly once with several tier_ids, and an empty "
            "tier_ids on a restricted code -> 422 promo.tier_not_applicable; (5) "
            "OpenAPI: document the PATCH event metadata fields and their null-clears "
            "semantics (they are absent from openapi.yaml today) and regenerate Go types "
            "+ TS client." + TAIL
        ),
        "steps": [
            "Metadata tri-state tests + 500 path.",
            "Logo PATCH tests; superadmin null=clear.",
            "Public checkout / recovery / validate promo-tier tests.",
            "OpenAPI for event metadata PATCH + codegen.",
        ],
        "complexity": 2,
        "dependencies": [446],
    },
    {
        "id": 449,
        "priority": START_PRIORITY + 2,
        "name": "DOCS [NORMAL]: wave-4 epilogue - MACS ops runbook + WP contract + AGENTS.md conventions",
        "description": (
            "Close the documentation debt of wave 4: (1) 08_architecture/"
            "17_macs_integration_contract.md must match the shipped contract exactly - "
            "holderStatus 0/1/2/3, envelope {id:int from the outbox row, created RFC3339 "
            "UTC, type, data = full Ticket}, only order.paid + ticket.refunded, HMAC "
            "header X-MACS-Signature sha256=<hex> over the raw body (optional on the "
            "receiver), retry 30 attempts / ~24h via outbox, 422 macs.export_incomplete, "
            "GA seatId range 1e9+ticket id, discountReason vocabulary; plus an ops "
            "section: how to register the MACS endpoint per org (PUT /organizations/"
            "{org_id}/macs-webhook), rotate the secret, re-export a session, read "
            "dead-lettered outbox rows; (2) 08_architecture/02_wordpress_integration_"
            "contract_ru.md: current_price / next_price_change_at on tiers and "
            "category_prices (AB-48), cancellation semantics visible to WP (AB-49); (3) "
            "AGENTS.md: record the conventions learned in passes 6-8 (never git add -A; "
            "integration tests must go through real handlers + real dispatcher; run "
            "golangci-lint with the repo cache path; gofmt integration-tagged files too "
            "since lint skips them)." + TAIL
        ),
        "steps": [
            "MACS contract doc reconciled with code + ops section.",
            "WP contract updated for AB-48/AB-49 fields.",
            "AGENTS.md conventions.",
        ],
        "complexity": 1,
        "dependencies": [447],
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
            f"arena_new_features_before_pass9_{datetime.now():%Y%m%d_%H%M%S}.db"
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
    print(f"Inserted #447..#449 (pass 9); backup: {backup}")


if __name__ == "__main__":
    install()
