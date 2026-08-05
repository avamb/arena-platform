"""Install AB-47b/AB-47c (session media gallery) into the AutoForge queue.

Owner decision 2026-08-04 (pass-2 review): Bil24's rigid poster format is the
defect; organizers need up to ~5 posters per session plus optional video
links. Full specs: 09_autoforge/admin_bootstrap_backlog.md, sections AB-47b
and AB-47c. The pass-2 columns (sessions/events.poster_media_id) remain as
the cover and are NOT to be reworked.

Both features depend on gate #433 (interactive pass 3 landed), so they run
with pass 4 (after AB-39/AB-40D by priority) and cannot start earlier even if
imported now. Idempotent, guarded and backed up like the other import
scripts - refuses on an unexpected queue head.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (434, 991)
START_ID = 435
START_PRIORITY = 543  # after AB-39 (539) / AB-40D (540) within pass 4

BACKLOG = "09_autoforge/admin_bootstrap_backlog.md"
GATE_PASS3 = 433

COMMON = (
    f"Full context: {BACKLOG} (sections AB-47b / AB-47c, after AB-47). Read "
    "them before starting. The pass-2 cover columns "
    "(sessions.poster_media_id / events.poster_media_id, resolution "
    "session ?? event) are CORRECT and stay - the gallery is additive. "
    "Do not mark passing from unit tests alone when the change requires "
    "PostgreSQL, browser, or container integration. Never weaken or delete "
    "an acceptance test to obtain green. Update openapi.yaml and regenerate "
    "the Go types and TS client in the same commit as any API change. "
    "Migration-head pin in tests must be bumped with any new migration."
)

FEATURES = [
    {
        "id": START_ID,
        "priority": START_PRIORITY,
        "category": "Reconstruction Wave 4",
        "name": "AB-47b [MAJOR]: Session media gallery - up to 5 posters + video links",
        "description": (
            "Severity: MAJOR. Owner decision 2026-08-04: organizers need a "
            "per-session gallery (posters carry the date/venue), not Bil24's "
            "rigid single-format slot. " + COMMON
        ),
        "steps": [
            "Migration: session_media_items(id uuid PK, session_id uuid NOT NULL FK sessions ON DELETE CASCADE, kind text CHECK (kind IN ('poster','video')), media_id uuid NULL FK media_objects, video_url text NULL, position smallint NOT NULL, UNIQUE(session_id, position), CHECK ((kind='poster' AND media_id IS NOT NULL AND video_url IS NULL) OR (kind='video' AND video_url IS NOT NULL AND media_id IS NULL))).",
            "Poster cap (5 per session) is a HANDLER constant, not a DB CHECK - raising it must not need a migration. Gallery posters reuse owner_type='session_poster'; do NOT widen the media_objects.owner_type CHECK.",
            "Video entries: URL only (https, host allowlist YouTube/VK/RuTube/Vimeo), validated in the handler. No hosting, no transcoding.",
            "No rigid format enforcement: store natural width/height in media metadata if not already there; admin may WARN on extreme aspect ratios, never block.",
            "API: GET/PUT /v1/sessions/{id}/media - PUT replaces the whole ordered list atomically (no separate reorder endpoints). Document in openapi.yaml with block-style error responses; regenerate Go + TS clients.",
            "Admin UI: gallery grid on the session (drag order -> position), poster upload via existing media flow, video-URL input. The event-level default and 'use for all sessions' checkbox keep applying to the COVER only.",
            "Tests: handler CRUD + validation (cap, URL allowlist, position uniqueness), plus an integration-tagged DB round-trip.",
        ],
        "complexity": 2,
        "dependencies": [GATE_PASS3],
    },
    {
        "id": START_ID + 1,
        "priority": START_PRIORITY + 1,
        "category": "Reconstruction Wave 4",
        "name": "AB-47c [MAJOR]: Poster cover + gallery on public surfaces (feed, widget, WP contract)",
        "description": (
            "Severity: MAJOR. Carries the AB-47 step-4 gap found in the pass-2 "
            "review: nothing outside the admin resolves posters today. "
            + COMMON
        ),
        "steps": [
            "Public feed and widget payloads expose the cover (session.poster_media_id ?? event.poster_media_id) AND the ordered gallery (posters + video entries) per session.",
            "Ticket PDF/email: cover only - no gallery.",
            "Update 08_architecture/02_wordpress_integration_contract_ru.md: poster URLs move from the event to session-level cover + gallery.",
            "Widget rendering of the gallery may be minimal (cover image + media list in the data layer) - the hybrid-hall map work is AB-40D, do not entangle them.",
            "Cover with widget e2e (mock suite at minimum). AGENTS.md: the Playwright dev server REQUIRES VITE_API_BASE_URL in the config env.",
        ],
        "complexity": 2,
        "dependencies": [START_ID],
    },
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
                print("AB-47b/c features already installed.")
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
            "arena_new_features_before_ab47bc_"
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
                    human_input_request, human_input_response, complexity
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL, ?)
                """,
                (
                    item["id"],
                    item["priority"],
                    item["category"],
                    item["name"],
                    item["description"],
                    json.dumps(item["steps"], ensure_ascii=False),
                    json.dumps(item["dependencies"]),
                    item["complexity"],
                ),
            )
        connection.commit()
        print(
            f"Installed AB-47b/c as features {START_ID}/{START_ID + 1} "
            f"(gated on #{GATE_PASS3}); backup: {backup}"
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
