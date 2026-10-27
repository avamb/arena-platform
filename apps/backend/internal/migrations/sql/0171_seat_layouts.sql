-- =====================================================================
-- arena_new — seat layouts, category colour, plan annotations (task S1d,
-- spec 08_architecture/26_seat_layout_management_plan_ru.md section 3.2 and
-- 08_architecture/40_event_center_execution_spec_ru.md section 5.S)
--
-- Three additions, none of them touches session_seats, reservations, the
-- gateway or the ledger:
--
--   * ticket_tiers.color — the colour of a category OF A SESSION. Until now
--     a colour lived only in the plan's geometry, so a category an organizer
--     created by painting seats had none of its own. NULL means "use the
--     colour of the geometry category" (clients keep that fallback).
--     Stored as #rrggbb.
--
--   * seating_plans.annotations — facts for the allocator that are NOT part
--     of the geometry and never change its checksum: the stage point, the
--     weight of each section and seats with a restricted view. Same JSON as
--     layoutgen.Annotations. Set once per plan (by its owner), read by the
--     suggest route when the request carries none of its own.
--
--   * seat_layouts — a layout as an object: slots (categories with name,
--     colour, price), the slot of each seat and the seats to close. It
--     belongs to an organization and is bound to a PLAN, not to a plan
--     version: applying it to a session goes by seat_key. algorithm and
--     params record which allocator algorithm made it, so it can be seen
--     later which ones are used (owner decision 2026-10-15). Applying a
--     layout is seatlayout.ApplyLayout -> one seat batch (kind
--     apply_layout) with the sessions row locked first.
--
-- No permission is added: the routes use session.seats.manage (0161), which
-- platform_superadmin already holds.
-- =====================================================================

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE ticket_tiers
    ADD COLUMN color text NULL;

ALTER TABLE ticket_tiers
    ADD CONSTRAINT ticket_tiers_color_check
    CHECK (color IS NULL OR color ~ '^#[0-9A-Fa-f]{6}$');

COMMENT ON COLUMN ticket_tiers.color IS
    'Colour of this category on the seat map, #rrggbb. NULL = the colour of '
    'the geometry category the seats came from (task S1d).';

ALTER TABLE seating_plans
    ADD COLUMN annotations jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE seating_plans
    ADD CONSTRAINT seating_plans_annotations_object_check
    CHECK (jsonb_typeof(annotations) = 'object');

COMMENT ON COLUMN seating_plans.annotations IS
    'Allocator facts outside the geometry: focal_point {x,y}, section_weights '
    '{section_key: 0..1}, obstructed_seat_keys []. Never part of the geometry '
    'checksum (task S1d).';

CREATE TABLE seat_layouts (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    seating_plan_id uuid        NOT NULL REFERENCES seating_plans(id) ON DELETE CASCADE,
    name            text        NOT NULL,
    source          text        NOT NULL,
    algorithm       text        NULL,
    params          jsonb       NULL,
    payload         jsonb       NOT NULL,
    created_by      uuid        NULL,
    created_by_label text       NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seat_layouts_source_check
        CHECK (source IN ('manual', 'geometry', 'allocator', 'copied_from_session')),
    CONSTRAINT seat_layouts_name_check
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    CONSTRAINT seat_layouts_algorithm_check
        CHECK (algorithm IS NULL OR char_length(algorithm) BETWEEN 1 AND 64),
    CONSTRAINT seat_layouts_params_check
        CHECK (params IS NULL OR (algorithm IS NOT NULL AND jsonb_typeof(params) = 'object')),
    CONSTRAINT seat_layouts_payload_check
        CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT seat_layouts_created_by_label_check
        CHECK (char_length(created_by_label) <= 200)
);

-- One name per organization and plan, ignoring case and outer spaces.
CREATE UNIQUE INDEX seat_layouts_org_plan_name_uq
    ON seat_layouts (org_id, seating_plan_id, lower(btrim(name)));

CREATE INDEX seat_layouts_org_plan_idx
    ON seat_layouts (org_id, seating_plan_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE IF EXISTS seat_layouts;

ALTER TABLE seating_plans DROP CONSTRAINT IF EXISTS seating_plans_annotations_object_check;
ALTER TABLE seating_plans DROP COLUMN IF EXISTS annotations;

ALTER TABLE ticket_tiers DROP CONSTRAINT IF EXISTS ticket_tiers_color_check;
ALTER TABLE ticket_tiers DROP COLUMN IF EXISTS color;
