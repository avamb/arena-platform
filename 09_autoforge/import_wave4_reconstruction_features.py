"""Install the AutoForge-owned half of wave 4 into features.db.

Source: 09_autoforge/admin_bootstrap_backlog.md, section
"Wave 4 - reconstruction alignment". That wave corrects four structural
deviations from the Bil24 model the project reconstructs, and it runs as SIX
ALTERNATING PASSES because the strongest model is only available in the
interactive session, not to AutoForge:

  1 interactive  AB-36/37/38 + the blocked->unavailable rename
  2 AutoForge    AB-42, AB-47, AB-43, AB-44, AB-46
  3 interactive  AB-40 A/B/C + AB-51   (seating model as one piece)
  4 AutoForge    AB-39, AB-40D
  5 interactive  AB-49, AB-48, AB-41   (inventory and money correctness)
  6 AutoForge    AB-50, AB-45

ONLY the AutoForge passes (2, 4, 6) are imported here - nine features. The
interactive passes are deliberately NOT queued: they are migrations that touch
nearly every catalog query, plus inventory and money correctness, where a
silent error is expensive. They live in the backlog markdown and are done in
session. Do not "complete" the wave by adding them here.

PRECONDITIONS, in pass order - the queue cannot express these because the
blocking work is not in the database:
  * every feature below requires interactive pass 1 (AB-36/37/38) merged, or
    it will be built against the old model and rewritten;
  * AB-39 and AB-40D additionally require interactive pass 3 (AB-40 A/B/C,
    AB-51);
  * AB-50 additionally requires interactive pass 5 (AB-49) - there must be a
    cancellation event before there is anything to propagate.
Run AutoForge on a pass only after the interactive pass before it has landed
and its gate is green.

Idempotent: refuses to overwrite or renumber an unexpected queue, exactly like
import_admin_bootstrap_features.py.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DB = ROOT / ".autoforge" / "features.db"

EXPECTED_HEAD = (423, 533)
START_ID = 424
START_PRIORITY = 534

BACKLOG = "09_autoforge/admin_bootstrap_backlog.md"


def feature(
    offset: int,
    code: str,
    severity: str,
    title: str,
    objective: str,
    steps: list[str],
    complexity: int = 2,
    dependencies: list[int] | None = None,
) -> dict[str, object]:
    return {
        "id": START_ID + offset,
        "priority": START_PRIORITY + offset,
        "category": "Reconstruction Wave 4",
        "name": f"{code} [{severity}]: {title}",
        "description": (
            f"Severity: {severity}. {objective} "
            f"Full context: {BACKLOG} (section {code}, under 'Wave 4 - "
            "reconstruction alignment'). Read that section before starting - it "
            "carries the source evidence from the Bil24 material and marks which "
            "current behaviours are CORRECT and must not be 'fixed'. "
            "Wave 4 preconditions apply: interactive passes land first, see the "
            "execution-plan table in the backlog. "
            "Do not mark passing from unit tests alone when the change requires "
            "PostgreSQL, browser, or container integration. Never weaken or "
            "delete an acceptance test to obtain green. Update openapi.yaml and "
            "regenerate the Go types and TS client in the same commit as any API "
            "change."
        ),
        "steps": steps,
        "complexity": complexity,
        "dependencies": dependencies,
    }


FEATURES = [
    # ---------------- Pass 2: UI breadth on the new model foundation ----------
    feature(
        0,
        "AB-42",
        "CRITICAL",
        "Event creation wizard (replaces the single create modal)",
        "Create Event posts only the event, leaving no session and no tier, so the "
        "result is unsellable - this is exactly what the owner hit live. The "
        "reference order is country -> city -> venue -> event -> seating plan -> "
        "session(s) -> categories/prices.",
        [
            "Step 1 - event identity (organization, name, visibility, description); creates immediately as draft. NO dates and NO venue: both moved to the session by interactive AB-36/AB-37.",
            "Step 2 - first session: venue via the AB-35 cascading picker, seating plan version, admission mode, start/end. Capacity is DERIVED and read-only; currency is shown as derived text with its source, not as a blank select.",
            "Step 3 - categories and prices for that session.",
            "Publish gate: event.publish refuses an event with no session, or a session with no priced tier, naming specifically what is missing. This is the owner's 'finalisation' - achieved by a gate, not by deferring creation, so a half-finished event stays resumable.",
            "The wizard is resumable: reopening a draft event lands on the first incomplete step.",
            "Browser test: no event -> published sellable event without leaving the wizard; and publishing an incomplete event is refused with the specific reason.",
        ],
        complexity=3,
    ),
    feature(
        1,
        "AB-47",
        "MAJOR",
        "Posters bind to the session, not the event",
        "Deliberate divergence from Bil24, which attaches artwork to the event. "
        "Organizers print the specific date and venue on a poster, so one image per "
        "event does not survive a multi-date run. Record it as intentional so nobody "
        "'corrects' it back.",
        [
            "Migration adds 'session_poster' to the media_objects.owner_type CHECK AND to mediastore.AllowedOwnerTypes IN THE SAME MIGRATION. AGENTS.md documents this trap: widening the Go allowlist alone makes POST /v1/media stream the bytes to storage and then fail the INSERT with 23514. Extend TestAllowedOwnerTypes_MatchMigrationCheckConstraint.",
            "Add sessions.poster_media_id (nullable FK to media_objects).",
            "Reuse without a join table: keep events.poster_media_id (migration 0051, currently unused) and repurpose it as the event-level DEFAULT that a new session inherits; a session may override it.",
            "Admin: one checkbox on upload - 'use for all sessions of this event' - writes the image as the event default and clears per-session overrides. This gives both 'apply to all now' and 'apply to sessions created later' with no extra schema.",
            "Resolution order everywhere a poster renders: session.poster_media_id ?? event.poster_media_id ?? none. Applies to the public feed, the widget, the WordPress cache contract (08_architecture/02_wordpress_integration_contract_ru.md currently lists poster URLs under the event - update it) and ticket PDF/email.",
            "Open question for the owner, do not guess: the Bil24 event pane shows TWO image slots. Confirm whether one poster per session is enough before fixing the column shape.",
        ],
    ),
    feature(
        2,
        "AB-43",
        "MAJOR",
        "Publications: sales-channel picker, derived city, honest FK errors",
        "Three defects in one small tab. The operator is asked to paste a feed-token "
        "UUID with no way to discover a valid one - the owner pasted the event's own "
        "id, the only id on screen - and the resulting FK violation surfaces as a "
        "generic 500.",
        [
            "Replace the free-text 'Feed token ID' input with a sales-channel picker; the feed token is resolved (or created) behind it. Add feed-token management to the Channels screen - hfeed/feed_tokens.go already implements full CRUD under a channel and no UI exists for it.",
            "Map the Postgres foreign-key violation (23503) on event_publications.feed_token_id to 404 publication.feed_token_not_found, and the city FK likewise. Today hcatalog/publications.go:138-145 collapses EVERY database error into 500 publication.internal.",
            "Document both new errors in openapi.yaml - the spec currently documents only the 500 - and regenerate the clients.",
            "Default the city scope from the session's venue city; expose an override only under 'advanced'. Keep the column: NULL still means a global publication.",
        ],
    ),
    feature(
        3,
        "AB-44",
        "NORMAL",
        "Events UI hygiene: modal dismissal, raw UUIDs, pricing-mode help",
        "Small independent defects on the events surface, all visible to the owner in "
        "the live walkthrough.",
        [
            "Create/Edit dialogs must NOT close on an outside click - events.tsx:1976 has the backdrop wired to onClick={onClose}. Escape closes with a confirmation when the form is dirty. There is no Escape handler at all today despite aria-modal='true'; reuse components/layout/ResponsiveDrawer.tsx, which already implements both correctly.",
            "If AB-42 has already replaced the create modal with the wizard, apply the rule to whatever dialogs remain - do not reinstate the old modal.",
            "The Venue column renders a shortened raw UUID (events.tsx:1373-1375). Render name + short display number per the AB-22 rule.",
            "The pricing-mode select has no help text or tooltip; add a per-mode description (fixed / free / pwyw with min-max).",
        ],
        complexity=1,
        dependencies=[START_ID + 0],
    ),
    feature(
        4,
        "AB-46",
        "NORMAL",
        "Domain layer has no tests at all",
        "internal/domain/{catalog,inventory,tickets,billing,reporting} hold the state "
        "machines and invariants and have ZERO test files between them. The only tests "
        "referencing them are import-restriction checks. The 423/423 figure is "
        "overwhelmingly HTTP-boundary tests against the composed server.",
        [
            "Table-driven tests per aggregate for the transition matrices and guards in all five packages.",
            "Cover at minimum: event and session status lifecycles, the session temporal-overlap rule, pricing-mode invariants including PWYW bounds, reservation state plus TTL precedence, ticket transitions, discount math, invoice transitions.",
            "These are pure packages with no I/O - no database, no 'integration' build tag needed.",
            "Assertions must reflect the documented invariants; do not soften one to obtain green.",
        ],
    ),
    # ---------------- Pass 4: large UI over the settled seating model ---------
    feature(
        5,
        "AB-39",
        "CRITICAL",
        "Per-seat price category assignment - table and map over one selection model",
        "OWNER-MANDATORY. The reference's core pricing gesture is missing: select seats "
        "on the hall map, assign them a price category ('Change the category of the "
        "selected seats'). Without it a mixed hall cannot be priced at all. Most of the "
        "machinery already exists and is unused - session_seats.tier_id, autoCreateTier "
        "in hseating/bind.go at bind time, and sessionSeats.tsx already renders the seat "
        "map and supports selection. What is missing is REASSIGNMENT.",
        [
            "PATCH /v1/event-sessions/{session_id}/seats/category - bulk assign one tier_id to a list of seat_keys; n=1 is the single-seat case.",
            "Reject reassignment of seats in sold/held status with 409 listing the conflicting seat keys; allow available and unavailable.",
            "Bump seat_status_version so widget and feed caches invalidate.",
            "After a successful bind every seat must carry a non-null tier_id.",
            "Admin - TWO equivalent surfaces over ONE selection model. Table: ID | Sector | Row | Seat | Category | Price | Barcode | Status, multi-select with a live 'Selected seats: N' counter, actions Category / Unavailable / Available / Price order, rows colour-coded by status, GA rows inline with empty coordinate columns. Map: click and shift-drag rubber-band selection, seat fill colour = category, legend above.",
            "Selecting in one surface selects in the other. The table makes bulk work practical on a 590-unit hall; the map makes it comprehensible.",
            "Browser test on the Palac Akropolis plan: select an arbitrary set of balcony seats, move them to another category, and verify the widget reflects the new price.",
        ],
        complexity=3,
    ),
    feature(
        6,
        "AB-40D",
        "MAJOR",
        "Widget: hybrid halls on one surface, zero mode switching",
        "Owner requirement and an already-mandated spec item "
        "(08_architecture/16_ticket_widget_ux_and_technology_ru.md:144-164: 'одна "
        "поверхность, ноль переключателей'). The current widget forces the buyer to "
        "switch between GA and assigned-seat modes; the owner names that switching as "
        "the thing to beat.",
        [
            "GA areas render on the SAME hall map as seats, as clickable polygons.",
            "Clicking a GA area opens a small inline quantity popover ('how many tickets'), bounded by that area's remaining capacity out of its shared pool. Clicking a seat still selects that seat.",
            "ONE cart holds both kinds of line item simultaneously. No mode toggle anywhere in the UI.",
            "Remaining GA capacity is a shared pool decremented by reservations, not a set of pseudo-seats the buyer picks from.",
            "The backend already supports the shape: admission_mode hybrid accepts seats and quantity in one reservation, and reservation_ga_items exists. This is presentation plus reservation wiring, not new inventory mechanics.",
            "Cover with both widget e2e suites (mock and :real). AGENTS.md: the Playwright dev server REQUIRES VITE_API_BASE_URL in the config env, otherwise the app throws on startup and renders an error screen instead of the UI.",
        ],
        complexity=3,
    ),
    # ---------------- Pass 6: integration and cleanup -------------------------
    feature(
        7,
        "AB-50",
        "CRITICAL",
        "Feed the external MACS scanning service (JSON export + webhooks)",
        "Gate control is NOT an in-platform scanner - it is the external Max Mobil "
        "Access Control System, which has its own backend, admin panel and iOS/Android "
        "apps. The platform's job is to feed it accurately. Build against the MACS "
        "source contract recorded in the backlog, NOT against the Bil24 docs, wherever "
        "the two differ: Bil24 is the format's ancestor, MACS is the actual receiver.",
        [
            "Ticket/order export in the exact shape of the owner's sample (orders at top level, tickets nested in ticketList, actionEvent per ticket) - both as a downloadable JSON file for the service's Import Tickets action and as an API endpoint.",
            "Webhook delivery with the MACS envelope {id:int, created, type, data}. MACS handles ONLY order.paid and ticket.refunded at POST /_wh/tickets - NOT order.cancelled and NOT the event.* triggers the Bil24 docs describe. A whole-order cancellation is therefore one ticket.refunded per ticket.",
            "ACCEPTANCE CRITERION: a ticket CANCELLED by the operator reaches MACS as ticket.refunded regardless of refund_mode (automatic, manual or none) - admission has nothing to do with whether money moved. MACS's gate check is status == 3 and it already rejects with 'Ticket was refunded <date>', so a truthful feed is sufficient to close the hole.",
            "Status is an INTEGER: 0 not used, 1 checked in, 2 checked out, 3 refunded. NEVER_USE is a Bil24 value, not a MACS one. MACS conflates usage and refund in that one field - keep them separate on our side and collapse only at the boundary.",
            "Required ticket fields: id:int, seatId:int, barcode, actionEvent (id, cityName, venueName, actionName, actionLegalOwner, showTime). Our ids are uuidv7 while MACS types id/seatId as int - register as a ticket system (ticket_system slug, system_ticket_id, system_ids) and pick the SIMPLEST stable collision-free integer scheme. Do not engineer an elaborate permanent one: widening MACS to accept string ids is the real long-term fix and belongs on the MACS backlog.",
            "Isolate ALL MACS-shaped mapping behind one adapter boundary; nothing MACS-specific may leak into the catalog or ticketing domain.",
            "Delivery semantics: POST, success is HTTP 200, retry over 24 hours. Reuse the existing outbox (0068 next_attempt_at / dead_lettered_at) - do not invent a second retry mechanism.",
            "Sign our webhooks even though the reference does not; webhook_subscribers already carries a secret. Verification must be optional on the receiver so MACS keeps working unchanged.",
            "Round-trip test against a stub receiver asserting what MACS STORED, not the HTTP status it returned: its importer silently fabricates missing values (random order ids, status PAID, 'Unknown City' / 'Unknown Venue', EAN-13), so an incomplete export imports as plausible garbage.",
            "Minimum action on the three internal scan endpoints, which are NOT the product: make them respect ticket status (the scan-events query already selects it and never reads it), mark them internal/testing-only in the OpenAPI description, and build nothing further on them.",
        ],
        complexity=3,
    ),
    feature(
        8,
        "AB-45",
        "NORMAL",
        "Dead schema: wire it or drop it",
        "Schema with no consumer, found during the wave-4 audit. Decide per item and "
        "leave none ambiguous.",
        [
            "Migration 0051 event metadata - slug, short_description, genre, age_rating, duration_minutes, teaser_url, trailer_url, meta_* plus the whole event_artists table - has zero readers and is referenced by no sqlc query or handler. The Bil24 editor confirms these as real operator inputs (Age limit, Duration, Genres, Promoter, Short/Full title, Rating, Min. service fee), so WIRE them, do not drop. poster_media_id is claimed by AB-47 - leave it alone.",
            "promo_codes.applies_to_tier_ids: a promo code scoped to specific tiers is silently applicable to ANY tier at checkout - validatePromoCodeRequest has no tier field and applyPromoCode never reads the column. Enforce the restriction.",
            "Organisation branding on ticket PDFs and emails: ten delivery.Payload fields are declared, rendered and unit-tested but NEVER populated in production (htickets/delivery_enqueue.go), so every delivered ticket falls back to platform-default branding. organizations.logo_media_id additionally has no write path at all. Populate them end to end.",
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
                print("Wave 4 AutoForge features already installed.")
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
            "arena_new_features_before_wave4_"
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
                    json.dumps(item["dependencies"]) if item["dependencies"] else None,
                    item["complexity"],
                ),
            )
        connection.commit()
        print(
            f"Installed {len(FEATURES)} wave-4 AutoForge features "
            f"(ids {START_ID}-{START_ID + len(FEATURES) - 1}); backup: {backup}"
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


if __name__ == "__main__":
    install()
