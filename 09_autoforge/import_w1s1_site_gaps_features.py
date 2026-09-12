"""Mini-wave W1-S1 (AutoForge) - gaps found live on the staging stand with the
Lampyris site flow: signed absolute poster URLs + API_PUBLIC_URL, auto-publish
to the API key's channel from the event bundle, city display names on import.
Features #535-#538, priorities 1092-1095.
Design authority: 08_architecture/22_site_facing_gaps_w1s1_ru.md.
Run:  python 09_autoforge/import_w1s1_site_gaps_features.py
"""

from __future__ import annotations

import json
import shutil
import sqlite3
from datetime import datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
EXPECTED_HEAD = (534, 1091)
CATEGORY = "WP Bil24 Compat W1-S1"
SPEC = "08_architecture/22_site_facing_gaps_w1s1_ru.md"

TAIL = (
    f" READ FIRST: {SPEC} (design authority, short - read it whole; section refs are its sections) "
    "and 09_autoforge/W1_BRIEFING.md. Gates for a sub-feature: go.exe build ./... && go.exe vet ./..., "
    "go.exe test on touched packages (no pipelines: > log 2>&1; echo EXIT:$?), gofmt -l on changed "
    "files, DB tests behind //go:build integration run once with "
    "DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable JWT_SIGNING_SECRET=x "
    "go.exe test -tags integration <pkg>. Touched openapi.yaml -> codegen (openapi30gen + "
    "oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml + "
    "node scripts/gen-ts-client.mjs, delete .compat30.gen.yaml) and commit generated files. NEVER git "
    "add . / -A - stage by path, check git status --short. Commit AND push. Never weaken or delete a "
    "test; write a progress note listing every gate command with its exit code."
)

FEATURES = [
    (
        535, 2, [],
        "W1-S1a: API_PUBLIC_URL + signed absolute poster URLs on the gateway wire + base_url/image_url/pdfUrl on the API host",
        "Spec section 1 items 1-2 and section 2.1. Add config.Config.APIPublicURL (env API_PUBLIC_URL) in "
        "internal/platform/config/config.go: empty -> falls back to APP_PUBLIC_URL; in production with "
        "BIL24_COMPAT_ENABLED=true it must be a non-empty https:// URL (extend validateProduction and its "
        "tests); document in .env.example, deploy/DOKPLOY.md (own table row) and docs/ops/bil24_gateway.md "
        "section 1. Route the gateway PublicBaseURL (bil24_tickets_shim.go, hcatalog/handler.go:137) to "
        "the API public URL. PUT gateway-credential must answer base_url = <API_PUBLIC_URL>/compat/bil24 "
        "and image_url = <API_PUBLIC_URL>/compat/bil24/image. Posters: hbil24/cmd_catalog.go posterURL() "
        "(and hfeed/public_feed.go mediaFileURL) currently emit a bare relative /v1/media-files/{uuid} that "
        "hmedia.DownloadMedia rejects with 401 (it requires expires+sig, see mediastore localSignedURL). "
        "Build an ABSOLUTE SIGNED URL via the mediastore signing mechanism with a 24h TTL on the API "
        "public URL (external storage backends: their own signed URL as-is); keep the legacy "
        "events.image_url fallback verbatim. Tests: unit for posterURL (absolute, carries expires and sig, "
        "base from config); integration: GET_ALL_ACTIONS bigPosterUrl fetched through the test httpserver "
        "returns 200 with the poster bytes; gateway-credential base_url ends with /compat/bil24; config "
        "fallback and production validation tests." + TAIL,
    ),
    (
        536, 3, [],
        "W1-S1b [MAJOR]: service actor carries ChannelID; event bundle with publish:true auto-publishes to the key channel so event.created reaches the site",
        "Spec section 1 items 3 and 5, section 2.2. Today applyPublish only flips events.status; the "
        "outbox v1.event.published is dispatched with no subscriber because ListWPSubscribersForEvent "
        "(gen/wp_webhook_subscribers.sql.go) needs event_publications -> agent_feed_tokens(active) -> "
        "webhook_subscribers(kind=bil24_wp) on the channel. (1) auth.Actor gains ChannelID *uuid.UUID for "
        "service actors; server_apikey_auth.go copies key.ChannelID. (2) In himports (both routes, both "
        "sources): when publish:true and the actor is a service actor with ChannelID, INSIDE the import "
        "transaction before commit: reuse an active non-revoked agent_feed_tokens row of that channel or "
        "create one (label auto:event-bundle), then PublishEvent (event_publications, existing ON "
        "CONFLICT semantics) with city_id of the session venue city; idempotent on repeat. Key without "
        "channel -> warning import.channel_publication_skipped. (3) Import response gains `publication` "
        "{channel_id, feed_token_id, publication_id} or null - update openapi.yaml + codegen. (4) "
        "Integration test on the full httpserver: channel + wp-webhook to wpstub + api key bound to the "
        "channel -> event-bundle publish:true -> REAL outbox.OutboxEventsDispatcher with "
        "bil24wire.Dispatcher -> wpstub receives event.created carrying compat_ids.action_event_id "
        "WITHOUT any manual publication; a key without channel_id -> warning and no event_publications "
        "row; a repeated bundle creates no second feed token or publication. (5) "
        "docs/ops/bil24_gateway.md section 8: the key must be bound to the site channel or no webhooks "
        "flow." + TAIL,
    ),
    (
        537, 2, [],
        "W1-S1c: city display-name translations when the import creates a city",
        "Spec section 1 item 4 and section 2.3. himports resolveGeography creates cities with slug only "
        "(slugify of Praha -> praha) and ListActionVenuesByOrg falls back to the slug, so GET_ALL_ACTIONS "
        "cityName is lowercase. When the import creates a city, also write the display name into "
        "i18n_text for the organization default_locale AND for en (when different), value = the incoming "
        "cityName with whitespace normalized and case preserved, using the same queries the hgeo "
        "city-create handler uses (no new tables); for an existing city with no translation, add it; "
        "never overwrite an existing translation. Applies to source=bil24 and source=arena. Tests: "
        "integration - bundle with cityName Praha -> GET_ALL_ACTIONS cityList[].cityName == Praha; "
        "repeat import does not duplicate translations; unit for the normalization." + TAIL,
    ),
    (
        538, 2, [535, 536, 537],
        "W1-S1 [EPIC-VERIFY]: end-to-end site flow on the stand model, full cold gate",
        "Spec section 2.4. Extend TestSuperadminOrgProvisioning533_Integration (or add a sibling) so "
        "that, as a real-login superadmin: provision org/channel/gateway-credential/wp-webhook(wpstub)/"
        "api key bound to the channel -> event-bundle publish:true with cityName Praha and a poster "
        "served by an httptest server -> the wpstub receives event.created without manual publication -> "
        "GET_ALL_ACTIONS shows cityName Praha, an absolute signed bigPosterUrl that GETs 200 through the "
        "test server, and gateway-credential base_url on the API public URL ending with /compat/bil24. "
        "Then the FULL cold gate: go.exe test -count=1 ./... (no pipelines); integration -count=1 -p 1 "
        "for httpserver, hauth, hcatalog, himports, hbil24, hfeed, tests/compat/bil24, migrations; "
        "golangci-lint @latest with absolute cache path; gofmt on all files changed in #535-#537; codegen "
        "drift (regenerate, git diff empty); npm run type-check and npm run admin:test. Progress note "
        "with every command and exit code; fix anything red here." + TAIL,
    ),
]


def main() -> None:
    con = sqlite3.connect(DB)
    head = con.execute("SELECT id, priority FROM features ORDER BY id DESC LIMIT 1").fetchone()
    if tuple(head) != EXPECTED_HEAD:
        raise SystemExit(f"queue head is {head}, expected {EXPECTED_HEAD}")
    (ROOT / ".autoforge" / "backups").mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    shutil.copy2(DB, ROOT / ".autoforge" / "backups" / f"arena_new_features_before_w1s1_{stamp}.db")
    steps = json.dumps(["Implement per spec section.", "Tests per spec.", "Gates + push + progress note."])
    for i, (fid, cx, deps, name, desc) in enumerate(FEATURES):
        con.execute(
            "INSERT INTO features (id, priority, category, name, description, steps, passes, "
            "in_progress, dependencies, needs_human_input, human_input_request, "
            "human_input_response, complexity) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, 0, NULL, NULL, ?)",
            (fid, 1092 + i, CATEGORY, name, desc, steps, json.dumps(deps), cx),
        )
    con.commit()
    for r in con.execute("SELECT id, priority, complexity, name FROM features WHERE id >= 535 ORDER BY id"):
        print(r)


if __name__ == "__main__":
    main()
