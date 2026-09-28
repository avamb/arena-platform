-- 0115_bot_event_center.sql — the Telegram event-center bot: who a Telegram
-- account is, how an organization invites its people into the bot, and where
-- an unfinished wizard lives (08_architecture/28_telegram_event_center_bot_ru.md).
--
-- The bot is an external REST client of arena-api acting AS the linked user,
-- so nothing here touches a business table: bot_telegram_links maps a
-- Telegram account to users.id, bot_invitations is the one-time code an
-- organization owner (or the platform superadmin) hands to a person, and
-- bot_drafts keeps the wizard state between messages so a bot restart never
-- loses a half-entered event.
--
-- Roles (spec §3.2): the bot knows two — owner and manager. Owner maps to the
-- existing org_admin role (seeded in 0009 with every org.* permission, but
-- never allowed by memberships_role_check until now — the CHECK is widened
-- here); manager maps to organizer, which gains the permissions the event
-- center wizard actually needs (venue/city/promoter creation, the
-- event-bundle import, and the read-side of sessions, tiers, orders and
-- publications for the event list and the sales summary). No NEW permission
-- is seeded, so the superadmin parity rule needs no separate grant.

-- +goose Up

-- 1. Owner = org_admin as a membership role.
ALTER TABLE memberships
    DROP CONSTRAINT memberships_role_check;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_role_check CHECK (role IN (
        'organizer',
        'agent',
        'platform_operator',
        'external_ticketing_operator',
        'platform_superadmin',
        'network_operator',
        'org_admin'
    ));

-- 2. Manager (organizer) and owner (org_admin) get the wizard's permission set.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN (
           'import.bil24_session',
           'venue.read', 'venue.create',
           'city.create',
           'promoter.read', 'promoter.manage',
           'event.create', 'event.read', 'event.update', 'event.publish',
           'session.read', 'tier.read',
           'order.read',
           'publication.read', 'feed_token.read', 'channel.read',
           'media.read', 'media.write',
           'org.read', 'membership.read'
       )
WHERE  r.name IN ('organizer', 'org_admin')
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- 3. Telegram account → arena user.
CREATE TABLE bot_telegram_links (
    telegram_user_id  bigint      PRIMARY KEY,
    user_id           uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    telegram_username text        NULL,
    locale            text        NOT NULL DEFAULT 'en',
    -- Remembered answers of the wizard (country, city, venue, age, promoter,
    -- currency, channels) — spec §5.1 "память ответов".
    defaults          jsonb       NOT NULL DEFAULT '{}'::jsonb,
    current_org_id    uuid        NULL REFERENCES organizations(id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz NULL
);

CREATE INDEX bot_telegram_links_user_idx ON bot_telegram_links (user_id);

-- 4. One-time invitation codes. The membership is created together with the
-- invitation (the person is already a member when the e-mail goes out, as
-- POST /v1/admin/organizations/{org_id}/members does); accepting the code
-- only binds the Telegram account. Only the SHA-256 of the code is stored.
CREATE TABLE bot_invitations (
    id                        uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                    uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id                   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email                     text        NOT NULL,
    role                      text        NOT NULL CHECK (role IN ('owner', 'manager')),
    code_hash                 text        NOT NULL UNIQUE,
    invited_by                uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    expires_at                timestamptz NOT NULL,
    accepted_at               timestamptz NULL,
    accepted_telegram_user_id bigint      NULL,
    created_at                timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX bot_invitations_org_idx ON bot_invitations (org_id, created_at DESC);

-- 5. The wizard's draft: one per Telegram user and organization.
CREATE TABLE bot_drafts (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    telegram_user_id bigint      NOT NULL REFERENCES bot_telegram_links(telegram_user_id) ON DELETE CASCADE,
    org_id           uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    mode             text        NOT NULL CHECK (mode IN ('create', 'edit')),
    event_id         uuid        NULL,
    step             text        NOT NULL,
    schema_version   integer     NOT NULL DEFAULT 1,
    state            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    -- What the non-atomic save already wrote (actionId, per-session ids),
    -- so a retry sends only the sessions that are still missing.
    saved            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT bot_drafts_user_org_uq UNIQUE (telegram_user_id, org_id)
);

-- +goose Down

DROP TABLE IF EXISTS bot_drafts;
DROP TABLE IF EXISTS bot_invitations;
DROP TABLE IF EXISTS bot_telegram_links;

DELETE FROM memberships WHERE role = 'org_admin';

ALTER TABLE memberships
    DROP CONSTRAINT memberships_role_check;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_role_check CHECK (role IN (
        'organizer',
        'agent',
        'platform_operator',
        'external_ticketing_operator',
        'platform_superadmin',
        'network_operator'
    ));
