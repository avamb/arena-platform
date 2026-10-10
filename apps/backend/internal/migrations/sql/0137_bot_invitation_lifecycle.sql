-- 0137_bot_invitation_lifecycle.sql — revoke and resend a bot invitation
-- (Telegram event-center bot, spec 08_architecture/35 §6.7, EC-16).
--
-- An invitation (migration 0115) creates the membership AT ONCE and mails a
-- one-time code. Until now the owner could neither take an invitation back
-- nor send the letter again: a wrongly typed address stayed a member of the
-- organization for ever, and an expired link meant a second POST that left
-- two invitation rows behind.
--
--   revoked_at          the owner annulled the invitation. The row stays (the
--                       audit trail, and the inviter notice reads accepted
--                       rows), but its code no longer redeems.
--   membership_created  the membership of this user in this organization was
--                       INSERTED by the transaction that created the
--                       invitation. Only then may a revocation remove it: a
--                       person who was a member before the invitation keeps
--                       their membership however the invitation ends.
--   last_sent_at        when the letter last went out (creation or resend);
--                       a resend is refused within ten minutes of it.
--
-- Backfill: last_sent_at starts at created_at. membership_created is derived
-- for the rows that already exist: the invitation row and the membership it
-- created were written by ONE transaction, so their DEFAULT now() columns hold
-- the very same instant (memberships.joined_at = bot_invitations.created_at);
-- a membership that predates the invitation has an earlier joined_at.
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up

ALTER TABLE bot_invitations
    ADD COLUMN revoked_at         timestamptz NULL,
    ADD COLUMN membership_created boolean     NOT NULL DEFAULT false,
    ADD COLUMN last_sent_at       timestamptz NOT NULL DEFAULT now();

UPDATE bot_invitations SET last_sent_at = created_at;

UPDATE bot_invitations i
SET    membership_created = true
FROM   memberships m
WHERE  m.user_id = i.user_id
  AND  m.org_id  = i.org_id
  AND  m.joined_at = i.created_at;

CREATE INDEX bot_invitations_user_org_idx ON bot_invitations (user_id, org_id);

COMMENT ON COLUMN bot_invitations.revoked_at IS
    'The owner annulled the invitation (migration 0137); its code no longer redeems. The row is kept.';
COMMENT ON COLUMN bot_invitations.membership_created IS
    'True when the transaction that created this invitation also inserted the membership; only then may a revocation remove that membership.';
COMMENT ON COLUMN bot_invitations.last_sent_at IS
    'When the invitation letter last went out (creation or resend); a resend is refused within ten minutes.';

-- +goose Down

DROP INDEX IF EXISTS bot_invitations_user_org_idx;

ALTER TABLE bot_invitations
    DROP COLUMN IF EXISTS last_sent_at,
    DROP COLUMN IF EXISTS membership_created,
    DROP COLUMN IF EXISTS revoked_at;
