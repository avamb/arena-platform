"""Mini-wave W1-E (AutoForge) - arena-native "event bundle": one idempotent
POST creates event+session+venue+tiers+poster from the WP site (and later bots).
Features #523-#527, priorities 1080-1084.

Design authority: 08_architecture/19_event_bundle_arena_native_spec_ru.md (short,
read whole). Owner decision 2026-09-11: transport option C, Lampyris first, staging only.
Complexity 2 routes to Sonnet 5, complexity 3 (executor only) to Opus 5.

Idempotent: refuses unless the queue head is EXPECTED_HEAD; backs up first.
Run:  python 09_autoforge/import_w1e_event_bundle_features.py
"""

from __future__ import annotations

import json
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (522, 1079)
CATEGORY = "WP Bil24 Compat W1-E"
START_ID = 523
START_PRIORITY = 1080

SPEC = "08_architecture/19_event_bundle_arena_native_spec_ru.md"

TAIL = (
    f" READ FIRST: {SPEC} (design authority, short - read it whole; section refs below are "
    "its sections) and 09_autoforge/W1_BRIEFING.md. Existing code you extend: "
    "httpserver/himports/{bil24_session.go,import_exec.go,seating.go}, wire structs in "
    "internal/adapters/bil24compat/import_wire.go, tests himports/bil24_session_517*_test.go. "
    "The legacy route /imports/bil24-session and its tests (#517/#518) must stay green "
    "UNCHANGED in behaviour. Gates for a sub-feature: go.exe build ./... && go.exe vet ./..., "
    "go.exe test on touched packages (no pipelines: > log 2>&1; echo EXIT:$?), gofmt -l on "
    "changed files, DB tests behind //go:build integration run once with "
    "DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable "
    "JWT_SIGNING_SECRET=x go.exe test -tags integration <pkg>. Touched openapi.yaml -> codegen "
    "(openapi30gen + oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml + "
    "node scripts/gen-ts-client.mjs, delete .compat30.gen.yaml), commit generated files, wire "
    "the route into buildDriftTestServer. NEVER git add . / -A - stage by path, check git "
    "status --short. Commit AND push. Never weaken or delete a test to get green; if the spec "
    "and the code disagree, follow the spec and say so in the progress note."
)

FEATURES = [
    {
        "id": 523,
        "name": "W1-E1a: migration 0099 session_external_refs + queries/gen + head pin",
        "description": (
            "Spec section 6. Add apps/backend/internal/migrations/sql/0099_session_external_refs.sql "
            "exactly as in section 6 (table session_external_refs(org_id, external_ref, session_id, "
            "created_at), PK (org_id, external_ref), unique index on session_id, CHECK length 1..200, "
            "Down = DROP TABLE, COMMENT ON TABLE explaining the purpose). Bump expectedHead in "
            "internal/migrations/migrations_head_test.go to 0099_session_external_refs.sql. Add "
            "canonical SQL in internal/adapters/postgres/queries/session_external_refs.sql and a "
            "hand-written gen wrapper internal/adapters/postgres/gen/session_external_refs.sql.go "
            "(style exemplar: bank_accounts.sql.go) with GetSessionByExternalRef(org_id, external_ref) "
            "-> (session_id, event_id), GetExternalRefBySession(session_id), InsertSessionExternalRef"
            "(org_id, external_ref, session_id). Integration test (//go:build integration) proving "
            "insert + both lookups + PK violation on a duplicate ref in the same org + the same ref "
            "allowed in another org. Prove the migration applies to the local Docker PG "
            "(cmd/arena-migrate) and note the result. Do NOT touch himports or the wire structs "
            "in this feature." + TAIL
        ),
        "steps": [
            "Migration 0099 + head pin bump + migrate against local Docker PG.",
            "queries/session_external_refs.sql + gen wrapper with 3 functions.",
            "Integration test for insert/lookups/uniqueness; go test on migrations + gen packages.",
        ],
        "complexity": 2,
        "dependencies": [],
    },
    {
        "id": 524,
        "name": "W1-E1b: wire fields source/externalRef/endTime/sellStartTime + per-source validation + error codes",
        "description": (
            "Spec sections 3, 3.1, 5. In internal/adapters/bil24compat/import_wire.go extend "
            "ImportSessionRequest with top-level `source` (string: bil24|arena) and `externalRef` "
            "(string, <=200 after trim), and ImportSessionActionEvent with `endTime` (HH:MM) and "
            "`sellStartTime` (RFC3339) - camelCase is allowed in this adapter package. Make id "
            "validation source-aware: keep ValidateExternalIDs unchanged for source=bil24 (all ids "
            "required and < 1e9); add ValidateArenaIDs for source=arena: every provided id "
            "(action.actionId, actionEvent.actionEventId, venue.venueId, categoryList[].categoryPriceId) "
            "is optional but if present must be >= 1e9, seat ids ignored. Add parse helpers "
            "ParseLocalEnd (endTime <= time means next day) and ParseSellStart (must be < sellEndTime "
            "when both present). In himports/bil24_session.go add the validation ladder for the new "
            "fields with the exact error codes of spec section 5: import.source_invalid, "
            "import.source_mismatch, import.external_ref_required, import.external_ref_invalid, "
            "import.arena_id_out_of_range, import.end_time_invalid, import.invalid_sell_start_time. "
            "Add warning codes import.venue_matched_by_name, import.tier_not_in_payload, "
            "import.field_ignored_for_source (and reuse existing WarnPosterSkipped, "
            "WarnSeatingNotImported). The handler must accept an explicit `forcedSource` parameter "
            "(or equivalent) so the legacy route can pin source=bil24 and reject source=arena with "
            "import.source_mismatch, while a missing `source` on the legacy route still means bil24. "
            "Do NOT implement the arena execution path yet: when source=arena passes validation, "
            "return 501 with code import.arena_source_not_implemented (feature #525 replaces this). "
            "Unit tests (no DB) for every ladder rung and for the parse helpers, in the style of "
            "bil24_session_517_test.go. Existing tests unchanged and green." + TAIL
        ),
        "steps": [
            "Wire struct fields + source-aware validation + parse helpers (bil24compat).",
            "Handler ladder with spec section 5 codes; legacy route pins source=bil24.",
            "Unit tests for ladder and helpers; go test bil24compat + himports.",
        ],
        "complexity": 2,
        "dependencies": [523],
    },
    {
        "id": 525,
        "name": "W1-E1c [MAJOR]: source=arena executor - matching rules, compat id minting, compat_ids/external_ref in response, poster dedup",
        "description": (
            "Spec sections 3.2, 3.3, 4, 9 (integration tests 1-5). Implement the source=arena branch "
            "inside the same transaction as executeImport in himports/import_exec.go (remove the 501 "
            "stub from #524). Matching order, exactly as section 3.2: (1) session by "
            "session_external_refs(org_id, externalRef) -> update path, event and venue taken from it; "
            "provided actionEventId that disagrees -> 409 import.external_ref_conflict; (2) session by "
            "provided actionEventId via compatids.Resolve(KindActionEvent) -> update path, write the "
            "external ref if absent, a different existing ref -> 409; (3) event: provided actionId -> "
            "existing (must belong to this org, else 404 import.compat_id_unknown), else the found "
            "session's event, else INSERT draft event - never match events by name; (4) venue: "
            "provided venueId -> existing, else active org venue by lower(btrim(name)) (warning "
            "import.venue_matched_by_name, do not update it), else create (timezone required -> 422 "
            "venue.timezone_required, geo resolution as today); (5) tiers: provided categoryPriceId -> "
            "existing tier of THIS session (other session -> 409 import.category_bound_elsewhere), else "
            "tier of this session by lower(btrim(name)), else create; tiers of the session absent from "
            "the payload are left untouched with warning import.tier_not_in_payload listing their "
            "compat ids; (6) then the shared path: upsertTiers/syncInventoryLedger/applyPublish; "
            "endTime -> sessions.end_at, sellStartTime -> ticket_tiers.sale_window_start. Any id that "
            "does not resolve to an object of this org -> 404 import.compat_id_unknown (no leak). "
            "After each created row (and for every existing row lacking a map entry) call "
            "compatids.Ensure (source='arena', ids >= 1e9). Response (both routes, both sources): add "
            "required `external_ref` (string|null) and `compat_ids` {action_id, action_event_id, "
            "venue_id, category_price_ids: int64 array aligned with the request categoryList order}; "
            "for source=bil24 this echoes the registered ids; tier_ids stays keyed by the decimal "
            "categoryPriceId (minted id for arena). Poster (section 3.3): keep sideLoadPoster outside "
            "the tx, but compute the checksum of the downloaded bytes and, if it equals the checksum "
            "of the event's current poster_media_id object, do not create a new media object and keep "
            "the link; else create and repoint. Integration tests (//go:build integration, himports) "
            "for scenarios 1-5 of section 9: create (created=true, all compat ids >= 1e9, map rows "
            "source='arena', external ref row, poster_media_id set via httptest server); exact repeat "
            "(created=false, same ids, no duplicate tiers, no second media object); edit by "
            "externalRef (price change + new tier, then a payload missing a tier -> warning, tier "
            "kept); 404 for a foreign-org id, 422 for id < 1e9, 409 for a ref bound to another "
            "session; venue matched by name. Use per-run randomized names/refs (shared dev-stand PG)."
            + TAIL
        ),
        "steps": [
            "Arena matching rules 1-5 inside the executeImport transaction; minting via compatids.Ensure.",
            "Response extension compat_ids/external_ref for both sources; endTime/sellStartTime applied.",
            "Poster checksum dedup in sideLoadPoster.",
            "Integration tests for section 9 scenarios 1-5; existing #517/#518 tests still green.",
        ],
        "complexity": 3,
        "dependencies": [524],
    },
    {
        "id": 526,
        "name": "W1-E1d: route /imports/event-bundle + legacy alias, OpenAPI + Go/TS codegen, fixture, docs",
        "description": (
            "Spec sections 2, 3, 4, 5, 7, 10. Mount POST /v1/organizations/{org_id}/imports/event-bundle "
            "(same handler, source required) and keep /imports/bil24-session as the alias that pins "
            "source=bil24 (permission import.bil24_session for both; API key or JWT member as today). "
            "openapi.yaml (3.1, no nullable:, every property with a description, block-style error "
            "responses): new path; new schema ImportEventBundleRequest = the bil24-session request plus "
            "source/externalRef/endTime/sellStartTime with the per-source rules in the descriptions; "
            "response schema gains required external_ref (type [string, \"null\"]) and compat_ids object "
            "(action_id, action_event_id, venue_id, category_price_ids int64 array) - the legacy "
            "response schema is the same object; add the section 5 error codes to the responses. "
            "Run codegen (Go types_gen.go + TS client), commit generated files, wire the route into "
            "buildDriftTestServer, make OpenAPI docs tests green. Add the contract fixture "
            "apps/backend/tests/compat/bil24/testdata/wp/event_bundle/arena_ga_lampyris.json with the "
            "exact body of spec section 3 (staging Lampyris values) and a small test that it decodes "
            "into ImportSessionRequest and passes validation. Docs: docs/ops/bil24_gateway.md new "
            "section 8 'Creating events from the site (event bundle)' with a curl example under an "
            "ak_ key and the compat_ids round-trip to GET_ALL_ACTIONS; update spec W1 "
            "08_architecture/18_bil24_compat_wave1_specification_ru.md section 13.4 so the no-seats "
            "branch points to the bundle instead of the multi-call chain (one paragraph + link to "
            "spec 19). If admin-web exposes the API-keys tab scope list, no change is needed "
            "(permission unchanged)." + TAIL
        ),
        "steps": [
            "Route mount + alias pinning; buildDriftTestServer.",
            "openapi.yaml path/schemas/errors; Go + TS codegen committed; docs tests green.",
            "Fixture arena_ga_lampyris.json + decode/validate test.",
            "docs/ops/bil24_gateway.md section 8; spec 18 section 13.4 update.",
        ],
        "complexity": 2,
        "dependencies": [525],
    },
    {
        "id": 527,
        "name": "W1-E [EPIC-VERIFY]: end-to-end bundle -> publish -> GET_ALL_ACTIONS, full gate with -count=1",
        "description": (
            "Spec section 9 scenarios 6-7 and the full gate. Add an integration test (//go:build "
            "integration; place it next to the compat harness in apps/backend/tests/compat/bil24 or in "
            "himports - whichever can boot the full httpserver with hbil24 mounted) that: seeds an org "
            "with a gateway channel (display_number = fid, bcrypt token, gateway enabled), POSTs the "
            "arena_ga_lampyris.json fixture (randomized externalRef and names per run) to "
            "/imports/event-bundle under an org API key with scope import.bil24_session and "
            "publish:true, then calls the gateway command GET_ALL_ACTIONS with that fid and asserts the "
            "action is present with actionId/actionEventId/venueId/categoryPriceId equal to the "
            "response compat_ids, day/time rendered in the venue timezone, categories with the fixture "
            "prices, and bigPosterUrl pointing at /v1/media-files/. Also assert the legacy route "
            "rejects source=arena with 422 import.source_mismatch and still accepts a bil24 body. Then "
            "run the FULL gate with a cold cache: go.exe test -count=1 ./... (no pipelines), "
            "integration suite for himports + tests/compat/bil24 + migrations with -count=1, "
            "golangci-lint @latest with absolute cache path, gofmt on all files changed in #523-#526, "
            "codegen drift check (regenerate and git diff must be empty), npm run type-check and "
            "npm run admin:test. Write a progress note listing every gate with its exact command and "
            "exit code; if anything is red, fix it in this feature, do not mark passing." + TAIL
        ),
        "steps": [
            "E2E integration test: bundle -> publish -> GET_ALL_ACTIONS ids/dates/poster; legacy alias checks.",
            "Full cold-cache gate: unit, integration, lint, codegen drift, admin type-check/tests.",
            "Progress note with commands and exit codes.",
        ],
        "complexity": 2,
        "dependencies": [526],
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
    shutil.copy2(DB, backup_dir / f"arena_new_features_before_w1e_bundle_{stamp}.db")

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
