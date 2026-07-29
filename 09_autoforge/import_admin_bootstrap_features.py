"""Install the Admin Bootstrap (AB) wave into features.db.

Source: 09_autoforge/admin_bootstrap_backlog.md — findings from the first
production-like deploy walkthrough by the product owner on 2026-07-29.
AB-2/13/14/15/17 were implemented in-session (commit ce1fc7e) and are NOT
imported. Idempotent: refuses to overwrite or renumber an unexpected queue,
exactly like import_pr2_hardening_features.py.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (391, 480)
START_ID = 392
START_PRIORITY = 481

BACKLOG = "09_autoforge/admin_bootstrap_backlog.md"


def feature(
    offset: int,
    code: str,
    severity: str,
    title: str,
    objective: str,
    steps: list[str],
    dependencies: list[int] | None = None,
) -> dict[str, object]:
    return {
        "id": START_ID + offset,
        "priority": START_PRIORITY + offset,
        "category": "Admin Bootstrap AB",
        "name": f"{code} [{severity}]: {title}",
        "description": (
            f"Severity: {severity}. {objective} "
            f"Full context: {BACKLOG} (section {code}). Source: 2026-07-29 "
            "owner walkthrough of the live staging deploy. Do not mark passing "
            "from unit tests alone when the fix requires PostgreSQL, browser, "
            "or container integration. Never weaken or delete an acceptance "
            "test to obtain green."
        ),
        "steps": steps,
        "dependencies": dependencies,
    }


FEATURES = [
    feature(
        0,
        "AB-18",
        "CRITICAL",
        "Legal-entity fields: implement the missing backend (silent data loss today)",
        "The org detail Legal & billing form PATCHes /v1/organizations/{id}; the "
        "server returns 200 and silently drops legal_name, tax_id(+scheme), "
        "registration_number, legal_address_*, contact_email, contact_phone, "
        "website_url, kyb_status. Columns exist since migration 0049; no handler "
        "reads or writes them.",
        [
            "Extend PATCH /v1/organizations/{id} to persist every 0049 legal field via sqlc; validate kyb transitions (pending/verified require legal_name), alpha-2 country, tax_id per scheme.",
            "Return the legal fields in GET /v1/admin/organizations and the single-org read used by the admin form.",
            "Update openapi.yaml + regenerate Go/TS types with zero drift.",
            "Integration test: PATCH all fields -> GET roundtrip returns them; kyb transition rules covered.",
            "Guardrail: unknown body fields are rejected (or logged loudly) so a 200-that-drops-fields cannot recur.",
        ],
    ),
    feature(
        1,
        "AB-3",
        "MAJOR",
        "SuperAdmin user directory: GET /v1/admin/users + list UI",
        "The /users page is create-only; there is no user list endpoint at all, so "
        "a superadmin cannot see any user, including themself.",
        [
            "API: GET /v1/admin/users with pagination and email-substring search returning id, email, created_at, email_verified_at, global roles, org memberships (superadmin.read).",
            "UI: table on /users (ResponsiveTable) with search box; row opens a detail drawer showing roles and memberships.",
            "Keep the existing create form; browser/vitest coverage for list+search states.",
        ],
    ),
    feature(
        2,
        "AB-4",
        "MAJOR",
        "Role management for existing users (add/remove roles and memberships)",
        "Roles can only be assigned at user creation; no API/UI to change an "
        "existing user's roles, org memberships, or deactivate a user.",
        [
            "API: audit-logged endpoints to grant/revoke global roles and org memberships (membership.grant / membership.revoke, X-Admin-Reason).",
            "UI: on the AB-3 user detail drawer — add/remove role with org picker and confirmation.",
            "Lockout guard: refuse removing the last active platform_superadmin; test it.",
        ],
        dependencies=[393],
    ),
    feature(
        3,
        "AB-12",
        "MAJOR",
        "Superadmin access to org-scoped resources without hand-inserted memberships",
        "orgauth.go enforces active membership with no superadmin bypass: bank "
        "accounts and every org-scoped surface 403 for platform_superadmin "
        "(org.access_denied). Bootstrap required inserting a membership row by hand.",
        [
            "Decide and implement: superadmin.read bypasses actorIsMemberOfOrg with mandatory X-Admin-Reason audit, or an explicit time-boxed act-as-org grant.",
            "Tests: member, non-member, superadmin, suspended membership paths.",
            "Remove the need for the hand-inserted membership row; document in deploy runbook.",
            "Document or unify the memberships.role CHECK list vs RBAC roles split (org_admin is not a valid membership role).",
        ],
    ),
    feature(
        4,
        "AB-5",
        "NORMAL",
        "Organization create/edit: country and locale pickers instead of raw ISO inputs",
        "Country is a raw alpha-2 text input (owner typed EST and dead-ended); "
        "locale is raw BCP-47. The geo registry exists but is not wired in.",
        [
            "Searchable country select fed from the geo registry (name + code), storing alpha-2; same control on the legal-address country field.",
            "Locale select with curated list (en/ru/et/uk/...) plus free-entry escape hatch.",
            "Vitest coverage; keep raw-input fallback when geo registry is empty.",
        ],
    ),
    feature(
        5,
        "AB-22",
        "MAJOR",
        "Human-readable identifiers platform-wide (name-first pickers + short display numbers)",
        "Internal UUIDv7 PKs leak into every operator surface; legacy Bil24 shows "
        "short ids + names ([10549] Palac Akropolis) and operators expect that. "
        "UUIDs stay the PK — this is a presentation layer.",
        [
            "UI rule: no form asks for a raw id — name-first pickers/typeahead everywhere (generalize the /users org picker to venue org/city, event->org binding, member tables show email/name).",
            "Migration: per-entity short display number (org, venue, event, channel, user) surfaced as 'Name · #N' in pickers and tables; UUID unchanged as PK/API id.",
            "Slugs in admin URLs for org/venue routes; UUID only in a collapsed developer-info block with copy button.",
            "Update every existing admin surface that currently prints a bare UUID.",
        ],
    ),
    feature(
        6,
        "AB-19",
        "NORMAL",
        "Org detail tabs: embedded scoped lists with create actions (not read-only shells)",
        "Organization card tabs (Users/Venues/Channels/Payments) render raw GET "
        "output with no create/edit; real CRUD hides on global sidebar pages.",
        [
            "Each org tab embeds the org-scoped list plus a primary create action prefilled with the org (or a deep link to the global page with ?org=<id> preselect).",
            "Users tab links to /users?org=<id> (pairs with the AB-17 picker preselect).",
            "Vitest coverage for tab render + deep-link parameters.",
        ],
    ),
    feature(
        7,
        "AB-20",
        "MAJOR",
        "Venue creation: geocoding-first flow (address -> name/city/coords)",
        "New-venue form demands raw UUIDs for org and city; owner expects the "
        "Bil24-style flow: paste an address, verify on a map, fields fill "
        "themselves.",
        [
            "Org picker + city picker fed from geo registry with inline create-city.",
            "Address autocomplete via a geocoding provider (default Nominatim; abstraction allows Google Places later) filling address lines, postal code, city, country, lat/long; map pin preview with manual adjustment; persist coordinates on the venue.",
            "Manual entry remains fully possible without the geocoder.",
            "Browser test: picking a suggestion fills the form; venue created with coordinates.",
        ],
    ),
    feature(
        8,
        "AB-8",
        "NORMAL",
        "SPA refreshes permissions without relogin",
        "Permissions load once at app boot; after a grant the operator sees stale "
        "gating until a hard reload.",
        [
            "Refetch /v1/me on window focus and after any 403 mutation response; show a 'permissions updated' toast instead of a dead-end.",
            "Vitest coverage for both triggers.",
        ],
    ),
    feature(
        9,
        "AB-7",
        "NORMAL",
        "Organizations list: server-side search and pagination",
        "/v1/admin/organizations returns every org in one response; UI filters "
        "locally and says so.",
        [
            "Add limit/offset (or cursor) + q= name/slug filter to the list endpoint; OpenAPI + types regenerated.",
            "Wire the UI filter input to the server query with debounce; keep local fallback.",
            "Integration test for filter + pagination.",
        ],
    ),
    feature(
        10,
        "AB-6",
        "MAJOR",
        "Mobile-friendly admin pass (owner operates from phone)",
        "Layout primitives exist (ResponsiveTable/Drawer) but no surface was "
        "audited for small screens.",
        [
            "Audit every registered route at 375x812: no horizontal page scroll, tap targets >=44px, dialogs fit viewport with internal scroll.",
            "Sidebar collapses to a drawer under 768px; scope/audit-reason bar wraps.",
            "Playwright viewport smoke in CI for /organizations, /users, /networks at mobile width.",
        ],
    ),
    feature(
        11,
        "AB-9",
        "NORMAL",
        "Ticket emails: Reply-To organizer contact email",
        "All outgoing mail uses the global SMTP_FROM; buyer replies should reach "
        "the organizer. OrgContactEmail is already in the delivery payload.",
        [
            "Set Reply-To to the org contact_email on ticket delivery emails when present; absent otherwise.",
            "Unit test on the delivery handler asserting the header.",
        ],
    ),
    feature(
        12,
        "AB-16",
        "MAJOR",
        "Invite-by-email flow for org members",
        "Add member requires an existing user; operators must hop to /users first. "
        "Natural flow is an email invitation (temp-password fallback until SMTP).",
        [
            "API: POST invite (email, org, role) creating an invited user + password-set link email; temp-password fallback while EMAIL_MODE=log.",
            "UI: Add member accepts any email and offers Invite when the user is absent; /users form gains the same path.",
            "Invitation token TTL + resend; audit-logged; integration test.",
        ],
        dependencies=[393],
    ),
    feature(
        13,
        "AB-21",
        "NORMAL",
        "Bil24 live import: venues and cities via the Bil24 API",
        "arena-bil24-import handles event snapshots only; owner has venues in Bil24 "
        "(e.g. [10549] Palac Akropolis) and wants them imported, not retyped. "
        "Official API docs: 01_official_bil24_docs/.",
        [
            "Extend arena-bil24-import (venues mode): auth against the Bil24 API, fetch countries/cities/venues.",
            "Map Bil24 geo to the geo registry (create-if-absent); venues stored with an external-id mapping column, address and coordinates preserved; idempotent re-runs.",
            "CLI contract mirrors the snapshot importer (dry-run, summary, exit codes); tests on recorded fixtures.",
        ],
    ),
    feature(
        14,
        "AB-1",
        "NORMAL",
        "RBAC seed audit beyond superadmin (org_admin/organizer/agent gaps)",
        "Migration 0071 fixed platform_superadmin. Remaining roles need the same "
        "audit so a fresh tenant can operate without hand grants.",
        [
            "Audit venue.*, channel.*, event.*, payment_config.write, media.* grants for org_admin/organizer/agent against what their UI surfaces require.",
            "Migration adding the missing grants; integration test provisioning a fresh org_admin who exercises each surface.",
            "CI check or migration-template note: every new permission seed must state which roles receive it, including platform_superadmin.",
        ],
    ),
    feature(
        15,
        "AB-10",
        "MAJOR",
        "Per-organizer sender identity (Brevo domain verification)",
        "Organizers with their own domain want tickets sent From their address; "
        "never collect organizer SMTP credentials.",
        [
            "Org fields: sender_email + verification status; superadmin UI shows required DNS records fetched from the Brevo API.",
            "Worker uses org sender_email as From when verified, else global SMTP_FROM with AB-9 Reply-To fallback.",
            "Verification poller job; tests with mocked Brevo API.",
        ],
        dependencies=[403],
    ),
    feature(
        16,
        "AB-11",
        "NORMAL",
        "Production-mode flip readiness (Brevo SMTP + Postgres TLS decision)",
        "Deploy runs APP_ENV=staging because production validation requires "
        "EMAIL_MODE=smtp and sslmode=require. BLOCKED on operator input: Brevo "
        "SMTP credentials from the owner.",
        [
            "Wire SMTP_* env into the worker deploy docs and compose template; flip EMAIL_MODE=smtp once credentials exist.",
            "Decide the in-network Postgres TLS story (enable ssl in postgres:17 or an explicit private-network override flag) and implement; then APP_ENV=production.",
            "Document the flip procedure in deploy/DOKPLOY.md and verify PR-00 validation passes in a container smoke.",
        ],
    ),
]


def install() -> None:
    connection = sqlite3.connect(DB)
    try:
        expected = [(item["id"], item["name"]) for item in FEATURES]
        existing = connection.execute(
            "SELECT id, name FROM features WHERE id >= ? ORDER BY id",
            (START_ID,),
        ).fetchall()
        if existing:
            if existing == expected:
                print("Admin Bootstrap features already installed.")
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
            "arena_new_features_before_ab_wave_"
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
        print(
            f"Installed {len(FEATURES)} AB features (ids {START_ID}-{START_ID + len(FEATURES) - 1}); backup: {backup}"
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
