-- +goose Up
-- =====================================================================
-- arena_new — Event Publications (Wave 12 — Public feed API, feature #151)
--
-- event_publications is a many-to-many join table between events and
-- agent_feed_tokens (feed channels). It answers the question: "which
-- feeds carry this event?".
--
-- Design decisions:
--   * event_id FK → events(id) ON DELETE CASCADE: removing an event
--     automatically unpublishes it from all feeds (no dangling refs).
--   * feed_token_id FK → agent_feed_tokens(id) ON DELETE CASCADE:
--     revoking a feed token removes all its publication entries.
--   * city_id is a nullable scope: when set, the publication is only
--     visible to consumers in that city (geo-filtered feed). NULL means
--     the publication is visible in all geographies.
--   * Composite UNIQUE (event_id, feed_token_id) prevents duplicate
--     publication entries and makes POST idempotent (ON CONFLICT DO NOTHING).
--   * published_at records when the publication was created; it is
--     intentionally separate from event.start_at.
--   * No soft-delete: publication rows are hard-deleted when the event
--     is unpublished (DELETE endpoint). The audit_events table captures
--     publish/unpublish history.
--   * Mirrors the legacy Bil24 "Subscriptions" panel (Tixnet UI):
--     each row corresponds to one "subscription" record in the old system.
--
-- ─────────────────────────────────────────────────────────────────────
-- Legacy Bil24 Subscriptions migration outline (step 3)
-- ─────────────────────────────────────────────────────────────────────
-- When migrating Bil24 subscription data to this table, the mapping is:
--
--   bil24.subscriptions.event_id  → event_publications.event_id
--   bil24.subscriptions.agent_id  → event_publications.feed_token_id
--       (requires a pre-populated agent_feed_tokens table with rows for
--        each Bil24 agent; use the token = sha256(agent_id||secret) scheme)
--   bil24.subscriptions.city_id   → event_publications.city_id
--       (map Bil24 city code to cities.id via cities.slug = lower(bil24_city_code))
--   bil24.subscriptions.created_at → event_publications.published_at
--
-- Suggested ETL query (run after arena-migrate up):
--
--   INSERT INTO event_publications (event_id, feed_token_id, city_id, published_at)
--   SELECT
--     e.id                        AS event_id,
--     ft.id                       AS feed_token_id,
--     c.id                        AS city_id,
--     bs.created_at               AS published_at
--   FROM  bil24.subscriptions   bs
--   JOIN  events                e  ON e.id::text = bs.arena_event_id::text
--   JOIN  agent_feed_tokens     ft ON ft.label   = 'bil24:' || bs.agent_id::text
--   LEFT JOIN cities            c  ON c.slug     = lower(bs.city_code)
--   ON CONFLICT (event_id, feed_token_id) DO NOTHING;
--
-- Validation query (compare counts):
--
--   SELECT COUNT(*) FROM bil24.subscriptions;
--   SELECT COUNT(*) FROM event_publications;
--
-- =====================================================================

CREATE TABLE event_publications (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    event_id      uuid        NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    feed_token_id uuid        NOT NULL REFERENCES agent_feed_tokens(id) ON DELETE CASCADE,
    city_id       uuid        REFERENCES cities(id),
    published_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT event_publications_unique UNIQUE (event_id, feed_token_id)
);

-- Index: list all publications for a given event (management API, list endpoint).
CREATE INDEX event_publications_event_id ON event_publications (event_id);

-- Index: list all events published to a given feed token (feed consumer path).
CREATE INDEX event_publications_feed_token_id ON event_publications (feed_token_id);

COMMENT ON TABLE event_publications IS
    'Many-to-many join between events and agent_feed_tokens. '
    'Mirrors the legacy Bil24 Subscriptions panel. '
    'ADR-013 (federated feeds). Feature #151 — Wave 12.';

COMMENT ON COLUMN event_publications.city_id IS
    'Optional city scope for geo-filtered feeds. NULL = visible in all cities.';

COMMENT ON COLUMN event_publications.feed_token_id IS
    'The agent feed token (channel credential) that this event is published to.';

-- ─────────────────────────────────────────────────────────────────────────────
-- Seed RBAC permissions for event publication management
-- ─────────────────────────────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('publication.create', 'Publish an event to an agent feed'),
    ('publication.read',   'List publications for an event or feed'),
    ('publication.delete', 'Unpublish an event from an agent feed')
ON CONFLICT DO NOTHING;

-- Grant all publication permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('publication.create', 'publication.read', 'publication.delete')
ON CONFLICT DO NOTHING;

-- Grant all publication permissions to org_admin (tenant admin manages own publications).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('publication.create', 'publication.read', 'publication.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('publication.create', 'publication.read', 'publication.delete')
);
DELETE FROM permissions
WHERE name IN ('publication.create', 'publication.read', 'publication.delete');
DROP TABLE IF EXISTS event_publications;
