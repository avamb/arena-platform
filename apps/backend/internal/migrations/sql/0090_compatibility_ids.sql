-- +goose Up
-- W1-A2a (feature #475): Bil24-compat integer identity map for catalog
-- entities (spec 08_architecture/18_bil24_compat_wave1_specification_ru.md
-- section 3.1).
--
-- The Bil24 wire protocol identifies actions, action events, category-price
-- tiers, venues, cities and countries by INTEGER ids. arena keys those
-- entities by uuidv7. We reconcile the two by lazily minting a stable
-- bigint id the first time the compat gateway exposes an arena-native
-- entity to a Bil24 client, and by recording externally-assigned bigint
-- ids when the import path (§13.2, W1-C) pulls a session from a real
-- Bil24 instance.
--
-- Layout: one shared sequence (compatibility_system_id_seq) whose values
-- always start at 1e9 so that any id < 1e9 is guaranteed to have come from
-- an external system (Bil24) and never collides with a locally-minted id.
-- A single map table keyed by (kind, platform_id) enforces the "one UUID ↔
-- one system_id forever" invariant; a secondary primary key on
-- (kind, system_id) enforces uniqueness of the integer id per kind.
--
-- session_seats gets a source column that mirrors the same policy for
-- seat/GA-unit ids that are minted directly into session_seats.system_seat_id
-- (0088). Rows imported from Bil24 (§13.3) carry 'bil24' — see §4 of the
-- spec for the numeric-range contract.

CREATE SEQUENCE compatibility_system_id_seq START WITH 1000000000;

COMMENT ON SEQUENCE compatibility_system_id_seq IS
    'W1-A2a: shared bigint identity sequence for arena-native rows exposed to '
    'the Bil24-compat gateway. Always starts at 1e9 so that any id < 1e9 is '
    'guaranteed to originate from an external system (Bil24) and cannot '
    'collide with a locally-minted id.';

CREATE TABLE compatibility_id_map (
    kind        text        NOT NULL CHECK (kind IN
                  ('action','action_event','category_price','venue','city','country')),
    system_id   bigint      NOT NULL,
    platform_id uuid        NOT NULL,
    source      text        NOT NULL CHECK (source IN ('arena','bil24')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, system_id),
    UNIQUE (kind, platform_id)
);

COMMENT ON TABLE compatibility_id_map IS
    'W1-A2a: durable mapping between platform uuidv7 catalog rows and the '
    'bigint ids that Bil24 clients see on the wire. Rows with source=''arena'' '
    'are minted lazily by compatids.Ensure/EnsureMany the first time an '
    'entity is exposed to the gateway (INSERT ... ON CONFLICT (kind, '
    'platform_id) DO NOTHING); their system_id comes from '
    'compatibility_system_id_seq and is therefore always >= 1e9. Rows with '
    'source=''bil24'' are inserted only by the Bil24 session importer '
    '(§13.2) via compatids.RegisterExternal, which rejects any system_id >= '
    '1e9 with compat.external_id_out_of_range.';

COMMENT ON COLUMN compatibility_id_map.kind IS
    'W1-A2a: category of the mapped entity. CHECK enum matches spec §3.1; '
    'seat is intentionally absent — Bil24 seatId lives on the SVG geometry '
    'and is copied to session_seats.system_seat_id at plan materialisation.';

ALTER TABLE session_seats ADD COLUMN system_seat_id_source text NOT NULL DEFAULT 'arena'
    CHECK (system_seat_id_source IN ('arena','bil24'));

COMMENT ON COLUMN session_seats.system_seat_id_source IS
    'W1-A2a: origin of the system_seat_id value. ''arena'' means the id was '
    'minted from session_seats_system_id_seq at seat-plan materialisation '
    '(0088). ''bil24'' means the id was copied from geometry.seats[].external_id '
    'during a Bil24 session import (§13.3) and therefore preserves the '
    'external seat id verbatim.';

-- +goose Down
ALTER TABLE session_seats DROP COLUMN IF EXISTS system_seat_id_source;
DROP TABLE IF EXISTS compatibility_id_map;
DROP SEQUENCE IF EXISTS compatibility_system_id_seq;
