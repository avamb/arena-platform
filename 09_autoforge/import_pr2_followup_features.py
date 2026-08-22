"""Install the PR2 follow-up wave into features.db.

Source: the 2026-07-18 adversarial verification of AutoForge's PR2 wave. Most
PR2 fixes verified REAL, but verification found residual defects the wave did
NOT actually close. Idempotent; refuses to renumber an unexpected queue.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (380, 462)
START_ID = 381
START_PRIORITY = 463


def feature(offset, code, severity, title, model, objective, steps, dependencies=None):
    return {
        "id": START_ID + offset,
        "priority": START_PRIORITY + offset,
        "category": "Production Hardening PR2 Follow-up",
        "name": f"{code} [{severity}]: {title}",
        "description": (
            f"Model: {model}. Severity: {severity}. {objective} "
            "Source: 2026-07-18 adversarial verification of the PR2 wave. The "
            "prior fix was verified INCOMPLETE — close the residual described "
            "here. Prove it against the real production wiring, not source-grep "
            "or a test-only helper. Never weaken or skip an acceptance test to go green."
        ),
        "steps": steps,
        "dependencies": dependencies,
    }


FEATURES = [
    feature(
        0, "PR2-25", "BLOCKER",
        "Actually enforce Bil24 gateway credentials in production",
        "opus",
        "PR2-18 added a real bcrypt fid/token check (validateGatewayToken) but it "
        "is gated behind requireToken, which defaults to false and is NEVER enabled "
        "by any production wiring or env var (bil24_shims.go New() never calls "
        "WithRequireToken(true); the commit's claim that 'production sets it' is "
        "false). So RESERVATION still creates real seated/GA holds with no secret. "
        "Worse, UN_RESERVE (handleBil24UnReserve) releases holds and cancels "
        "reservations with NO credential check even when the flag is on.",
        [
            "Enable credential enforcement in production wiring (config-driven, default-on in production; fail fast if the gateway is mounted without a credential source).",
            "Add fid/token validation to handleBil24UnReserve and any other state-mutating command (CREATE_ORDER/CANCEL/RESERVATION/UN_RESERVE) before it mutates state.",
            "Alternatively, if Bil24 is not in first-release scope, feature-flag the entire gateway OFF in production and prove it is unmounted.",
            "Add a test that the PRODUCTION handler wiring (not a WithRequireToken(true) test helper) rejects an unauthenticated RESERVATION and UN_RESERVE.",
            "Remove the dead GetActiveRolesForUserInOrg-style misleading scaffolding/comments left by the prior fix if unused.",
        ],
        [374],
    ),
    feature(
        1, "PR2-26", "MAJOR",
        "Extend org-membership enforcement to the remaining org-scoped routes",
        "opus",
        "PR2-01 added requireOrgMembership to only 5 surfaces (bank accounts, "
        "payment configs, channels, venues, org update); the commit's 'ALL "
        "org-scoped routes' is an overclaim. Still unguarded by membership: "
        "DELETE /v1/organizations/{id} (destructive), GET org, events, sessions, "
        "tiers, promo-codes, inventory, feed-tokens, seating/seats, "
        "external-allocations, complimentary, and billing usage/invoices — the "
        "same cross-tenant class is live there. The guard is also fail-open "
        "(returns true when membershipQueries==nil) with no positive test proving "
        "a real member is admitted through the membership query.",
        [
            "Add requireOrgMembership to every remaining org-scoped route, prioritising destructive writes (DELETE org first).",
            "Make the guard fail-closed (deny) when the membership query dependency is missing, and add a startup check that it is wired.",
            "Add a positive integration test that a legitimate active member IS admitted through the real membership query (not the nil-skip path).",
            "Add cross-tenant denial tests for the newly-guarded routes.",
        ],
        [357],
    ),
    feature(
        2, "PR2-27", "MAJOR",
        "Close the PR2-04 held->sold convert-failure window",
        "opus",
        "PR2-04 wired convertReservationTx (SellReservationSeatsTx + ConfirmCapacity "
        "+ UpdateReservationState('converted')) into all completion paths, but it "
        "runs in its OWN transaction, separate from CompleteCheckoutSession, and "
        "every call site treats its failure as non-fatal (log + continue). So if "
        "the convert tx fails after completion commits, the reservation stays "
        "active/draft and the TTL worker can still release and resell the paid "
        "seats — the original BLOCKER window is narrowed, not closed.",
        [
            "Make the held->sold conversion part of the same durable unit as completion, or enqueue a durable retried job so a failed conversion cannot be silently dropped.",
            "On unrecoverable conversion failure, ensure the reservation cannot be selected by GetExpiredReservations (e.g. mark converted/non-expirable before releasing the completion).",
            "Add an integration test that injects a conversion failure and proves the paid seats are never resold by the TTL worker.",
        ],
        [360],
    ),
    feature(
        3, "PR2-28", "MAJOR",
        "Restore gutted assertions and strengthen weak acceptance tests",
        "sonnet",
        "Verification found test-integrity erosion: auth_session_118_test.go step3 "
        "(revocation check in refresh flow) was reduced to an assertion-free "
        "closure (func(_ *testing.T) with only recover()) that passes "
        "unconditionally, and several PR2 acceptance suites rely on source-grep "
        "assertions (findFileByName + strings.Contains on magic strings like "
        "\"feature #367\") that pass if the source merely contains a token, not "
        "if runtime behaviour is correct.",
        [
            "Restore a real behavioural assertion to auth_session_118 step3 (assert the refresh flow actually performs the revocation/DB check, failing on wrong status).",
            "Replace or supplement the highest-risk source-grep acceptance tests (PR2-03/04/07/10/11/12) with tests that invoke the real handler and assert behaviour.",
            "Confirm the CI integration gate (go test -tags=integration) actually runs the BLOCKER proofs (e.g. the PR2-04 resale test) and is a required dependency of publish.",
            "Audit the wave for any other assertion-free subtests and fix them.",
        ],
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
                print("PR2 follow-up features already installed.")
                return
            raise RuntimeError(f"Conflicting features at/above {START_ID}: {existing}")
        head = connection.execute("SELECT MAX(id), MAX(priority) FROM features").fetchone()
        if head != EXPECTED_HEAD:
            raise RuntimeError(f"Unexpected head {head}; expected {EXPECTED_HEAD}. Refusing insert.")

        backup = ROOT / ".autoforge" / "backups" / (
            "arena_new_features_before_pr2_followup_"
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
        print(f"Installed {len(FEATURES)} follow-up features (ids {START_ID}-{START_ID+len(FEATURES)-1}); backup: {backup}")
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
