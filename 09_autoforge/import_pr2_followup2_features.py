"""Install the PR2 second follow-up wave into features.db.

Source: 2026-07-19 adversarial verification of AutoForge's follow-up wave
(features 381-387). Most fixes verified REAL, but three residual defects
survived — most importantly a CI gate that provably cannot run the BLOCKER
regression proofs. Idempotent; refuses to renumber an unexpected queue.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (387, 472)
START_ID = 388
START_PRIORITY = 473


def feature(offset, code, severity, title, model, objective, steps, dependencies=None):
    return {
        "id": START_ID + offset,
        "priority": START_PRIORITY + offset,
        "category": "Production Hardening PR2 Follow-up 2",
        "name": f"{code} [{severity}]: {title}",
        "description": (
            f"Model: {model}. Severity: {severity}. {objective} "
            "Source: 2026-07-19 adversarial verification of features 381-387. "
            "The prior fix was verified INCOMPLETE — close the residual described "
            "here. Prove it by RUNNING the affected code against a live migrated "
            "PostgreSQL, not by source-grep (strings.Contains) or a test that "
            "skips. Never mark passing while an acceptance test unconditionally skips."
        ),
        "steps": steps,
        "dependencies": dependencies,
    }


FEATURES = [
    feature(
        0, "PR2-30", "BLOCKER",
        "Make the CI integration gate actually run the resale BLOCKER proofs",
        "opus",
        "PR2-28 claimed to make the PR2-04/PR2-27 resale-BLOCKER integration tests "
        "execute in CI, but they provably cannot: (1) the integration job env "
        "(.github/workflows/ci.yml) sets only DATABASE_URL/REDIS_URL, so "
        "'arena-migrate up' fails with 'JWT_SIGNING_SECRET is required when "
        "ENABLE_DEV_AUTH=true' before any test runs; (2) the precondition query in "
        "checkout_pr2_04_360_integration_test.go and checkout_pr2_27_383_integration_test.go "
        "joins on sessions.org_id, a column that does NOT exist (org_id lives on "
        "checkout_sessions, not sessions), so row.Scan always errors and the test "
        "hits t.Skipf unconditionally; (3) arena-seed never inserts an event/session "
        "row, so the (org,channel,session) triple can never resolve. The gate is "
        "green-theater; the BLOCKER regression proof never executes.",
        [
            "Fix the integration job env so migrate+seed succeed (set APP_ENV/JWT_SIGNING_SECRET appropriately for the integration profile, or run migrate in a mode that does not require dev-auth secrets).",
            "Fix the integration test precondition to resolve the session via its real FK (sessions -> events -> organizations, or use checkout_sessions.org_id) so the query succeeds.",
            "Make arena-seed insert a minimal event + session so the (org, channel, session) triple resolves in CI.",
            "Convert the unconditional t.Skipf on the precondition into a t.Fatalf when DATABASE_URL IS set (only skip when the DB is genuinely absent), so a broken precondition FAILS instead of silently passing.",
            "Prove in a live run that TestPR204Integration_* and TestPR227Integration_* actually EXECUTE (not SKIP) and their resale assertions run; capture the output.",
            "Remove the assertion-free stub test pr2_384_behavioral_test.go:~360 (TestPR2_384_AssertionAudit_... with func(_ *testing.T) and empty body).",
        ],
        [383],
    ),
    feature(
        1, "PR2-31", "MAJOR",
        "Guard external-allocations and complimentary org-scoped routes",
        "opus",
        "PR2-26 extended org-membership enforcement to most routes but MISSED two "
        "that were explicitly in its own gap list: external-allocations "
        "(inventory_shims.go handleCreate/List/Get/PatchExternalAllocation, "
        "mounted at /organizations/{org_id}/external-allocations, gated only by "
        "RBAC allocation.*) and complimentary issuances (tickets_shims.go "
        "handleCreate/List/GetComplimentaryIssuance at /organizations/{org_id}/complimentary). "
        "Neither the shims nor the hinventory/htickets sub-packages perform a "
        "membership check, so a permission-holder in Org A can read/write Org B's "
        "allocations and complimentary tickets by supplying B's UUID.",
        [
            "Add enforceOrgMembership to every external-allocations and complimentary handler that takes an {org_id} path param.",
            "Decide and implement the check for /complimentary/{id}/revoke (which has no org_id param — resolve the issuance's org and verify membership).",
            "Add a positive admission test (real member admitted) and a cross-tenant denial test (403 org.access_denied) for both surfaces, invoking the real router.",
        ],
        [382],
    ),
    feature(
        2, "PR2-32", "MAJOR",
        "Close remaining Bil24 auth gaps (SCAN_TICKET + mounted-without-auth config)",
        "sonnet",
        "PR2-25 added token enforcement to RESERVATION and UN_RESERVE, but "
        "SCAN_TICKET (bil24_compat.go ~781) genuinely mutates state via "
        "MarkBarcodeScanned(~854) with NO requireToken guard, so if the gateway is "
        "enabled an unauthenticated caller can mark barcodes scanned. Also the "
        "config combination BIL24_COMPAT_ENABLED=true + BIL24_REQUIRE_TOKEN=false "
        "is env-reachable and mounts the gateway with zero authentication — only a "
        "doc-comment discourages it. (Lower urgency because the gateway defaults "
        "OFF in production per PR2-25B, but live if an operator enables it.)",
        [
            "Add credential validation to SCAN_TICKET (and audit every other state-mutating Bil24 command) before it mutates state.",
            "In validateProduction, fail fast when BIL24_COMPAT_ENABLED=true and BIL24_REQUIRE_TOKEN=false (never mount the gateway unauthenticated in production).",
            "Add a test that SCAN_TICKET is rejected without a valid token and that the unsafe config combination is refused at startup.",
        ],
        [381, 385],
    ),
]


def install():
    connection = sqlite3.connect(DB, timeout=30)
    connection.execute("PRAGMA busy_timeout=30000")
    try:
        expected = [(i["id"], i["name"]) for i in FEATURES]
        existing = connection.execute(
            "SELECT id, name FROM features WHERE id >= ? ORDER BY id", (START_ID,)
        ).fetchall()
        if existing:
            if existing == expected:
                print("PR2 follow-up 2 features already installed.")
                return
            raise RuntimeError(f"Conflicting features at/above {START_ID}: {existing}")
        head = connection.execute("SELECT MAX(id), MAX(priority) FROM features").fetchone()
        if head != EXPECTED_HEAD:
            raise RuntimeError(f"Unexpected head {head}; expected {EXPECTED_HEAD}. Refusing insert.")

        backup = ROOT / ".autoforge" / "backups" / (
            "arena_new_features_before_pr2_followup2_"
            + datetime.now().strftime("%Y%m%d_%H%M%S") + ".db"
        )
        backup.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(backup) as b:
            connection.backup(b)

        connection.execute("BEGIN IMMEDIATE")
        for i in FEATURES:
            connection.execute(
                """
                INSERT INTO features (
                    id, priority, category, name, description, steps,
                    passes, in_progress, dependencies, needs_human_input,
                    human_input_request, human_input_response
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL)
                """,
                (
                    i["id"], i["priority"], i["category"], i["name"], i["description"],
                    json.dumps(i["steps"], ensure_ascii=False),
                    json.dumps(i["dependencies"]) if i["dependencies"] else None,
                ),
            )
        connection.commit()
        print(f"Installed {len(FEATURES)} follow-up-2 features (ids {START_ID}-{START_ID+len(FEATURES)-1}); backup: {backup}")
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
