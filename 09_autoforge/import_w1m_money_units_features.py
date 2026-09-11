"""Mini-wave W1-M (AutoForge) - Bil24 gateway money units: wire = major units
(float, <= 2 dp) like real Bil24 / WooCommerce, DB = minor bigint, one
conversion package. Features #528-#530, priorities 1085-1087.

Design authority: 08_architecture/20_bil24_gateway_money_units_spec_ru.md.
Owner decision 2026-09-11. Complexity 3 -> Opus 5 (the big edit), 2 -> Sonnet 5.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_w1m_money_units_features.py
"""

from __future__ import annotations

import json
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (527, 1084)
CATEGORY = "WP Bil24 Compat W1-M"
START_ID = 528
START_PRIORITY = 1085

SPEC = "08_architecture/20_bil24_gateway_money_units_spec_ru.md"

TAIL = (
    f" READ FIRST: {SPEC} (design authority, short - read it whole; section refs below are its "
    "sections) and 09_autoforge/W1_BRIEFING.md. This wave DELIBERATELY changes what the gateway "
    "puts on the wire: today hbil24 emits DB minor units verbatim, the spec says major units with "
    "<= 2 decimals - follow the spec, and where the code, a code comment or AGENTS.md disagree "
    "with the spec, the spec wins and you fix the comment/AGENTS.md. Golden fixtures under "
    "tests/compat/bil24/testdata/wp/golden stay UNCHANGED; the seeds in seed_test.go are "
    "multiplied by 100 instead (spec section 5) - that is the one sanctioned way to keep them "
    "green. Gates for a sub-feature: go.exe build ./... && go.exe vet ./..., go.exe test on "
    "touched packages (no pipelines: > log 2>&1; echo EXIT:$?), gofmt -l on changed files, DB "
    "tests behind //go:build integration run once with "
    "DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable "
    "JWT_SIGNING_SECRET=x go.exe test -tags integration <pkg> (the compat harness "
    "tests/compat/bil24 MUST be run this way after every money change). Touched openapi.yaml -> "
    "codegen (openapi30gen + oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml + "
    "node scripts/gen-ts-client.mjs, delete .compat30.gen.yaml) and commit generated files. NEVER "
    "git add . / -A - stage by path, check git status --short. Commit AND push. Never weaken or "
    "delete a test to get green; write a progress note listing every gate command with its exit "
    "code."
)

FEATURES = [
    {
        "id": 528,
        "name": "W1-M1 [MAJOR]: money package + all hbil24 emit sites and PAY_ORDER/REFUND/import consume sites on major-unit wire",
        "description": (
            "Spec sections 2, 3, 5. (1) New package internal/adapters/bil24compat/money with "
            "Major(minor int64) float64 (divide by scale, round to 2 dp), Minor(major float64) int64 "
            "(round half away from zero), ScaleFor(currency) with a table for exponent-0 currencies "
            "(JPY, ISK, HUF, ...) defaulting to 100, plus unit tests (rounding, reversibility for "
            "<= 2 dp inputs, ScaleFor). (2) Convert EVERY emit site in the table of section 3 to "
            "money.Major: GET_ALL_ACTIONS minPrice/maxPrice/price (cmd_catalog.go, "
            "cmd_catalog_events.go), GET_SEAT_LIST categoryList/seatList price (cmd_seat_list.go), "
            "GET_CART price/sum/discountAmount/chargeAmount/totalSum (cmd_cart_get.go - keep the "
            "fee_percent math in minor units, convert once when encoding), RESERVATION cart view and "
            "pricing paths (cmd_cart_view.go, cmd_cart.go bil24FinancialFields), CREATE_ORDER_EXT "
            "(cmd_order_create.go), both legacy GET_ORDER_INFO paths (cmd_order.go) so they match "
            "the already-correct bil24wire projection in cmd_order_wire.go. (3) Consume sites: "
            "PAY_ORDER compares money.Minor(amount) with orders.total using a tolerance of ONE minor "
            "unit (replace payAmountToleranceMajor), and records both minor amounts on mismatch; "
            "REFUND_TICKET refundPriceMinorUnits and bil24compat PriceMinorUnits delegate to "
            "money.Minor. (4) Fixtures: multiply price_amount seeds in "
            "tests/compat/bil24/seed_test.go by 100 (500->50000, 900->90000, 1250->125000, and any "
            "other money seed) so the UNCHANGED goldens (sum 500, totalSum 525, minPrice 900/1250, "
            "PAY_ORDER amount 525) stay green; add one fractional golden case per section 5 (seed "
            "1890 -> price 18.9, charge 0.95, totalSum 19.85; PAY_ORDER accepts 19.85/19.84/19.86 "
            "and rejects 19.9 with amount_mismatch) and assert the JSON renders 18.9 not 18.90 or "
            "18.899999. (5) Fix event_bundle_527_integration_test.go and scenario08_import_test.go to "
            "expect 450/900 and 900/350 - delete the 'known gap' comments. (6) Static guardrail test "
            "in tests/staticanalysis forbidding the literals '/ 100' and '* 100' in hbil24, macs and "
            "bil24wire outside the money package. (7) Rewrite the AGENTS.md gotcha 'The Bil24 compat "
            "gateway does NOT convert money units on the wire' to the new rule (wire major, DB minor, "
            "conversion only in bil24compat/money, PAY_ORDER compares Minor(amount) with tolerance 1) "
            "and add a link to spec 20 in BEHAVIOR_DIFFERENCES.md section 8. Run the whole compat "
            "harness with -tags integration -count=1 before marking done." + TAIL
        ),
        "steps": [
            "money package with Major/Minor/ScaleFor and unit tests.",
            "Convert the 13 hbil24 emit sites; PAY_ORDER/REFUND_TICKET/import consume via money.Minor.",
            "Seeds x100, fractional golden case, fix #527/scenario08 expectations.",
            "Static guardrail, AGENTS.md rewrite, BEHAVIOR_DIFFERENCES link; full compat harness green.",
        ],
        "complexity": 3,
        "dependencies": [],
    },
    {
        "id": 529,
        "name": "W1-M2: MACS order.paid/ticket.refunded money to major units via money; bil24wire on money; contract doc",
        "description": (
            "Spec sections 2, 4. internal/platform/macs/export.go: Order.{Sum,Discount,Charge,TotalSum} "
            "and Ticket.{Price,Discount,Charge,TotalPrice,RefundPrice} become float64 (RefundPrice "
            "*float64) filled with money.Major from the orderexport minor values; JSON tags unchanged; "
            "update the 'minor units' comments. bil24wire/encode.go: replace the local major() helper "
            "with money.Major (behaviour unchanged). Update 08_architecture/"
            "17_macs_integration_contract.md (price/discount rows, lines ~74 and ~88) to state major "
            "units with <= 2 dp, same as the real Bil24 webhook MACS already ingests. Tests: the MACS "
            "stub/round-trip tests (TestMACS_RoundTrip, TestMACS_AB50e_ThreeTicketRoundTrip, and the "
            "W1 MACS tests) assert totalSum/price have <= 2 decimals and equal the bil24wire value for "
            "the same order; add a case with a fractional price (18.9). Keep macs/*.go free of '/ 100' "
            "literals (guardrail from #528)." + TAIL
        ),
        "steps": [
            "macs.Order/Ticket money fields to float64 major via money.Major; comments updated.",
            "bil24wire major() -> money.Major.",
            "Contract doc 17 updated; MACS round-trip tests assert major units incl. fractional case.",
        ],
        "complexity": 2,
        "dependencies": [528],
    },
    {
        "id": 530,
        "name": "W1-M [EPIC-VERIFY]: end-to-end money round-trip bundle -> catalog -> cart -> PAY_ORDER -> webhooks, full cold gate",
        "description": (
            "Spec section 6 and section 5. Add an integration test (//go:build integration, next to "
            "the compat harness) that: POSTs an event bundle with categoryList prices 450 and 900 "
            "(randomized names/externalRef) with publish:true, calls GET_ALL_ACTIONS and asserts "
            "price 450/900 and minPrice 450, RESERVATION of one GA unit of the 450 category and "
            "asserts sum 450 and totalSum = 450 plus the channel fee in major units with <= 2 dp, "
            "CREATE_ORDER_EXT totalSum equal to that, PAY_ORDER with amount = that same float -> "
            "resultCode 0, then asserts the order.paid webhook delivered to wpstub and the payload "
            "delivered to the MACS stub carry the same totalSum as a major-unit number. Then the FULL "
            "cold gate: go.exe test -count=1 ./... (no pipelines), integration -count=1 for "
            "tests/compat/bil24, himports, macs, migrations; golangci-lint @latest with absolute "
            "cache path; gofmt on all files changed in #528-#529; codegen drift (regenerate, git diff "
            "empty); npm run type-check and npm run admin:test. Progress note with every command and "
            "exit code; if anything is red fix it here, do not mark passing." + TAIL
        ),
        "steps": [
            "E2E integration test: bundle -> GET_ALL_ACTIONS -> RESERVATION -> CREATE_ORDER_EXT -> PAY_ORDER -> wpstub + MACS stub, all major units.",
            "Full cold-cache gate: unit, integration, lint, drift, admin.",
            "Progress note with commands and exit codes.",
        ],
        "complexity": 2,
        "dependencies": [529],
    },
]


def main() -> None:
    if not DB.exists():
        raise SystemExit(f"missing {DB}")
    con = sqlite3.connect(DB)
    head = con.execute("SELECT id, priority FROM features ORDER BY id DESC LIMIT 1").fetchone()
    if tuple(head) != EXPECTED_HEAD:
        raise SystemExit(f"queue head is {head}, expected {EXPECTED_HEAD}; refusing to import")
    backup_dir = ROOT / ".autoforge" / "backups"
    backup_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    shutil.copy2(DB, backup_dir / f"arena_new_features_before_w1m_money_{stamp}.db")

    for i, f in enumerate(FEATURES):
        assert f["id"] == START_ID + i, f["id"]
        con.execute(
            "INSERT INTO features (id, priority, category, name, description, steps, passes, "
            "in_progress, dependencies, needs_human_input, human_input_request, "
            "human_input_response, complexity) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL, ?)",
            (
                f["id"],
                START_PRIORITY + i,
                CATEGORY,
                f["name"],
                f["description"],
                json.dumps(f["steps"]),
                json.dumps(f["dependencies"]),
                f["complexity"],
            ),
        )
    con.commit()
    rows = con.execute(
        "SELECT id, priority, complexity, name FROM features WHERE id >= ? ORDER BY id", (START_ID,)
    ).fetchall()
    for r in rows:
        print(r)
    print(f"imported {len(rows)} features")


if __name__ == "__main__":
    main()
