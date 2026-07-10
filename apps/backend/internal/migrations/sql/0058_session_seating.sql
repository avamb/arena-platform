-- +goose Up
-- =====================================================================
-- arena_new — Session seating (Wave SEAT-B1, feature #305)
--
-- Extends the sessions / tickets / reservations model with the seat-
-- level tables and columns required by §5.2 of
-- 09_autoforge/seating_backlog.md. Turns each session into either a
-- pure general-admission run (unchanged behaviour), an assigned-seats
-- run bound to a specific seating_plan_versions row, or a hybrid
-- run mixing assigned seats + standing zones.
--
-- Design notes:
--
--   * sessions.admission_mode drives the branching in the checkout,
--     hcatalog, and hseating handlers. The DB-side CHECK
--     `sessions_seated_requires_plan` guarantees that any non-GA
--     session HAS a seating_plan_version_id bound; the app-side rule
--     that the referenced version must be locked_at != NULL is
--     enforced in hseating (locked_at is materialized once the first
--     session binds; §5.1 immutability contract).
--
--   * sessions.seat_status_version is a session-scoped monotonic
--     counter incremented on every seat status change (hold / release
--     / sell / block / unblock). Combined with session_seats.status_
--     version it powers delta seat-status endpoints and the
--     GET_SEAT_LIST / seat_status_url Bil24 compatibility surface.
--
--   * session_seats materializes one row per assigned seat when a
--     seated session is provisioned (SEAT-B2). status defaults to
--     'available'; the transactional hold protocol taken from the
--     inventory_ledger idiom (§5.2 concurrency contract) locks rows
--     in seat_key order under SELECT … FOR UPDATE, then flips
--     'available' → 'held' with a conditional UPDATE. A single failed
--     UPDATE (0 rows) aborts the reservation with 409 and the
--     conflicting seat_keys.
--
--   * reservation_seats is a pure join table (no id column, composite
--     PK). It exists so a reservation can carry an arbitrary set of
--     seats without the schema having to encode a cardinality; the
--     seat lookup during ticket issuance walks
--     reservation_seats.session_seat_id -> session_seats.
--
--   * tickets.seat_key / seat_sector / seat_row / seat_number are
--     denormalized copies of the parent session_seats row taken at
--     issuance. They keep tickets independently renderable (PDF +
--     Apple/Google wallet templates) even after a plan is forked or
--     archived.
--
-- App-side rules (not enforced by the DB):
--
--   * A session already carrying issued tickets or non-cancelled
--     reservations is admission_mode-immutable; changing it requires
--     a new session.
--   * seat_status_version and status_version are advanced only inside
--     the same transaction that mutates session_seats.status.
--   * The seat lock ordering rule (ORDER BY seat_key ASC) is a hard
--     app-side contract — do NOT weaken it, it prevents deadlocks.
-- =====================================================================

-- ─────────────────────────────────────────────────────────────────────
-- sessions: admission mode + seating plan binding + status version
-- ─────────────────────────────────────────────────────────────────────

ALTER TABLE sessions
    ADD COLUMN admission_mode text NOT NULL DEFAULT 'general_admission'
        CHECK (admission_mode IN ('general_admission','assigned_seats','hybrid')),
    ADD COLUMN seating_plan_version_id uuid NULL
        REFERENCES seating_plan_versions(id),
    ADD COLUMN seat_status_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT sessions_seated_requires_plan CHECK
        (admission_mode = 'general_admission' OR seating_plan_version_id IS NOT NULL);

COMMENT ON COLUMN sessions.admission_mode IS
    'Seating mode for this session: general_admission (capacity-only, '
    'legacy behaviour), assigned_seats (every seat materialized in '
    'session_seats), or hybrid (assigned seats + standing zones). '
    'Feature #305 — Wave SEAT-B1.';

COMMENT ON COLUMN sessions.seating_plan_version_id IS
    'FK to the seating_plan_versions row this session is bound to. '
    'NULL for pure general-admission sessions; NOT NULL for '
    'assigned_seats / hybrid (enforced by sessions_seated_requires_plan). '
    'Locking the referenced version (seating_plan_versions.locked_at) '
    'is enforced app-side by hseating on first binding.';

COMMENT ON COLUMN sessions.seat_status_version IS
    'Session-scoped monotonic counter incremented on every seat status '
    'transition. Combined with session_seats.status_version it drives '
    'delta seat-status endpoints and the GET_SEAT_LIST / '
    'seat_status_url Bil24 compatibility surface (§5.2, §7 SEAT-B4).';

-- ─────────────────────────────────────────────────────────────────────
-- session_seats — one row per assigned seat per session
-- ─────────────────────────────────────────────────────────────────────

CREATE TABLE session_seats (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    session_id     uuid NOT NULL REFERENCES sessions(id),
    seat_key       text NOT NULL,
    sector_name    text NOT NULL,
    row_name       text NOT NULL,
    seat_number    text NOT NULL,
    tier_id        uuid NULL REFERENCES ticket_tiers(id),
    status         text NOT NULL DEFAULT 'available' CHECK (status IN
                     ('available','held','sold','blocked')),
    reservation_id uuid NULL REFERENCES reservations(id),
    status_version bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, seat_key)
);

CREATE INDEX session_seats_status_idx
    ON session_seats (session_id, status);

CREATE INDEX session_seats_version_idx
    ON session_seats (session_id, status_version);

COMMENT ON TABLE session_seats IS
    'One row per assigned seat per session. Materialized when a '
    'seated session is provisioned (SEAT-B2) from the parent '
    'seating_plan_versions.geometry. Seat status transitions run '
    'under SELECT … FOR UPDATE with a deterministic seat_key lock '
    'order to prevent deadlocks. Feature #305 — Wave SEAT-B1.';

COMMENT ON COLUMN session_seats.seat_key IS
    'Stable identifier copied from the version geometry '
    '(§5.3 <section.key>|<row.key>|<number>). Unique per session.';

COMMENT ON COLUMN session_seats.sector_name IS
    'Human-readable sector / section name denormalized from the '
    'geometry for direct rendering on tickets and admin UIs.';

COMMENT ON COLUMN session_seats.row_name IS
    'Human-readable row name denormalized from the geometry.';

COMMENT ON COLUMN session_seats.seat_number IS
    'Seat number denormalized from the geometry (text, since some '
    'venues encode letters or non-numeric identifiers).';

COMMENT ON COLUMN session_seats.tier_id IS
    'Optional binding to a ticket_tiers row (price category). '
    'NULL until the session-level category → tier mapping is '
    'applied (§7 SEAT-B2).';

COMMENT ON COLUMN session_seats.status IS
    'Seat lifecycle: available (default) → held (reservation open) '
    '→ sold (ticket issued), or blocked (admin hold). Transitions '
    'MUST be gated by the FOR UPDATE + conditional-UPDATE hold '
    'protocol described in §5.2.';

COMMENT ON COLUMN session_seats.reservation_id IS
    'FK to the reservations row that currently holds this seat. '
    'Cleared on release / expiry; kept on convert (informational).';

COMMENT ON COLUMN session_seats.status_version IS
    'Row-scoped stamp taken from sessions.seat_status_version at the '
    'moment of the last status transition. Powers delta seat-status '
    'endpoints without requiring a full-table scan.';

-- ─────────────────────────────────────────────────────────────────────
-- reservation_seats — reservation ↔ session_seats join
-- ─────────────────────────────────────────────────────────────────────

CREATE TABLE reservation_seats (
    reservation_id  uuid NOT NULL REFERENCES reservations(id),
    session_seat_id uuid NOT NULL REFERENCES session_seats(id),
    PRIMARY KEY (reservation_id, session_seat_id)
);

COMMENT ON TABLE reservation_seats IS
    'Join between reservations and session_seats. Written inside the '
    'transactional hold in the seated checkout path; walked during '
    'ticket issuance to hydrate seat_key / seat_sector / seat_row / '
    'seat_number onto the emitted tickets. Feature #305 — Wave SEAT-B1.';

-- ─────────────────────────────────────────────────────────────────────
-- tickets: denormalized seat coordinates for issuance-time snapshot
-- ─────────────────────────────────────────────────────────────────────

ALTER TABLE tickets
    ADD COLUMN seat_key    text NULL,
    ADD COLUMN seat_sector text NULL,
    ADD COLUMN seat_row    text NULL,
    ADD COLUMN seat_number text NULL;

COMMENT ON COLUMN tickets.seat_key IS
    'Denormalized seat_key copied from session_seats at issuance. '
    'NULL for general-admission tickets. Feature #305 — Wave SEAT-B1.';

COMMENT ON COLUMN tickets.seat_sector IS
    'Denormalized sector name copied from session_seats at issuance. '
    'NULL for general-admission tickets.';

COMMENT ON COLUMN tickets.seat_row IS
    'Denormalized row name copied from session_seats at issuance. '
    'NULL for general-admission tickets.';

COMMENT ON COLUMN tickets.seat_number IS
    'Denormalized seat number copied from session_seats at issuance. '
    'NULL for general-admission tickets.';

-- +goose Down
ALTER TABLE tickets
    DROP COLUMN IF EXISTS seat_number,
    DROP COLUMN IF EXISTS seat_row,
    DROP COLUMN IF EXISTS seat_sector,
    DROP COLUMN IF EXISTS seat_key;

DROP TABLE IF EXISTS reservation_seats;

DROP INDEX IF EXISTS session_seats_version_idx;
DROP INDEX IF EXISTS session_seats_status_idx;
DROP TABLE IF EXISTS session_seats;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_seated_requires_plan;
ALTER TABLE sessions
    DROP COLUMN IF EXISTS seat_status_version,
    DROP COLUMN IF EXISTS seating_plan_version_id,
    DROP COLUMN IF EXISTS admission_mode;
