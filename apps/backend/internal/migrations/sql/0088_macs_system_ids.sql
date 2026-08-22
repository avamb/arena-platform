-- +goose Up
-- AB-50a: MACS ticket-system registration + integer identity scheme.
--
-- MACS (Max Mobil Access Control System) types ticket and seat ids as
-- INTEGER while our primary keys are uuidv7. We add stable, collision-free
-- bigint identity columns backed by per-table sequences so the adapter layer
-- can feed MACS with the integer it expects without touching our UUID-keyed
-- catalog.
--
-- Design decision (owner, 2026-08-22): use the SIMPLEST scheme that works.
-- Widening MACS to accept string ids is the real long-term fix and belongs on
-- the MACS backlog. Until then, each new ticket/seat row gets the next value
-- from its dedicated sequence; existing rows are backfilled at ADD COLUMN time
-- (PostgreSQL calls nextval() for each existing row when the DEFAULT is
-- volatile). The ids are INTERNAL for this sub-feature: no API surface
-- changes. The adapter boundary lives in
-- apps/backend/internal/platform/macs.
--
-- Sequence design:
--   tickets_system_id_seq   — one shared counter across all tickets
--   session_seats_system_id_seq — one shared counter across all seats/units
-- Collisions between the two are intentional / irrelevant: seatId and
-- ticketId travel on different MACS JSON fields and are never compared to
-- each other by the scanning service.

-- 1. Ticket-system registry: one row per external scanning integration.
CREATE TABLE ticket_systems (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    slug       text        NOT NULL UNIQUE,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE ticket_systems IS
    'AB-50a: registry of external ticket-scanning systems that this platform '
    'feeds. Currently one entry: MACS (Max Mobil Access Control System at '
    'macs.arenasoldout.com). Slug is the stable machine identifier used in '
    'webhook subscriber configuration and export filenames.';

-- Seed the one known integration.
INSERT INTO ticket_systems (slug, name)
VALUES ('macs', 'Max Mobil Access Control System (MACS)');

-- 2. tickets.system_ticket_id — stable integer identity per ticket.
CREATE SEQUENCE tickets_system_id_seq;

ALTER TABLE tickets
    ADD COLUMN system_ticket_id bigint NOT NULL
        DEFAULT nextval('tickets_system_id_seq')
        UNIQUE;

COMMENT ON COLUMN tickets.system_ticket_id IS
    'AB-50a: stable bigint identity assigned at issuance (sequence-backed, '
    'collision-free across all orgs). Mapped to id in the MACS JSON export '
    'envelope and to seatId on the ticket in ticketList[]. Never reused: '
    'cancellation does not free the integer back to the sequence.';

-- 3. session_seats.system_seat_id — stable integer identity per seat/unit.
CREATE SEQUENCE session_seats_system_id_seq;

ALTER TABLE session_seats
    ADD COLUMN system_seat_id bigint NOT NULL
        DEFAULT nextval('session_seats_system_id_seq')
        UNIQUE;

COMMENT ON COLUMN session_seats.system_seat_id IS
    'AB-50a: stable bigint identity assigned at seat-plan materialisation '
    '(sequence-backed, collision-free across all sessions). Mapped to seatId '
    'in the MACS JSON export and the per-ticket actionEvent. Note: when a '
    'seating plan is rebound the old session_seats rows are deleted and new '
    'rows are inserted, receiving fresh ids — the old integer ids are retired '
    'permanently, which is the correct MACS behaviour (a rebind invalidates '
    'any prior import).';

-- +goose Down

ALTER TABLE session_seats DROP COLUMN IF EXISTS system_seat_id;
DROP SEQUENCE IF EXISTS session_seats_system_id_seq;

ALTER TABLE tickets DROP COLUMN IF EXISTS system_ticket_id;
DROP SEQUENCE IF EXISTS tickets_system_id_seq;

DROP TABLE IF EXISTS ticket_systems;
