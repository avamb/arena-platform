-- +goose Up
-- =====================================================================
-- arena_new — tracked sales links and QR codes (spec 43, task QL-1)
--
-- An organizer prints a QR code on a poster. The QR carries a short address
-- (go.arenasoldout.com/<code>), the platform redirects to the event page with
-- utm_* and ref, counts the scan, and the buyer's order is tied back to the
-- link (last touch within 7 days, spec 43 section 2).
--
-- sales_links        one row per printed link (never deleted, only archived)
-- sales_link_hits    scans per day and country, no personal data
-- checkout_sessions.attribution / orders.attribution   the cleaned touch json
-- orders.sales_link_id                                 the link the order came through
--
-- Foreign keys carry NO cascade on purpose: a link outlives the event it
-- points at (the redirect then falls back to the organization page, spec 43
-- section 4.3), and a printed code must keep working.
--
-- short_host is the host the link was issued under (run decision R-QL2): a
-- verified organizer domain may issue NEW links later without reprinting the
-- old ones.
--
-- Permissions sales_link.read / sales_link.manage are granted to org_admin,
-- organizer and platform_superadmin in this same migration (the 532 parity
-- guard). An organization API key may hold them (the site's event center
-- works with a key), so they are NOT in apikeys.IsForbiddenScope.
-- =====================================================================
SET LOCAL lock_timeout = '5s';

CREATE TABLE sales_links (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id),
    code               text        NOT NULL,
    short_host         text        NOT NULL DEFAULT 'go.arenasoldout.com',
    target_kind        text        NOT NULL,
    event_id           uuid        REFERENCES events(id),
    session_id         uuid        REFERENCES sessions(id),
    promoter_id        uuid        REFERENCES org_promoters(id),
    url                text,
    label              text        NOT NULL,
    placement_kind     text        NOT NULL DEFAULT 'poster',
    utm                jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status             text        NOT NULL DEFAULT 'active',
    scan_count         bigint      NOT NULL DEFAULT 0,
    suspect_scan_count bigint      NOT NULL DEFAULT 0,
    last_scanned_at    timestamptz,
    created_by         uuid        REFERENCES users(id),
    created_via        text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sales_links_code_check CHECK (code ~ '^[a-z2-9]{7}$'),
    CONSTRAINT sales_links_short_host_check CHECK (length(short_host) BETWEEN 1 AND 253),
    CONSTRAINT sales_links_target_kind_check
        CHECK (target_kind IN ('org_page', 'promoter_page', 'event', 'session', 'url')),
    CONSTRAINT sales_links_label_check CHECK (length(label) BETWEEN 1 AND 120),
    CONSTRAINT sales_links_placement_kind_check
        CHECK (placement_kind IN ('poster', 'flyer', 'sign', 'social', 'email', 'messenger', 'other')),
    CONSTRAINT sales_links_utm_check CHECK (jsonb_typeof(utm) = 'object'),
    CONSTRAINT sales_links_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT sales_links_url_length_check CHECK (url IS NULL OR (length(url) <= 2048 AND url ~* '^https?://')),
    -- Which column a target kind needs, one rule per column so a violation
    -- names the column. event: the event, no session. session: the session
    -- (event_id is filled by the application too, denormalized for the
    -- "links of this event" list). promoter_page: the promoter. url: the
    -- address. org_page: none of them.
    CONSTRAINT sales_links_event_target_check
        CHECK (target_kind <> 'event' OR (event_id IS NOT NULL AND session_id IS NULL)),
    CONSTRAINT sales_links_session_target_check
        CHECK ((target_kind = 'session') = (session_id IS NOT NULL)),
    CONSTRAINT sales_links_promoter_target_check
        CHECK ((target_kind = 'promoter_page') = (promoter_id IS NOT NULL)),
    CONSTRAINT sales_links_url_target_check
        CHECK ((target_kind = 'url') = (url IS NOT NULL)),
    CONSTRAINT sales_links_event_stray_check
        CHECK (target_kind IN ('event', 'session') OR event_id IS NULL)
);

CREATE UNIQUE INDEX sales_links_code_uq ON sales_links (code);
CREATE INDEX sales_links_org_idx ON sales_links (org_id, status, created_at DESC);
CREATE INDEX sales_links_event_idx ON sales_links (event_id) WHERE event_id IS NOT NULL;

CREATE TABLE sales_link_hits (
    sales_link_id uuid   NOT NULL REFERENCES sales_links(id) ON DELETE CASCADE,
    day           date   NOT NULL,
    country       text   NOT NULL DEFAULT '',
    hits          bigint NOT NULL DEFAULT 0,
    suspect_hits  bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (sales_link_id, day, country)
);

ALTER TABLE checkout_sessions ADD COLUMN attribution jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE orders ADD COLUMN sales_link_id uuid REFERENCES sales_links(id);
ALTER TABLE orders ADD COLUMN attribution jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX orders_sales_link_idx ON orders (sales_link_id) WHERE sales_link_id IS NOT NULL;

INSERT INTO permissions (name, description) VALUES
    ('sales_link.read',   'Read tracked sales links, their QR codes and scan and order statistics'),
    ('sales_link.manage', 'Create, edit, retarget and archive tracked sales links')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('sales_link.read', 'sales_link.manage')
WHERE  r.name IN ('org_admin', 'organizer')
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('sales_link.read', 'sales_link.manage')
WHERE  r.name = 'platform_superadmin'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name IN ('sales_link.read', 'sales_link.manage'));

DELETE FROM permissions WHERE name IN ('sales_link.read', 'sales_link.manage');

DROP INDEX IF EXISTS orders_sales_link_idx;
ALTER TABLE orders DROP COLUMN IF EXISTS attribution;
ALTER TABLE orders DROP COLUMN IF EXISTS sales_link_id;
ALTER TABLE checkout_sessions DROP COLUMN IF EXISTS attribution;

DROP TABLE IF EXISTS sales_link_hits;
DROP INDEX IF EXISTS sales_links_event_idx;
DROP INDEX IF EXISTS sales_links_org_idx;
DROP INDEX IF EXISTS sales_links_code_uq;
DROP TABLE IF EXISTS sales_links;
