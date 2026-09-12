"""Mini-wave W1-S0 (AutoForge) - platform superadmin parity on org-scoped
surfaces: membership bypass must not depend on the JWT roles claim, and the
superadmin role must hold every permission. Features #531-#533,
priorities 1088-1090.

Design authority: 08_architecture/21_superadmin_org_access_parity_ru.md.
Found live on the staging stand 2026-09-12 (owner logged in as superadmin).

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_w1s0_superadmin_parity_features.py
"""

from __future__ import annotations

import json
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (530, 1087)
CATEGORY = "WP Bil24 Compat W1-S0"
START_ID = 531
START_PRIORITY = 1088

SPEC = "08_architecture/21_superadmin_org_access_parity_ru.md"

TAIL = (
    f" READ FIRST: {SPEC} (design authority, short - read it whole) and "
    "09_autoforge/W1_BRIEFING.md. Key code: httpserver/mount_v1.go (applyAuth, "
    "markSuperadminOrgAccess), internal/platform/auth/auth.go (WithSuperadminOrgAccess), "
    "internal/platform/permissions/rbac_checker.go (DBChecker.Check and its "
    "GetActiveRolesForUser fallback), hcatalog/orgauth.go (requireOrgMembership), "
    "hauth/login.go (IssueJWT with nil roles - do NOT change token issuance in this wave), "
    "migrations 0034_superadmin.sql, 0071_superadmin_all_permissions.sql, 0095, 0097, "
    "docs/ops/superadmin_org_access.md. Gates for a sub-feature: go.exe build ./... && "
    "go.exe vet ./..., go.exe test on touched packages (no pipelines: > log 2>&1; echo EXIT:$?), "
    "gofmt -l on changed files, DB tests behind //go:build integration run once with "
    "DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable "
    "JWT_SIGNING_SECRET=x go.exe test -tags integration <pkg>. A new migration bumps "
    "expectedHead in internal/migrations/migrations_head_test.go and must be proven against "
    "the local Docker PG with cmd/arena-migrate. NEVER git add . / -A - stage by path, check "
    "git status --short. Commit AND push. Never weaken or delete a test; write a progress note "
    "listing every gate command with its exit code."
)

FEATURES = [
    {
        "id": 531,
        "name": "W1-S0a: superadmin org-access bypass resolves roles server-side, not from the JWT roles claim",
        "description": (
            "Spec sections 2 and 3.1. Today httpserver/mount_v1.go markSuperadminOrgAccess requires "
            "hasRole(actor.Roles, \"platform_superadmin\") but login/refresh-issued JWTs carry no "
            "roles claim (verified live: payload has only sub and exp), so a real superadmin gets "
            "403 org.access_denied on every org-scoped page (channels, api-keys, customers, orders) "
            "of an organization they are not a member of. Fix: when actor.Type is user and "
            "actor.Roles is empty, resolve roles through the same memberships source the "
            "permissions.DBChecker uses (GetActiveRolesForUser, reuse its cache/TTL - expose a small "
            "helper on the checker or share the interface; do not add a second uncached DB round "
            "trip per request), then apply the existing hasRole + perms.Check(superadmin.read) + "
            "X-Admin-Reason contract unchanged. Service actors (API keys) keep no bypass. Do NOT "
            "put roles into the JWT in this feature. Tests: unit test for the middleware with an "
            "actor whose claims carry no roles but whose DB roles include platform_superadmin "
            "(bypass marker set) and with a plain user (not set); integration test "
            "(//go:build integration, full httpserver via the usual test server builder) doing a "
            "REAL POST /v1/auth/login as a seeded platform_superadmin user, then GET "
            "/v1/organizations/{foreign org}/channels: with X-Admin-Reason -> 200, without -> 400 "
            "superadmin.missing_reason, and a non-member ordinary user -> 403 org.access_denied; "
            "assert the superadmin.organization_access audit row. Update "
            "docs/ops/superadmin_org_access.md with one paragraph on where roles come from." + TAIL
        ),
        "steps": [
            "Server-side role resolution in markSuperadminOrgAccess (shared cached source).",
            "Unit tests for the middleware; integration test through real login.",
            "docs/ops/superadmin_org_access.md paragraph.",
        ],
        "complexity": 2,
        "dependencies": [],
    },
    {
        "id": 532,
        "name": "W1-S0b: migration 0100 superadmin permission parity + guardrails + AGENTS.md rule",
        "description": (
            "Spec sections 2 and 3.2. Migration 0071 granted platform_superadmin all 122 permissions "
            "of its time; migrations 0092-0098 seeded order.read, order.write, customer.read, "
            "customer.import, api_key.manage, import.bil24_session and granted them only to other "
            "roles, so the superadmin gets 403 permissions.denied on the API-keys tab and the "
            "customer/order surfaces (local DB after 0099: permissions=128, superadmin grants=122). "
            "Add apps/backend/internal/migrations/sql/0100_superadmin_permission_parity.sql: Up = "
            "INSERT INTO role_permissions SELECT platform_superadmin role, every permission ... ON "
            "CONFLICT DO NOTHING (idempotent for all current and re-run safe); Down = delete only the "
            "six permissions named above from platform_superadmin. Bump expectedHead to 0100 and "
            "prove the migration on the local Docker PG (arena-migrate). Guardrails: (1) integration "
            "test after migrations asserting count(permissions) == count(role_permissions of "
            "platform_superadmin); (2) a static test over the embedded migrations FS (pattern: "
            "mediastore.TestAllowedOwnerTypes_MatchMigrationCheckConstraint) that every migration "
            "numbered > 0100 which contains 'INSERT INTO permissions' also grants those names to "
            "platform_superadmin in the same file (search for the literal role name), so the drift "
            "cannot recur silently. (3) AGENTS.md gotcha: 'a new permission must be granted to "
            "platform_superadmin in the same migration, else TestSuperadminPermissionParity fails'. "
            "Also verify GET /v1/me for a superadmin now lists api_key.manage (extend an existing "
            "/v1/me integration test if there is one)." + TAIL
        ),
        "steps": [
            "Migration 0100 + head pin + local migrate proof.",
            "Integration parity test + static migrations-FS guardrail.",
            "AGENTS.md rule; /v1/me assertion.",
        ],
        "complexity": 2,
        "dependencies": [],
    },
    {
        "id": 533,
        "name": "W1-S0 [EPIC-VERIFY]: superadmin provisions a site org end-to-end, full cold gate",
        "description": (
            "Spec section 3.3. Integration test (//go:build integration, full httpserver with hbil24 "
            "and himports mounted, wpstub for the WP receiver) that, as a REAL-login platform "
            "superadmin with X-Admin-Reason and WITHOUT any memberships row: creates an organization "
            "(randomized slug), creates a sales channel in it, PUT gateway-credential (gets fid + "
            "token), PUT wp-webhook pointing at the wpstub, POST api-keys with scopes "
            "[import.bil24_session] (gets ak_ key), then with that key POST imports/event-bundle "
            "using testdata/wp/event_bundle/arena_ga_lampyris.json (randomized externalRef/names, "
            "publish:true), then GET_ALL_ACTIONS on /compat/bil24/json with the channel fid/token "
            "returns the action with the compat ids from the bundle response and prices 450/900. "
            "Assert every step's status code and that the wpstub received the event.created webhook. "
            "Then the FULL cold gate: go.exe test -count=1 ./... (no pipelines); integration -count=1 "
            "for httpserver, hauth, hcatalog, himports, tests/compat/bil24, migrations; golangci-lint "
            "@latest with absolute cache path; gofmt on all files changed in #531-#532; codegen drift "
            "(regenerate, git diff empty); npm run type-check and npm run admin:test. Progress note "
            "with every command and exit code; fix anything red here, do not mark passing otherwise."
            + TAIL
        ),
        "steps": [
            "E2E provisioning test as real-login superadmin without memberships.",
            "Full cold-cache gate.",
            "Progress note with commands and exit codes.",
        ],
        "complexity": 2,
        "dependencies": [531, 532],
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
    shutil.copy2(DB, backup_dir / f"arena_new_features_before_w1s0_superadmin_{stamp}.db")

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
