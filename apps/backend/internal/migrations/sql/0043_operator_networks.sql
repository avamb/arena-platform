-- 0043_operator_networks.sql — Operator Network persistence (feature #204).
--
-- Introduces the persistence layer for the Operator Network model described in
-- 09_autoforge/admin_ui/operator_network_design_note.md (feature #202).
--
-- Three tables are created:
--   operator_networks            — the network entity itself.
--   network_users                — users assigned as `network_operator` on a
--                                  given network (administrative roster).
--   network_organizations        — organizations attached to a network in
--                                  either `organizer` or `agent` capacity
--                                  (single join table replaces parallel
--                                  organizer/agent tables; the design note
--                                  calls this out as the canonical shape).
--
-- All tables use uuidv7 PKs, soft-archive via archived_at, and
-- created_at/updated_at timestamps. Indexes are added to support the lookups
-- the admin UI and middleware will perform (users-by-network,
-- networks-by-user, orgs-by-network, networks-by-org filtered by assignment
-- kind).

-- +goose Up

-- ── operator_networks ────────────────────────────────────────────────────────

CREATE TABLE operator_networks (
    id          uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    name        text        NOT NULL,
    slug        text        NOT NULL,
    status      text        NOT NULL DEFAULT 'active'
                CONSTRAINT operator_networks_status_check
                CHECK (status IN ('active', 'suspended', 'archived')),
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT operator_networks_name_nonempty CHECK (length(btrim(name)) > 0),
    CONSTRAINT operator_networks_slug_format
        CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$'),
    CONSTRAINT operator_networks_archived_consistent
        CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

-- Slug must be globally unique among non-archived networks. Archived networks
-- keep their slug for audit but are excluded so the slug can be reused.
CREATE UNIQUE INDEX operator_networks_slug_active
    ON operator_networks (slug)
    WHERE archived_at IS NULL;

-- Index for listing active networks ordered by creation.
CREATE INDEX operator_networks_status_created_at
    ON operator_networks (status, created_at DESC);

COMMENT ON TABLE operator_networks IS
    'Operator network entity. A network is a logical grouping of organizer/'
    'agent organizations administered by one or more network_operator users. '
    'Feature #204 — Operator Network persistence.';

COMMENT ON COLUMN operator_networks.slug IS
    'URL-safe identifier for the network (a-z, 0-9, hyphen). Unique among '
    'non-archived networks; reusable after archive.';

-- ── network_users ────────────────────────────────────────────────────────────
--
-- Roster of users acting as network_operator on a network. This is the
-- administrative source of truth ("which users may operate this network");
-- effective permission resolution still flows through the RBAC engine and
-- memberships (see the design note §2.1).

CREATE TABLE network_users (
    id         uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    network_id uuid        NOT NULL REFERENCES operator_networks(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id)             ON DELETE CASCADE,
    role       text        NOT NULL DEFAULT 'network_operator'
               CONSTRAINT network_users_role_check
               CHECK (role IN ('network_operator')),
    status     text        NOT NULL DEFAULT 'active'
               CONSTRAINT network_users_status_check
               CHECK (status IN ('active', 'suspended', 'revoked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT network_users_unique UNIQUE (network_id, user_id, role)
);

CREATE INDEX network_users_network_id_active
    ON network_users (network_id)
    WHERE status = 'active';

CREATE INDEX network_users_user_id_active
    ON network_users (user_id)
    WHERE status = 'active';

COMMENT ON TABLE network_users IS
    'Assignment of users as network_operator on an operator_network. Feature #204.';

-- ── network_organizations ────────────────────────────────────────────────────
--
-- Attachment of an organization to a network. The assignment_kind column
-- distinguishes the capacity in which the organization participates — either
-- as an event organizer that the network operator coordinates, or as an
-- agent that resells on behalf of those organizers. Per the design note this
-- single table intentionally replaces parallel organizer/agent tables.

CREATE TABLE network_organizations (
    id              uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    network_id      uuid        NOT NULL REFERENCES operator_networks(id) ON DELETE CASCADE,
    organization_id uuid        NOT NULL REFERENCES organizations(id)     ON DELETE CASCADE,
    assignment_kind text        NOT NULL
                    CONSTRAINT network_organizations_kind_check
                    CHECK (assignment_kind IN ('organizer', 'agent')),
    status          text        NOT NULL DEFAULT 'active'
                    CONSTRAINT network_organizations_status_check
                    CHECK (status IN ('active', 'suspended', 'revoked')),
    attached_at     timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT network_organizations_unique
        UNIQUE (network_id, organization_id, assignment_kind)
);

CREATE INDEX network_organizations_network_id_active
    ON network_organizations (network_id, assignment_kind)
    WHERE status = 'active';

CREATE INDEX network_organizations_organization_id_active
    ON network_organizations (organization_id, assignment_kind)
    WHERE status = 'active';

COMMENT ON TABLE network_organizations IS
    'Attachment of an organization to an operator_network as either an '
    'organizer or an agent. Single join table replaces parallel '
    'organizer/agent tables. Feature #204.';

-- +goose Down

DROP TABLE IF EXISTS network_organizations;
DROP TABLE IF EXISTS network_users;
DROP TABLE IF EXISTS operator_networks;
