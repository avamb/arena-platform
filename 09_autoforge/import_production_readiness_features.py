"""Install the production-readiness AutoForge wave into features.db.

The markdown backlog is a planning artifact; AutoForge executes only database
records. This importer is intentionally idempotent and refuses to overwrite or
renumber an unexpected queue.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"
BACKLOG = "09_autoforge/production_readiness_backlog.md"


def feature(
    feature_id: int,
    priority: int,
    code: str,
    title: str,
    model: str,
    objective: str,
    steps: list[str],
    dependencies: list[int] | None = None,
) -> dict[str, object]:
    return {
        "id": feature_id,
        "priority": priority,
        "category": "Production Readiness PR",
        "name": f"{code}: {title}",
        "description": (
            f"Model: {model}. {objective} Read and satisfy the complete "
            f"acceptance criteria in {BACKLOG}, section {code}. Do not mark "
            "passing from unit tests when the section requires a real "
            "PostgreSQL, SMTP, webhook, container, browser, or CI integration."
        ),
        "steps": steps,
        "dependencies": dependencies,
    }


FEATURES = [
    feature(
        344,
        426,
        "PR-00",
        "Establish one production configuration contract",
        "opus",
        "Make runtime configuration, examples, Dokploy docs, and production validation one typed fail-fast contract.",
        [
            "Inventory every env variable against config.Load and runtime consumers; wire it or remove stale documentation.",
            "Add typed canonical URL, email/SMTP mode, outbox mode/signing, media signing, and health-target settings.",
            "Reject every unsafe production fallback enumerated by PR-00 and redact secrets.",
            "Add table-driven tests for each rejection and one valid production profile.",
            "Synchronize .env.example, deploy/DOKPLOY.md, and hardening docs.",
            "Run config tests, go test ./..., and docker compose config --quiet; record exact outputs.",
        ],
    ),
    feature(
        345,
        427,
        "PR-01",
        "Make internally issued JWTs work in production",
        "opus",
        "Replace protected routes' disabled StubProvider dependency with a production verifier compatible with normal login and refresh tokens.",
        [
            "Introduce an authentication-verifier interface and retain StubProvider only for explicit development.",
            "Validate algorithm, signature, expiry, issuer, audience, session/revocation, and claims using the IssueJWT contract.",
            "Wire every protected route to the production-capable verifier; keep dev mint endpoints unavailable in production.",
            "Test invalid, expired, tampered, wrong-issuer, and wrong-audience tokens.",
            "Add PostgreSQL integration: production login -> /v1/me -> protected route -> refresh -> logout.",
            "Run tagged auth/httpserver integration and auth unit tests; record outputs.",
        ],
        [344],
    ),
    feature(
        346,
        428,
        "PR-02",
        "Deliver registration and reset mail through durable jobs",
        "opus",
        "Replace verification/reset token logging with durable email jobs and canonical non-Host-derived links.",
        [
            "Define durable verification and password-reset email jobs without secret-bearing logs.",
            "Build links exclusively from validated canonical public URL configuration.",
            "Enqueue with failure-safe transaction semantics and preserve anti-enumeration responses.",
            "Add escaped locale-aware templates with expiry information.",
            "Test enqueue failure, single use, expiry, anti-enumeration, and absence of tokens/full signed URLs in logs.",
            "Use PostgreSQL plus SMTP capture to prove both messages arrive and complete their flows.",
        ],
        [344],
    ),
    feature(
        347,
        429,
        "PR-03",
        "Make ticket SMTP delivery fail honestly",
        "opus",
        "Allow delivery status sent only after a real configured sender accepts the ticket message.",
        [
            "Restrict LogSender and sender-less delivery to explicit non-production mode.",
            "Implement honest queued/processing/sent/failed-or-disabled transitions; missing sender must never produce sent.",
            "Preserve retry/dead-letter behaviour and add stable delivery idempotency for reconciliation.",
            "Redact SMTP credentials, tokens, barcodes, and signed URLs.",
            "Integration-test a captured ticket email with PDF and queued -> processing -> sent.",
            "Integration-test SMTP refusal/timeout and prove status is not sent.",
        ],
        [344],
    ),
    feature(
        348,
        430,
        "PR-04",
        "Make outbox delivery explicit and fail-closed",
        "opus",
        "Require explicit webhook or disabled mode and prevent noop dispatch from marking production events processed.",
        [
            "Add validated outbox modes; webhook mode requires URL and strong signing secret.",
            "Ensure noop/disabled mode never claims or marks production rows processed.",
            "Leave timeout, non-2xx, signing, and configuration failures retryable.",
            "Document retry/dead-letter observability without leaking sensitive payload data.",
            "Integration-test signature, success, failure/retry, duplicate safety, and disabled mode with PostgreSQL and a real HTTP receiver.",
        ],
        [344],
    ),
    feature(
        349,
        431,
        "PR-05",
        "Give API and worker correct container healthchecks",
        "sonnet",
        "Make the shared image healthcheck resolve the actual API or worker endpoint and synchronize Dokploy/Compose.",
        [
            "Implement deterministic target resolution: HEALTH_ADDR, then process-specific listen address.",
            "Set/document explicit correct API and worker health addresses in Dokploy and Compose.",
            "Build and start separate API and worker containers; prove both become healthy.",
            "Prove an unavailable target becomes unhealthy and preserve internal-only worker metrics exposure.",
        ],
        [344],
    ),
    feature(
        350,
        432,
        "PR-06",
        "Repair the tagged PostgreSQL integration suite",
        "sonnet",
        "Make go test -tags=integration ./... compile and pass with the pinned goose dependency and current migration head.",
        [
            "Fix migration concurrency tests against the pinned goose API or perform a reviewed compatible upgrade.",
            "Remove stale scaffold_echo truncation and safely handle current application/schema-migration tables.",
            "Make worker retry/dead-letter tests isolated and order-independent.",
            "Run go test -tags=integration ./... twice from clean state.",
            "Run go test ./... and record exact pass summaries.",
        ],
    ),
    feature(
        351,
        433,
        "PR-07",
        "Produce a deployable admin-web artifact",
        "opus",
        "Build a deterministic non-root production admin image instead of Vite dev server/source mounts.",
        [
            "Add production build/runtime with static serving, SPA fallback, cache headers, and health endpoint.",
            "Implement safe runtime API-base configuration without browser secrets.",
            "Disable public source maps and split the oversized bundle under a documented budget.",
            "Hide or feature-flag Reports, Content, and POS placeholders in production navigation.",
            "Add browser smoke using production login and /v1/me against the built artifact.",
            "Update Dokploy service/domain/env/health/rollback docs and run unit/build/container smoke.",
        ],
        [345],
    ),
    feature(
        352,
        434,
        "PR-08",
        "Make widget E2E terminate cleanly and remove a11y warnings",
        "sonnet",
        "Fix the Playwright/open-handle leak and the SeatMapView interactive-element accessibility warning without suppressing it.",
        [
            "Diagnose and fix Playwright/demo-server child lifecycle and stale-server reuse.",
            "Ensure success exits zero, failure exits nonzero, and failure artifacts remain available.",
            "Fix SeatMapView semantics and keyboard handling without compiler-warning suppression.",
            "Run widget unit tests and build.",
            "Run E2E twice under an outer 120-second timeout; verify zero exit and no orphan browser/server/Node process.",
        ],
    ),
    feature(
        353,
        435,
        "PR-09",
        "Make container builds reproducible and minimal",
        "sonnet",
        "Exclude host dependencies, agent state, build/test artifacts, VCS data, and secrets from Docker contexts and runtime images.",
        [
            "Add .dockerignore for .git, .autoforge, node_modules, local dist, tests/results, Playwright/editor artifacts, and secrets.",
            "Build every production target from a clean checkout without host node_modules/dist.",
            "Inspect runtime image users/files for only required assets and non-root operation where feasible.",
            "Scan images for env files, databases, source maps, test reports, tokens, and private keys.",
            "Record a materially reduced context size versus the audited approximately 108 MB.",
        ],
        [351],
    ),
    feature(
        354,
        436,
        "PR-10",
        "Turn CI into a real publication gate",
        "opus",
        "Block every image/widget/release publication until lint, unit/race, tagged integration, OpenAPI, admin, widget, and real acceptance gates pass.",
        [
            "Add go test -tags=integration ./... with Docker/Testcontainers support.",
            "Add admin unit/build/production-container smoke gates.",
            "Require widget unit/build/size and real-backend acceptance; mock-only E2E is non-gating.",
            "Make build-and-push/release depend on every gate and never publish from PRs.",
            "Add timeouts, always-uploaded failure artifacts, concurrency cancellation, and least-privilege permissions.",
            "Add a workflow dependency regression test and demonstrate a deliberately failing gate skips publication.",
        ],
        [345, 346, 347, 348, 349, 350, 351, 352, 353],
    ),
    feature(
        355,
        437,
        "PR-11",
        "Make the load-test workflow fail honestly",
        "sonnet",
        "Run migrations/setup before k6 and make readiness or threshold failures return nonzero while still preserving artifacts.",
        [
            "Run current migrations and deterministic seed/setup before readiness and k6.",
            "Make readiness deadline failure nonzero and always capture all service logs.",
            "Remove continue-on-error from k6; upload results with if: always().",
            "Add a production-login plus protected-endpoint load scenario.",
            "Document profile/thresholds and negative-test unavailable API plus breached threshold.",
        ],
        [350, 354],
    ),
    feature(
        356,
        438,
        "PR-12",
        "Automate current-head release evidence and refresh stale docs",
        "opus",
        "Replace migration-0041 claims with a reproducible current-head production-like release gate and evidence report.",
        [
            "Discover embedded migration head dynamically and remove stale 0041 expectations.",
            "Synchronize README, release/hardening checklists, and Dokploy docs.",
            "Start a fresh production-like stack and migrate through current head.",
            "Smoke production auth, auth emails, ticket PDF email, signed outbox, API/worker health, admin login, and widget entry.",
            "Exercise rollback procedure without claiming irreversible down migrations are safe.",
            "Create evidence with commit, image digests, migration head, commands, timestamps, and outcomes; block rather than reuse old evidence.",
        ],
        [354, 355],
    ),
]


def install() -> None:
    expected_ids = [item["id"] for item in FEATURES]
    connection = sqlite3.connect(DB, timeout=30)
    connection.execute("PRAGMA busy_timeout=30000")
    try:
        existing = connection.execute(
            "SELECT id, name FROM features WHERE id BETWEEN 344 AND 356 "
            "OR name LIKE 'PR-%' ORDER BY id"
        ).fetchall()
        if existing:
            expected = [(item["id"], item["name"]) for item in FEATURES]
            if existing == expected:
                print("Production-readiness features are already installed.")
                return
            raise RuntimeError(f"Conflicting PR-wave features exist: {existing}")

        head = connection.execute(
            "SELECT MAX(id), MAX(priority) FROM features"
        ).fetchone()
        if head != (343, 425):
            raise RuntimeError(
                f"Unexpected queue head {head}; refusing explicit IDs {expected_ids}"
            )

        backup = ROOT / ".autoforge" / "backups" / (
            "arena_new_features_before_pr_wave_"
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
                    json.dumps(item["dependencies"])
                    if item["dependencies"]
                    else None,
                ),
            )
        connection.commit()
        print(f"Installed {len(FEATURES)} features; backup: {backup}")
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
