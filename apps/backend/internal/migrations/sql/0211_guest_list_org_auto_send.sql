-- 0211_guest_list_org_auto_send.sql — the guest list goes to the whole team
-- before every session (owner request 2026-11-08, Vino&Co mails its guests
-- the day before an event and needs a complete, current list).
--
-- organizations.guest_list_auto_send: the organization-level switch, ON by
-- default, edited by the owner in the event center (PUT
-- /v1/organizations/{org_id}/guest-list-settings, permission org.update).
--
-- guest_list_org_sends: one row per session and kind ('24h', '3h'). The job
-- guest_list.org_auto INSERTs the row (ON CONFLICT DO NOTHING) BEFORE it
-- builds or sends anything, so a list is sent at most once whatever fails
-- afterwards; the counters say what went out. This is separate from the
-- per-person guest_list_deliveries of migration 0187.
--
-- No permission is seeded: org.read and org.update already exist and are
-- granted to platform_superadmin.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    ADD COLUMN guest_list_auto_send boolean NOT NULL DEFAULT true;

COMMENT ON COLUMN organizations.guest_list_auto_send IS
    'Owner switch: send the current guest list to the owners, managers and the sales Telegram groups 24 h and 3 h before every session.';

CREATE TABLE guest_list_org_sends (
    session_id     uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind           text        NOT NULL CHECK (kind IN ('24h', '3h')),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    claimed_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz,
    outcome        text,
    tickets        integer,
    emails_sent    integer     NOT NULL DEFAULT 0,
    emails_failed  integer     NOT NULL DEFAULT 0,
    chats_sent     integer     NOT NULL DEFAULT 0,
    chats_failed   integer     NOT NULL DEFAULT 0,
    PRIMARY KEY (session_id, kind)
);

CREATE INDEX guest_list_org_sends_org_idx ON guest_list_org_sends (org_id, claimed_at);

COMMENT ON TABLE guest_list_org_sends IS
    'Dedupe of the automatic team guest list: one row per session and kind, written before the send.';

-- +goose Down
DROP TABLE IF EXISTS guest_list_org_sends;
ALTER TABLE organizations DROP COLUMN IF EXISTS guest_list_auto_send;
