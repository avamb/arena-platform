-- +goose Up
-- =====================================================================
-- arena_new — Seating plans (Wave SEAT-A1, feature #302)
--
-- Two-table core of the venue seating model per seating_backlog §5.1:
--
--   * seating_plans          — logical plan owned by one organization,
--                              tied to a venue, with a lifecycle
--                              (draft / active / archived) and a
--                              visibility (private / shared_read /
--                              public_template / operator_verified).
--                              Plans support fork lineage via
--                              source_seating_plan_id and point at the
--                              currently-published version through
--                              current_version_id (nullable, populated
--                              once the first version is created).
--
--   * seating_plan_versions  — immutable-once-bound snapshot of the
--                              plan's canonical geometry (JSONB), with
--                              a monotonic version_number scoped to the
--                              parent plan, a sha256 checksum of the
--                              canonical JSON, capacity numbers, and
--                              an optional pointer to the source SVG
--                              in media storage (Wave G).
--
-- App-side rules (enforced in hseating handlers, not by the DB):
--
--   * A version referenced by any session with sales or issued tickets
--     is immutable — mutations MUST create a new version instead
--     (03_platform_management_api_and_permissions_ru.md:154).
--   * A plan you do not own is un-editable — the fork endpoint clones
--     the geometry into a new plan under your org, recording the
--     source_seating_plan_id lineage.
--
-- Referenced permissions (also seeded below):
--
--   * seating_plan.read
--   * seating_plan.create
--   * seating_plan.update.own
--   * seating_plan.fork
--   * seating_plan.share
--   * seating_plan.verify
--   * seating_plan.archive.own
--   * event_session.assign_seating_plan
--
-- Granted to the organizer and org_admin roles, mirroring the
-- venue.* seeding pattern (0012_venues.sql).
-- =====================================================================

CREATE TABLE seating_plans (
    id                      uuid PRIMARY KEY DEFAULT uuidv7(),
    venue_id                uuid NOT NULL REFERENCES venues(id),
    owner_org_id            uuid NOT NULL REFERENCES organizations(id),
    name                    text NOT NULL,
    plan_type               text NOT NULL CHECK (plan_type IN
                              ('assigned_seats', 'general_admission', 'tables', 'mixed')),
    visibility              text NOT NULL DEFAULT 'private' CHECK (visibility IN
                              ('private', 'shared_read', 'public_template', 'operator_verified')),
    status                  text NOT NULL DEFAULT 'draft' CHECK (status IN
                              ('draft', 'active', 'archived')),
    source_seating_plan_id  uuid NULL REFERENCES seating_plans(id),
    current_version_id      uuid NULL,  -- FK added after seating_plan_versions exists
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    deleted_at              timestamptz NULL
);

-- List active plans by owning org quickly (dashboard, CRUD list endpoint).
CREATE INDEX seating_plans_owner_org_active
    ON seating_plans (owner_org_id)
    WHERE deleted_at IS NULL;

-- List plans attached to a venue quickly.
CREATE INDEX seating_plans_venue_active
    ON seating_plans (venue_id)
    WHERE deleted_at IS NULL;

-- Follow fork lineage forward from a source plan.
CREATE INDEX seating_plans_source_plan
    ON seating_plans (source_seating_plan_id)
    WHERE source_seating_plan_id IS NOT NULL;

CREATE TABLE seating_plan_versions (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    seating_plan_id     uuid NOT NULL REFERENCES seating_plans(id),
    version_number      integer NOT NULL,
    geometry            jsonb NOT NULL,
    geometry_checksum   text NOT NULL,
    svg_asset_media_id  uuid NULL,
    capacity_seated     integer NOT NULL,
    capacity_standing   integer NOT NULL DEFAULT 0,
    locked_at           timestamptz NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (seating_plan_id, version_number)
);

-- List versions for a plan quickly, newest first.
CREATE INDEX seating_plan_versions_plan_recent
    ON seating_plan_versions (seating_plan_id, version_number DESC);

-- Cross-check integrity: current_version_id points into
-- seating_plan_versions once the first version exists.
ALTER TABLE seating_plans
    ADD CONSTRAINT seating_plans_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES seating_plan_versions(id);

-- ─────────────────────────────────────────────────────────────────────
-- Documentation comments
-- ─────────────────────────────────────────────────────────────────────

COMMENT ON TABLE seating_plans IS
    'Logical seating plan owned by one organization, attached to a '
    'venue. Immutable ownership (owner_org_id); mutations require the '
    'owner or a fork. Feature #302 — Wave SEAT-A1.';

COMMENT ON COLUMN seating_plans.owner_org_id IS
    'Owning organization. Only members with seating_plan.update.own may '
    'mutate the plan; other orgs must fork (seating_plan.fork).';

COMMENT ON COLUMN seating_plans.plan_type IS
    'Seating layout family: assigned_seats | general_admission | tables '
    '| mixed. Determines which geometry primitives are permitted in the '
    'version canonical model (§5.3 of the seating backlog).';

COMMENT ON COLUMN seating_plans.visibility IS
    'Share scope: private (owner only), shared_read (linked orgs read), '
    'public_template (any org can fork), operator_verified (network '
    'operator has vouched for correctness).';

COMMENT ON COLUMN seating_plans.status IS
    'Lifecycle: draft (mutable, no sessions may bind), active '
    '(published, sessions may bind), archived (soft-retired, no new '
    'session bindings). Soft-delete is separate (deleted_at).';

COMMENT ON COLUMN seating_plans.source_seating_plan_id IS
    'Fork lineage: NULL for originals; set to the source plan id when '
    'this plan was created via seating_plan.fork.';

COMMENT ON COLUMN seating_plans.current_version_id IS
    'Pointer to the currently-published seating_plan_versions row. '
    'NULL until the first version is created. Sessions bind to a '
    'specific version_id (not the plan), so bumping this pointer does '
    'not affect live sessions.';

COMMENT ON TABLE seating_plan_versions IS
    'Immutable-once-bound snapshot of a plan''s canonical geometry. '
    'A version referenced by any session with sales or issued tickets '
    'is app-side immutable — mutations create a new version instead. '
    'Feature #302 — Wave SEAT-A1.';

COMMENT ON COLUMN seating_plan_versions.geometry IS
    'Canonical JSON geometry model (sectors, rows, seats, tables). '
    'Schema defined in §5.3 of 09_autoforge/seating_backlog.md.';

COMMENT ON COLUMN seating_plan_versions.geometry_checksum IS
    'sha256 hex digest of the canonical (sorted-key) geometry JSON. '
    'Used to detect drift when re-importing SVG or migrating format.';

COMMENT ON COLUMN seating_plan_versions.svg_asset_media_id IS
    'Optional pointer to the original uploaded SVG stored via the '
    'media_objects table (Wave G). NULL for versions created without '
    'an SVG source (e.g. programmatic imports).';

COMMENT ON COLUMN seating_plan_versions.capacity_seated IS
    'Number of assigned-seat positions in the geometry.';

COMMENT ON COLUMN seating_plan_versions.capacity_standing IS
    'Number of general-admission / standing positions in the geometry. '
    'Defaults to 0 for pure assigned_seats plans.';

COMMENT ON COLUMN seating_plan_versions.locked_at IS
    'Set the first time a session binds to this version. Once non-NULL '
    'the row is app-side immutable — new versions must be created for '
    'geometry edits.';

-- ─────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for seating plans
-- ─────────────────────────────────────────────────────────────────────
-- NOTE: seating_plan.share, seating_plan.verify, and
-- seating_plan.archive.own are deliberately forward-seeded — no route is
-- gated on them yet. Share / verify surfaces arrive in a later SEAT
-- wave; archive is currently expressed as a status="archived" PATCH on
-- /v1/seating-plans/{id}, which is gated by seating_plan.update.own.
-- Seeding them now keeps the permission catalog (and the role grants
-- below) stable so the future routes need no follow-up migration.

INSERT INTO permissions (name, description) VALUES
    ('seating_plan.read',                'Read seating plans and their versions'),
    ('seating_plan.create',              'Create a new seating plan within the owning organization'),
    ('seating_plan.update.own',          'Update / add a new version to a seating plan the org owns'),
    ('seating_plan.fork',                'Fork a shared or public seating plan into the org'),
    ('seating_plan.share',               'Change the visibility / share scope of an owned seating plan'),
    ('seating_plan.verify',              'Mark a public seating plan as operator_verified (network operator only)'),
    ('seating_plan.archive.own',         'Archive an owned seating plan (soft retire, no new session bindings)'),
    ('event_session.assign_seating_plan','Bind a seating plan version to an event session')
ON CONFLICT DO NOTHING;

-- Grant the full set to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN (
    'seating_plan.read',
    'seating_plan.create',
    'seating_plan.update.own',
    'seating_plan.fork',
    'seating_plan.share',
    'seating_plan.verify',
    'seating_plan.archive.own',
    'event_session.assign_seating_plan'
  )
ON CONFLICT DO NOTHING;

-- Grant the full set (except verify, which is a network operator action)
-- to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN (
    'seating_plan.read',
    'seating_plan.create',
    'seating_plan.update.own',
    'seating_plan.fork',
    'seating_plan.share',
    'seating_plan.archive.own',
    'event_session.assign_seating_plan'
  )
ON CONFLICT DO NOTHING;

-- Grant the same set to the organizer role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'organizer'
  AND  p.name IN (
    'seating_plan.read',
    'seating_plan.create',
    'seating_plan.update.own',
    'seating_plan.fork',
    'seating_plan.share',
    'seating_plan.archive.own',
    'event_session.assign_seating_plan'
  )
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN (
        'seating_plan.read',
        'seating_plan.create',
        'seating_plan.update.own',
        'seating_plan.fork',
        'seating_plan.share',
        'seating_plan.verify',
        'seating_plan.archive.own',
        'event_session.assign_seating_plan'
    )
);
DELETE FROM permissions WHERE name IN (
    'seating_plan.read',
    'seating_plan.create',
    'seating_plan.update.own',
    'seating_plan.fork',
    'seating_plan.share',
    'seating_plan.verify',
    'seating_plan.archive.own',
    'event_session.assign_seating_plan'
);

ALTER TABLE seating_plans DROP CONSTRAINT IF EXISTS seating_plans_current_version_fk;
DROP INDEX IF EXISTS seating_plan_versions_plan_recent;
DROP TABLE IF EXISTS seating_plan_versions;
DROP INDEX IF EXISTS seating_plans_source_plan;
DROP INDEX IF EXISTS seating_plans_venue_active;
DROP INDEX IF EXISTS seating_plans_owner_org_active;
DROP TABLE IF EXISTS seating_plans;
