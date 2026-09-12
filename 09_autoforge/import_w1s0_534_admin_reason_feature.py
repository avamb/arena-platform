"""W1-S0 follow-up (AutoForge) - admin-web must send X-Admin-Reason on
cross-tenant org-scoped READS and auto-retry once on superadmin.missing_reason.
Feature #534, priority 1091. Design authority:
08_architecture/21_superadmin_org_access_parity_ru.md section 6.
Run:  python 09_autoforge/import_w1s0_534_admin_reason_feature.py
"""
from __future__ import annotations
import json, shutil, sqlite3
from datetime import datetime
from pathlib import Path
ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (533, 1090)
SPEC = "08_architecture/21_superadmin_org_access_parity_ru.md"
DESC = (
    "Spec section 6 of " + SPEC + " (read it whole, it is short). Live on the stand after #531: "
    "the backend answers 200 to GET /v1/organizations/{org}/channels and .../api-keys for a "
    "platform superadmin ONLY when X-Admin-Reason is present, else 400 superadmin.missing_reason. "
    "The SPA (apps/admin-web/src/lib/api/reason.ts) attaches the header on org-scoped paths only "
    "for mutation methods (REASON_REQUIRED_MUTATION_REGEX), so the organization drawer tabs "
    "(Channels, Venues, Payments, Users) and the /channels?org=... page show "
    "'superadmin.missing_reason ... Retry' and the Retry button re-issues the request without the "
    "header. Fix: (1) apps/admin-web/src/lib/api/client.ts: on a 400 with code "
    "superadmin.missing_reason, retry ONCE automatically with the stored reason "
    "(sessionStorage arena.admin.adminReason, via the existing adminReason override) for any path "
    "and method; if no stored reason, keep the existing prompt flow. (2) reason.ts: make the "
    "org-scoped patterns require the header on ALL methods (move channels, venues, payment-configs, "
    "members, bank-accounts to REASON_REQUIRED_REGEX and add events, sessions, customers, orders, "
    "imports under /v1/organizations/{id}/...) - the header is harmless for an org member and "
    "mandatory for a superadmin. (3) Every Retry button in error states passes adminReason "
    "explicitly. (4) Tests: extend reason.test.ts (GET on org-scoped paths requires the reason), "
    "add a client test for the auto-retry (first response 400 missing_reason -> second request "
    "carries X-Admin-Reason -> 200, and no infinite loop when the second also fails), keep the "
    "whole admin vitest suite green: npm --prefix apps/admin-web run test -- --run, and "
    "npm --prefix apps/admin-web run type-check, plus a production build "
    "(npm --prefix apps/admin-web run build) must succeed. No backend changes. NEVER git add . / -A; "
    "stage by path; commit AND push; progress note with commands and exit codes."
)
def main():
    con = sqlite3.connect(DB)
    head = con.execute("SELECT id, priority FROM features ORDER BY id DESC LIMIT 1").fetchone()
    if tuple(head) != EXPECTED_HEAD:
        raise SystemExit(f"queue head is {head}, expected {EXPECTED_HEAD}")
    (ROOT / ".autoforge" / "backups").mkdir(parents=True, exist_ok=True)
    shutil.copy2(DB, ROOT / ".autoforge" / "backups" / f"arena_new_features_before_534_{datetime.now():%Y%m%d_%H%M%S}.db")
    con.execute(
        "INSERT INTO features (id, priority, category, name, description, steps, passes, in_progress, "
        "dependencies, needs_human_input, human_input_request, human_input_response, complexity) "
        "VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL, ?)",
        (534, 1091, "WP Bil24 Compat W1-S0",
         "W1-S0c: admin-web sends X-Admin-Reason on cross-tenant org-scoped reads and auto-retries once on superadmin.missing_reason",
         DESC,
         json.dumps(["client.ts auto-retry once on superadmin.missing_reason with stored reason.",
                     "reason.ts org-scoped patterns on all methods; Retry buttons pass adminReason.",
                     "reason.test.ts + client retry test; vitest, type-check, build green."]),
         json.dumps([]), 2))
    con.commit()
    print(con.execute("SELECT id, priority, name FROM features WHERE id=534").fetchone())
if __name__ == "__main__":
    main()
